// 命令 edudesk-watchdog 是设计 05 §2.1 定义的 EduDeskWatchdogSvc。
//
// 它以**真实 Windows 服务**形态运行（由 SCM 托管），守护主展示/后端进程：
//
//	| 场景                 | 策略                              | 实现位置            |
//	| 主进程崩溃            | 5s 内重启                          | watchdog.Supervisor |
//	| 主进程挂起（心跳超时）  | 15s 无心跳 -> kill + restart       | watchdog.Supervisor |
//	| 连续崩溃 >= 3 次（5min）| 停止重启循环 + FALLBACK + 写日志     | watchdog.Supervisor |
//	| 服务自身故障           | SCM 恢复策略（重启 x2, 第 3 次跑脚本）| install 子命令      |
//
// 子命令：
//
//	install   注册并启动服务（需管理员）
//	uninstall 停止并删除服务（需管理员）
//	status    查询服务与终端状态
//	debug     前台运行监督循环（不注册服务，便于开发验证）
//	run       以服务方式运行（SCM 调用；无参数时默认）
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"edudesk/internal/auth"
	"edudesk/internal/heartbeat"
	"edudesk/internal/watchdog"
)

const (
	serviceName = "EduDeskWatchdogSvc"
	displayName = "EduDesk Watchdog Service"
	serviceDesc = "Monitors and restarts the EduDesk display process (design 05 sec 2.1)."
)

func main() {
	if len(os.Args) < 2 {
		runAsService()
		return
	}
	cmd, rest := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "run":
		runAsService()
		return
	case "install":
		err = cmdInstall(rest)
	case "uninstall":
		err = cmdUninstall()
	case "start":
		err = cmdStartStop(true)
	case "stop":
		err = cmdStartStop(false)
	case "status":
		err = cmdStatus()
	case "debug":
		err = cmdDebug(rest)
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: "+err.Error())
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`edudesk-watchdog - EduDesk display watchdog (Windows service)

usage:
  edudesk-watchdog install   -exe <path> [-args "<a b c>"] [-workdir <dir>]
                             [-code TERMINAL-01] [-tick 2] [-recovery <script>]
                             (-dev | -secret <32+ bytes>)
  edudesk-watchdog uninstall
  edudesk-watchdog start
  edudesk-watchdog stop
  edudesk-watchdog status
  edudesk-watchdog debug     [-config <path>] [-exe <path>] [-args "<a b>"] [-code C]
  edudesk-watchdog run        (invoked by SCM)
`)
}

// ------------------------------------------------------------------
// 服务模式
// ------------------------------------------------------------------

func runAsService() {
	cfg, err := LoadConfig()
	if err != nil {
		// 服务启动阶段配置缺失是无法自愈的：写事件日志也做不到（无日志路径），
		// 直接以非零退出码返回，由 SCM 恢复策略接管。
		fmt.Fprintln(os.Stderr, "edudesk-watchdog: load config failed: "+err.Error())
		os.Exit(1)
		return
	}
	log, err := NewLogger(cfg.LogDir, 5<<20, 4)
	if err != nil {
		os.Exit(1)
		return
	}
	defer log.Close()
	log.Info("=== EduDeskWatchdogSvc boot ===")

	h := &serviceHandler{cfg: cfg, log: log}
	if err := svc.Run(serviceName, h); err != nil {
		log.Error("svc.Run failed: %v", err)
	}
}

// ------------------------------------------------------------------
// debug 模式（前台运行监督循环）
// ------------------------------------------------------------------

func cmdDebug(args []string) error {
	fs := flag.NewFlagSet("debug", flag.ExitOnError)
	cfgPath := fs.String("config", ConfigPath(), "config file path")
	exe := fs.String("exe", "", "override main process exe path")
	argsStr := fs.String("args", "", "override main process args")
	code := fs.String("code", "", "override terminal code")
	devMode := fs.Bool("dev", false, "append --dev to the supervised process args")
	if err := fs.Parse(args); err != nil {
		return err
	}
	supArgs := splitArgs(*argsStr)
	if *devMode && !containsFlag(supArgs, "--dev") {
		supArgs = append(supArgs, "--dev")
	}

	var cfg Config
	if *exe != "" || *cfgPath != ConfigPath() {
		// 允许完全从命令行构造一个临时配置（开发验证用）
		cfg = Config{
			ExePath:      *exe,
			Args:         supArgs,
			WorkDir:      filepath.Dir(*exe),
			TerminalCode: firstNonEmpty(*code, "TERMINAL-DEV"),
			TickSec:      2,
			LogDir:       heartbeat.LogDir(),
		}
	} else {
		c, err := LoadConfig()
		if err != nil {
			return fmt.Errorf("load config: %w (run install first, or pass -exe)", err)
		}
		cfg = c
		if *code != "" {
			cfg.TerminalCode = *code
		}
	}
	if cfg.ExePath == "" {
		return errors.New("no main process exe configured (-exe required when no config exists)")
	}

	log, err := NewLogger(cfg.LogDir, 5<<20, 4)
	if err != nil {
		return err
	}
	defer log.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	runner := newProcRunner(cfg, log)
	sup := watchdog.New(runner, watchdog.Options{})
	sup.OnEvent = func(e watchdog.Event) {
		// debug 模式同时打到控制台
		fmt.Println("EVENT", e.String())
		switch e.Kind {
		case watchdog.EvFallback:
			log.Error("event: %s", e.String())
		default:
			log.Info("event: %s", e.String())
		}
	}

	fmt.Printf("debug watchdog running\nexe      : %s %v\nheartbeat: %s\nlog      : %s\nctrl+c to stop\n",
		cfg.ExePath, cfg.Args, heartbeat.Path(), filepath.Join(cfg.LogDir, "watchdog.log"))

	h := &serviceHandler{cfg: cfg, log: log}
	h.monitor(ctx, sup)
	return nil
}

// ------------------------------------------------------------------
// install / uninstall / status
// ------------------------------------------------------------------

func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	exe := fs.String("exe", "", "main process executable (required)")
	argsStr := fs.String("args", "", "main process arguments (space separated)")
	workdir := fs.String("workdir", "", "main process working directory")
	code := fs.String("code", "TERMINAL-01", "terminal code shown on FALLBACK")
	tick := fs.Int("tick", 2, "supervision tick seconds")
	recovery := fs.String("recovery", "", "recovery script for SCM 3rd failure action")
	// P0-1 联动：被守护进程现在会因弱密钥**拒绝启动**。若这里不把密钥传下去，
	// 看门狗会看到进程秒退、判定崩溃、5min 内 3 次后打 FALLBACK —— 整机黑屏。
	// 因此密钥在这里是一等参数，不能靠手改 JSON。
	devMode := fs.Bool("dev", false, "pass --dev to the supervised process (demo secret; NOT for pilot/production)")
	secret := fs.String("secret", os.Getenv("EDUDESK_SECRET"), "signing secret handed to the supervised process (falls back to $EDUDESK_SECRET)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if !isElevated() {
		return errors.New("administrator privileges required to install a Windows service")
	}
	if *exe == "" {
		return errors.New("-exe is required")
	}
	absExe, err := filepath.Abs(*exe)
	if err != nil {
		return err
	}
	if _, err := os.Stat(absExe); err != nil {
		return fmt.Errorf("main process not found: %s", absExe)
	}
	if *workdir == "" {
		*workdir = filepath.Dir(absExe)
	}

	// 组装被守护进程参数，并在此拦住「装完就崩」的组合。
	supArgs := splitArgs(*argsStr)
	if !*devMode {
		if err := auth.SecretOK(*secret); err != nil {
			return fmt.Errorf("refusing to install: supervised process would fail to start (%v); pass -dev for a demo box or -secret <32+ bytes>", err)
		}
	}
	if *devMode && !containsFlag(supArgs, "--dev") {
		supArgs = append(supArgs, "--dev")
	} else if *secret != "" && !containsFlag(supArgs, "-secret") && !containsFlag(supArgs, "--secret") {
		supArgs = append(supArgs, "-secret", *secret)
	}

	// 路径规范化：从 Git Bash / MSYS 传参时，反斜杠与正斜杠可能被混用成
	// `C://Users//x` 这类形式，直接写进 SCM 的 recovery command 会导致脚本无法执行。
	// 统一 Clean + FromSlash 成合法 Windows 路径。
	//
	// 还必须 Abs：SCM 恢复命令由 SCM 自己启动，工作目录不是我们的 cwd，
	// 相对路径（`..\deploy-windows\x.cmd`）在 SCM 眼里无意义 —— 第 3 次恢复动作
	// 会静默失败，而前两次重启看起来一切正常，故障要到真正需要恢复时才暴露。
	if *recovery != "" {
		*recovery = filepath.Clean(filepath.FromSlash(*recovery))
		abs, err := filepath.Abs(*recovery)
		if err != nil {
			return fmt.Errorf("recovery script path: %w", err)
		}
		*recovery = abs
		if _, err := os.Stat(*recovery); err != nil {
			return fmt.Errorf("recovery script not found: %s", *recovery)
		}
	}
	*workdir = filepath.Clean(filepath.FromSlash(*workdir))

	// 1) 写配置
	cfg := Config{
		ExePath:        absExe,
		Args:           supArgs,
		WorkDir:        *workdir,
		TerminalCode:   *code,
		TickSec:        *tick,
		LogDir:         heartbeat.LogDir(),
		RecoveryScript: *recovery,
	}
	if err := SaveConfig(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	fmt.Println("config written:", ConfigPath())

	// 2) 目录授权：心跳文件由用户会话中的主进程写入，服务（LocalSystem）只读。
	//    因此必须给 Users 组授予 Modify 权限，否则主进程写心跳会被拒绝，
	//    看门狗会把健康的主进程误判为「挂起」并不断重启。
	if err := grantUsersModify(heartbeat.Dir()); err != nil {
		fmt.Fprintln(os.Stderr, "warn: grant failed (heartbeat write may fail):", err)
	} else {
		fmt.Println("granted Modify on", heartbeat.Dir(), "to BUILTIN\\Users")
	}

	// 3) 注册服务
	self, err := os.Executable()
	if err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect SCM: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err == nil {
		// 服务已存在：**复用同一句柄**继续更新恢复策略。
		// 注意：这里绝不能提前 s.Close() —— 关闭后再调用 SetRecoveryActions /
		// SetRecoveryCommand / Start 会因句柄失效而报「句柄无效」，
		// 导致「配置更新失败但脚本仍打印 installed」的静默错误。
		defer s.Close()
		fmt.Println("service already exists; updating config and recovery policy")
	} else {
		s, err = m.CreateService(serviceName, self, mgr.Config{
			StartType:        mgr.StartAutomatic,
			DisplayName:      displayName,
			Description:      serviceDesc,
			DelayedAutoStart: true,
			ErrorControl:     mgr.ErrorNormal,
		})
		if err != nil {
			return fmt.Errorf("create service: %w", err)
		}
		defer s.Close()
		fmt.Println("service created:", serviceName)
	}

	// 4) SCM 恢复策略（设计 05 §2.1 第 4 行）：
	//    第 1/2 次失败重启服务；第 3 次运行恢复脚本。
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 15 * time.Second},
		{Type: mgr.RunCommand, Delay: 30 * time.Second},
	}
	if err := s.SetRecoveryActions(actions, 86400); err != nil {
		fmt.Fprintln(os.Stderr, "warn: set recovery actions failed:", err)
	} else {
		fmt.Println("recovery policy set (restart x2, then run recovery command)")
	}
	if cfg.RecoveryScript != "" {
		if err := s.SetRecoveryCommand(cfg.RecoveryScript); err != nil {
			fmt.Fprintln(os.Stderr, "warn: set recovery command failed:", err)
		} else {
			fmt.Println("recovery command:", cfg.RecoveryScript)
		}
	} else {
		fmt.Fprintln(os.Stderr, "warn: no -recovery script given; 3rd failure action has no command")
	}

	// 5) 启动服务
	if err := s.Start(); err != nil {
		// 已经在运行会报 ERROR_SERVICE_ALREADY_RUNNING
		fmt.Fprintln(os.Stderr, "note: start service returned:", err)
	} else {
		fmt.Println("service started")
	}

	fmt.Println("\ninstalled. verify with: edudesk-watchdog status")
	return nil
}

func cmdUninstall() error {
	if !isElevated() {
		return errors.New("administrator privileges required to uninstall a Windows service")
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect SCM: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service %s not installed: %w", serviceName, err)
	}
	defer s.Close()

	// 尝试停止（未运行时会报错，忽略）
	if _, err := s.Control(svc.Stop); err != nil {
		fmt.Println("note: stop returned:", err)
	} else {
		// 等服务真正停止
		for i := 0; i < 20; i++ {
			time.Sleep(300 * time.Millisecond)
			st, err := s.Query()
			if err != nil || st.State == svc.Stopped {
				break
			}
		}
	}
	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	fmt.Println("service removed:", serviceName)
	fmt.Println("note: config/heartbeat/log files kept at", heartbeat.Dir())
	return nil
}

// cmdStartStop 启停服务。
//
// 为什么不直接用 sc.exe：部分受管环境把 sc.exe 列入程序黑名单，
// 运维无法调用。提供内建子命令可绕开该限制，也更适合写进 Runbook。
func cmdStartStop(start bool) error {
	if !isElevated() {
		return errors.New("administrator privileges required to control a Windows service")
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect SCM: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service %s not installed: %w", serviceName, err)
	}
	defer s.Close()

	if start {
		if err := s.Start(); err != nil {
			return err
		}
		fmt.Println("service start requested:", serviceName)
		return nil
	}

	if _, err := s.Control(svc.Stop); err != nil {
		return err
	}
	// 等待真正停止（SCM 是异步的）
	for i := 0; i < 30; i++ {
		time.Sleep(300 * time.Millisecond)
		st, err := s.Query()
		if err != nil {
			break
		}
		if st.State == svc.Stopped {
			fmt.Println("service stopped:", serviceName)
			fmt.Println("note: the display process is intentionally NOT terminated")
			return nil
		}
	}
	fmt.Println("service stop requested (still pending)")
	return nil
}

func cmdStatus() error {
	fmt.Println("service  :", serviceName)
	m, err := mgr.Connect()
	if err != nil {
		fmt.Println("  SCM      : connect failed:", err)
	} else {
		defer m.Disconnect()
		s, err := m.OpenService(serviceName)
		if err != nil {
			fmt.Println("  status   : NOT INSTALLED")
		} else {
			defer s.Close()
			st, err := s.Query()
			if err != nil {
				fmt.Println("  status   : query failed:", err)
			} else {
				fmt.Printf("  status   : %s (accepts=%d)\n", stateText(st.State), st.Accepts)
			}
			if acts, err := s.RecoveryActions(); err == nil {
				var parts []string
				for _, a := range acts {
					parts = append(parts, fmt.Sprintf("%s@%s", actionText(a.Type), a.Delay))
				}
				fmt.Println("  recovery :", strings.Join(parts, ", "))
			}
			if cp, err := s.RecoveryCommand(); err == nil && cp != "" {
				fmt.Println("  rec-cmd  :", cp)
			}
		}
	}

	fmt.Println("config   :", ConfigPath())
	if cfg, err := LoadConfig(); err == nil {
		fmt.Printf("  exe=%s args=%v workdir=%s code=%s tick=%ds\n",
			cfg.ExePath, cfg.Args, cfg.WorkDir, cfg.TerminalCode, cfg.TickSec)
	} else {
		fmt.Println("  (not found)")
	}

	fmt.Println("heartbeat:", heartbeat.Path())
	if s, err := heartbeat.Read(); err == nil {
		fmt.Printf("  pid=%d seq=%d age=%s\n", s.PID, s.Seq, time.Since(s.At).Truncate(time.Millisecond))
	} else {
		fmt.Println("  (not found)")
	}

	fmt.Println("state    :", StatePath())
	if b, err := os.ReadFile(StatePath()); err == nil {
		fmt.Println("  " + strings.ReplaceAll(strings.TrimSpace(string(b)), "\n", "\n  "))
	} else {
		fmt.Println("  (not found)")
	}
	return nil
}

// ------------------------------------------------------------------
// 辅助
// ------------------------------------------------------------------

func isElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// grantUsersModify 给 BUILTIN\Users 授予目录 Modify 权限。
func grantUsersModify(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// S-1-5-32-545 = BUILTIN\Users；* 前缀表示按 SID 解析（不受系统语言影响）
	cmd := exec.Command("icacls", dir, "/grant", "*S-1-5-32-545:(OI)(CI)M", "/T", "/C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// splitArgs 把 "a b c" 拆成参数切片（简单空格分割；含空格参数请写进配置文件）。
func splitArgs(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return strings.Fields(s)
}

// containsFlag 判断参数切片里是否已有该开关（避免 -dev / -secret 被重复追加）。
// 同时匹配 "-x" 与 "--x"，并容忍 "-x=value" 写法。
func containsFlag(args []string, flagName string) bool {
	bare := strings.TrimLeft(flagName, "-")
	for _, a := range args {
		trimmed := strings.TrimLeft(a, "-")
		if trimmed == bare || strings.HasPrefix(trimmed, bare+"=") {
			return true
		}
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func stateText(s svc.State) string {
	switch s {
	case svc.Stopped:
		return "STOPPED"
	case svc.StartPending:
		return "START_PENDING"
	case svc.StopPending:
		return "STOP_PENDING"
	case svc.Running:
		return "RUNNING"
	case svc.PausePending:
		return "PAUSE_PENDING"
	case svc.Paused:
		return "PAUSED"
	case svc.ContinuePending:
		return "CONTINUE_PENDING"
	default:
		return fmt.Sprintf("STATE(%d)", s)
	}
}

func actionText(t int) string {
	switch t {
	case mgr.NoAction:
		return "none"
	case mgr.ServiceRestart:
		return "restart"
	case mgr.RunCommand:
		return "run-command"
	case mgr.ComputerReboot:
		return "reboot"
	default:
		return fmt.Sprintf("action(%d)", t)
	}
}
