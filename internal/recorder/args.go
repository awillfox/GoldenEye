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
