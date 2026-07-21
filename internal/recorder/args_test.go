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
