package httpapi

import (
	"mockatoms/internal/core"
	"testing"
)

func TestProjectOrderHTTPSharedCSRFAndConflict(t *testing.T) {
	f := newAuthFixture(t)
	a, b := authClient(), authClient()
	aCSRF, bCSRF := f.register(a, "order_a"), f.register(b, "order_b")
	_, p1 := f.request(a, "POST", "/api/projects", aCSRF, `{"title":"one"}`)
	_, p2 := f.request(a, "POST", "/api/projects", aCSRF, `{"title":"two"}`)
	body := core.JSON(map[string]any{"revision": 0, "ids": []any{p1["id"], p2["id"]}})
	if code, _ := f.request(a, "PUT", "/api/project-order", "", body); code != 403 {
		t.Fatal("CSRF", code)
	}
	if code, result := f.request(a, "PUT", "/api/project-order", aCSRF, body); code != 200 || result["revision"] != float64(1) {
		t.Fatal(code, result)
	}
	if code, result := f.request(b, "GET", "/api/project-order", "", ""); code != 200 || result["projects"].([]any)[0].(map[string]any)["id"] != p1["id"] {
		t.Fatal(code, result)
	}
	stale := core.JSON(map[string]any{"revision": 0, "ids": []any{p2["id"], p1["id"]}})
	if code, result := f.request(b, "PUT", "/api/project-order", bCSRF, stale); code != 409 || result["code"] != "order_conflict" {
		t.Fatal(code, result)
	}
	if code, _ := f.request(b, "PUT", "/api/project-order", bCSRF, `{"revision":1,"ids":["bad","bad"]}`); code != 400 {
		t.Fatal(code)
	}
}
