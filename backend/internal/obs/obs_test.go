package obs

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestLoggerStampsContext(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger(&buf, slog.LevelInfo).With("job_type", "transcribe")
	ctx := WithUserID(WithCorrelationID(context.Background(), "c-1"), "u-1")
	l.InfoContext(ctx, "job.claimed", "job_id", "j1")
	l.Debug("dropped")
	out := buf.String()
	for _, want := range []string{`"msg":"job.claimed"`, `"correlation_id":"c-1"`, `"user_id":"u-1"`, `"job_type":"transcribe"`, `"job_id":"j1"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in %s", want, out)
		}
	}
	if strings.Contains(out, "dropped") || CorrelationID(context.Background()) != "" {
		t.Fatal(out)
	}
}
