// Package app wires domain services for cmd/api and cmd/worker, so tests exercise the
// exact production wiring.
package app

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/ai/providers"
	"phoebe/internal/audio"
	"phoebe/internal/auth"
	"phoebe/internal/config"
	"phoebe/internal/documents"
	"phoebe/internal/events"
	"phoebe/internal/export"
	"phoebe/internal/httpapi"
	"phoebe/internal/jobs"
	"phoebe/internal/meetings"
	"phoebe/internal/storage"
	"phoebe/internal/transcripts"
	"phoebe/internal/uploads"
	"phoebe/internal/usage"
	"phoebe/internal/users"
)

// revoker avoids a typed-nil users.AppleRevoker when Apple is not configured.
func revoker(a *auth.AppleClient) users.AppleRevoker {
	if a == nil {
		return nil
	}
	return a
}

func APIDeps(cfg *config.Config, log *slog.Logger, pool *pgxpool.Pool, store storage.Storage, signer *storage.Signer) (httpapi.Deps, error) {
	apple, err := auth.NewAppleClient(cfg)
	if err != nil {
		return httpapi.Deps{}, err
	}
	authSvc := auth.NewService(pool, cfg, log, apple)
	use := usage.New(pool, cfg, log)
	ms := meetings.New(pool, cfg, store, log)
	ts := transcripts.New(pool, cfg, store, use, nil, log)
	return httpapi.Deps{
		Config:      cfg,
		Log:         log,
		DB:          pool,
		Storage:     store,
		Signer:      signer,
		Sessions:    authSvc,
		Auth:        authSvc,
		Users:       users.New(pool, cfg, store, revoker(apple), log),
		Meetings:    ms,
		FoldersTags: ms,
		Uploads:     uploads.New(pool, cfg, store, log, ts),
		Transcripts: ts,
		Documents:   documents.New(pool, cfg, use, nil, log),
		Exports:     export.NewService(pool, store, signer, log),
		Files:       export.NewFiles(pool, store, signer),
		Usage:       use,
		Events:      events.New(pool),
	}, nil
}

// RegisterWorker registers every job type and every backend.md §4.7 sweep. Expired job
// leases need no sweep: jobs.Claim reclaims them.
func RegisterWorker(ctx context.Context, r *jobs.Runner, cfg *config.Config, log *slog.Logger, pool *pgxpool.Pool, store storage.Storage) error {
	if cfg.STTProvider != "fake" {
		if err := audio.CheckFFmpeg(ctx, cfg.FFmpegPath); err != nil {
			return err
		}
	}
	stt, err := providers.STT(cfg)
	if err != nil {
		return err
	}
	llm, err := providers.LLM(cfg)
	if err != nil {
		return err
	}
	apple, err := auth.NewAppleClient(cfg)
	if err != nil {
		return err
	}
	use := usage.New(pool, cfg, log)
	ms := meetings.New(pool, cfg, store, log)
	docs := documents.New(pool, cfg, use, llm, log)

	r.Register(jobs.TypeTranscribe, transcripts.New(pool, cfg, store, use, stt, log).TranscribeSpec())
	r.Register(jobs.TypeGenerateNotes, docs.NotesSpec())
	r.Register(jobs.TypeGenerateMoM, docs.MoMSpec())
	r.Register(jobs.TypePurgeMeeting, ms.PurgeMeetingSpec())
	r.Register(jobs.TypePurgeAccount, users.New(pool, cfg, store, revoker(apple), log).PurgeAccountSpec())

	r.RegisterSweep("upload_expiry", uploads.New(pool, cfg, store, log, nil).ExpireSweep)
	r.RegisterSweep("audio_retention", ms.RetentionSweep)
	r.RegisterSweep("byte_purge", ms.BytePurgeSweep)
	r.RegisterSweep("orphans", ms.OrphanSweep)
	r.RegisterSweep("alerts", jobs.AlertSweep(pool, log, cfg.AlertQueueAge, cfg.AlertDailyCostUSD))
	return nil
}
