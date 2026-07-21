package recorder

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func captureLogs(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)
	fn()
	return buf.String()
}

// TestLogStderrCollapsesRepeats covers the two per-frame warnings these cameras
// actually emit. Each distinct warning must be logged in full exactly once, then
// counted — and a one-off real error must never be collapsed away.
func TestLogStderrCollapsesRepeats(t *testing.T) {
	var lines []string
	for i := 0; i < 30; i++ {
		// Varying addresses and numbers must still collapse to one key.
		lines = append(lines,
			"[vost#0:0/copy @ 0x57a72935b580] Non-monotonic DTS; previous: 78458"+string(rune('0'+i%10))+", current: 784583; changing to 784584",
			"[hevc @ 0x64ea1c44168"+string(rune('0'+i%10))+"] Skipping invalid undecodable NALU: 39",
		)
	}
	lines = append(lines, "Connection to tcp://127.0.0.1:8554 failed: Connection refused")

	out := captureLogs(t, func() {
		<-logStderr("cam1", strings.NewReader(strings.Join(lines, "\n")+"\n"))
	})

	if got := strings.Count(out, "Non-monotonic DTS; previous"); got != 2 {
		// One verbatim first sighting + one summary line carrying the example.
		t.Errorf("DTS warning appeared %d times, want 2 (first sighting + summary)", got)
	}
	if got := strings.Count(out, "undecodable NALU"); got != 2 {
		t.Errorf("NALU warning appeared %d times, want 2", got)
	}
	if got := strings.Count(out, "count=29"); got != 2 {
		t.Errorf("expected both warnings summarised with count=29, got:\n%s", out)
	}
	if got := strings.Count(out, "Connection refused"); got != 1 {
		t.Errorf("real error appeared %d times, want 1", got)
	}
}

func TestNormalizeWarningCollapsesVariants(t *testing.T) {
	a := normalizeWarning("[hevc @ 0x64ea1c441680] Skipping invalid undecodable NALU: 39")
	b := normalizeWarning("[hevc @ 0x64ea1c2c2b00] Skipping invalid undecodable NALU: 39")
	if a != b {
		t.Errorf("addresses not collapsed:\n%s\n%s", a, b)
	}

	c := normalizeWarning("Non-monotonic DTS; previous: 784583, current: 784583; changing to 784584")
	d := normalizeWarning("Non-monotonic DTS; previous: 921383, current: 921383; changing to 921384")
	if c != d {
		t.Errorf("numbers not collapsed:\n%s\n%s", c, d)
	}

	// Distinct warnings must not collapse into each other.
	if normalizeWarning("Failed reading RTSP data") == normalizeWarning("Error constructing the frame RPS") {
		t.Error("distinct warnings collapsed to the same key")
	}
}
