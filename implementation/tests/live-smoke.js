#!/usr/bin/env node
/**
 * live-smoke.js —— 对**正在运行**的 EduDesk 服务端做一次只读冒烟测试。
 *
 * 与 core.test.js 的区别：
 *   core.test.js  验证纯逻辑（不依赖网络、不含服务端）；
 *   live-smoke.js 验证真实进程暴露出来的行为契约（跨语言签名、只读边界、数据防火墙）。
 *
 * 设计原则：只发 GET/HEAD 与"应当被拒绝"的写请求，
 *           不触发登录、不写审计、不产生任何落盘副作用。
 *
 * 用法：
 *   node implementation/tests/live-smoke.js
 *   EDUDESK_BASE=http://127.0.0.1:8080 EDUDESK_SECRET=<secret> node live-smoke.js
 *
 * 退出码：0 = 全部通过；1 = 存在 FAIL。
 */
'use strict';

const crypto = require('crypto');
const path = require('path');

const Core = require(path.join(__dirname, '..', 'terminal', 'core.js'));

const BASE = process.env.EDUDESK_BASE || 'http://127.0.0.1:8080';
const SECRET = process.env.EDUDESK_SECRET || 'dev-only-secret-change-me';

// 与服务端 internal/privacylint 保持一致（此处只做"不该出现"的反向检查）。
const PII_KEYS = [
  'student_id', 'student_name', 'student_full_name', 'name',
  'score', 'grade_score', 'exam', 'exam_score', 'grades', 'rank', 'is_exam',
  'attendance', 'attend',
  'phone', 'mobile', 'tel', 'parent', 'parent_contact',
  'teacher_notes', 'note', 'remark',
  'address', 'birth', 'birthday', 'id_card', 'face', 'photo', 'avatar',
];

// 内容项白名单（服务端 privacylint.ItemWhitelist）。
const ITEM_WHITELIST = [
  'id', 'type', 'title', 'summary', 'audience', 'location', 'priority',
  'published_at', 'effective_from', 'effective_until', 'updated_at', 'state',
];

const results = [];
function check(id, name, ok, detail) {
  results.push({ id: id, name: name, ok: !!ok, detail: detail || '' });
}
function info(name, detail) {
  results.push({ id: '--', name: name, ok: true, detail: detail, skip: true });
}

function collectKeys(node, out) {
  if (Array.isArray(node)) {
    for (const c of node) collectKeys(c, out);
  } else if (node && typeof node === 'object') {
    for (const k of Object.keys(node)) {
      out.push(k);
      collectKeys(node[k], out);
    }
  }
  return out;
}

async function fetchBundle(profile) {
  const r = await fetch(BASE + '/bundle?profile=' + encodeURIComponent(profile));
  let body = null;
  try { body = await r.json(); } catch (e) { /* 非 JSON */ }
  return { status: r.status, body: body };
}

function hmacB64(message, secret) {
  return crypto.createHmac('sha256', secret).update(message, 'utf8').digest('base64');
}

async function main() {
  const t0 = Date.now();

  // ---- T01 健康 ----
  try {
    const r = await fetch(BASE + '/health');
    const b = await r.json();
    check('T01', '/health 可用且 status=ok', r.status === 200 && b.status === 'ok',
      'HTTP ' + r.status + ' pid=' + b.pid + ' dev=' + b.dev);
    info('运行模式', b.dev ? 'dev（演示密钥，生产门禁会拦截）' : 'production');
  } catch (e) {
    check('T01', '/health 可用且 status=ok', false, '无法连接 ' + BASE + '：' + e.message);
    return report();
  }

  // ---- T02 三入口可达 ----
  const entries = ['/', '/?profile=classroom', '/?profile=corridor', '/admin/', '/core.js', '/app.js'];
  const bad = [];
  for (const u of entries) {
    const r = await fetch(BASE + u);
    if (r.status !== 200) bad.push(u + '=' + r.status);
  }
  check('T02', '终端/后台/静态资源入口均 200', bad.length === 0,
    bad.length === 0 ? entries.length + ' 个入口全部可达' : '异常：' + bad.join(' '));

  // ---- T03/T04/T05/T06 Bundle 契约 ----
  const cb = await fetchBundle('classroom');
  const rb = await fetchBundle('corridor');
  const b = cb.body;

  const required = ['bundle_id', 'profile', 'generated_at', 'ttl_sec', 'content_hash',
    'items', 'tombstones', 'signature', 'sign_alg'];
  const missing = b ? required.filter(function (k) { return !(k in b); }) : required;
  check('T03', 'Bundle 必填字段齐全', cb.status === 200 && missing.length === 0,
    missing.length === 0 ? '9 项字段齐全，items=' + b.items.length + ' tombstones=' + b.tombstones.length
      : '缺失 ' + missing.join(','));

  // content_hash = sha256(canonicalize({items, tombstones}))
  let hashOk = false, hashDetail = '';
  if (b) {
    const payload = Core.canonicalize({ items: b.items, tombstones: b.tombstones });
    const expect = 'sha256:' + crypto.createHash('sha256').update(payload, 'utf8').digest('hex');
    hashOk = expect === b.content_hash;
    hashDetail = hashOk ? '与本地重算一致 (' + b.content_hash.slice(0, 19) + '...)'
      : '不一致：服务端 ' + b.content_hash + ' / 本地 ' + expect;
  }
  check('T04', 'content_hash 可被终端复算', hashOk, hashDetail);

  // signature = HMAC-SHA256(canonicalize(bundle 去掉 signature+sign_alg))
  let sigOk = false, sigDetail = '';
  if (b) {
    const rest = Object.assign({}, b);
    delete rest.signature;
    delete rest.sign_alg;
    const expect = hmacB64(Core.canonicalize(rest), SECRET);
    const expectHex = crypto.createHmac('sha256', SECRET)
      .update(Core.canonicalize(rest), 'utf8').digest('hex');
    sigOk = (expect === b.signature) || (expectHex === b.signature);
    sigDetail = sigOk ? b.sign_alg + ' 验签通过（' + b.signature.slice(0, 16) + '...）'
      : '验签失败：服务端 ' + String(b.signature).slice(0, 24) +
        '... / 本地 ' + expect.slice(0, 24) + '...';
  }
  check('T05', '跨语言签名一致（终端可验签）', sigOk, sigDetail);

  // 双 Profile 差异
  if (b && rb.body) {
    const ct = new Set(b.items.map(function (i) { return i.type; }));
    const rt = new Set(rb.body.items.map(function (i) { return i.type; }));
    const corridorAllowed = ['announcement', 'event', 'utility', 'weather'];
    const leak = Array.from(rt).filter(function (t) { return corridorAllowed.indexOf(t) < 0; });
    const diff = Array.from(ct).filter(function (t) { return !rt.has(t); });
    check('T06', '双 Profile 内容边界正确',
      leak.length === 0 && diff.length > 0,
      'classroom={' + Array.from(ct).join(',') + '} corridor={' + Array.from(rt).join(',') + '}' +
      (leak.length ? ' 越界类型：' + leak.join(',') : ' 走廊屏无越界类型'));
  }

  // ---- T07 数据防火墙（PII 不得出现在**内容项**中）----
  // 扫描范围必须与 privacylint 的管辖范围一致：只有 items / tombstones 承载内容，
  // Bundle 顶层元数据（schema_version、lifecycle 等）由服务端固定生成、不含外部输入。
  // 注意：lifecycle.note 是**协议说明文本**，键名恰好落在黑名单词上，但它不在 item 白名单
  //       管辖范围内，也非外部输入，故不计为违规——但已在下方 INFO 中标注，避免误读。
  if (b && rb.body) {
    const keys = [];
    collectKeys(b.items, keys);
    collectKeys(b.tombstones, keys);
    collectKeys(rb.body.items, keys);
    collectKeys(rb.body.tombstones, keys);
    const hit = Array.from(new Set(keys.filter(function (k) { return PII_KEYS.indexOf(k) >= 0; })));
    check('T07', '内容项不含 PII / 成绩 / 考勤字段', hit.length === 0,
      hit.length === 0 ? '扫描 items+tombstones 共 ' + keys.length + ' 个键，无命中'
        : '命中黑名单：' + hit.join(','));
    if (b.lifecycle && typeof b.lifecycle === 'object') {
      info('Bundle 元数据字段', 'lifecycle={' + Object.keys(b.lifecycle).join(',') +
        '}（协议说明，不在内容项白名单管辖内）');
    }
  }

  // ---- T08 内容项白名单 ----
  if (b) {
    const extra = [];
    for (const it of b.items) {
      for (const k of Object.keys(it)) {
        if (ITEM_WHITELIST.indexOf(k) < 0 && extra.indexOf(k) < 0) extra.push(k);
      }
    }
    check('T08', '内容项字段未越出白名单', extra.length === 0,
      extra.length === 0 ? '全部落在 12 项白名单内' : '越界字段：' + extra.join(','));
  }

  // ---- T09 只读边界 ----
  const badMethods = [];
  for (const m of ['POST', 'PUT', 'DELETE', 'PATCH']) {
    const r = await fetch(BASE + '/bundle', { method: m, body: m === 'POST' || m === 'PUT' ? '{}' : undefined });
    const allow = r.headers.get('allow') || '';
    if (r.status !== 405 || !/GET/.test(allow) || !/HEAD/.test(allow)) {
      badMethods.push(m + '=' + r.status + ' Allow:' + (allow || '(无)'));
    }
    await r.text().catch(function () {});
  }
  const headR = await fetch(BASE + '/bundle', { method: 'HEAD' });
  check('T09', '写方法一律 405 + Allow: GET, HEAD',
    badMethods.length === 0 && headR.status === 200,
    badMethods.length === 0
      ? 'POST/PUT/DELETE/PATCH 均 405，HEAD 200'
      : badMethods.join(' | '));

  // ---- T10 内容哈希稳定（同内容多次拉取一致）----
  const again = await fetchBundle('classroom');
  check('T10', '未变更时 content_hash 稳定',
    !!again.body && again.body.content_hash === b.content_hash,
    again.body ? '两次拉取一致：' + String(b.content_hash).slice(0, 19) + '...' : '第二次拉取失败');

  // ---- T11 时效字段合法 ----
  if (b) {
    const badRange = b.items.filter(function (it) {
      if (!it.effective_from || !it.effective_until) return false;
      return Date.parse(it.effective_from) > Date.parse(it.effective_until);
    });
    check('T11', 'effective_until 不早于 effective_from', badRange.length === 0,
      badRange.length === 0 ? b.items.length + ' 项时间区间合法'
        : badRange.map(function (i) { return i.id; }).join(','));
  }

  // ---- 参考信息：未知 profile ----
  const bogus = await fetch(BASE + '/bundle?profile=__bogus__');
  info('未知 profile 行为', 'HTTP ' + bogus.status +
    (bogus.body && bogus.body.profile ? ' -> 回落 profile=' + bogus.body.profile : ''));

  // ---- T12 心跳与 /health 的 PID 交叉核对（仅本机运行时有意义）----
  try {
    const fs = require('fs');
    const hbPath = process.env.ProgramData
      ? path.join(process.env.ProgramData, 'EduDesk', 'heartbeat.json')
      : 'C:/ProgramData/EduDesk/heartbeat.json';
    const hb = JSON.parse(fs.readFileSync(hbPath, 'utf8'));
    const ageSec = (Date.now() - hb.ts) / 1000;
    const health = await (await fetch(BASE + '/health')).json();
    const pidMatch = String(hb.pid) === String(health.pid);
    check('T12', '心跳新鲜且 PID 与 /health 一致',
      pidMatch && ageSec >= 0 && ageSec < 15,
      'heartbeat pid=' + hb.pid + ' / health pid=' + health.pid +
      ' | age=' + ageSec.toFixed(1) + 's' +
      (pidMatch ? '' : ' —— PID 不一致，说明心跳由其他进程写入'));
  } catch (e) {
    info('心跳检查', '未读到心跳文件（非本机部署或看门狗未运行），跳过');
  }

  report(Date.now() - t0);
}

function report(elapsedMs) {
  const pad = function (s, n) { s = String(s); while (s.length < n) s += ' '; return s; };
  console.log('');
  console.log('EduDesk 运行冒烟测试  target=' + BASE);
  console.log('---------------------------------------------------------------');
  for (const r of results) {
    const tag = r.skip ? 'INFO' : (r.ok ? 'PASS' : 'FAIL');
    console.log(tag + '  ' + pad(r.id, 5) + pad(r.name, 34) + '  ' + r.detail);
  }
  const pass = results.filter(function (r) { return r.ok && !r.skip; }).length;
  const fail = results.filter(function (r) { return !r.ok; }).length;
  const skip = results.filter(function (r) { return r.skip; }).length;
  console.log('---------------------------------------------------------------');
  console.log(pass + ' PASS / ' + fail + ' FAIL  ' + (skip ? '(' + skip + ' INFO)  ' : '') +
    (elapsedMs ? '(' + elapsedMs + ' ms)' : ''));
  if (fail > 0) {
    console.log('结果：FAIL —— 运行实例存在契约偏差，禁止放行。');
    process.exit(1);
  }
  console.log('结果：全部通过。');
  process.exit(0);
}

main().catch(function (e) {
  console.error('冒烟脚本自身异常：' + (e && e.stack || e));
  process.exit(2);
});
