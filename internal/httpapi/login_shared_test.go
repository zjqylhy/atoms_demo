package httpapi

import (
	"encoding/json"
	"io"
	"mockatoms/internal/agent"
	"mockatoms/internal/config"
	"mockatoms/internal/core"
	"mockatoms/internal/store"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

type authFixture struct {
	t      *testing.T
	store  *store.Store
	server *httptest.Server
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	c := config.Config{MaxConcurrent: 4, RunSeconds: 10, MaxCalls: 10, MaxTools: 10}
	e := agent.New(s, c, nil)
	ts := httptest.NewServer(New(s, e, c))
	t.Cleanup(func() { ts.Close(); e.Shutdown(); s.Close() })
	return &authFixture{t, s, ts}
}
func authClient() *http.Client { jar, _ := cookiejar.New(nil); return &http.Client{Jar: jar} }
func (f *authFixture) request(c *http.Client, method, path, token, body string) (int, map[string]any) {
	f.t.Helper()
	r, _ := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
	r.Header.Set("X-CSRF-Token", token)
	resp, err := c.Do(r)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return resp.StatusCode, out
}
func (f *authFixture) boot(c *http.Client) map[string]any {
	f.t.Helper()
	status, out := f.request(c, "GET", "/api/bootstrap", "", "")
	if status != 200 {
		f.t.Fatalf("bootstrap %d: %v", status, out)
	}
	return out
}
func (f *authFixture) register(c *http.Client, name string) string {
	f.t.Helper()
	boot := f.boot(c)
	status, out := f.request(c, "POST", "/api/auth/register", boot["csrf"].(string), core.JSON(map[string]string{"username": name, "password": "a long test password"}))
	if status != 200 {
		f.t.Fatalf("register %d: %v", status, out)
	}
	return f.boot(c)["csrf"].(string)
}

func TestLoginRequiredForEveryBusinessRoute(t *testing.T) {
	f := newAuthFixture(t)
	// Legacy guest data remains intact and becomes visible to any account.
	_, guest, err := f.store.NewGuest()
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.store.Create(guest.Workspace, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/projects/" + p.ID
	routes := [][2]string{{"GET", "/api/projects"}, {"HEAD", "/api/projects"}, {"POST", "/api/projects"}, {"GET", base}, {"HEAD", base}, {"PATCH", base}, {"DELETE", base}, {"POST", base + "/actions"}, {"POST", base + "/inputs"}, {"GET", base + "/files"}, {"HEAD", base + "/export"}, {"GET", base + "/export"}, {"GET", base + "/preview"}, {"POST", base + "/preview-access"}, {"GET", base + "/app-state"}, {"PUT", base + "/app-state"}, {"POST", base + "/app-state/reset"}, {"GET", base + "/app-backups"}, {"GET", base + "/events"}, {"GET", base + "/shares"}, {"POST", base + "/shares"}, {"DELETE", base + "/shares/nope"}, {"POST", "/api/catalog/todo/copy"}, {"POST", "/api/shares/" + strings.Repeat("a", 64) + "/copy"}}
	for _, kind := range []string{"missing", "guest", "revoked"} {
		c := authClient()
		token := ""
		if kind == "guest" {
			token = f.boot(c)["csrf"].(string)
		}
		if kind == "revoked" {
			token = f.register(c, "revoked_account")
			u, _ := http.NewRequest("GET", f.server.URL, nil)
			cookies := c.Jar.Cookies(u.URL)
			status, _ := f.request(c, "POST", "/api/auth/logout", token, `{}`)
			if status != 200 {
				t.Fatal(status)
			}
			c.Jar.SetCookies(u.URL, cookies)
		}
		for _, route := range routes {
			t.Run(kind+route[0]+route[1], func(t *testing.T) {
				status, out := f.request(c, route[0], route[1], token, `{"action":"submit","text":"must not execute","request_id":"deny-run"}`)
				if status != 401 || route[0] != "HEAD" && out["code"] != "login_required" {
					t.Fatalf("%d %v", status, out)
				}
			})
		}
	}
	saved, err := f.store.Get(guest.Workspace, p.ID)
	if err != nil || len(saved.Runs) != 0 || len(saved.Messages) != 0 {
		t.Fatalf("unauthorized side effect: %v %v", saved, err)
	}
	list, _ := f.store.List(guest.Workspace)
	if len(list) != 1 {
		t.Fatal("created or removed projects", list)
	}
}

func TestRegisterGlobalAccessLoginAndLogout(t *testing.T) {
	f := newAuthFixture(t)
	a, b := authClient(), authClient()
	boot := f.boot(a)
	payload := `{"username":"alice","password":"a long test password"}`
	if status, _ := f.request(a, "POST", "/api/auth/register", "", payload); status != 403 {
		t.Fatal("CSRF", status)
	}
	if status, _ := f.request(a, "POST", "/api/auth/register", boot["csrf"].(string), payload); status != 200 {
		t.Fatal("register", status)
	}
	aCSRF := f.boot(a)["csrf"].(string)
	status, p := f.request(a, "POST", "/api/projects", aCSRF, `{"title":"shared"}`)
	if status != 201 {
		t.Fatal(status, p)
	}
	base := "/api/projects/" + p["id"].(string)
	bCSRF := f.register(b, "bob")
	if status, _ = f.request(b, "PATCH", base, bCSRF, `{"title":"edited by bob"}`); status != 200 {
		t.Fatal(status)
	}
	if status, p = f.request(a, "GET", base, "", ""); status != 200 || p["title"] != "edited by bob" {
		t.Fatal(status, p)
	}
	for _, method := range []string{"GET", "HEAD"} {
		if status, _ = f.request(b, method, base, "", ""); status != 200 {
			t.Fatal(status)
		}
	}
	if status, _ = f.request(b, "POST", "/api/auth/logout", bCSRF, `{}`); status != 200 {
		t.Fatal(status)
	}
	boot = f.boot(b)
	if status, out := f.request(b, "POST", "/api/auth/login", boot["csrf"].(string), `{"username":"alice","password":"wrong password"}`); status != 401 || out["code"] != "invalid_credentials" {
		t.Fatal(status, out)
	}
	if status, _ = f.request(b, "POST", "/api/auth/login", boot["csrf"].(string), payload); status != 200 {
		t.Fatal(status)
	}
	bCSRF = f.boot(b)["csrf"].(string)
	if status, _ = f.request(b, "GET", base, "", ""); status != 200 {
		t.Fatal(status)
	}
	u, _ := http.NewRequest("GET", f.server.URL, nil)
	old := b.Jar.Cookies(u.URL)
	if status, _ = f.request(b, "POST", "/api/auth/logout", bCSRF, `{}`); status != 200 {
		t.Fatal(status)
	}
	stale := authClient()
	stale.Jar.SetCookies(u.URL, old)
	if status, _ = f.request(stale, "GET", base, "", ""); status != 401 {
		t.Fatal("revoked session", status)
	}
	if status, _ = f.request(a, "GET", base, "", ""); status != 200 {
		t.Fatal("other session revoked", status)
	}
	if f.boot(b)["user"] != nil {
		t.Fatal("logout still signed in")
	}
}

func TestAuthDatabaseFailureDoesNotFallBackToGuest(t *testing.T) {
	f := newAuthFixture(t)
	c := authClient()
	f.register(c, "alice")
	f.store.Close()
	for _, path := range []string{"/api/bootstrap", "/api/projects"} {
		status, out := f.request(c, "GET", path, "", "")
		if status != 503 || out["code"] == "login_required" {
			t.Fatal(status, out)
		}
	}
}
