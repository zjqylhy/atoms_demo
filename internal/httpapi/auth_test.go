package httpapi

import (
	"encoding/json"
	"io"
	"mockatoms/internal/agent"
	"mockatoms/internal/config"
	"mockatoms/internal/store"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

func TestRegisterLoginImportAndLogoutHTTP(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := config.Config{}
	e := agent.New(s, c, nil)
	defer e.Shutdown()
	ts := httptest.NewServer(New(s, e, c))
	defer ts.Close()
	newClient := func() *http.Client { jar, _ := cookiejar.New(nil); return &http.Client{Jar: jar} }
	request := func(client *http.Client, path, token, body string) (int, map[string]any) {
		t.Helper()
		method := "GET"
		if body != "" {
			method = "POST"
		}
		r, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		r.Header.Set("X-CSRF-Token", token)
		resp, er := client.Do(r)
		if er != nil {
			t.Fatal(er)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(data, &out)
		return resp.StatusCode, out
	}
	a := newClient()
	_, boot := request(a, "/api/bootstrap", "", "")
	csrf := boot["csrf"].(string)
	_, p := request(a, "/api/projects", csrf, `{"title":"keep me"}`)
	projectID := p["id"].(string)
	register := `{"username":"Alice","password":"a long safe test password"}`
	if status, _ := request(a, "/api/auth/register", "", register); status != 403 {
		t.Fatal("CSRF bypass", status)
	}
	if status, _ := request(a, "/api/auth/register", csrf, register); status != 200 {
		t.Fatal("registration", status)
	}
	_, boot = request(a, "/api/bootstrap", "", "")
	if boot["user"].(map[string]any)["username"] != "alice" {
		t.Fatal(boot)
	}
	b := newClient()
	_, other := request(b, "/api/bootstrap", "", "")
	_, visitorProject := request(b, "/api/projects", other["csrf"].(string), `{"title":"keep guest separate"}`)
	if status, _ := request(b, "/api/projects/"+projectID, "", ""); status != 404 {
		t.Fatal("guest read private", status)
	}
	login := `{"username":"alice","password":"a long safe test password","import_guest":false}`
	if status, _ := request(b, "/api/auth/login", other["csrf"].(string), login); status != 200 {
		t.Fatal("second-device login", status)
	}
	if status, _ := request(b, "/api/projects/"+projectID, "", ""); status != 200 {
		t.Fatal("second-device restore", status)
	}
	_, boot = request(b, "/api/bootstrap", "", "")
	u, _ := http.NewRequest("GET", ts.URL, nil)
	oldCookies := b.Jar.Cookies(u.URL)
	if status, _ := request(b, "/api/auth/logout", boot["csrf"].(string), `{}`); status != 200 {
		t.Fatal(status)
	}
	stale := newClient()
	stale.Jar.SetCookies(u.URL, oldCookies)
	if status, _ := request(stale, "/api/projects/"+projectID, "", ""); status != 401 {
		t.Fatal("logged-out cookie still valid", status)
	}
	if status, _ := request(a, "/api/projects/"+projectID, "", ""); status != 200 {
		t.Fatal("logout revoked other device", status)
	}
	if status, _ := request(b, "/api/bootstrap", "", ""); status != 200 {
		t.Fatal(status)
	}
	if status, _ := request(b, "/api/projects/"+visitorProject["id"].(string), "", ""); status != 200 {
		t.Fatal("skipped import lost guest access", status)
	}
}
