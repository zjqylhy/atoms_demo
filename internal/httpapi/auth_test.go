package httpapi

import (
	"mockatoms/internal/config"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientIPRequiresExplicitTrustedPeer(t *testing.T) {
	for _, tc := range []struct {
		name, remote, forwarded, allowed, want string
		loopback                               bool
	}{
		{"direct spoof rejected", "203.0.113.4:1234", "198.51.100.9", "", "203.0.113.4", false},
		{"loopback opt in", "127.0.0.1:1234", "198.51.100.9", "", "198.51.100.9", true},
		{"loopback default denied", "127.0.0.1:1234", "198.51.100.9", "", "127.0.0.1", false},
		{"docker default denied", "172.18.0.11:1234", "198.51.100.9", "", "172.18.0.11", true},
		{"docker explicit peer", "172.18.0.11:1234", "192.0.2.8, 198.51.100.9", "172.18.0.11", "198.51.100.9", false},
		{"adjacent container denied", "172.18.0.12:1234", "198.51.100.9", "172.18.0.11", "172.18.0.12", false},
		{"invalid last hop denied", "172.18.0.11:1234", "198.51.100.9, unknown", "172.18.0.11", "172.18.0.11", false},
		{"invalid configuration denied", "172.18.0.11:1234", "198.51.100.9", "172.18.0.0/16,invalid", "172.18.0.11", false},
		{"ipv6 canonical address", "[::1]:1234", "2001:0db8::1", "172.18.0.11, ::1", "2001:db8::1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{Config: config.Config{TrustProxy: tc.loopback, TrustedProxyIPs: tc.allowed}}
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tc.remote
			r.Header.Set("X-Forwarded-For", tc.forwarded)
			if got := s.clientIP(r); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
