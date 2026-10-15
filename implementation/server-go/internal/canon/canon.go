// Package canon 实现与浏览器端 core.js 逐字节一致的规范化 JSON 与 HMAC 签名。
//
// 设计依据：04-只读展示API与终端最小权限（整体签名）、D-09（默认 HMAC-SHA256）。
//
// 关键约束：签名字节必须与终端 Web Crypto 计算结果完全一致，
// 否则终端验签失败会进入 DEGRADED。因此字符串转义、键排序、数字格式
// 均严格对齐 ECMAScript JSON.stringify 语义。
package canon

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// SignAlg 为签名算法标识，随包下发供终端核对。
const SignAlg = "HMAC-SHA256"

// excludedKeys 为签发后附加、不计入签名载荷的字段。
var excludedKeys = map[string]bool{
	"signature": true,
	"sign_alg":  true,
}

// quote 将字符串编码为与 JavaScript JSON.stringify 完全一致的字面量。
//
// 差异点说明（Go 原生 json.Marshal 不满足，故自行实现）：
//   - Go 默认转义 < > & 为 \u003c 等，JS 不会；
//   - Go 对 \b \f 输出 \u0008 \u000c，JS 输出 \b \f；
//   - 两者均不转义非 ASCII 字符，此处保持一致（中文原样输出）。
func quote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// number 渲染数值，语义对齐 JSON.stringify：
//   - 整数值 int64 输出为十进制整数（无小数点、无指数）；
//   - 非整数按 strconv 的 'g' 格式，最短往返表示。
func number(v interface{}) string {
	switch n := v.(type) {
	case int:
		return strconv.Itoa(n)
	case int32:
		return strconv.FormatInt(int64(n), 10)
	case int64:
		return strconv.FormatInt(n, 10)
	case float64:
		if n == float64(int64(n)) {
			return strconv.FormatInt(int64(n), 10)
		}
		return strconv.FormatFloat(n, 'g', -1, 64)
	case float32:
		return number(float64(n))
	default:
		return fmt.Sprint(v)
	}
}

// Value 递归写出规范化 JSON 字符串。
func Value(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case bool:
		if t {
			return "true"
		}
		return "false"
	case string:
		return quote(t)
	case int, int32, int64, float32, float64:
		return number(t)
	case []interface{}:
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = Value(e)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case []string:
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = quote(e)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case map[string]interface{}:
		keys := make([]string, 0, len(t))
		for k := range t {
			if excludedKeys[k] {
				continue
			}
			keys = append(keys, k)
		}
		// JS Array.prototype.sort 默认按 UTF-16 码元升序；
		// 对 ASCII 键（设计内所有键名）而言与字节序一致。
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, quote(k)+":"+Value(t[k]))
		}
		return "{" + strings.Join(parts, ",") + "}"
	default:
		// 兜底：未知类型（含 struct）先做一次 JSON 往返，再按 JS 语义规范化。
		// 这样可保证「服务端签名」与「终端用 JSON.parse 后重算」逐字节一致；
		// 直接 fmt.Sprint(struct) 会产生 {"&{...}"} 式载荷，导致终端验签必然失败。
		if bs, err := json.Marshal(v); err == nil {
			var decoded interface{}
			if err := json.Unmarshal(bs, &decoded); err == nil {
				return Value(decoded)
			}
		}
		return quote(fmt.Sprint(v))
	}
}

// Sign 对给定值计算 HMAC-SHA256 并 base64 编码。
func Sign(payload interface{}, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(Value(payload)))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// Verify 校验对象的签名是否与 secret 匹配（常量时间比较）。
// 返回 ok 与失败原因，便于终端/服务端给出可审计的错误码。
func Verify(obj map[string]interface{}, secret string) (bool, string) {
	if obj == nil {
		return false, "BUNDLE_MISSING"
	}
	raw, ok := obj["signature"].(string)
	if !ok || raw == "" {
		return false, "SIGNATURE_MISSING"
	}
	provided, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return false, "SIGNATURE_MALFORMED"
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(Value(obj)))
	expected := mac.Sum(nil)
	if !hmac.Equal(provided, expected) {
		return false, "SIGNATURE_MISMATCH"
	}
	return true, "OK"
}

// Stamp 为 map 附加 signature 与 sign_alg，返回新 map（不修改入参）。
func Stamp(obj map[string]interface{}, secret string) map[string]interface{} {
	out := make(map[string]interface{}, len(obj)+2)
	for k, v := range obj {
		if excludedKeys[k] {
			continue
		}
		out[k] = v
	}
	out["signature"] = Sign(out, secret)
	out["sign_alg"] = SignAlg
	return out
}