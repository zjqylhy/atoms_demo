package agent

import (
	"encoding/json"
	"errors"
	"mockatoms/internal/core"
	"mockatoms/internal/validation"
)

// Preserve structured validation issues alongside the readable error on both
// protocol retries and tool rejections. Apply the same redaction to each view.
func (e *Engine) failureFeedback(failure error) map[string]any {
	feedback := map[string]any{"ok": false, "error": e.Config.Redact(failure.Error())}
	var problems *validation.Error
	if errors.As(failure, &problems) {
		feedback["validation"] = json.RawMessage(e.Config.Redact(core.JSON(problems)))
	}
	return feedback
}
