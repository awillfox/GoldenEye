# Browser-playable recordings Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Recordings play with sound (and picture, for the H.265 camera) in Firefox, with segment boundaries aligned to the wall clock.

**Architecture:** mediamtx stops recording. GoldenEye supervises one `ffmpeg` process per enabled camera; each reads mediamtx's already-connected native RTSP path, converts G711 audio to AAC (and H.265 video to H.264 where flagged), and writes clock-aligned fragmented-MP4 segments straight into `RECORDINGS_DIR/<camera>/`. A new `cameras.transcode_video` column drives copy-vs-encode. A stale-segment check replaces the safety net mediamtx's recorder used to provide. A one-time script converts the ~34 GB already on disk.

**Tech Stack:** Go 1.26, chi v5, pgx v5 + sqlc, Atlas, viper, `html/template` + Tailwind, mediamtx v1.9.3 HTTP API, ffmpeg 8.0.1 (`libx264`, `aac`).

**Spec:** `docs/superpowers/specs/2026-07-21-recording-audio-transcode-design.md`

## Global Constraints

- Never hand-edit `sqlc/` — regenerate with `task sqlcgen`.
- Schema change order: edit `schema.hcl` → `task migrate-dev` → `task generate-sql-schema` → `task sqlcgen`. Commit generated files with the change.
- Settings (`retention_days`, `segment_minutes`) live in `config.json`, never the DB. Secrets live in `.env`.
- Segment filename format must stay `%Y-%m-%d_%H-%M-%S.mp4` inside `RECORDINGS_DIR/<camera>/` — the indexer, thumbnails, and rotation all depend on it.
- The `<name>-preview` mediamtx path and its `runOnDemand` config are out of scope and must not change: WebRTC carries no AAC, so the preview keeps G711.
- Deploy host for verification: `coffee@100.86.43.70`, app dir `/home/coffee/goldeneye`, recordings at `/home/coffee/goldeneye/recordings`.
- ffmpeg segment options, verified working on that host, exact values:
  `-f segment -segment_time <secs> -segment_atclocktime 1 -segment_format mp4 -segment_format_options movflags=+frag_keyframe+empty_moov+default_base_moof -reset_timestamps 1 -strftime 1`
- Video encode args for flagged cameras, exact values:
  `-c:v libx264 -preset ultrafast -tune zerolatency -pix_fmt yuv420p -g 60`
- Audio args for every camera, exact values: `-c:a aac -b:a 32k`

## File Structure

| File | Responsibility |
|---|---|
| `schema.hcl`, `schema.sql`, `sqlc/` | `cameras.transcode_video` column (generated files follow) |
| `internal/sql/cameras.sql` | create/update carry `transcode_video` |
| `internal/sql/recordings.sql` | new `ListCameraRecordingHealth` query |
| `internal/recorder/args.go` | pure `buildArgs` — camera + settings → ffmpeg argv |
| `internal/recorder/args_test.go` | table test for argv |
| `internal/recorder/supervisor.go` | process lifecycle: start, restart with backoff, stop, `Sync`, `Status` |
| `internal/recorder/supervisor_test.go` | restart/stop/sync behaviour with a fake binary |
| `internal/camera/service.go` | `transcode_video` plumbing; mediamtx recorder switched off; recorder resync hook |
| `internal/web/server.go` | camera create/update carry the flag; dashboard passes stale map |
| `internal/web/templates/dashboard.html` | transcode checkbox; stale badge |
| `internal/index/indexer.go` | stale warning each scan |
| `main.go` | construct supervisor, sync on startup, stop on shutdown |
| `scripts/backfill_aac.sh` | one-time conversion of existing footage |

---

### Task 1: `transcode_video` column, end to end

**Files:**
- Modify: `schema.hcl` (cameras table)
- Modify: `internal/sql/cameras.sql`
- Modify: `internal/camera/service.go:85` (`Create`), `internal/camera/service.go:110` (`Update`)
- Modify: `internal/web/server.go:316` (`apiCreateCamera`), `internal/web/server.go:335` (`apiUpdateCamera`)
- Modify: `internal/web/templates/dashboard.html` (form field + `editCamera` + submit handler)
- Regenerated: `schema.sql`, `sqlc/models.go`, `sqlc/cameras.sql.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `sqlc.Camera.TranscodeVideo bool`; `camera.Service.Create(ctx, name, rtspURL string, transcodeVideo bool)`; `camera.Service.Update(ctx, id int64, name, rtspURL string, enabled, transcodeVideo bool)`.

- [ ] **Step 1: Add the column to `schema.hcl`**

In `table "cameras"`, directly after the `column "codec"` block:

```hcl
  column "transcode_video" {
    type    = boolean
    null    = false
    default = false
    comment = "Re-encode video to H.264 when recording (needed for H.265 cameras)"
  }
```

- [ ] **Step 2: Apply and regenerate**

```bash
task migrate-dev
task generate-sql-schema
```

Expected: `atlas` reports the `ALTER TABLE "cameras" ADD COLUMN "transcode_video"` applied; `schema.sql` now contains `transcode_video`.

- [ ] **Step 3: Update the queries**

In `internal/sql/cameras.sql`, replace the `CreateCamera` and `UpdateCamera` queries:

```sql
-- name: CreateCamera :one
INSERT INTO cameras (name, rtsp_url, enabled, transcode_video)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: UpdateCamera :one
UPDATE cameras
SET name            = $2,
    rtsp_url        = $3,
    enabled         = $4,
    codec           = $5,
    transcode_video = $6,
    updated_at      = now()
WHERE id = $1
RETURNING *;
```

- [ ] **Step 4: Regenerate sqlc**

```bash
task sqlcgen
grep -n "TranscodeVideo" sqlc/models.go sqlc/cameras.sql.go
```

Expected: `TranscodeVideo bool` on `sqlc.Camera`, `CreateCameraParams`, and `UpdateCameraParams`.

- [ ] **Step 5: Thread the flag through the camera service**

`internal/camera/service.go` — change the two signatures and the params structs:

```go
func (s *Service) Create(ctx context.Context, name, rtspURL string, transcodeVideo bool) (*sqlc.Camera, error) {
	if !validName.MatchString(name) {
		return nil, fmt.Errorf("camera name must be lowercase alphanumeric/dash/underscore, 1-63 chars")
	}

	cam, err := s.q.CreateCamera(ctx, sqlc.CreateCameraParams{
		Name:           name,
		RtspUrl:        rtspURL,
		Enabled:        true,
		TranscodeVideo: transcodeVideo,
	})
```

```go
func (s *Service) Update(ctx context.Context, id int64, name, rtspURL string, enabled, transcodeVideo bool) (*sqlc.Camera, error) {
	if !validName.MatchString(name) {
		return nil, fmt.Errorf("camera name must be lowercase alphanumeric/dash/underscore, 1-63 chars")
	}

	old, err := s.q.GetCamera(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get camera %d: %w", id, err)
	}

	cam, err := s.q.UpdateCamera(ctx, sqlc.UpdateCameraParams{
		ID:             id,
		Name:           name,
		RtspUrl:        rtspURL,
		Enabled:        enabled,
		Codec:          old.Codec,
		TranscodeVideo: transcodeVideo,
	})
```

- [ ] **Step 6: Thread it through the HTTP handlers**

`internal/web/server.go` — `apiCreateCamera`:

```go
	var body struct {
		Name           string `json:"name"`
		RtspURL        string `json:"rtsp_url"`
		TranscodeVideo bool   `json:"transcode_video"`
	}
	if err := render.DecodeJSON(r.Body, &body); err != nil {
		renderErr(w, r, http.StatusBadRequest, err)
		return
	}

	cam, err := s.camSvc.Create(r.Context(), body.Name, body.RtspURL, body.TranscodeVideo)
```

`apiUpdateCamera`:

```go
	var body struct {
		Name           string `json:"name"`
		RtspURL        string `json:"rtsp_url"`
		Enabled        bool   `json:"enabled"`
		TranscodeVideo bool   `json:"transcode_video"`
	}
	if err = render.DecodeJSON(r.Body, &body); err != nil {
		renderErr(w, r, http.StatusBadRequest, err)
		return
	}

	cam, err := s.camSvc.Update(r.Context(), id, body.Name, body.RtspURL, body.Enabled, body.TranscodeVideo)
```

- [ ] **Step 7: Add the checkbox to the dashboard**

`internal/web/templates/dashboard.html` — after the existing "Enabled" checkbox block (the `div` containing `id="camera-enabled"`), add:

```html
      <div class="flex items-center gap-3 px-4 py-3 rounded-2xl shadow-neu-inset-sm">
        <input id="camera-transcode" type="checkbox" class="w-4 h-4 rounded accent-neu-accent"/>
        <label class="text-sm font-medium text-neu-fg">Transcode video to H.264 <span class="text-neu-muted">(needed for H.265 cameras)</span></label>
      </div>
```

In the same file, the Edit button's `onclick` (currently `editCamera({{.ID}}, '{{.Name}}', '{{.RtspUrl}}', {{.Enabled}})`) becomes:

```html
            onclick="editCamera({{.ID}}, '{{.Name}}', '{{.RtspUrl}}', {{.Enabled}}, {{.TranscodeVideo}})"
```

`openCameraModal` (the function that sets `camera-enabled` to `true`) gains:

```js
  document.getElementById('camera-transcode').checked = false;
```

`editCamera` becomes:

```js
function editCamera(id, name, rtsp, enabled, transcode) {
  document.getElementById('camera-modal-title').textContent = 'Edit Camera';
  document.getElementById('camera-id').value = id;
  document.getElementById('camera-name').value = name;
  document.getElementById('camera-rtsp').value = rtsp;
  document.getElementById('camera-enabled').checked = enabled;
  document.getElementById('camera-transcode').checked = transcode;
  document.getElementById('camera-error').classList.add('hidden');
  document.getElementById('camera-modal').classList.remove('hidden');
}
```

The form submit body becomes:

```js
  const body = {
    name: document.getElementById('camera-name').value,
    rtsp_url: document.getElementById('camera-rtsp').value,
    enabled: document.getElementById('camera-enabled').checked,
    transcode_video: document.getElementById('camera-transcode').checked,
  };
```

- [ ] **Step 8: Build and test**

```bash
go build ./... && task test
```

Expected: build succeeds, all existing tests pass. (No new test here — this task is schema plumbing with no logic; the behaviour it enables is tested in Task 2.)

- [ ] **Step 9: Commit**

```bash
git add schema.hcl schema.sql sqlc/ internal/sql/cameras.sql internal/camera/service.go internal/web/server.go internal/web/templates/dashboard.html
git commit -m "feat(camera): add transcode_video flag"
```

---

### Task 2: `recorder.buildArgs`

**Files:**
- Create: `internal/recorder/args.go`
- Test: `internal/recorder/args_test.go`

**Interfaces:**
- Consumes: `sqlc.Camera` (including `TranscodeVideo` from Task 1).
- Produces:
  - `type Config struct { RecordingsDir string; RTSPBase string }`
  - `func buildArgs(cam sqlc.Camera, segmentMinutes int, cfg Config) []string` — ffmpeg argv **without** the binary name.
  - `func OutputDir(cfg Config, camName string) string`

- [ ] **Step 1: Write the failing test**

Create `internal/recorder/args_test.go`:

```go
package recorder

import (
	"strings"
	"testing"

	"golden_eye/sqlc"
)

func argsString(args []string) string { return strings.Join(args, " ") }

func TestBuildArgsCopyCamera(t *testing.T) {
	cam := sqlc.Camera{Name: "front-door", TranscodeVideo: false}
	cfg := Config{RecordingsDir: "/rec", RTSPBase: "rtsp://127.0.0.1:8554"}

	got := argsString(buildArgs(cam, 10, cfg))

	for _, want := range []string{
		"-i rtsp://127.0.0.1:8554/front-door",
		"-c:v copy",
		"-c:a aac -b:a 32k",
		"-segment_time 600",
		"-segment_atclocktime 1",
		"-segment_format_options movflags=+frag_keyframe+empty_moov+default_base_moof",
		"/rec/front-door/%Y-%m-%d_%H-%M-%S.mp4",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "libx264") {
		t.Errorf("copy camera must not encode:\n%s", got)
	}
}

func TestBuildArgsTranscodeCamera(t *testing.T) {
	cam := sqlc.Camera{Name: "cashier", TranscodeVideo: true}
	cfg := Config{RecordingsDir: "/rec", RTSPBase: "rtsp://127.0.0.1:8554"}

	got := argsString(buildArgs(cam, 10, cfg))

	if !strings.Contains(got, "-c:v libx264 -preset ultrafast -tune zerolatency -pix_fmt yuv420p -g 60") {
		t.Errorf("missing encode args:\n%s", got)
	}
	if strings.Contains(got, "-c:v copy") {
		t.Errorf("transcode camera must not copy video:\n%s", got)
	}
}

func TestBuildArgsSegmentMinutes(t *testing.T) {
	cases := []struct {
		minutes int
		want    string
	}{
		{1, "-segment_time 60"},
		{10, "-segment_time 600"},
		{60, "-segment_time 3600"},
		{0, "-segment_time 600"},  // invalid input falls back to the 10-minute default
		{-5, "-segment_time 600"}, // same
	}
	cfg := Config{RecordingsDir: "/rec", RTSPBase: "rtsp://127.0.0.1:8554"}
	for _, tc := range cases {
		got := argsString(buildArgs(sqlc.Camera{Name: "c"}, tc.minutes, cfg))
		if !strings.Contains(got, tc.want) {
			t.Errorf("minutes=%d: missing %q in:\n%s", tc.minutes, tc.want, got)
		}
	}
}

func TestOutputDir(t *testing.T) {
	got := OutputDir(Config{RecordingsDir: "/rec"}, "front-door")
	if got != "/rec/front-door" {
		t.Errorf("got %q, want %q", got, "/rec/front-door")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/recorder/ -run TestBuildArgs -v`
Expected: FAIL — `undefined: buildArgs`, `undefined: Config`.

- [ ] **Step 3: Write the implementation**

Create `internal/recorder/args.go`:

```go
// Package recorder supervises one ffmpeg process per enabled camera. Each
// process reads mediamtx's already-connected native path, converts the camera's
// G711 audio to AAC (browsers cannot decode G711 inside MP4), optionally
// re-encodes H.265 video to H.264, and writes clock-aligned fragmented-MP4
// segments straight into RECORDINGS_DIR/<camera>/.
package recorder

import (
	"path/filepath"
	"strconv"

	"golden_eye/sqlc"
)

// defaultSegmentMinutes mirrors config.Manager's default and guards against a
// zero or negative value reaching ffmpeg, where it would disable segmenting.
const defaultSegmentMinutes = 10

// Config holds the deployment-level values the recorder needs.
type Config struct {
	RecordingsDir string // RECORDINGS_DIR
	RTSPBase      string // MEDIAMTX_RTSP, e.g. rtsp://127.0.0.1:8554
}

// OutputDir returns the directory segments for a camera are written to.
func OutputDir(cfg Config, camName string) string {
	return filepath.Join(cfg.RecordingsDir, camName)
}

// buildArgs returns the ffmpeg argv (excluding the binary name) for one camera.
//
// The input is mediamtx's native path for the camera, so the camera itself is
// never opened a second time. Audio is always re-encoded to AAC. Video is copied
// unless the camera is flagged for transcoding, which costs real CPU (measured:
// ~140% of one core for a 4K H.265 stream) and is only needed for cameras
// browsers cannot decode.
//
// Segments are cut by ffmpeg's segment muxer with -segment_atclocktime, so
// boundaries land on wall-clock multiples of the segment duration rather than
// counting from stream start. The fragmented-MP4 flags keep a segment readable
// even if the process is killed mid-write.
func buildArgs(cam sqlc.Camera, segmentMinutes int, cfg Config) []string {
	if segmentMinutes <= 0 {
		segmentMinutes = defaultSegmentMinutes
	}

	args := []string{
		"-nostdin",
		"-loglevel", "warning",
		"-rtsp_transport", "tcp",
		"-i", cfg.RTSPBase + "/" + cam.Name,
	}

	if cam.TranscodeVideo {
		args = append(args,
			"-c:v", "libx264",
			"-preset", "ultrafast",
			"-tune", "zerolatency",
			"-pix_fmt", "yuv420p",
			"-g", "60",
		)
	} else {
		args = append(args, "-c:v", "copy")
	}

	args = append(args,
		"-c:a", "aac",
		"-b:a", "32k",
		"-f", "segment",
		"-segment_time", strconv.Itoa(segmentMinutes*60),
		"-segment_atclocktime", "1",
		"-segment_format", "mp4",
		"-segment_format_options", "movflags=+frag_keyframe+empty_moov+default_base_moof",
		"-reset_timestamps", "1",
		"-strftime", "1",
		filepath.Join(OutputDir(cfg, cam.Name), "%Y-%m-%d_%H-%M-%S.mp4"),
	)

	return args
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/recorder/ -v`
Expected: PASS — four tests.

- [ ] **Step 5: Commit**

```bash
git add internal/recorder/args.go internal/recorder/args_test.go
git commit -m "feat(recorder): ffmpeg argv builder for AAC clock-aligned segments"
```

---

### Task 3: `recorder.Supervisor`

**Files:**
- Create: `internal/recorder/supervisor.go`
- Test: `internal/recorder/supervisor_test.go`

**Interfaces:**
- Consumes: `buildArgs`, `Config`, `OutputDir` from Task 2; `*config.Manager` for `SegmentMinutes`.
- Produces:
  - `func New(cfg Config, settings *config.Manager) *Supervisor`
  - `func (s *Supervisor) Sync(ctx context.Context, cams []sqlc.Camera)`
  - `func (s *Supervisor) Stop()`
  - `func (s *Supervisor) Status() map[string]ProcState`
  - `type ProcState struct { Camera string; PID int; StartedAt time.Time; Restarts int; LastError string }`

- [ ] **Step 1: Write the failing test**

Create `internal/recorder/supervisor_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/recorder/ -run TestSupervisor -v`
Expected: FAIL — `undefined: New`, `undefined: Supervisor`.

- [ ] **Step 3: Write the implementation**

Create `internal/recorder/supervisor.go`:

```go
package recorder

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"golden_eye/internal/config"
	"golden_eye/sqlc"
)

const (
	defaultBaseBackoff = 1 * time.Second
	defaultMaxBackoff  = 30 * time.Second
	// healthyRuntime is how long a process must survive before its restart
	// backoff resets. A camera that drops after an hour should reconnect fast.
	healthyRuntime = 60 * time.Second
	// termGrace is how long ffmpeg gets to flush and exit after SIGTERM.
	termGrace = 5 * time.Second
)

// ProcState is a snapshot of one camera's recorder process, for the dashboard.
type ProcState struct {
	Camera    string    `json:"camera"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	Restarts  int       `json:"restarts"`
	LastError string    `json:"last_error"`
}

// proc tracks one supervised ffmpeg process.
type proc struct {
	key    string // argv joined; identity of the desired command
	cancel context.CancelFunc
	done   chan struct{}

	mu    sync.Mutex
	state ProcState
}

// Supervisor keeps one ffmpeg per enabled camera running.
type Supervisor struct {
	cfg      Config
	settings *config.Manager

	// Overridable for tests.
	binary      string
	baseBackoff time.Duration
	maxBackoff  time.Duration
	buildArgsFn func(cam sqlc.Camera, segmentMinutes int, cfg Config) []string

	mu    sync.Mutex
	procs map[string]*proc // key: camera name
}

// New creates a Supervisor. Nothing runs until Sync is called.
func New(cfg Config, settings *config.Manager) *Supervisor {
	return &Supervisor{
		cfg:         cfg,
		settings:    settings,
		binary:      "ffmpeg",
		baseBackoff: defaultBaseBackoff,
		maxBackoff:  defaultMaxBackoff,
		buildArgsFn: buildArgs,
		procs:       make(map[string]*proc),
	}
}

// Sync reconciles running processes with the desired set: start what is
// missing, stop what is no longer wanted, and restart only those whose command
// actually changed (rename, RTSP URL, transcode flag, segment duration).
func (s *Supervisor) Sync(ctx context.Context, cams []sqlc.Camera) {
	segMin := s.settings.Get().SegmentMinutes

	desired := make(map[string][]string, len(cams))
	for _, cam := range cams {
		if !cam.Enabled {
			continue
		}
		desired[cam.Name] = s.buildArgsFn(cam, segMin, s.cfg)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Stop processes that are unwanted or whose command changed.
	for name, p := range s.procs {
		args, want := desired[name]
		if want && strings.Join(args, " ") == p.key {
			continue
		}
		slog.Info("recorder: stopping", "camera", name)
		s.stopLocked(name, p)
	}

	// Start whatever is missing.
	for name, args := range desired {
		if _, running := s.procs[name]; running {
			continue
		}
		dir := OutputDir(s.cfg, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			slog.Error("recorder: create output dir", "camera", name, "dir", dir, "err", err)
			continue
		}

		runCtx, cancel := context.WithCancel(context.Background())
		p := &proc{
			key:    strings.Join(args, " "),
			cancel: cancel,
			done:   make(chan struct{}),
			state:  ProcState{Camera: name},
		}
		s.procs[name] = p
		slog.Info("recorder: starting", "camera", name)
		go s.run(runCtx, name, args, p)
	}
}

// stopLocked terminates a process and removes it. Caller holds s.mu.
func (s *Supervisor) stopLocked(name string, p *proc) {
	p.cancel()
	<-p.done
	delete(s.procs, name)
}

// Stop terminates every process. Called during graceful shutdown.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, p := range s.procs {
		s.stopLocked(name, p)
	}
}

// Status returns a snapshot of every supervised process.
func (s *Supervisor) Status() map[string]ProcState {
	s.mu.Lock()
	procs := make(map[string]*proc, len(s.procs))
	for k, v := range s.procs {
		procs[k] = v
	}
	s.mu.Unlock()

	out := make(map[string]ProcState, len(procs))
	for name, p := range procs {
		p.mu.Lock()
		out[name] = p.state
		p.mu.Unlock()
	}
	return out
}

// run keeps one process alive until ctx is cancelled. Process exit is normal —
// cameras drop, networks blip — so it always restarts, with backoff that resets
// once a run has been healthy for a while.
func (s *Supervisor) run(ctx context.Context, camName string, args []string, p *proc) {
	defer close(p.done)

	backoff := s.baseBackoff
	for {
		if ctx.Err() != nil {
			return
		}

		cmd := exec.CommandContext(ctx, s.binary, args...)
		// SIGTERM lets ffmpeg finalise the segment it is writing; SIGKILL only
		// after termGrace.
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		cmd.WaitDelay = termGrace

		stderr, err := cmd.StderrPipe()
		if err != nil {
			slog.Error("recorder: stderr pipe", "camera", camName, "err", err)
			return
		}

		startedAt := time.Now()
		if err = cmd.Start(); err != nil {
			p.setError(err.Error())
			slog.Error("recorder: start failed", "camera", camName, "err", err)
			if !s.sleepOrDone(ctx, backoff) {
				return
			}
			backoff = s.nextBackoff(backoff)
			continue
		}

		p.setRunning(cmd.Process.Pid, startedAt)
		slog.Info("recorder: running", "camera", camName, "pid", cmd.Process.Pid)

		// Drain stderr fully before Wait: os/exec closes the pipe on Wait, and
		// reading after that is a documented race.
		<-logStderr(camName, stderr)
		waitErr := cmd.Wait()
		ran := time.Since(startedAt)

		if ctx.Err() != nil {
			slog.Info("recorder: stopped", "camera", camName, "ran", ran.Round(time.Second))
			return
		}

		msg := "exited cleanly"
		if waitErr != nil {
			msg = waitErr.Error()
		}
		p.setExited(msg)
		slog.Warn("recorder: process exited, restarting",
			"camera", camName, "ran", ran.Round(time.Second), "err", msg, "backoff", backoff)

		if ran >= healthyRuntime {
			backoff = s.baseBackoff
		}
		if !s.sleepOrDone(ctx, backoff) {
			return
		}
		backoff = s.nextBackoff(backoff)
	}
}

func (s *Supervisor) nextBackoff(cur time.Duration) time.Duration {
	next := cur * 2
	if next > s.maxBackoff {
		next = s.maxBackoff
	}
	return next
}

// sleepOrDone waits for d, returning false if ctx was cancelled first.
func (s *Supervisor) sleepOrDone(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// logStderr drains ffmpeg's stderr into slog, tagged with the camera. The
// returned channel closes once the pipe hits EOF, which happens when the
// process exits — so the caller can wait on it before calling cmd.Wait.
func logStderr(camName string, r io.Reader) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			slog.Warn("ffmpeg", "camera", camName, "msg", sc.Text())
		}
	}()
	return done
}

func (p *proc) setRunning(pid int, at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state.PID = pid
	p.state.StartedAt = at
	p.state.LastError = ""
}

func (p *proc) setExited(msg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state.PID = 0
	p.state.Restarts++
	p.state.LastError = msg
}

func (p *proc) setError(msg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state.PID = 0
	p.state.Restarts++
	p.state.LastError = msg
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/recorder/ -v -race`
Expected: PASS — all `TestBuildArgs*`, `TestOutputDir`, and all four `TestSupervisor*` tests, with no race warnings.

- [ ] **Step 5: Commit**

```bash
git add internal/recorder/supervisor.go internal/recorder/supervisor_test.go
git commit -m "feat(recorder): supervise one ffmpeg per camera"
```

---

### Task 4: Wire the recorder in, switch mediamtx recording off

**Files:**
- Modify: `internal/camera/service.go` (`buildPathConfig`, `Service` struct, `New`, `Create`, `Update`, `Delete`)
- Modify: `main.go`

**Interfaces:**
- Consumes: `recorder.New`, `Sync`, `Stop` from Task 3.
- Produces: `camera.Syncer` interface (`Sync(ctx context.Context)`); `camera.New(..., sync Syncer)`; `camera.Service.SyncRecorders(ctx)`.

- [ ] **Step 1: Turn off the mediamtx recorder**

`internal/camera/service.go` — `buildPathConfig` becomes (note the doc comment change; `settings` is no longer read here):

```go
// buildPathConfig constructs the mediamtx PathConfig for a camera's native
// source path. mediamtx no longer records: its recorder writes G711 audio as
// raw LPCM ('ipcm'), which no browser can decode, and it cannot align segment
// boundaries to the wall clock. Recording is done by the ffmpeg processes in
// internal/recorder, which read this path.
func (s *Service) buildPathConfig(cam sqlc.Camera) mediamtx.PathConfig {
	return mediamtx.PathConfig{
		Source:         cam.RtspUrl,
		SourceOnDemand: false, // always connected — the recorder reads this path 24/7
		Record:         false,
	}
}
```

Remove the now-unused `fmt.Sprintf` locals from that function only. `fmt` is still used elsewhere in the file.

- [ ] **Step 2: Add the resync hook to the camera service**

In `internal/camera/service.go`, above `type Service struct`:

```go
// Syncer is implemented by the recorder supervisor. It is an interface so the
// camera package does not import recorder, which imports config and sqlc.
type Syncer interface {
	Sync(ctx context.Context, cams []sqlc.Camera)
}
```

Add the field and constructor parameter:

```go
type Service struct {
	db       *pgxpool.Pool
	q        *sqlc.Queries
	mtx      *mediamtx.Client
	settings *config.Manager
	env      *config.Env
	rec      Syncer
}

// New creates a camera Service. rec may be nil (useful in tests); when set, it
// is resynced after every change so recording follows camera CRUD immediately.
func New(db *pgxpool.Pool, mtx *mediamtx.Client, settings *config.Manager, env *config.Env, rec Syncer) *Service {
	return &Service{
		db:       db,
		q:        sqlc.New(db),
		mtx:      mtx,
		settings: settings,
		env:      env,
		rec:      rec,
	}
}

// SyncRecorders reloads the camera list and hands it to the recorder supervisor.
func (s *Service) SyncRecorders(ctx context.Context) {
	if s.rec == nil {
		return
	}
	cams, err := s.q.ListCameras(ctx)
	if err != nil {
		slog.Error("sync recorders: list cameras", "err", err)
		return
	}
	s.rec.Sync(ctx, cams)
}
```

`ListCameras` (not `ListEnabledCameras`) is deliberate: the supervisor needs to see disabled cameras in order to stop them.

- [ ] **Step 3: Call it after every mutation**

In `internal/camera/service.go`, add `s.SyncRecorders(ctx)` as the last statement before the successful return of `Reconcile`, `Create`, `Update`, and `Delete`. For example, at the end of `Create`:

```go
	s.SyncRecorders(ctx)
	return &cam, nil
```

and at the end of `Delete`:

```go
	s.SyncRecorders(ctx)
	return nil
```

- [ ] **Step 4: Wire it in `main.go`**

Add `"golden_eye/internal/recorder"` to the imports. Replace the camera service block:

```go
	// ── Recorder supervisor + camera service ──────────────────────────────────
	rec := recorder.New(recorder.Config{
		RecordingsDir: env.RecordingsDir,
		RTSPBase:      env.MediamtxRTSP,
	}, settings)
	defer rec.Stop()

	camSvc := camera.New(pool, mtxClient, settings, env, rec)
	reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if rErr := camSvc.Reconcile(reconcileCtx); rErr != nil {
		slog.Warn("startup reconcile incomplete", "err", rErr)
	}
	reconcileCancel()
```

In the shutdown block, stop recorders before the HTTP server drains, right after `workerStop()`:

```go
	<-sigCh
	slog.Info("shutting down...")
	workerStop()
	rec.Stop()
```

(`defer rec.Stop()` stays as a safety net for the early-return error paths; `Stop` is idempotent because the second call finds an empty map.)

- [ ] **Step 5: Build and test**

```bash
go build ./... && task test
```

Expected: build succeeds; all tests pass.

- [ ] **Step 6: Verify locally against the real mediamtx**

```bash
task dev
```

Then in another shell, with at least one camera configured:

```bash
pgrep -af "ffmpeg .*segment_atclocktime"
ls -la --time-style=+%T ./recordings/*/ | tail -5
```

Expected: one ffmpeg per enabled camera; a new `.mp4` appearing at the next clock boundary. If no camera is reachable locally, record this step as deferred to Task 7 rather than claiming it passed.

- [ ] **Step 7: Commit**

```bash
git add internal/camera/service.go main.go
git commit -m "feat(recorder): record via supervised ffmpeg, stop mediamtx recorder"
```

---

### Task 5: Stale-recording detection

**Files:**
- Modify: `internal/sql/recordings.sql`
- Modify: `internal/index/indexer.go` (struct, `New`, `scan`)
- Modify: `main.go` (indexer construction)
- Modify: `internal/web/server.go:277` (`dashboard`)
- Modify: `internal/web/templates/dashboard.html` (badge)
- Regenerated: `sqlc/recordings.sql.go`

**Interfaces:**
- Consumes: `config.Manager.Get().SegmentMinutes`.
- Produces: `sqlc.ListCameraRecordingHealthRow{ID int64, Name string, Enabled bool, LastStartedAt pgtype.Timestamptz}`; `index.New(db, recordingsDir, interval, settings)`.

- [ ] **Step 1: Add the query**

Append to `internal/sql/recordings.sql`:

```sql
-- name: ListCameraRecordingHealth :many
SELECT c.id, c.name, c.enabled, MAX(r.started_at)::timestamptz AS last_started_at
FROM cameras c
LEFT JOIN recordings r ON r.camera_id = c.id
GROUP BY c.id, c.name, c.enabled
ORDER BY c.name;
```

- [ ] **Step 2: Regenerate and inspect**

```bash
task sqlcgen
grep -n "ListCameraRecordingHealthRow" -A 6 sqlc/recordings.sql.go
```

Expected: a row struct with `ID`, `Name`, `Enabled`, `LastStartedAt pgtype.Timestamptz`.

- [ ] **Step 3: Warn from the indexer**

`internal/index/indexer.go` — add the settings dependency:

```go
type Indexer struct {
	db            *pgxpool.Pool
	q             *sqlc.Queries
	recordingsDir string
	interval      time.Duration
	settings      *config.Manager
}

func New(db *pgxpool.Pool, recordingsDir string, interval time.Duration, settings *config.Manager) *Indexer {
	return &Indexer{
		db:            db,
		q:             sqlc.New(db),
		recordingsDir: recordingsDir,
		interval:      interval,
		settings:      settings,
	}
}
```

Add `"golden_eye/internal/config"` to that file's imports, and add this method:

```go
// checkStale warns when an enabled camera has produced no segment for longer
// than two segment durations. mediamtx no longer records as a fallback, so a
// wedged recorder would otherwise be silent data loss.
func (idx *Indexer) checkStale(ctx context.Context) {
	segMin := idx.settings.Get().SegmentMinutes
	if segMin <= 0 {
		segMin = 10
	}
	limit := time.Duration(2*segMin) * time.Minute

	rows, err := idx.q.ListCameraRecordingHealth(ctx)
	if err != nil {
		slog.Warn("indexer: recording health", "err", err)
		return
	}
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		if !row.LastStartedAt.Valid {
			slog.Warn("camera has no recordings at all", "camera", row.Name)
			continue
		}
		if age := time.Since(row.LastStartedAt.Time); age > limit {
			slog.Warn("camera recording is stale",
				"camera", row.Name,
				"last_segment", row.LastStartedAt.Time,
				"age", age.Round(time.Minute),
				"limit", limit)
		}
	}
}
```

Call it as the final statement of `scan`, after the `filepath.WalkDir` block completes:

```go
	idx.checkStale(ctx)
```

- [ ] **Step 4: Update the indexer construction in `main.go`**

```go
	indexer := index.New(pool, env.RecordingsDir, 2*time.Minute, settings)
```

- [ ] **Step 5: Show the badge on the dashboard**

`internal/web/server.go` — `dashboard`:

```go
func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	cameras, err := s.q.ListCameras(r.Context())
	if err != nil {
		http.Error(w, "error loading cameras", http.StatusInternalServerError)
		return
	}

	// Stale = no new segment for more than two segment durations. mediamtx no
	// longer records as a fallback, so this is the visible signal that the
	// recorder for a camera has stopped producing.
	segMin := s.settings.Get().SegmentMinutes
	if segMin <= 0 {
		segMin = 10
	}
	limit := time.Duration(2*segMin) * time.Minute
	stale := make(map[int64]bool, len(cameras))
	if rows, hErr := s.q.ListCameraRecordingHealth(r.Context()); hErr == nil {
		for _, row := range rows {
			if !row.Enabled {
				continue
			}
			stale[row.ID] = !row.LastStartedAt.Valid || time.Since(row.LastStartedAt.Time) > limit
		}
	} else {
		slog.Warn("dashboard: recording health", "err", hErr)
	}

	s.render(w, "dashboard.html", map[string]interface{}{
		"Claims":     auth.ClaimsFromCtx(r.Context()),
		"Cameras":    cameras,
		"Stale":      stale,
		"HLSBase":    "/hls",
		"WebRTCBase": "/webrtc",
	})
}
```

Ensure `"log/slog"` and `"time"` are imported in `internal/web/server.go`.

- [ ] **Step 6: Add the badge markup**

`internal/web/templates/dashboard.html` — immediately after the existing `{{if not .Enabled}}…{{end}}` "disabled" badge block:

```html
        {{if index $.Stale .ID}}
        <div class="absolute top-3 right-3 px-2.5 py-1 rounded-full bg-red-50 shadow-neu-sm text-red-600 text-xs font-medium">
          not recording
        </div>
        {{end}}
```

- [ ] **Step 7: Build and test**

```bash
go build ./... && task test
```

Expected: build succeeds; all tests pass.

- [ ] **Step 8: Verify the template renders**

```bash
task run
```

Load `/` as an admin. Expected: page renders (template parse errors surface at startup via `validateTemplate`); a camera with no recent segment shows the red "not recording" badge.

- [ ] **Step 9: Commit**

```bash
git add internal/sql/recordings.sql sqlc/ internal/index/indexer.go internal/web/server.go internal/web/templates/dashboard.html main.go
git commit -m "feat(recordings): warn and badge when a camera stops recording"
```

---

### Task 6: Backfill script for existing footage

**Files:**
- Create: `scripts/backfill_aac.sh`

**Interfaces:**
- Consumes: nothing from earlier tasks — it is a standalone operator tool run on the host.
- Produces: converted files in place; originals under `<recordings_dir>/.orig/<camera>/`.

- [ ] **Step 1: Write the script**

Create `scripts/backfill_aac.sh`:

```bash
#!/usr/bin/env bash
# One-time conversion of already-recorded segments to browser-playable MP4.
#
# mediamtx's recorder wrote G711 audio as raw LPCM ('ipcm'), which no browser
# can decode, so existing recordings play silently. This rewrites each file with
# AAC audio, and re-encodes H.265 video to H.264 when --reencode is given.
#
# Safe to interrupt and re-run: files whose audio is already AAC are skipped.
# Originals are moved to <dir>/.orig/<camera>/ and are NOT deleted — remove that
# directory by hand once playback has been spot-checked.
#
# Usage:
#   scripts/backfill_aac.sh /home/coffee/goldeneye/recordings [--camera NAME] [--reencode]
set -uo pipefail

DIR="${1:-}"
if [[ -z "$DIR" || ! -d "$DIR" ]]; then
  echo "usage: $0 <recordings_dir> [--camera NAME] [--reencode]" >&2
  exit 2
fi
shift

CAMERA=""
REENCODE=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --camera)   CAMERA="${2:-}"; shift 2 ;;
    --reencode) REENCODE=1; shift ;;
    *)          echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

ORIG_ROOT="$DIR/.orig"
LOG="$DIR/.backfill.log"
converted=0; skipped=0; failed=0

log() { printf '%s %s\n' "$(date +%FT%T)" "$*" | tee -a "$LOG"; }

log "backfill start dir=$DIR camera=${CAMERA:-all} reencode=$REENCODE"

# .orig and .thumbs are excluded so re-runs never reprocess originals.
while IFS= read -r -d '' f; do
  rel="${f#"$DIR"/}"
  cam="${rel%%/*}"
  [[ -n "$CAMERA" && "$cam" != "$CAMERA" ]] && continue

  atag=$(ffprobe -v error -select_streams a:0 -show_entries stream=codec_tag_string -of csv=p=0 "$f" 2>/dev/null)
  if [[ "$atag" == "mp4a" ]]; then
    skipped=$((skipped + 1))
    continue
  fi

  vcodec=$(ffprobe -v error -select_streams v:0 -show_entries stream=codec_name -of csv=p=0 "$f" 2>/dev/null)
  src_dur=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$f" 2>/dev/null)

  if [[ "$vcodec" == "hevc" && "$REENCODE" == "1" ]]; then
    vargs=(-c:v libx264 -preset ultrafast)
  else
    vargs=(-c:v copy)
  fi

  tmp="$f.tmp.mp4"
  rm -f "$tmp"
  if ! nice -n 19 ionice -c3 ffmpeg -nostdin -v error -y -i "$f" \
        "${vargs[@]}" -c:a aac -b:a 32k -movflags +faststart "$tmp"; then
    log "FAIL convert $rel"
    rm -f "$tmp"
    failed=$((failed + 1))
    continue
  fi

  # Verify before destroying anything: both streams present, audio is AAC,
  # duration within one second of the source.
  new_atag=$(ffprobe -v error -select_streams a:0 -show_entries stream=codec_tag_string -of csv=p=0 "$tmp" 2>/dev/null)
  new_dur=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$tmp" 2>/dev/null)
  ok=$(awk -v a="${src_dur:-0}" -v b="${new_dur:-0}" 'BEGIN{d=a-b; if(d<0)d=-d; print (d<=1.0)?"1":"0"}')

  if [[ "$new_atag" != "mp4a" || "$ok" != "1" ]]; then
    log "FAIL verify $rel (audio=$new_atag src_dur=$src_dur new_dur=$new_dur)"
    rm -f "$tmp"
    failed=$((failed + 1))
    continue
  fi

  mkdir -p "$ORIG_ROOT/$cam"
  if ! mv "$f" "$ORIG_ROOT/$cam/$(basename "$f")"; then
    log "FAIL stash original $rel"
    rm -f "$tmp"
    failed=$((failed + 1))
    continue
  fi
  mv "$tmp" "$f"
  converted=$((converted + 1))
  log "ok $rel (video=$vcodec)"
done < <(find "$DIR" -mindepth 2 -name '*.mp4' -not -path "$ORIG_ROOT/*" -not -path "$DIR/.thumbs/*" -print0 | sort -z)

log "backfill done converted=$converted skipped=$skipped failed=$failed"
log "originals kept in $ORIG_ROOT — delete manually after spot-checking playback"
```

- [ ] **Step 2: Make it executable and shell-check the syntax**

```bash
chmod +x scripts/backfill_aac.sh
bash -n scripts/backfill_aac.sh && echo "syntax ok"
```

Expected: `syntax ok`.

- [ ] **Step 3: Dry-run it against a copied sample**

On the deploy host, prove it on throwaway data before touching real recordings:

```bash
ssh coffee@100.86.43.70 '
  rm -rf /tmp/bf && mkdir -p /tmp/bf/front-door &&
  cp "$(find /home/coffee/goldeneye/recordings/front-door -name "*.mp4" | head -1)" /tmp/bf/front-door/ &&
  ls /tmp/bf/front-door/'
scp scripts/backfill_aac.sh coffee@100.86.43.70:/tmp/backfill_aac.sh
ssh coffee@100.86.43.70 'bash /tmp/backfill_aac.sh /tmp/bf'
ssh coffee@100.86.43.70 '
  ffprobe -v error -show_entries stream=codec_name,codec_tag_string -of default=noprint_wrappers=1 /tmp/bf/front-door/*.mp4;
  ls -la /tmp/bf/.orig/front-door/'
```

Expected: `codec_name=h264 / codec_tag_string=avc1` plus `codec_name=aac / codec_tag_string=mp4a`; the original present under `.orig/front-door/`; log line `converted=1 skipped=0 failed=0`.

- [ ] **Step 4: Prove the re-run skip works**

```bash
ssh coffee@100.86.43.70 'bash /tmp/backfill_aac.sh /tmp/bf'
```

Expected: `converted=0 skipped=1 failed=0`.

- [ ] **Step 5: Clean up the sample**

```bash
ssh coffee@100.86.43.70 'rm -rf /tmp/bf /tmp/backfill_aac.sh'
```

- [ ] **Step 6: Commit**

```bash
git add scripts/backfill_aac.sh
git commit -m "feat(scripts): one-time AAC backfill for existing recordings"
```

---

### Task 7: Deploy and verify on the host

**Files:** none changed — this task captures evidence.

**Interfaces:**
- Consumes: everything above.
- Produces: the verification record that lets the work be called done.

- [ ] **Step 1: Build and ship**

```bash
task build
ssh coffee@100.86.43.70 'cp /home/coffee/goldeneye/goldeneye /home/coffee/goldeneye/goldeneye.bak.$(date +%s)'
rsync bin/goldeneye coffee@100.86.43.70:/home/coffee/goldeneye/goldeneye
rsync scripts/backfill_aac.sh coffee@100.86.43.70:/home/coffee/goldeneye/backfill_aac.sh
```

- [ ] **Step 2: Apply the schema change to the production database**

```bash
ssh coffee@100.86.43.70 'psql "postgresql://postgres:PASSWORD@localhost:5432/goldeneye" -c "ALTER TABLE cameras ADD COLUMN IF NOT EXISTS transcode_video boolean NOT NULL DEFAULT false;"'
```

Take the real connection string from `/home/coffee/goldeneye/.env` (`PSQL_URL`). Expected: `ALTER TABLE`.

- [ ] **Step 3: Restart and confirm the processes exist**

```bash
ssh coffee@100.86.43.70 'sudo systemctl restart goldeneye && sleep 5 && systemctl is-active goldeneye && pgrep -af "ffmpeg .*segment_atclocktime"'
```

Expected: `active`, and one ffmpeg line per enabled camera.

- [ ] **Step 4: Flag the H.265 camera**

In the dashboard, edit `cashier` and tick "Transcode video to H.264". Then:

```bash
ssh coffee@100.86.43.70 'pgrep -af "ffmpeg .*cashier"'
```

Expected: the `cashier` command now contains `libx264`; the other two still contain `-c:v copy`.

- [ ] **Step 5: Verify the produced segments**

Wait for one segment boundary, then:

```bash
ssh coffee@100.86.43.70 '
  for d in /home/coffee/goldeneye/recordings/*/; do
    f=$(ls -t "$d"*.mp4 2>/dev/null | head -2 | tail -1)
    echo "=== $f"
    ffprobe -v error -show_entries stream=codec_name,codec_tag_string -show_entries format=duration -of default=noprint_wrappers=1 "$f"
  done'
```

Expected, for every camera: `codec_tag_string=avc1` and `codec_tag_string=mp4a`; duration close to `segment_minutes * 60`.

- [ ] **Step 6: Verify clock alignment**

```bash
ssh coffee@100.86.43.70 'ls -1 /home/coffee/goldeneye/recordings/front-door/ | tail -4'
```

Expected: filenames at `:00`, `:10`, `:20` (+ up to a few seconds of keyframe lag).

- [ ] **Step 7: Verify CPU under the real combined load**

```bash
ssh coffee@100.86.43.70 'top -b -n 2 -d 5 | grep -E "ffmpeg|Cpu\(s\)" | tail -8; uptime'
```

Expected: roughly 140% for the `cashier` process, low single digits for the others, and a load average comfortably under 4. If load approaches or exceeds 4, stop and report — the spec's accepted cost was measured per job, not combined.

- [ ] **Step 8: Verify playback in Firefox**

Open a new recording for each camera in the browser. Expected: sound on all three; picture on `cashier`. Record what was actually observed, per camera.

- [ ] **Step 9: Confirm the stale badge is quiet**

Reload the dashboard. Expected: no "not recording" badge while all three recorders are healthy.

- [ ] **Step 10: Run the backfill**

```bash
ssh coffee@100.86.43.70 'tmux new -d -s backfill "bash /home/coffee/goldeneye/backfill_aac.sh /home/coffee/goldeneye/recordings --reencode"'
ssh coffee@100.86.43.70 'tail -5 /home/coffee/goldeneye/recordings/.backfill.log'
```

The `cashier` re-encode runs at roughly 1:1 with footage duration — expect this to take on the order of a day and a half of wall clock. Check progress with the `tail` command above.

- [ ] **Step 11: Spot-check and clean up**

After the backfill finishes, play one converted file per camera in Firefox, then:

```bash
ssh coffee@100.86.43.70 'du -sh /home/coffee/goldeneye/recordings/.orig && rm -rf /home/coffee/goldeneye/recordings/.orig'
```

- [ ] **Step 12: Record the result**

Report per step what was observed, not what was expected. Any step that could not be verified is reported as unverified, not as passing.

---

## Rollback

Redeploy the previous binary (`/home/coffee/goldeneye/goldeneye.bak.*`) and restart. The old binary's reconcile sets `record: true` back on the native mediamtx paths, so recording resumes on the old pipeline. The `transcode_video` column is additive and harmless to the old binary. Segments written by either pipeline stay indexed and playable.

The one irreversible step is the backfill's file replacement, which is why `.orig` is retained until Step 11.
