package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golden_eye/internal/auth"
	"golden_eye/internal/camera"
	"golden_eye/internal/config"
	"golden_eye/internal/db"
	"golden_eye/internal/index"
	"golden_eye/internal/mediamtx"
	"golden_eye/internal/recorder"
	"golden_eye/internal/rotation"
	"golden_eye/internal/web"
	"golden_eye/sqlc"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	// ── Config ────────────────────────────────────────────────────────────────
	env, err := config.LoadEnv()
	if err != nil {
		return fmt.Errorf("load env: %w", err)
	}

	settings, err := config.NewManager("")
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}

	// ── Database ──────────────────────────────────────────────────────────────
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	pool, err := db.New(ctx, env.PSQLURL)
	cancel()
	if err != nil {
		return fmt.Errorf("connect db: %w", err)
	}
	defer pool.Close()
	slog.Info("database connected")

	// ── Admin bootstrap ───────────────────────────────────────────────────────
	authSvc := auth.New(env.MasterKey)
	if err = bootstrapAdmin(pool, authSvc, env); err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}

	// ── mediamtx client ───────────────────────────────────────────────────────
	mtxClient := mediamtx.New(env.MediamtxAPI)
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err = mtxClient.Ping(pingCtx); err != nil {
		pingCancel()
		// Warn but don't exit — mediamtx may start after us in some setups.
		slog.Warn("mediamtx not reachable at startup; camera sync may lag", "err", err)
	} else {
		pingCancel()
		slog.Info("mediamtx connected", "api", env.MediamtxAPI)
	}

	// ── Recorder supervisor + camera service ──────────────────────────────────
	rec := recorder.New(recorder.Config{
		RecordingsDir: env.RecordingsDir,
		RTSPBase:      env.MediamtxRTSP,
	}, settings)
	defer rec.Stop()

	camSvc := camera.New(pool, mtxClient, settings, env, rec)
	reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if rErr := camSvc.Reconcile(reconcileCtx); rErr != nil {
		slog.Warn("startup reconcile incomplete", "err", rErr)
	}
	reconcileCancel()

	// ── Background workers ────────────────────────────────────────────────────
	workerCtx, workerStop := context.WithCancel(context.Background())
	defer workerStop()

	indexer := index.New(pool, env.RecordingsDir, 2*time.Minute, settings)
	go indexer.Run(workerCtx)

	sweeper := rotation.New(pool, settings)
	go sweeper.Run(workerCtx)

	// ── HTTP server ───────────────────────────────────────────────────────────
	srv, err := web.New(pool, authSvc, camSvc, settings, env)
	if err != nil {
		return fmt.Errorf("build server: %w", err)
	}

	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", env.Port),
		Handler:      srv.Router(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// ── Graceful shutdown ─────────────────────────────────────────────────────
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		slog.Info("GoldenEye started", "addr", httpServer.Addr)
		if listenErr := httpServer.ListenAndServe(); listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
			slog.Error("http server", "err", listenErr)
		}
	}()

	<-sigCh
	slog.Info("shutting down...")
	workerStop()
	rec.Stop()

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutCancel()
	if err = httpServer.Shutdown(shutCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	slog.Info("stopped")
	return nil
}

// bootstrapAdmin creates the first admin user from env if the users table is empty.
func bootstrapAdmin(pool *pgxpool.Pool, authSvc *auth.Service, env *config.Env) error {
	if env.AdminUser == "" || env.AdminPass == "" {
		return nil // no bootstrap configured
	}

	q := sqlc.New(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	count, err := q.CountUsers(ctx)
	if err != nil {
		return fmt.Errorf("count users: %w", err)
	}
	if count > 0 {
		return nil // users exist — skip bootstrap
	}

	hash, err := authSvc.HashPassword(env.AdminPass)
	if err != nil {
		return fmt.Errorf("hash admin password: %w", err)
	}

	_, err = q.CreateUser(ctx, sqlc.CreateUserParams{
		Username:     env.AdminUser,
		PasswordHash: hash,
		Role:         auth.RoleAdmin,
	})
	if err != nil {
		return fmt.Errorf("create admin user: %w", err)
	}

	slog.Info("admin user created from .env bootstrap", "username", env.AdminUser)
	return nil
}
