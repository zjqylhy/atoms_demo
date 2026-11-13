package model

import (
	"context"
	"errors"
	"mockatoms/internal/config"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConnectionLossIsRetryableButUnknownUsageRemainsUnknown(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	defer upstream.Close()
	client := New(config.Config{Protocol: "chat_completions", BaseURL: upstream.URL, Model: "test", APIKey: "test-secret"})
	_, usage, err := client.Generate(context.Background(), nil, nil)
	var failure *ServiceError
	if !errors.As(err, &failure) || !failure.Retryable || usage.Known {
		t.Fatal(err, usage)
	}
}
