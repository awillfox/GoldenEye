package camera

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"

	"golden_eye/internal/config"
	"golden_eye/internal/mediamtx"
	"golden_eye/sqlc"

	"github.com/jackc/pgx/v5/pgxpool"
)

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// Service manages cameras in the database and keeps mediamtx in sync.
type Service struct {
	db       *pgxpool.Pool
	q        *sqlc.Queries
	mtx      *mediamtx.Client
	settings *config.Manager
	env      *config.Env
}

// New creates a camera Service.
func New(db *pgxpool.Pool, mtx *mediamtx.Client, settings *config.Manager, env *config.Env) *Service {
	return &Service{
		db:       db,
		q:        sqlc.New(db),
		mtx:      mtx,
		settings: settings,
		env:      env,
	}
}

// Reconcile syncs mediamtx paths with the enabled cameras in the DB.
// Called on startup and after camera CRUD operations.
func (s *Service) Reconcile(ctx context.Context) error {
	cameras, err := s.q.ListEnabledCameras(ctx)
	if err != nil {
		return fmt.Errorf("list enabled cameras: %w", err)
	}

	existing, err := s.mtx.ListConfigPaths(ctx)
	if err != nil {
		return fmt.Errorf("list mediamtx paths: %w", err)
	}

	existingMap := make(map[string]struct{}, len(existing))
	for _, p := range existing {
		existingMap[p.Name] = struct{}{}
	}

	wantMap := make(map[string]struct{}, len(cameras)*2)
	for _, cam := range cameras {
		for name, cfg := range s.desiredPaths(cam) {
			wantMap[name] = struct{}{}
			if _, ok := existingMap[name]; ok {
				if err = s.mtx.UpdatePath(ctx, name, cfg); err != nil {
					slog.Warn("update mediamtx path", "path", name, "err", err)
				}
			} else {
				if err = s.mtx.AddPath(ctx, name, cfg); err != nil {
					slog.Warn("add mediamtx path", "path", name, "err", err)
				}
			}
		}
	}

	// Remove orphan paths (in mediamtx but not in DB).
	for name := range existingMap {
		if _, ok := wantMap[name]; !ok {
			if err = s.mtx.DeletePath(ctx, name); err != nil {
				slog.Warn("delete orphan mediamtx path", "name", name, "err", err)
			}
		}
	}

	return nil
}

// Create adds a camera to the DB and registers its mediamtx path.
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
	if err != nil {
		return nil, fmt.Errorf("create camera: %w", err)
	}

	for pathName, cfg := range s.desiredPaths(cam) {
		if err = s.mtx.AddPath(ctx, pathName, cfg); err != nil {
			// Log but don't fail — reconcile on next startup will fix it.
			slog.Warn("add mediamtx path after create", "path", pathName, "err", err)
		}
	}

	return &cam, nil
}

// Update modifies a camera and syncs mediamtx.
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
	if err != nil {
		return nil, fmt.Errorf("update camera: %w", err)
	}

	// If name changed, delete the old paths (main + preview).
	if old.Name != name {
		for _, pathName := range pathNamesFor(old.Name) {
			if err = s.mtx.DeletePath(ctx, pathName); err != nil {
				slog.Warn("delete old mediamtx path on rename", "path", pathName, "err", err)
			}
		}
	}

	if enabled {
		for pathName, cfg := range s.desiredPaths(cam) {
			if old.Name != name {
				if err = s.mtx.AddPath(ctx, pathName, cfg); err != nil {
					slog.Warn("add mediamtx path after update", "path", pathName, "err", err)
				}
			} else {
				if err = s.mtx.UpdatePath(ctx, pathName, cfg); err != nil {
					slog.Warn("update mediamtx path", "path", pathName, "err", err)
				}
			}
		}
	} else {
		for _, pathName := range pathNamesFor(name) {
			if err = s.mtx.DeletePath(ctx, pathName); err != nil {
				slog.Warn("delete mediamtx path on disable", "path", pathName, "err", err)
			}
		}
	}

	return &cam, nil
}

// Delete removes a camera from the DB and deregisters its mediamtx path.
func (s *Service) Delete(ctx context.Context, id int64) error {
	cam, err := s.q.GetCamera(ctx, id)
	if err != nil {
		return fmt.Errorf("get camera %d: %w", id, err)
	}

	if err = s.q.DeleteCamera(ctx, id); err != nil {
		return fmt.Errorf("delete camera %d: %w", id, err)
	}

	for _, pathName := range pathNamesFor(cam.Name) {
		if err = s.mtx.DeletePath(ctx, pathName); err != nil {
			slog.Warn("delete mediamtx path on camera delete", "path", pathName, "err", err)
		}
	}

	return nil
}

// previewPathName is the mediamtx path serving the browser-friendly (H264)
// transcode of a camera for the WebRTC live preview.
func previewPathName(name string) string { return name + "-preview" }

// pathNamesFor returns every mediamtx path owned by a camera (recording + preview).
func pathNamesFor(name string) []string {
	return []string{name, previewPathName(name)}
}

// desiredPaths returns the full set of mediamtx paths a camera should have:
//   - <name>          native source, 24/7 recording (codec copied, no CPU cost)
//   - <name>-preview  on-demand H264 transcode for browser WebRTC preview
func (s *Service) desiredPaths(cam sqlc.Camera) map[string]mediamtx.PathConfig {
	paths := map[string]mediamtx.PathConfig{
		cam.Name: s.buildPathConfig(cam),
	}
	if cam.Enabled {
		paths[previewPathName(cam.Name)] = s.buildPreviewPathConfig(cam)
	}
	return paths
}

// buildPathConfig constructs the mediamtx PathConfig for a camera's native
// recording path.
func (s *Service) buildPathConfig(cam sqlc.Camera) mediamtx.PathConfig {
	st := s.settings.Get()
	segDur := fmt.Sprintf("%dm", st.SegmentMinutes)
	recordPath := fmt.Sprintf("%s/%s/%%Y-%%m-%%d_%%H-%%M-%%S-%%f", s.env.RecordingsDir, cam.Name)

	return mediamtx.PathConfig{
		Source:                cam.RtspUrl,
		SourceOnDemand:        false, // always connected (24/7 recording)
		Record:                cam.Enabled,
		RecordFormat:          "fmp4",
		RecordPath:            recordPath,
		RecordSegmentDuration: segDur,
	}
}

// buildPreviewPathConfig constructs the on-demand transcode path used for the
// WebRTC live preview. It transcodes video to H264 (browsers can't decode H265
// over WebRTC) while copying the G711 audio through untouched (WebRTC carries it
// natively). ffmpeg only runs while a viewer is connected, so there is no
// steady-state CPU cost; recording still uses the untouched native stream.
func (s *Service) buildPreviewPathConfig(cam sqlc.Camera) mediamtx.PathConfig {
	// $RTSP_PORT and $MTX_PATH are expanded by mediamtx; cam.Name is a validated
	// slug. Input is mediamtx's own already-connected native path, so the camera
	// is not opened a second time.
	ffmpeg := fmt.Sprintf(
		"ffmpeg -nostdin -loglevel warning -rtsp_transport tcp "+
			"-i rtsp://127.0.0.1:$RTSP_PORT/%s "+
			"-c:v libx264 -preset ultrafast -tune zerolatency -profile:v baseline -pix_fmt yuv420p -g 30 "+
			"-c:a copy "+
			"-f rtsp rtsp://127.0.0.1:$RTSP_PORT/$MTX_PATH",
		cam.Name,
	)
	return mediamtx.PathConfig{
		RunOnDemand:           ffmpeg,
		RunOnDemandRestart:    true,
		RunOnDemandCloseAfter: "10s",
	}
}
