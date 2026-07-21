package recorder

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestLogStderrSuppressesNoisyWarning checks that the per-frame DTS warning is
// logged once and then counted, while unrelated ffmpeg output still passes
// through untouched.
func TestLogStderrSuppressesNoisyWarning(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	var lines []string
	for i := 0; i < 50; i++ {
		lines = append(lines, "[vost#0:0/copy @ 0x1] Non-monotonic DTS; previous: 1, current: 1; changing to 2")
	}
	lines = append(lines, "Connection to tcp://127.0.0.1:8554 failed: Connection refused")

	<-logStderr("cam1", strings.NewReader(strings.Join(lines, "\n")+"\n"))

	out := buf.String()

	if got := strings.Count(out, "Non-monotonic DTS; previous"); got != 1 {
		t.Errorf("full noisy line logged %d times, want 1", got)
	}
	if !strings.Contains(out, "count=49") {
		t.Errorf("expected summary with count=49, got:\n%s", out)
	}
	if !strings.Contains(out, "Connection refused") {
		t.Error("real ffmpeg error was swallowed")
	}
}
