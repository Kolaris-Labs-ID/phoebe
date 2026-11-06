package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata"

	"phoebe/internal/app"
	"phoebe/internal/config"
	"phoebe/internal/db"
	"phoebe/internal/jobs"
	"phoebe/internal/obs"
	"phoebe/internal/storage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := obs.NewLogger(os.Stdout, cfg.LogLevel)

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

	runner := jobs.NewRunner(pool, log, jobs.RunnerConfig{
		WorkerID:      cfg.WorkerID,
		Concurrency:   cfg.WorkerConcurrency,
		PollInterval:  cfg.WorkerPollInterval,
		Lease:         cfg.JobLease,
		ShutdownGrace: cfg.WorkerShutdownGrace,
		RetryBase:     cfg.JobRetryBase,
		SweepInterval: cfg.SweepInterval,
	})
	if err := app.RegisterWorker(ctx, runner, cfg, log, pool, store); err != nil {
		return err
	}
	log.Info("startup.worker", "count", cfg.WorkerConcurrency)
	return runner.Run(ctx)
}
