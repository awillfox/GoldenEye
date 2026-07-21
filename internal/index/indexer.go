package index

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golden_eye/internal/config"
	"golden_eye/sqlc"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// segmentTimeLayout matches mediamtx's %Y-%m-%d_%H-%M-%S filename pattern.
const segmentTimeLayout = "2006-01-02_15-04-05"

// Indexer periodically scans the recordings directory and upserts recording rows.
type Indexer struct {
	db            *pgxpool.Pool
	q             *sqlc.Queries
	recordingsDir string
	interval      time.Duration
	settings      *config.Manager
}

// New creates an Indexer.
func New(db *pgxpool.Pool, recordingsDir string, interval time.Duration, settings *config.Manager) *Indexer {
	return &Indexer{
		db:            db,
		q:             sqlc.New(db),
		recordingsDir: recordingsDir,
		interval:      interval,
		settings:      settings,
	}
}

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

// Run starts the periodic scan loop. Blocks until ctx is cancelled.
func (idx *Indexer) Run(ctx context.Context) {
	// Resolve symlinks so filepath.WalkDir sees a real directory as the root.
	if resolved, err := filepath.EvalSymlinks(idx.recordingsDir); err == nil {
		idx.recordingsDir = resolved
	}
	slog.Info("indexer started", "dir", idx.recordingsDir, "interval", idx.interval)
	ticker := time.NewTicker(idx.interval)
	defer ticker.Stop()

	// Scan once immediately on start.
	idx.scan(ctx)

	for {
		select {
		case <-ctx.Done():
			slog.Info("indexer stopped")
			return
		case <-ticker.C:
			idx.scan(ctx)
		}
	}
}

// scan walks the recordings directory and upserts segment files into the DB.
func (idx *Indexer) scan(ctx context.Context) {
	cameras, err := idx.q.ListCameras(ctx)
	if err != nil {
		slog.Error("indexer: list cameras", "err", err)
		return
	}
	camByName := make(map[string]sqlc.Camera, len(cameras))
	for _, c := range cameras {
		camByName[c.Name] = c
	}

	// thumbsDir stores generated JPEG thumbnails alongside recordings.
	thumbsDir := filepath.Join(idx.recordingsDir, ".thumbs")

	err = filepath.WalkDir(idx.recordingsDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			slog.Warn("indexer: walk error", "path", path, "err", walkErr)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".mp4") && !strings.HasSuffix(path, ".fmp4") {
			return nil
		}

		absPath, absErr := filepath.Abs(path)
		if absErr != nil {
			return nil
		}

		// Expected path structure: <recordingsDir>/<cameraName>/<datetime>[-frac].fmp4
		rel, relErr := filepath.Rel(idx.recordingsDir, path)
		if relErr != nil {
			return nil
		}
		parts := strings.SplitN(rel, string(os.PathSeparator), 2)
		if len(parts) < 2 {
			return nil
		}
		camName := parts[0]

		cam, ok := camByName[camName]
		if !ok {
			return nil // unknown camera; skip
		}

		startedAt, ok := parseSegmentTime(d.Name())
		if !ok {
			return nil
		}

		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}

		params := sqlc.UpsertRecordingParams{
			CameraID:  cam.ID,
			Path:      absPath,
			StartedAt: pgtype.Timestamptz{Time: startedAt, Valid: true},
			SizeBytes: pgtype.Int8{Int64: info.Size(), Valid: true},
		}
		rec, upsertErr := idx.q.UpsertRecording(ctx, params)
		if upsertErr != nil {
			slog.Warn("indexer: upsert recording", "path", absPath, "err", upsertErr)
			return nil
		}

		// Generate thumbnail and probe duration only when not yet set.
		if !rec.ThumbnailPath.Valid || rec.ThumbnailPath.String == "" {
			idx.generateMeta(ctx, rec.ID, absPath, thumbsDir)
		}

		return nil
	})

	if err != nil {
		slog.Error("indexer: walk", "err", err)
	}

	idx.checkStale(ctx)
}

// generateMeta runs ffprobe for duration and ffmpeg for thumbnail, then saves to DB.
func (idx *Indexer) generateMeta(ctx context.Context, id int64, videoPath, thumbsDir string) {
	if err := os.MkdirAll(thumbsDir, 0o755); err != nil {
		slog.Warn("indexer: mkdir thumbs", "err", err)
		return
	}

	thumbPath := filepath.Join(thumbsDir, fmt.Sprintf("%d.jpg", id))

	// ffprobe: get duration in seconds.
	durSecs := probeDuration(videoPath)

	// ffmpeg: extract frame at 3s (or mid-point for short clips).
	seekSecs := 3.0
	if durSecs > 0 && float64(durSecs) < seekSecs*2 {
		seekSecs = float64(durSecs) / 2.0
	}
	seekStr := fmt.Sprintf("%.2f", seekSecs)

	cmd := exec.CommandContext(ctx,
		"ffmpeg", "-y",
		"-ss", seekStr,
		"-i", videoPath,
		"-vframes", "1",
		"-vf", "scale=320:-1",
		"-q:v", "5",
		thumbPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		slog.Warn("indexer: ffmpeg thumbnail", "path", videoPath, "err", err, "output", string(out))
		return
	}

	metaParams := sqlc.UpdateRecordingMetaParams{
		ID:            id,
		ThumbnailPath: pgtype.Text{String: thumbPath, Valid: true},
	}
	if durSecs > 0 {
		metaParams.DurationSecs = pgtype.Int4{Int32: int32(durSecs), Valid: true} //nolint:gosec
	}
	if err := idx.q.UpdateRecordingMeta(ctx, metaParams); err != nil {
		slog.Warn("indexer: update meta", "id", id, "err", err)
	}
}

// probeDuration uses ffprobe to get video duration in seconds. Returns 0 on error.
func probeDuration(videoPath string) int {
	type ffprobeFormat struct {
		Duration string `json:"duration"`
	}
	type ffprobeOut struct {
		Format ffprobeFormat `json:"format"`
	}

	cmd := exec.Command("ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		videoPath,
	)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	var result ffprobeOut
	if err = json.Unmarshal(out, &result); err != nil {
		return 0
	}
	var dur float64
	if _, err = fmt.Sscanf(result.Format.Duration, "%f", &dur); err != nil {
		return 0
	}
	return int(dur)
}

// parseSegmentTime extracts the start time from a mediamtx segment filename.
// Format: YYYY-MM-DD_HH-MM-SS[-microseconds].fmp4
func parseSegmentTime(name string) (time.Time, bool) {
	base := strings.TrimSuffix(strings.TrimSuffix(name, ".fmp4"), ".mp4")

	// Try stripping fractional suffix (last dash-delimited component may be microseconds).
	if idx := strings.LastIndex(base, "-"); idx > 0 {
		candidate := base[:idx]
		if t, err := time.ParseInLocation(segmentTimeLayout, candidate, time.Local); err == nil {
			return t, true
		}
	}

	if t, err := time.ParseInLocation(segmentTimeLayout, base, time.Local); err == nil {
		return t, true
	}

	return time.Time{}, false
}
