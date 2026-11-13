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
}
