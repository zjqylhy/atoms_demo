package agent

import (
	"context"
	"errors"
	"math/rand/v2"
	"mockatoms/internal/config"
	"mockatoms/internal/model"
	"strings"
	"time"
)

// One transport retry counter covers every service error in the current action.
// TPM keeps its existing two retries; other transient failures allow three.
func transportRetry(err error, attempt, reserved int, c config.Config) (time.Duration, bool) {
	var tpm *model.TPMThrottleError
	if errors.As(err, &tpm) {
		if attempt >= 2 {
			return 0, false
		}
		wait := tpm.RetryAfter
		if c.ProviderTPM > 0 {
			wait = max(wait, time.Duration(float64(time.Minute)*float64(reserved)/float64(c.ProviderTPM)))
		}
		if wait <= 0 {
			wait = time.Second
		}
		return wait, true
	}
	var service *model.ServiceError
	if !errors.As(err, &service) || !service.Retryable || service.Budget || attempt >= 3 {
		return 0, false
	}
	return max(time.Second*time.Duration(1<<attempt)+time.Duration(rand.IntN(250))*time.Millisecond, service.RetryAfter), true
}

func demoFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var service *model.ServiceError
	var throttle *model.TPMThrottleError
	if errors.As(err, &service) || errors.As(err, &throttle) {
		return true
	}
	text := err.Error()
	return strings.Contains(text, "模型") || strings.Contains(text, "对话摘要")
}

func demoTemplate(goal string) string {
	if strings.Contains(goal, "阅读") || strings.Contains(goal, "书单") {
		return "reading"
	}
	if strings.Contains(goal, "计算") || strings.Contains(goal, "复利") {
		return "calculator"
	}
	return "board"
}
