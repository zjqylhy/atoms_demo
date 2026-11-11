package model

import (
	"context"
	"encoding/json"
	"mockatoms/internal/config"
	"mockatoms/internal/core"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProtocolsAndSecrets(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer private-test-key" {
					t.Error("missing key")
				}
				var payload map[string]any
				_ = json.NewDecoder(r.Body).Decode(&payload)
				if strings.Contains(core.JSON(payload), "private-test-key") {
					t.Error("key in prompt")
				}
				w.Header().Set("Content-Type", "application/json")
				if protocol == "responses" {
					w.Write([]byte(`{"status":"completed","output":[{"type":"reasoning","id":"opaque"},{"type":"function_call","call_id":"c1","name":"read_file","arguments":"{\"path\":\"a.txt\"}"}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
				} else {
					w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.txt\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
				}
			}))
			defer server.Close()
			c := New(config.Config{Protocol: protocol, BaseURL: server.URL, Model: "test", APIKey: "private-test-key"})
			m, u, e := c.Generate(context.Background(), []core.ModelMessage{{Role: "user", Content: "read"}}, nil)
			if e != nil {
				t.Fatal(e)
			}
			if len(m.ToolCalls) != 1 || !u.Known || u.Total != 5 {
				t.Fatalf("bad reply: %+v %+v", m, u)
			}
			if protocol == "responses" && len(m.ProtocolItems) != 2 {
				t.Fatal("protocol items lost")
			}
		})
	}
}
func TestRejectRedirectAndRedact(t *testing.T) {
	called := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer source.Close()
	c := New(config.Config{Protocol: "chat_completions", BaseURL: source.URL, Model: "test", APIKey: "secret"})
	if _, _, e := c.Generate(context.Background(), nil, nil); e == nil || called {
		t.Fatal("redirect was followed")
	}
	source.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"message":"secret invalid"}}`))
	})
	_, _, e := c.Generate(context.Background(), nil, nil)
	if e == nil || strings.Contains(e.Error(), "secret") {
		t.Fatal("error leaked key")
	}
}
