package agent

import (
	"context"
	"errors"
	"mockatoms/internal/config"
	"mockatoms/internal/core"
	"mockatoms/internal/model"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestServiceRecoveryBoundsAndNoBudgetBypass(t *testing.T) {
	for _, status := range []int{429, 502, 503, 504} {
		err := &model.ServiceError{Status: status, Retryable: true, RetryAfter: 3 * time.Second}
		for attempt := 0; attempt < 3; attempt++ {
			delay, ok := transportRetry(err, attempt, 0, config.Config{})
			if !ok || delay < 3*time.Second || delay > 5*time.Second {
				t.Fatal(delay, ok)
			}
		}
		if _, ok := transportRetry(err, 3, 0, config.Config{}); ok {
			t.Fatal("retry budget reset")
		}
	}
	for _, err := range []error{&model.ServiceError{Status: 429, Budget: true}, &model.ServiceError{Status: 400}, context.Canceled} {
		if _, ok := transportRetry(err, 0, 0, config.Config{}); ok || model.CanBackup(err) {
			t.Fatal("invalid retry", err)
		}
	}
}

func TestMissingModelCreatesDistinctPersistentDemoWithoutCharging(t *testing.T) {
	e, p := testEngine(t, model.New(config.Config{}))
	started, err := e.Start("alice", p.ID, "制作阅读清单", "build", "missing-model", "submit", "")
	if err != nil {
		t.Fatal(err)
	}
	p = waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
	if p.Recovery == nil || p.Runs[started.ActiveRun].Calls != 0 || p.Tasks[p.CurrentTask].Status != "failed" || len(p.Files) != 0 {
		t.Fatal(core.JSON(p.Public()))
	}
	demo, err := e.Store.Get("alice", p.Recovery.ProjectID)
	if err != nil || demo.ID == p.ID || demo.Source.Kind != "fallback" || demo.Source.Template != "reading" || core.PreviewHTML(demo.Reports, demo.Files) == "" {
		t.Fatal(demo, err)
	}
	if _, err = e.Store.InstallFallback("alice", p.ID, started.ActiveRun, "board", "repeat"); err != nil {
		t.Fatal(err)
	}
	list, _ := e.Store.List("alice")
	if len(list) != 2 {
		t.Fatal("duplicated fallback", len(list))
	}
	if _, err = e.Start("alice", p.ID, "", "build", "retry-original", "continue", ""); err != nil {
		t.Fatal(err)
	}
	p = waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
	list, _ = e.Store.List("alice")
	if len(list) != 2 || p.Recovery.ProjectID != demo.ID {
		t.Fatal("retry created another demo")
	}
	plan, err := e.Store.Create("alice", "unapproved plan")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Start("alice", plan.ID, "规划网页", "plan", "plan-no-model", "submit", ""); err != nil {
		t.Fatal(err)
	}
	plan = waitProject(t, e, plan.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
	if plan.Recovery != nil {
		t.Fatal("unapproved plan created an implementation")
	}
}

func TestHTTPServiceRetryAndBackupChargeEverySend(t *testing.T) {
	for _, status := range []int{429, 503, 401} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var primary, backup atomic.Int32
			complete := func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"finish","type":"function","function":{"name":"finish_task","arguments":"{\"summary\":\"完成回答\",\"answer_only\":true}"}}]}}]}`))
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/backup/") {
					backup.Add(1)
					complete(w)
					return
				}
				calls := primary.Add(1)
				if status != 401 && calls == 2 {
					complete(w)
					return
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"message":"temporary"}}`))
			}))
			defer upstream.Close()
			cfg := config.Config{Protocol: "chat_completions", BaseURL: upstream.URL, Model: "demo", APIKey: "local-test-secret", BackupBaseURL: upstream.URL + "/backup", BackupModel: "demo", BackupAPIKey: "backup-test-secret"}
			e, p := testEngine(t, model.New(cfg))
			e.Config.BackupBaseURL = cfg.BackupBaseURL
			e.Config.BackupModel = cfg.BackupModel
			e.Config.BackupAPIKey = cfg.BackupAPIKey
			e.Config.Protocol = cfg.Protocol
			started, err := e.Start("alice", p.ID, "回答问题", "build", "service-recovery", "submit", "")
			if err != nil {
				t.Fatal(err)
			}
			p = waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
			if p.Runs[started.ActiveRun].Status != "completed" || p.Runs[started.ActiveRun].Calls != 2 || p.Recovery != nil {
				t.Fatal(core.JSON(p.Public()))
			}
			if status == 401 && (primary.Load() != 1 || backup.Load() != 1) {
				t.Fatal(primary.Load(), backup.Load())
			}
		})
	}
}

func TestStopAndWaitingDoNotCreateDemo(t *testing.T) {
	if demoFailure(context.Canceled) || demoFailure(core.ErrWaiting) || demoFailure(core.ErrStale) || demoFailure(errors.New("执行器不可用")) {
		t.Fatal("unrelated failure became model fallback")
	}
}

func TestBothModelRoutesFailToOneDemoWithoutExtraBackupRetries(t *testing.T) {
	var sends atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends.Add(1)
		if strings.HasPrefix(r.URL.Path, "/backup/") {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(401)
		}
		_, _ = w.Write([]byte(`{"error":{"message":"unavailable"}}`))
	}))
	defer upstream.Close()
	cfg := config.Config{Protocol: "chat_completions", BaseURL: upstream.URL, Model: "test", APIKey: "primary-secret", BackupBaseURL: upstream.URL + "/backup", BackupModel: "test", BackupAPIKey: "backup-secret"}
	e, p := testEngine(t, model.New(cfg))
	e.Config.Protocol = cfg.Protocol
	e.Config.BackupBaseURL = cfg.BackupBaseURL
	e.Config.BackupModel = cfg.BackupModel
	e.Config.BackupAPIKey = cfg.BackupAPIKey
	if _, err := e.Start("alice", p.ID, "构建应用", "build", "both-routes-fail", "submit", ""); err != nil {
		t.Fatal(err)
	}
	p = waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
	if sends.Load() != 2 || p.Recovery == nil || p.Tasks[p.CurrentTask].Status != "failed" {
		t.Fatal(sends.Load(), core.JSON(p.Public()))
	}
}
