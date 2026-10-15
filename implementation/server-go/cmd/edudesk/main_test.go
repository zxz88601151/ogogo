package main

import (
	"net/http"
	"testing"
)

// clientIP 是 P1-8 限流的计数键。它曾直接返回 r.RemoteAddr（含临时端口），
// 于是每个新建连接都被算成新来源、锁定永远不触发 —— 这个测试把该回归钉住。
func TestClientIPStripsPort(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		want       string
	}{
		{
			name:       "IPv4 带端口",
			remoteAddr: "10.0.0.5:51234",
			want:       "10.0.0.5",
		},
		{
			name:       "IPv6 带端口",
			remoteAddr: "[fe80::1]:51234",
			want:       "fe80::1",
		},
		{
			name:       "X-Forwarded-For 优先且取首个",
			remoteAddr: "127.0.0.1:1",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.9, 10.0.0.1"},
			want:       "203.0.113.9",
		},
		{
			name:       "X-Real-IP 次选",
			remoteAddr: "127.0.0.1:1",
			headers:    map[string]string{"X-Real-IP": "198.51.100.7"},
			want:       "198.51.100.7",
		},
		{
			name:       "转发头带空格需裁剪",
			remoteAddr: "127.0.0.1:1",
			headers:    map[string]string{"X-Forwarded-For": " 203.0.113.9 , 10.0.0.1"},
			want:       "203.0.113.9",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &http.Request{RemoteAddr: c.remoteAddr, Header: http.Header{}}
			for k, v := range c.headers {
				r.Header.Set(k, v)
			}
			if got := clientIP(r); got != c.want {
				t.Errorf("clientIP = %q, want %q", got, c.want)
			}
		})
	}
}

// 同一来源的不同连接必须映射到同一个限流键，否则锁定形同虚设。
func TestClientIPSameHostSameKey(t *testing.T) {
	a := &http.Request{RemoteAddr: "10.0.0.5:40001", Header: http.Header{}}
	b := &http.Request{RemoteAddr: "10.0.0.5:59000", Header: http.Header{}}
	if clientIP(a) != clientIP(b) {
		t.Errorf("同源不同端口应得到同一限流键：%q vs %q", clientIP(a), clientIP(b))
	}
}
