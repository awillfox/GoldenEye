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

	wantMap := make(map[string]struct{}, len(cameras))
	for _, cam := range cameras {
		wantMap[cam.Name] = struct{}{}
		cfg := s.buildPathConfig(cam)
		if _, ok := existingMap[cam.Name]; ok {
			if err = s.mtx.UpdatePath(ctx, cam.Name, cfg); err != nil {
				slog.Warn("update mediamtx path", "camera", cam.Name, "err", err)
			}
		} else {
			if err = s.mtx.AddPath(ctx, cam.Name, cfg); err != nil {
				slog.Warn("add mediamtx path", "camera", cam.Name, "err", err)
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
func (s *Service) Create(ctx context.Context, name, rtspURL string) (*sqlc.Camera, error) {
	if !validName.MatchString(name) {
		return nil, fmt.Errorf("camera name must be lowercase alphanumeric/dash/underscore, 1-63 chars")
	}

	cam, err := s.q.CreateCamera(ctx, sqlc.CreateCameraParams{
		Name:    name,
		RtspUrl: rtspURL,
		Enabled: true,
	})
	if err != nil {
		return nil, fmt.Errorf("create camera: %w", err)
	}

	if err = s.mtx.AddPath(ctx, name, s.buildPathConfig(cam)); err != nil {
		// Log but don't fail — reconcile on next startup will fix it.
		slog.Warn("add mediamtx path after create", "camera", name, "err", err)
	}

	return &cam, nil
}

// Update modifies a camera and syncs mediamtx.
func (s *Service) Update(ctx context.Context, id int64, name, rtspURL string, enabled bool) (*sqlc.Camera, error) {
	if !validName.MatchString(name) {
		return nil, fmt.Errorf("camera name must be lowercase alphanumeric/dash/underscore, 1-63 chars")
	}

	old, err := s.q.GetCamera(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get camera %d: %w", id, err)
	}

	cam, err := s.q.UpdateCamera(ctx, sqlc.UpdateCameraParams{
		ID:      id,
		Name:    name,
		RtspUrl: rtspURL,
		Enabled: enabled,
		Codec:   old.Codec,
	})
	if err != nil {
		return nil, fmt.Errorf("update camera: %w", err)
	}

	// If name changed, delete old path and add new one.
	if old.Name != name {
		if err = s.mtx.DeletePath(ctx, old.Name); err != nil {
			slog.Warn("delete old mediamtx path on rename", "old", old.Name, "err", err)
		}
	}

	if enabled {
		cfg := s.buildPathConfig(cam)
		if old.Name != name {
			if err = s.mtx.AddPath(ctx, name, cfg); err != nil {
				slog.Warn("add mediamtx path after update", "camera", name, "err", err)
			}
		} else {
			if err = s.mtx.UpdatePath(ctx, name, cfg); err != nil {
				slog.Warn("update mediamtx path", "camera", name, "err", err)
			}
		}
	} else {
		if err = s.mtx.DeletePath(ctx, name); err != nil {
			slog.Warn("delete mediamtx path on disable", "camera", name, "err", err)
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

	if err = s.mtx.DeletePath(ctx, cam.Name); err != nil {
		slog.Warn("delete mediamtx path on camera delete", "camera", cam.Name, "err", err)
	}

	return nil
}

// buildPathConfig constructs the mediamtx PathConfig for a camera row.
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
