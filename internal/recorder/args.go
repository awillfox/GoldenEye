// Package recorder supervises one ffmpeg process per enabled camera. Each
// process reads mediamtx's already-connected native path and writes clock-
// aligned fragmented-MP4 segments straight into RECORDINGS_DIR/<camera>/.
//
// mediamtx's own recorder cannot align segment boundaries to the wall clock —
// it counts from stream start — which is the reason this package exists. Video
// is always copied; audio is re-encoded to AAC only because ffmpeg's MP4 muxer
// refuses to carry the cameras' G711 ("Could not find tag for codec pcm_alaw").
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
// never opened a second time. Video is copied, so recording costs almost no CPU
// (~2% per camera measured on the deploy host, including a 4K H.265 stream).
//
// Segments are cut by ffmpeg's segment muxer with -segment_atclocktime, so
// boundaries land on wall-clock multiples of the segment duration rather than
// counting from stream start: a recorder starting at 12:57 with a 10-minute
// segment closes its first file at 13:00, then runs 13:00, 13:10, 13:20. The
// fragmented-MP4 flags keep a segment readable even if the process is killed
// mid-write.
func buildArgs(cam sqlc.Camera, segmentMinutes int, cfg Config) []string {
	if segmentMinutes <= 0 {
		segmentMinutes = defaultSegmentMinutes
	}

	return []string{
		"-nostdin",
		"-loglevel", "warning",
		"-rtsp_transport", "tcp",
		"-i", cfg.RTSPBase + "/" + cam.Name,
		"-c:v", "copy",
		"-c:a", "aac",
		"-b:a", "32k",
		"-f", "segment",
		"-segment_time", strconv.Itoa(segmentMinutes * 60),
		"-segment_atclocktime", "1",
		"-segment_format", "mp4",
		"-segment_format_options", "movflags=+frag_keyframe+empty_moov+default_base_moof",
		"-reset_timestamps", "1",
		"-strftime", "1",
		filepath.Join(OutputDir(cfg, cam.Name), "%Y-%m-%d_%H-%M-%S.mp4"),
	}
}
