package rotation

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"golden_eye/internal/config"
	"golden_eye/sqlc"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Sweeper periodically deletes recordings older than the configured retention period.
type Sweeper struct {
	db       *pgxpool.Pool
	q        *sqlc.Queries
	settings *config.Manager
}

// New creates a Sweeper.
func New(db *pgxpool.Pool, settings *config.Manager) *Sweeper {
	return &Sweeper{
		db:       db,
		q:        sqlc.New(db),
		settings: settings,
	}
}

// Run starts the hourly sweep loop. Blocks until ctx is cancelled.
func (sw *Sweeper) Run(ctx context.Context) {
	slog.Info("rotation sweeper started")
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	// Run once immediately on start to clean up after a restart.
	sw.sweep(ctx)

	for {
		select {
		case <-ctx.Done():
			slog.Info("rotation sweeper stopped")
			return
		case <-ticker.C:
			sw.sweep(ctx)
		}
	}
}

// sweep deletes all recordings older than retention_days from the DB and filesystem.
func (sw *Sweeper) sweep(ctx context.Context) {
	days := int32(sw.settings.Get().RetentionDays) //nolint:gosec
	paths, err := sw.q.DeleteOldRecordings(ctx, days)
	if err != nil {
		slog.Error("rotation sweep: delete old recordings", "err", err)
		return
	}

	deleted, missing := 0, 0
	dirs := make(map[string]struct{})
	for _, row := range paths {
		dirs[filepath.Dir(row.Path)] = struct{}{}
		if removeErr := os.Remove(row.Path); removeErr != nil {
			if os.IsNotExist(removeErr) {
				missing++
			} else {
				slog.Warn("rotation sweep: remove file", "path", row.Path, "err", removeErr)
			}
		} else {
			deleted++
		}
		if row.ThumbnailPath.Valid && row.ThumbnailPath.String != "" {
			_ = os.Remove(row.ThumbnailPath.String)
		}
	}

	if len(paths) > 0 {
		slog.Info("rotation sweep complete",
			"deleted_rows", len(paths),
			"deleted_files", deleted,
			"already_missing", missing,
		)
	}

	for dir := range dirs {
		pruneEmptyDir(dir)
	}
}

// pruneEmptyDir removes dir if it is empty (ignores errors).
func pruneEmptyDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	if len(entries) == 0 {
		if err = os.Remove(dir); err == nil {
			slog.Debug("rotation sweep: removed empty dir", "dir", dir)
		}
	}
}
