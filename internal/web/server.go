package web

import (
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golden_eye/internal/auth"
	"golden_eye/internal/camera"
	"golden_eye/internal/config"
	"golden_eye/sqlc"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/go-chi/render"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed templates static
var embedFS embed.FS

// Server holds all handler dependencies.
type Server struct {
	db          *pgxpool.Pool
	q           *sqlc.Queries
	authSvc     *auth.Service
	camSvc      *camera.Service
	settings    *config.Manager
	env         *config.Env
	hlsProxy    *httputil.ReverseProxy
	webrtcProxy *httputil.ReverseProxy
}

// New creates the Server and validates template parsing on startup.
func New(
	db *pgxpool.Pool,
	authSvc *auth.Service,
	camSvc *camera.Service,
	settings *config.Manager,
	env *config.Env,
) (*Server, error) {
	// Validate that all templates parse correctly at startup.
	for _, page := range []string{"login.html", "dashboard.html", "recordings.html"} {
		if err := validateTemplate(page); err != nil {
			return nil, err
		}
	}

	hlsTarget, err := url.Parse(env.MediamtxHLS)
	if err != nil {
		return nil, err
	}

	webrtcTarget, err := url.Parse(env.MediamtxWebRTC)
	if err != nil {
		return nil, err
	}
	webrtcProxy := httputil.NewSingleHostReverseProxy(webrtcTarget)
	// mediamtx's WHEP responses carry a Location header pointing at the session
	// resource (for teardown). Rewrite it back under our /webrtc proxy prefix so
	// the browser's DELETE stays same-origin and auth-gated.
	webrtcProxy.ModifyResponse = func(resp *http.Response) error {
		if loc := resp.Header.Get("Location"); loc != "" {
			if u, perr := url.Parse(loc); perr == nil && strings.HasPrefix(u.Path, "/") {
				resp.Header.Set("Location", "/webrtc"+u.Path)
			}
		}
		return nil
	}

	return &Server{
		db:          db,
		q:           sqlc.New(db),
		authSvc:     authSvc,
		camSvc:      camSvc,
		settings:    settings,
		env:         env,
		hlsProxy:    httputil.NewSingleHostReverseProxy(hlsTarget),
		webrtcProxy: webrtcProxy,
	}, nil
}

// validateTemplate parses base + page to catch errors at startup, not at runtime.
func validateTemplate(page string) error {
	if page == "login.html" {
		_, err := template.New("").ParseFS(embedFS, "templates/"+page)
		return err
	}
	_, err := template.New("").ParseFS(embedFS, "templates/base.html", "templates/"+page)
	return err
}

// Router returns the configured chi router.
func (s *Server) Router() *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Accept", "Content-Type"},
	}))

	// Static assets (embedded).
	staticFS, _ := fs.Sub(embedFS, "static")
	r.Handle("/static/*", http.StripPrefix("/static", http.FileServer(http.FS(staticFS))))

	// Public routes.
	r.Get("/login", s.loginPage)
	r.Post("/login", s.loginSubmit)
	r.Post("/logout", s.logout)
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Authenticated routes.
	r.Group(func(r chi.Router) {
		r.Use(s.authSvc.Middleware)

		r.Get("/", s.dashboard)
		r.Get("/recordings", s.recordingsPage)

		// Auth-gated HLS proxy → mediamtx HLS.
		r.HandleFunc("/hls/{name}/*", s.hlsProxyHandler)

		// Auth-gated WebRTC (WHEP) proxy → mediamtx WebRTC. Used for the live
		// preview so camera G711 audio (unsupported by HLS) is delivered.
		r.HandleFunc("/webrtc/*", s.webrtcProxyHandler)

		// VOD: serve recording files with Range support.
		r.Get("/vod/{id}", s.vodHandler)

		// Thumbnail: serve pre-generated JPEG.
		r.Get("/thumbnail/{id}", s.thumbnailHandler)

		// Recordings API (auth-gated, all roles).
		r.Get("/api/recordings", s.apiListRecordings)

		// Admin-only API.
		r.Group(func(r chi.Router) {
			r.Use(auth.AdminOnly)

			// Cameras
			r.Get("/api/cameras", s.apiListCameras)
			r.Post("/api/cameras", s.apiCreateCamera)
			r.Put("/api/cameras/{id}", s.apiUpdateCamera)
			r.Delete("/api/cameras/{id}", s.apiDeleteCamera)

			// Settings
			r.Get("/api/settings", s.apiGetSettings)
			r.Put("/api/settings", s.apiUpdateSettings)

			// Users
			r.Get("/api/users", s.apiListUsers)
			r.Post("/api/users", s.apiCreateUser)
			r.Put("/api/users/{id}", s.apiUpdateUser)
			r.Delete("/api/users/{id}", s.apiDeleteUser)
		})
	})

	return r
}

// ── HLS Proxy ────────────────────────────────────────────────────────────────

func (s *Server) hlsProxyHandler(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	rest := chi.URLParam(r, "*")
	r.URL.Path = "/" + name + "/" + rest
	r.URL.RawPath = ""
	s.hlsProxy.ServeHTTP(w, r)
}

// ── WebRTC (WHEP) Proxy ────────────────────────────────────────────────────────

// webrtcProxyHandler forwards WHEP signaling (POST offer / DELETE session) to
// mediamtx, stripping the /webrtc prefix. Media itself flows peer-to-peer over
// UDP and does not pass through this proxy.
func (s *Server) webrtcProxyHandler(w http.ResponseWriter, r *http.Request) {
	rest := chi.URLParam(r, "*")
	r.URL.Path = "/" + rest
	r.URL.RawPath = ""
	s.webrtcProxy.ServeHTTP(w, r)
}

// ── VOD ──────────────────────────────────────────────────────────────────────

func (s *Server) vodHandler(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt64Param(r, "id")
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	rec, err := s.q.GetRecording(r.Context(), id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	http.ServeFile(w, r, rec.Path)
}

func (s *Server) thumbnailHandler(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt64Param(r, "id")
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	rec, err := s.q.GetRecording(r.Context(), id)
	if err != nil || !rec.ThumbnailPath.Valid || rec.ThumbnailPath.String == "" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, rec.ThumbnailPath.String)
}

// ── Auth ─────────────────────────────────────────────────────────────────────

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "login.html", map[string]interface{}{"Error": r.URL.Query().Get("error")})
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/login?error=bad+request", http.StatusFound)
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	user, err := s.q.GetUserByUsername(r.Context(), username)
	if err != nil || !s.authSvc.CheckPassword(user.PasswordHash, password) {
		http.Redirect(w, r, "/login?error=invalid+credentials", http.StatusFound)
		return
	}

	token, err := s.authSvc.IssueToken(user.ID, user.Username, user.Role)
	if err != nil {
		slog.Error("issue token", "err", err)
		http.Redirect(w, r, "/login?error=server+error", http.StatusFound)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName(),
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(24 * time.Hour),
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: auth.CookieName(), MaxAge: -1, Path: "/"})
	http.Redirect(w, r, "/login", http.StatusFound)
}

// ── Pages ─────────────────────────────────────────────────────────────────────

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	cameras, err := s.q.ListCameras(r.Context())
	if err != nil {
		http.Error(w, "error loading cameras", http.StatusInternalServerError)
		return
	}
	s.render(w, "dashboard.html", map[string]interface{}{
		"Claims":     auth.ClaimsFromCtx(r.Context()),
		"Cameras":    cameras,
		"HLSBase":    "/hls",
		"WebRTCBase": "/webrtc",
	})
}

func (s *Server) recordingsPage(w http.ResponseWriter, r *http.Request) {
	cameras, err := s.q.ListCameras(r.Context())
	if err != nil {
		http.Error(w, "error loading cameras", http.StatusInternalServerError)
		return
	}
	s.render(w, "recordings.html", map[string]interface{}{
		"Claims":  auth.ClaimsFromCtx(r.Context()),
		"Cameras": cameras,
	})
}

// ── API handlers ──────────────────────────────────────────────────────────────

// Cameras

func (s *Server) apiListCameras(w http.ResponseWriter, r *http.Request) {
	cams, err := s.q.ListCameras(r.Context())
	if err != nil {
		renderErr(w, r, http.StatusInternalServerError, err)
		return
	}
	render.JSON(w, r, cams)
}

func (s *Server) apiCreateCamera(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name           string `json:"name"`
		RtspURL        string `json:"rtsp_url"`
		TranscodeVideo bool   `json:"transcode_video"`
	}
	if err := render.DecodeJSON(r.Body, &body); err != nil {
		renderErr(w, r, http.StatusBadRequest, err)
		return
	}

	cam, err := s.camSvc.Create(r.Context(), body.Name, body.RtspURL, body.TranscodeVideo)
	if err != nil {
		renderErr(w, r, http.StatusBadRequest, err)
		return
	}
	render.Status(r, http.StatusCreated)
	render.JSON(w, r, cam)
}

func (s *Server) apiUpdateCamera(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt64Param(r, "id")
	if err != nil {
		renderErr(w, r, http.StatusBadRequest, err)
		return
	}
	var body struct {
		Name           string `json:"name"`
		RtspURL        string `json:"rtsp_url"`
		Enabled        bool   `json:"enabled"`
		TranscodeVideo bool   `json:"transcode_video"`
	}
	if err = render.DecodeJSON(r.Body, &body); err != nil {
		renderErr(w, r, http.StatusBadRequest, err)
		return
	}

	cam, err := s.camSvc.Update(r.Context(), id, body.Name, body.RtspURL, body.Enabled, body.TranscodeVideo)
	if err != nil {
		renderErr(w, r, http.StatusBadRequest, err)
		return
	}
	render.JSON(w, r, cam)
}

func (s *Server) apiDeleteCamera(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt64Param(r, "id")
	if err != nil {
		renderErr(w, r, http.StatusBadRequest, err)
		return
	}
	if err = s.camSvc.Delete(r.Context(), id); err != nil {
		renderErr(w, r, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Settings

func (s *Server) apiGetSettings(w http.ResponseWriter, r *http.Request) {
	render.JSON(w, r, s.settings.Get())
}

func (s *Server) apiUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var body config.Settings
	if err := render.DecodeJSON(r.Body, &body); err != nil {
		renderErr(w, r, http.StatusBadRequest, err)
		return
	}
	if err := s.settings.Update(body); err != nil {
		renderErr(w, r, http.StatusBadRequest, err)
		return
	}
	render.JSON(w, r, s.settings.Get())
}

// Users

func (s *Server) apiListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.q.ListUsers(r.Context())
	if err != nil {
		renderErr(w, r, http.StatusInternalServerError, err)
		return
	}
	render.JSON(w, r, users)
}

func (s *Server) apiCreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := render.DecodeJSON(r.Body, &body); err != nil {
		renderErr(w, r, http.StatusBadRequest, err)
		return
	}
	if body.Role != auth.RoleAdmin && body.Role != auth.RoleViewer {
		renderErr(w, r, http.StatusBadRequest, errString("role must be admin or viewer"))
		return
	}
	hash, err := s.authSvc.HashPassword(body.Password)
	if err != nil {
		renderErr(w, r, http.StatusInternalServerError, err)
		return
	}
	user, err := s.q.CreateUser(r.Context(), sqlc.CreateUserParams{
		Username:     body.Username,
		PasswordHash: hash,
		Role:         body.Role,
	})
	if err != nil {
		renderErr(w, r, http.StatusBadRequest, err)
		return
	}
	render.Status(r, http.StatusCreated)
	render.JSON(w, r, user)
}

func (s *Server) apiUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt64Param(r, "id")
	if err != nil {
		renderErr(w, r, http.StatusBadRequest, err)
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"` // empty = keep existing
		Role     string `json:"role"`
	}
	if err = render.DecodeJSON(r.Body, &body); err != nil {
		renderErr(w, r, http.StatusBadRequest, err)
		return
	}
	if body.Role != auth.RoleAdmin && body.Role != auth.RoleViewer {
		renderErr(w, r, http.StatusBadRequest, errString("role must be admin or viewer"))
		return
	}

	existing, err := s.q.GetUserByID(r.Context(), id)
	if err != nil {
		renderErr(w, r, http.StatusNotFound, err)
		return
	}

	hash := existing.PasswordHash
	if body.Password != "" {
		hash, err = s.authSvc.HashPassword(body.Password)
		if err != nil {
			renderErr(w, r, http.StatusInternalServerError, err)
			return
		}
	}

	user, err := s.q.UpdateUser(r.Context(), sqlc.UpdateUserParams{
		ID:           id,
		Username:     body.Username,
		PasswordHash: hash,
		Role:         body.Role,
	})
	if err != nil {
		renderErr(w, r, http.StatusBadRequest, err)
		return
	}
	render.JSON(w, r, user)
}

// recordingJSON is the clean API representation of a recording.
type recordingJSON struct {
	ID           int64  `json:"id"`
	CameraID     int64  `json:"camera_id"`
	StartedAt    string `json:"started_at"`
	DurationSecs int    `json:"duration_secs"`
	SizeBytes    int64  `json:"size_bytes"`
	HasThumbnail bool   `json:"has_thumbnail"`
	ThumbnailURL string `json:"thumbnail_url,omitempty"`
}

// Recordings API

func (s *Server) apiListRecordings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	params := sqlc.ListRecordingsParams{
		Limit:  50,
		Offset: 0,
	}

	if v := q.Get("limit"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil && n > 0 && n <= 500 {
			params.Limit = int32(n) //nolint:gosec
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil && n >= 0 {
			params.Offset = int32(n) //nolint:gosec
		}
	}
	if v := q.Get("camera_id"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			params.CameraID = pgtype.Int8{Int64: n, Valid: true}
		}
	}
	if v := q.Get("from"); v != "" {
		if t, err := time.ParseInLocation("2006-01-02T15:04:05", v, time.Local); err == nil {
			params.FromTime = pgtype.Timestamptz{Time: t, Valid: true}
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.ParseInLocation("2006-01-02T15:04:05", v, time.Local); err == nil {
			params.ToTime = pgtype.Timestamptz{Time: t, Valid: true}
		}
	}

	recs, err := s.q.ListRecordings(r.Context(), params)
	if err != nil {
		renderErr(w, r, http.StatusInternalServerError, err)
		return
	}

	out := make([]recordingJSON, 0, len(recs))
	for _, rec := range recs {
		rj := recordingJSON{
			ID:       rec.ID,
			CameraID: rec.CameraID,
		}
		if rec.StartedAt.Valid {
			rj.StartedAt = rec.StartedAt.Time.Format(time.RFC3339)
		}
		if rec.DurationSecs.Valid {
			rj.DurationSecs = int(rec.DurationSecs.Int32)
		}
		if rec.SizeBytes.Valid {
			rj.SizeBytes = rec.SizeBytes.Int64
		}
		if rec.ThumbnailPath.Valid && rec.ThumbnailPath.String != "" {
			rj.HasThumbnail = true
			rj.ThumbnailURL = "/thumbnail/" + strconv.FormatInt(rec.ID, 10)
		}
		out = append(out, rj)
	}
	render.JSON(w, r, out)
}

func (s *Server) apiDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt64Param(r, "id")
	if err != nil {
		renderErr(w, r, http.StatusBadRequest, err)
		return
	}
	if err = s.q.DeleteUser(r.Context(), id); err != nil {
		renderErr(w, r, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// render parses base + page templates fresh per request so that each page's
// {{define "content"}} does not conflict with another page's definition.
func (s *Server) render(w http.ResponseWriter, page string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	var (
		t   *template.Template
		err error
	)
	if page == "login.html" {
		t, err = template.New("").ParseFS(embedFS, "templates/"+page)
	} else {
		t, err = template.New("").ParseFS(embedFS, "templates/base.html", "templates/"+page)
	}
	if err != nil {
		slog.Error("parse template", "page", page, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}

	execName := "base"
	if page == "login.html" {
		execName = "login.html"
	}
	if err = t.ExecuteTemplate(w, execName, data); err != nil {
		slog.Error("execute template", "page", page, "err", err)
	}
}

type httpError struct {
	Error string `json:"error"`
}

func renderErr(w http.ResponseWriter, r *http.Request, code int, err error) {
	render.Status(r, code)
	render.JSON(w, r, httpError{Error: err.Error()})
}

type errString string

func (e errString) Error() string { return string(e) }

func parseInt64Param(r *http.Request, key string) (int64, error) {
	raw := chi.URLParam(r, key)
	if raw == "" {
		return 0, errString("missing " + key)
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, errString("invalid " + key)
	}
	return v, nil
}
