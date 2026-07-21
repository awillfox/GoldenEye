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
// does. Template errors are otherwise only visible at request time.
func TestDashboardRender(t *testing.T) {
	tmpl, err := template.New("").ParseFS(embedFS, "templates/base.html", "templates/dashboard.html")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	data := map[string]any{
		"Claims": &auth.Claims{UserID: 1, Username: "admin", Role: auth.RoleAdmin},
		"Cameras": []sqlc.Camera{
			{ID: 1, Name: "cashier", RtspUrl: "rtsp://x/1", Enabled: true},
			{ID: 2, Name: "front-door", RtspUrl: "rtsp://x/2", Enabled: false},
		},
		"HLSBase":    "/hls",
		"WebRTCBase": "/webrtc",
	}

	var buf bytes.Buffer
	if err = tmpl.ExecuteTemplate(&buf, "base", data); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()

	// html/template's JS-context escaper pads interpolated values with spaces and
	// escapes '/' as '\/', so match on the shape rather than the literal source.
	if !strings.Contains(out, `editCamera( 1 , 'cashier', 'rtsp:\/\/x\/1',  true )`) {
		t.Error("editCamera call not rendered as expected")
	}
	if got := strings.Count(out, "disabled"); got < 1 {
		t.Error("disabled badge missing for the disabled camera")
	}
}
