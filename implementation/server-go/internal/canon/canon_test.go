package canon

import "testing"

// 黄金向量由浏览器端 core.js canonicalize 生成（见 testdata 说明），
// 用于锁定 Go 与 JS 的规范化字节完全一致（否则终端验签必失败 → DEGRADED）。
func TestValueMatchesJSGoldenVectors(t *testing.T) {
	cases := []struct {
		name string
		in   interface{}
		want string
	}{
		{"键序无关", map[string]interface{}{"b": int64(1), "a": int64(2)}, `{"a":2,"b":1}`},
		{"排除signature", map[string]interface{}{"a": int64(2), "signature": "x", "sign_alg": "HMAC-SHA256"}, `{"a":2}`},
		{"嵌套", map[string]interface{}{"z": int64(1), "a": map[string]interface{}{"y": int64(2), "x": int64(3)}}, `{"a":{"x":3,"y":2},"z":1}`},
		{"数组保序", []interface{}{int64(1), int64(2)}, `[1,2]`},
		{"字符串", "hi", `"hi"`},
		{"中文原样", "校园信息", `"校园信息"`},
		{"转义引号", `he said "hi"`, `"he said \"hi\""`},
		{"转义反斜杠", `a\b`, `"a\\b"`},
		{"控制字符", "a\nb", `"a\nb"`},
		{"null", nil, `null`},
		{"bool", true, `true`},
		{"整数值float", float64(3), `3`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Value(c.in); got != c.want {
				t.Errorf("Value() = %s, want %s", got, c.want)
			}
		})
	}
}

// 关键安全属性：HTML 敏感字符不被转义（与 JS 一致），否则签名与终端不一致。
func TestValueDoesNotEscapeHTMLChars(t *testing.T) {
	got := Value(map[string]interface{}{"t": "<a>&"})
	want := `{"t":"<a>&"}`
	if got != want {
		t.Errorf("HTML 转义行为与 JS 不一致: got %s want %s", got, want)
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	obj := map[string]interface{}{
		"schema_version": "1.0",
		"profile":        "classroom",
		"items":          []interface{}{},
	}
	stamped := Stamp(obj, "secret")
	ok, reason := Verify(stamped, "secret")
	if !ok {
		t.Fatalf("验签失败: %s", reason)
	}
	// 错误密钥必须失败
	if ok, _ := Verify(stamped, "wrong"); ok {
		t.Fatal("错误密钥不应通过验签")
	}
	// 篡改内容必须失败
	tampered := map[string]interface{}{}
	for k, v := range stamped {
		tampered[k] = v
	}
	tampered["profile"] = "corridor"
	if ok, _ := Verify(tampered, "secret"); ok {
		t.Fatal("篡改 profile 后不应通过验签")
	}
}

func TestVerifyMissingSignature(t *testing.T) {
	if ok, reason := Verify(map[string]interface{}{"a": int64(1)}, "s"); ok || reason != "SIGNATURE_MISSING" {
		t.Fatalf("期望 SIGNATURE_MISSING, got ok=%v reason=%s", ok, reason)
	}
	if ok, reason := Verify(nil, "s"); ok || reason != "BUNDLE_MISSING" {
		t.Fatalf("期望 BUNDLE_MISSING, got ok=%v reason=%s", ok, reason)
	}
}