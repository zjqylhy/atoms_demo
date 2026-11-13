package agent

import (
	"encoding/json"
	"errors"
	"mockatoms/internal/core"
	"mockatoms/internal/validation"
	"strings"
)

// Preserve structured validation issues alongside the readable error on both
// protocol retries and tool rejections. Apply the same redaction to each view.
func (e *Engine) failureFeedback(failure error) map[string]any {
	feedback := map[string]any{"ok": false, "error": e.Config.Redact(failure.Error())}
	var problems *validation.Error
	if errors.As(failure, &problems) {
		redact := func(text string) string {
			text = e.Config.Redact(text)
			// Syntax excerpts contain JSON escapes, while paths contain decoded text.
			for _, secret := range []string{e.Config.APIKey, e.Config.BackupAPIKey, e.Config.RunnerToken} {
				if secret != "" {
					encoded := core.JSON(secret)
					text = strings.ReplaceAll(text, encoded[1:len(encoded)-1], "[REDACTED]")
				}
			}
			return text
		}
		safe := problems.Redacted(redact)
		feedback["error"] = redact(strings.Replace(failure.Error(), problems.Error(), safe.Error(), 1))
		feedback["validation"] = safe
	}
	var assessment *AcceptanceAssessment
	if errors.As(failure, &assessment) {
		feedback["acceptance"] = json.RawMessage(e.Config.Redact(core.JSON(assessment)))
	}
	return feedback
}
