package web

import (
	"bytes"
	"html/template"
	"strings"
	"testing"

	"golden_eye/internal/auth"
	"golden_eye/sqlc"
)

// renderPage executes base + page the same way render() does. Template errors
// are otherwise only visible at request time.
func renderPage(t *testing.T, page string, data map[string]any) string {
	t.Helper()
	tmpl, err := template.New("").ParseFS(embedFS, "templates/base.html", "templates/"+page)
	if err != nil {
		t.Fatalf("parse %s: %v", page, err)
	}
	var buf bytes.Buffer
	if err = tmpl.ExecuteTemplate(&buf, "base", data); err != nil {
		t.Fatalf("execute %s: %v", page, err)
	}
	return buf.String()
}

func testCameras() []sqlc.Camera {
	return []sqlc.Camera{
		{ID: 1, Name: "cashier", RtspUrl: "rtsp://x/1", Enabled: true},
		{ID: 2, Name: "front-door", RtspUrl: "rtsp://x/2", Enabled: false},
	}
}

func TestDashboardRenderAdmin(t *testing.T) {
	out := renderPage(t, "dashboard.html", map[string]any{
		"Active":     "live",
		"Claims":     &auth.Claims{UserID: 1, Username: "admin", Role: auth.RoleAdmin},
		"Cameras":    testCameras(),
		"HLSBase":    "/hls",
		"WebRTCBase": "/webrtc",
	})

	for _, want := range []string{
		`id="tile-1"`,
		`data-name="cashier"`,
		`data-rtsp="rtsp://x/1"`,                   // admin can edit, so the URL is present
		`data-whep="/webrtc/cashier-preview/whep"`, // live view endpoint
		`data-state="idle"`,                        // enabled camera starts idle
		`data-state="off"`,                         // disabled camera starts off
		`id="camera-dialog"`,
		`id="delete-dialog"`,
		`id="settings-dialog"`,
		`aria-label="Edit front-door"`,
		`<a href="/" class="nav-tab" aria-current="page">`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("admin dashboard missing %q", want)
		}
	}
}

func TestDashboardRenderViewer(t *testing.T) {
	out := renderPage(t, "dashboard.html", map[string]any{
		"Active":     "live",
		"Claims":     &auth.Claims{UserID: 2, Username: "guard", Role: auth.RoleViewer},
		"Cameras":    testCameras(),
		"WebRTCBase": "/webrtc",
	})

	// Viewers must never receive RTSP URLs (they carry camera credentials) or
	// admin-only controls.
	for _, forbidden := range []string{"rtsp://x/", "data-rtsp", `id="camera-dialog"`, `id="settings-dialog"`, "Edit cashier"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("viewer dashboard contains %q", forbidden)
		}
	}
	if !strings.Contains(out, `data-whep="/webrtc/cashier-preview/whep"`) {
		t.Error("viewer dashboard missing live view endpoint")
	}
}

func TestDashboardRenderEmpty(t *testing.T) {
	out := renderPage(t, "dashboard.html", map[string]any{
		"Active":  "live",
		"Claims":  &auth.Claims{UserID: 2, Username: "guard", Role: auth.RoleViewer},
		"Cameras": []sqlc.Camera{},
	})
	if !strings.Contains(out, "No cameras yet") || !strings.Contains(out, "Ask an admin") {
		t.Error("viewer empty state not rendered")
	}
}

func TestRecordingsRender(t *testing.T) {
	out := renderPage(t, "recordings.html", map[string]any{
		"Active":  "recordings",
		"Claims":  &auth.Claims{UserID: 1, Username: "admin", Role: auth.RoleAdmin},
		"Cameras": testCameras(),
	})
	for _, want := range []string{
		`<option value="1">cashier</option>`,
		`<a href="/recordings" class="nav-tab" aria-current="page">`,
		`id="play-dialog"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("recordings page missing %q", want)
		}
	}
}
