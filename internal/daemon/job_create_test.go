package daemon

import (
	"net/http"
	"testing"
)

func TestSameOriginLocal(t *testing.T) {
	cases := []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		{"loopback host, no origin", "127.0.0.1:8787", "", true},
		{"localhost host, no origin", "localhost:8787", "", true},
		{"ipv6 loopback, no origin", "[::1]:8787", "", true},
		{"loopback host + loopback origin", "127.0.0.1:8787", "http://127.0.0.1:8787", true},
		{"loopback host + localhost origin", "127.0.0.1:8787", "http://localhost:8787", true},
		{"non-loopback host", "evil.example.com", "", false},
		{"rebinding: loopback host, remote origin", "127.0.0.1:8787", "http://evil.example.com", false},
		{"remote origin with port", "127.0.0.1:8787", "https://attacker.test:443", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &http.Request{Host: tc.host, Header: http.Header{}}
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if got := sameOriginLocal(r); got != tc.want {
				t.Fatalf("sameOriginLocal(host=%q, origin=%q) = %v, want %v", tc.host, tc.origin, got, tc.want)
			}
		})
	}
}
