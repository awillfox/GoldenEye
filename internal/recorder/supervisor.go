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
