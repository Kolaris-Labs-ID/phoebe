package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	"phoebe/internal/app"
	"phoebe/internal/config"
	"phoebe/internal/db"
	"phoebe/internal/httpapi"
	"phoebe/internal/obs"
	"phoebe/internal/storage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "api:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.CheckAPIStartup(); err != nil {
		return err
	}
	log := obs.NewLogger(os.Stdout, cfg.LogLevel)
	if cfg.DevLoginEnabled {
		log.Warn("startup.dev_login_enabled")
	}
	if !cfg.AppleConfigured() {
		log.Warn("startup.apple_revocation_not_configured")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	store, err := storage.NewLocalFS(cfg.StorageDir)
	if err != nil {
		return err
	}
	signer := storage.NewSigner(cfg.SigningSecret, cfg.PublicBaseURL, cfg.SignedURLTTL)

	deps, err := app.APIDeps(cfg, log, pool, store, signer)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.New(deps),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("startup.listening", "addr", cfg.HTTPAddr)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
