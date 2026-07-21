package web

import (
	"bytes"
	"html/template"
	"strings"
	"testing"

	"golden_eye/internal/auth"
	"golden_eye/sqlc"
)

// TestDashboardRender executes the dashboard template the same way render()
// does. Template errors are otherwise only visible at request time, and the
// stale badge reads $.Stale from inside a range over .Cameras — a lookup that
// silently breaks if the handler stops supplying the map.
func TestDashboardRender(t *testing.T) {
	tmpl, err := template.New("").ParseFS(embedFS, "templates/base.html", "templates/dashboard.html")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	cams := []sqlc.Camera{
		{ID: 1, Name: "cashier", RtspUrl: "rtsp://x/1", Enabled: true, TranscodeVideo: true},
		{ID: 2, Name: "front-door", RtspUrl: "rtsp://x/2", Enabled: true, TranscodeVideo: false},
	}
	data := map[string]interface{}{
		"Claims":     &auth.Claims{UserID: 1, Username: "admin", Role: auth.RoleAdmin},
		"Cameras":    cams,
		"Stale":      map[int64]bool{1: true, 2: false},
		"HLSBase":    "/hls",
		"WebRTCBase": "/webrtc",
	}

	var buf bytes.Buffer
	if err = tmpl.ExecuteTemplate(&buf, "base", data); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()

	if got := strings.Count(out, "not recording"); got != 1 {
		t.Errorf("stale badge count: got %d, want 1", got)
	}
	if !strings.Contains(out, "camera-transcode") {
		t.Error("transcode checkbox missing from the camera modal")
	}
	// The transcode flag must reach editCamera so the checkbox reflects state.
	// html/template's JS-context escaper pads interpolated values with spaces and
	// escapes '/' as '\/', so match on the shape rather than the literal source.
	if !strings.Contains(out, `editCamera( 1 , 'cashier', 'rtsp:\/\/x\/1',  true ,  true )`) {
		t.Error("editCamera call missing the transcode argument for cashier")
	}
	if !strings.Contains(out, `editCamera( 2 , 'front-door', 'rtsp:\/\/x\/2',  true ,  false )`) {
		t.Error("editCamera call missing the transcode argument for front-door")
	}
}
