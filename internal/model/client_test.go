package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mockatoms/internal/config"
	"mockatoms/internal/core"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestReservationMatchesSerializedRequestBytes(t *testing.T) {
	const maxOutput = 12288
	messages := []core.ModelMessage{
		{Role: "system", Content: "检查产物"},
		{Role: "user", Content: "生成网页，包含 <main>、引号 \" 和换行\n。"},
	}
	client := New(config.Config{Protocol: "chat_completions", ToolMode: "json", Model: "test", APIKey: "test-key", MaxOutputTokens: maxOutput})
	request, err := client.PrepareRequest(messages, nil, len(messages))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body)+maxOutput != request.ReservationTokens() {
			t.Errorf("reservation = %d, request bytes = %d, read error = %v", request.ReservationTokens(), len(body), err)
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{\"content\":\"已完成\",\"calls\":[]}"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	client.Config.BaseURL = server.URL
	if _, _, err := client.Generate(context.Background(), messages, nil); err != nil {
		t.Fatal(err)
	}
}

func TestDialogueOutputBudget(t *testing.T) {
	for _, tc := range []struct{ configured, want int }{{1024, 1024}, {16384, 8192}} {
		client := New(config.Config{Model: "test", MaxOutputTokens: tc.configured})
		request, err := client.PrepareDialogue("旧对话")
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			MaxTokens int `json:"max_tokens"`
		}
		if err := json.Unmarshal(request.body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.MaxTokens != tc.want || request.ReservationTokens() != request.ByteSize()+tc.want {
			t.Fatalf("configured=%d, output=%d, reservation=%d", tc.configured, payload.MaxTokens, request.ReservationTokens())
		}
	}
}

func TestModelRequestByteLimit(t *testing.T) {
	const defaultLimit = 500 * 1024
	client := New(config.Config{Protocol: "chat_completions", Model: "test", APIKey: "test-key"})
	messages := []core.ModelMessage{{Role: "user"}}
	empty, err := client.PrepareRequest(messages, nil, len(messages))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength != defaultLimit {
			t.Errorf("sent %d bytes, want %d", r.ContentLength, defaultLimit)
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("read request: %v", err)
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	client.Config.BaseURL = server.URL
	messages[0].Content = strings.Repeat("a", defaultLimit-empty.ByteSize())
	if _, _, err := client.Generate(context.Background(), messages, nil); err != nil {
		t.Fatal(err)
	}
	messages[0].Content += "a"
	if _, _, err := client.Generate(context.Background(), messages, nil); err == nil || !strings.Contains(err.Error(), "请求 512001 字节，超过 512000 字节上限") {
		t.Fatalf("default limit error = %v", err)
	}
	t.Setenv("ATOMS_MAX_REQUEST_BYTES", "65536")
	client.Config.MaxRequestBytes = config.Load().MaxRequestBytes
	messages[0].Content = strings.Repeat("a", 65537-empty.ByteSize())
	if _, _, err := client.Generate(context.Background(), messages, nil); err == nil || !strings.Contains(err.Error(), "请求 65537 字节，超过 65536 字节上限") {
		t.Fatalf("configured limit error = %v", err)
	}
}

func TestOnlyGatewayModelTPMIsRetryable(t *testing.T) {
	for _, tt := range []struct {
		reason    string
		status    int
		retryable bool
	}{
		{"model_tpm", http.StatusTooManyRequests, true},
		{"model_rpm", http.StatusTooManyRequests, false},
		{"monthly_budget", http.StatusTooManyRequests, false},
		{"", http.StatusTooManyRequests, false},
		{"model_tpm", http.StatusServiceUnavailable, false},
	} {
		t.Run(tt.reason+http.StatusText(tt.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "3")
				w.WriteHeader(tt.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
					"message": "rate, token quota or budget exceeded",
					"code":    "insufficient_quota",
					"reason":  tt.reason,
				}})
			}))
			defer server.Close()
			client := New(config.Config{Protocol: "chat_completions", BaseURL: server.URL, Model: "test", APIKey: "test-key"})
			_, usage, err := client.Generate(context.Background(), nil, nil)
			var throttle *TPMThrottleError
			if err == nil || errors.As(err, &throttle) != tt.retryable || usage.Known {
				t.Fatalf("error = %v, usage = %+v", err, usage)
			}
			if tt.retryable && throttle.RetryAfter != 3*time.Second {
				t.Fatalf("retry delay = %s", throttle.RetryAfter)
			}
		})
	}
}
