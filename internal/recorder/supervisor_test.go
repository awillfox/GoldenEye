package recorder

import (
	"context"
	"os"
	"testing"
	"time"

	"golden_eye/internal/config"
	"golden_eye/sqlc"
)

// newTestSupervisor returns a Supervisor that runs /bin/sh instead of ffmpeg,
// with a short backoff so restart behaviour is observable in a test.
func newTestSupervisor(t *testing.T, shArgs ...string) *Supervisor {
	t.Helper()
	settings, err := config.NewManager(t.TempDir() + "/config.json")
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	s := New(Config{RecordingsDir: t.TempDir(), RTSPBase: "rtsp://127.0.0.1:8554"}, settings)
	s.binary = "/bin/sh"
	s.baseBackoff = 10 * time.Millisecond
	s.maxBackoff = 20 * time.Millisecond
	s.buildArgsFn = func(cam sqlc.Camera, segmentMinutes int, cfg Config) []string {
		return shArgs
	}
	return s
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func TestSupervisorRestartsOnExit(t *testing.T) {
	s := newTestSupervisor(t, "-c", "exit 1")
	defer s.Stop()

	s.Sync(context.Background(), []sqlc.Camera{{Name: "cam1", Enabled: true}})

	ok := waitFor(t, 3*time.Second, func() bool {
		return s.Status()["cam1"].Restarts >= 2
	})
	if !ok {
		t.Fatalf("expected at least 2 restarts, got %d", s.Status()["cam1"].Restarts)
	}
}

func TestSupervisorSyncLeavesUnchangedProcessAlone(t *testing.T) {
	s := newTestSupervisor(t, "-c", "sleep 30")
	defer s.Stop()

	cams := []sqlc.Camera{{ID: 1, Name: "cam1", Enabled: true}}
	s.Sync(context.Background(), cams)

	if !waitFor(t, 2*time.Second, func() bool { return s.Status()["cam1"].PID != 0 }) {
		t.Fatal("process never started")
	}
	first := s.Status()["cam1"].PID

	s.Sync(context.Background(), cams) // identical desired state
	time.Sleep(100 * time.Millisecond)

	if got := s.Status()["cam1"].PID; got != first {
		t.Errorf("process restarted unnecessarily: pid %d -> %d", first, got)
	}
}

func TestSupervisorStopsDisabledCamera(t *testing.T) {
	s := newTestSupervisor(t, "-c", "sleep 30")
	defer s.Stop()

	s.Sync(context.Background(), []sqlc.Camera{{ID: 1, Name: "cam1", Enabled: true}})
	if !waitFor(t, 2*time.Second, func() bool { return s.Status()["cam1"].PID != 0 }) {
		t.Fatal("process never started")
	}

	s.Sync(context.Background(), []sqlc.Camera{{ID: 1, Name: "cam1", Enabled: false}})

	// Generous timeout: /bin/sh may not forward SIGTERM to its `sleep` child, so
	// teardown can take the full termGrace before SIGKILL lands.
	if !waitFor(t, 10*time.Second, func() bool { _, ok := s.Status()["cam1"]; return !ok }) {
		t.Fatal("disabled camera still running")
	}
}

func TestSupervisorCreatesOutputDir(t *testing.T) {
	s := newTestSupervisor(t, "-c", "sleep 30")
	defer s.Stop()

	s.Sync(context.Background(), []sqlc.Camera{{ID: 1, Name: "cam1", Enabled: true}})

	dir := OutputDir(s.cfg, "cam1")
	if !waitFor(t, 2*time.Second, func() bool {
		_, err := os.Stat(dir)
		return err == nil
	}) {
		t.Fatalf("output dir %s not created", dir)
	}
}
