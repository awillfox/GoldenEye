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
// unless the camera is flagged for transcoding.
//
// Transcoding is expensive in both dimensions, measured on the 4K H.265 cashier
// stream on the deploy host (2026-07-21):
//
//	preset ultrafast, no rate control  21.7 Mbps  ~141% CPU   (~234 GB/day)
//	preset ultrafast, -crf 28           9.9 Mbps  ~131% CPU   (~107 GB/day)
//	preset veryfast,  -crf 28           2.8 Mbps  ~197% CPU   (~30 GB/day)
//	preset veryfast,  -crf 28, 1080p    0.9 Mbps  ~101% CPU   (~10 GB/day)
//
// The source itself is only 2.05 Mbps, so the settings below inflate storage
// ~10x. They are left as-is because no camera currently sets the flag; before
// enabling it on a high-resolution camera, add rate control and re-measure, or
// the disk fills in days rather than the configured retention window.
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
