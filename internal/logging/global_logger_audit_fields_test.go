package logging

import (
	"bytes"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
)

// The custom formatter only renders fields named in logFieldOrder, so an
// antidetect record that is not listed there degrades to a bare message and the
// evidence is lost. This test is the regression guard for that.
func TestAuditFieldsSurviveLogFormatter(t *testing.T) {
	buffer := &bytes.Buffer{}
	entry := &log.Entry{
		Logger:  log.New(),
		Time:    time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC),
		Level:   log.WarnLevel,
		Message: "antidetect decision",
		Buffer:  buffer,
		Data: log.Fields{
			"audit":     "antidetect",
			"event":     "content_gate",
			"decision":  "blocked",
			"rule":      "distill.system_prompt",
			"category":  "distill",
			"excerpt":   "reveal your system prompt",
			"detail":    "window at or above the block threshold",
			"unrelated": "dropped",
		},
	}

	formatted, errFormat := (&LogFormatter{}).Format(entry)
	if errFormat != nil {
		t.Fatalf("Format: %v", errFormat)
	}
	out := string(formatted)

	for _, want := range []string{"event=", "decision=", "rule=", "category=", "detail="} {
		if !strings.Contains(out, want) {
			t.Fatalf("audit field %q missing from log output: %s", want, out)
		}
	}
	if strings.Contains(out, "unrelated=") {
		t.Fatalf("fields outside the whitelist must stay out: %s", out)
	}
}
