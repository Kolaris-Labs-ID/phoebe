package uploads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"

	"github.com/jackc/pgx/v5"

	"phoebe/internal/apperr"
	"phoebe/internal/audio"
	"phoebe/internal/db"
	"phoebe/internal/events"
	"phoebe/internal/ids"
	"phoebe/internal/jobs"
	"phoebe/internal/meetings"
	"phoebe/internal/storage"
)

var audioTypes = []string{audio.M4A, audio.MP3, audio.WAV}

// Complete implements POST /v1/uploads/{id}/complete and the inline complete of the last
// chunk (backend.md §7.3): claim verify → verify outside any tx → finalize.
func (s *Service) Complete(ctx context.Context, userID, uploadID string) (*Result, error) {
	owned, err := loadOwned(ctx, s.pool, userID, uploadID)
	if err != nil {
		return nil, err
	}
	lease := ids.New()
	var x *session
	var done *Result
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.LockMeeting(ctx, tx, owned.MeetingID, userID); err != nil {
			return err
		}
		if x, err = lockSession(ctx, tx, uploadID, owned.MeetingID); err != nil {
			return err
		}
		switch x.Status {
		case "completed":
			done, err = s.storedResult(ctx, tx, x)
			return err
		case "failed", "canceled":
			return apperr.WithDetails(apperr.UploadNotOpen, map[string]any{"status": x.Status})
		case "expired":
			return apperr.New(apperr.UploadExpired)
		case "open":
			switch {
			case x.ReceivedBytes < x.SizeBytes:
				return apperr.WithDetails(apperr.UploadIncomplete, map[string]any{"received_bytes": x.ReceivedBytes, "size_bytes": x.SizeBytes})
			case x.Expired:
				return apperr.New(apperr.UploadExpired)
			case x.LeaseLive:
				return apperr.New(apperr.UploadBusy)
			}
		case "verifying":
			if x.LeaseLive {
				return apperr.New(apperr.UploadBusy)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE upload_sessions SET status = 'verifying', writer_lease_id = $2,
			writer_lease_until = now() + $3 * interval '1 millisecond', updated_at = now() WHERE id = $1`,
			uploadID, lease, ms(s.cfg.UploadWriterLease)); err != nil {
			return err
		}
		return db.TouchMeeting(ctx, tx, x.MeetingID)
	})
	if err != nil || done != nil {
		return done, err
	}

	ct, durationMS, final, verr, err := s.verify(ctx, x)
	if err != nil {
		s.releaseLease(ctx, uploadID, lease)
		return nil, err
	}
	if verr != nil {
		return nil, s.fail(ctx, x, lease, verr)
	}
	res, err := s.finalize(ctx, x, lease, ct, durationMS, final)
	if err != nil && apperr.CodeOf(err) == "" {
		s.releaseLease(ctx, uploadID, lease)
	}
	return res, err
}

func (s *Service) storedResult(ctx context.Context, q db.Querier, x *session) (*Result, error) {
	a, err := meetings.LoadAudio(ctx, q, x.MeetingID)
	if err != nil {
		return nil, err
	}
	j, err := meetings.LatestJob(ctx, q, x.MeetingID, jobs.TypeTranscribe)
	if err != nil {
		return nil, err
	}
	return &Result{Upload: x.view(s.cfg.UploadChunkMaxBytes), Audio: a, Job: j}, nil
}

// verify checks the received bytes under the verify lease. verr is the client-visible
// rejection; err is an I/O failure (session stays verifying, retry reclaims the lease).
// On success the bytes are at final.
func (s *Service) verify(ctx context.Context, x *session) (ct string, durationMS int64, final string, verr, err error) {
	src := x.StorageKey
	_, exists, err := s.storage.Stat(ctx, src)
	if err != nil {
		return "", 0, "", nil, err
	}
	if !exists {
		src = ""
		for _, t := range audioTypes {
			k := finalKey(x, t)
			if _, ok, err := s.storage.Stat(ctx, k); err != nil {
				return "", 0, "", nil, err
			} else if ok {
				src = k
				break
			}
		}
	}
	if src == "" {
		return "", 0, "", apperr.New(apperr.ChecksumMismatch), nil
	}
	f, size, err := s.storage.Open(ctx, src)
	if errors.Is(err, storage.ErrNotFound) {
		return "", 0, "", apperr.New(apperr.ChecksumMismatch), nil
	}
	if err != nil {
		return "", 0, "", nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", 0, "", nil, err
	}
	if size != x.SizeBytes || hex.EncodeToString(h.Sum(nil)) != x.SHA256 {
		return "", 0, "", apperr.New(apperr.ChecksumMismatch), nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", 0, "", nil, err
	}
	ct, durationMS, err = audio.Probe(f, size)
	if err == nil {
		err = audio.CheckDuration(durationMS, s.cfg.MaxAudioSeconds*1000)
	}
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return "", 0, "", ae, nil
	}
	if err != nil {
		return "", 0, "", nil, err
	}
	final = finalKey(x, ct)
	if src != final {
		if err := s.storage.Move(ctx, src, final); err != nil {
			// A reclaiming verifier may have moved it first.
			if _, ok, serr := s.storage.Stat(ctx, final); !errors.Is(err, storage.ErrNotFound) || serr != nil || !ok {
				return "", 0, "", nil, err
			}
		}
	}
	return ct, durationMS, final, nil, nil
}

func (s *Service) deleteBytes(ctx context.Context, x *session) {
	ctx = context.WithoutCancel(ctx)
	_ = s.storage.Delete(ctx, x.StorageKey)
	for _, t := range audioTypes {
		_ = s.storage.Delete(ctx, finalKey(x, t))
	}
}

// fail records a verification rejection (session failed, meeting back to awaiting_audio)
// and returns verr, or the error that prevented recording it.
func (s *Service) fail(ctx context.Context, x *session, lease string, verr error) error {
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.LockMeeting(ctx, tx, x.MeetingID, x.UserID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE upload_sessions SET status = 'failed', failure_reason = $3, writer_lease_id = NULL,
			writer_lease_until = NULL, updated_at = now() WHERE id = $1 AND writer_lease_id = $2 AND status = 'verifying'`,
			x.ID, lease, string(apperr.CodeOf(verr)))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apperr.New(apperr.UploadBusy)
		}
		if _, err := meetings.Transition(ctx, tx, x.MeetingID, meetings.UploadAbandoned); err != nil {
			return err
		}
		return db.TouchMeeting(ctx, tx, x.MeetingID)
	})
	if apperr.Is(err, apperr.UploadBusy) {
		return err
	}
	if err != nil && !errors.Is(err, apperr.ErrNotFound) {
		s.releaseLease(ctx, x.ID, lease)
		return err
	}
	s.deleteBytes(ctx, x)
	if err != nil {
		return err
	}
	return verr
}

func (s *Service) finalize(ctx context.Context, x *session, lease, ct string, durationMS int64, final string) (*Result, error) {
	assetID := ids.New()
	var job *jobs.Job
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.LockMeeting(ctx, tx, x.MeetingID, x.UserID); err != nil {
			return err
		}
		var held bool
		if err := tx.QueryRow(ctx, `SELECT status = 'verifying' AND writer_lease_id IS NOT DISTINCT FROM $2::uuid
			FROM upload_sessions WHERE id = $1 FOR UPDATE`, x.ID, lease).Scan(&held); err != nil {
			return err
		}
		if !held {
			return apperr.New(apperr.UploadBusy)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO audio_assets (id, meeting_id, upload_session_id, content_type, size_bytes,
			sha256, duration_ms, storage_key) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			assetID, x.MeetingID, x.ID, ct, x.SizeBytes, x.SHA256, durationMS, final); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE upload_sessions SET status = 'completed', completed_at = now(),
			writer_lease_id = NULL, writer_lease_until = NULL, updated_at = now() WHERE id = $1`, x.ID); err != nil {
			return err
		}
		if _, err := meetings.Transition(ctx, tx, x.MeetingID, meetings.UploadVerified); err != nil {
			return err
		}
		if err := events.Emit(ctx, tx, x.UserID, assetID, events.UploadVerified, x.MeetingID); err != nil {
			return err
		}
		if x.TranscribeKey != nil && s.starter != nil {
			j, err := s.autoStart(ctx, tx, x)
			if err != nil {
				return err
			}
			job = j
		}
		return db.TouchMeeting(ctx, tx, x.MeetingID)
	})
	if errors.Is(err, apperr.ErrNotFound) {
		_ = s.storage.Delete(context.WithoutCancel(ctx), final)
	}
	if err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "upload.completed", "meeting_id", x.MeetingID, "size_bytes", x.SizeBytes,
		"content_type", ct, "audio_ms", durationMS)
	done, err := loadOwned(ctx, s.pool, x.UserID, x.ID)
	if err != nil {
		return nil, err
	}
	a, err := meetings.LoadAudio(ctx, s.pool, x.MeetingID)
	if err != nil {
		return nil, err
	}
	return &Result{Upload: done.view(s.cfg.UploadChunkMaxBytes), Audio: a, Job: meetings.JobView(job)}, nil
}

// autoStart swallows refusals (402 quota, 409, …): the meeting stays uploaded with job
// null, and iOS's explicit POST …/transcription gets the reason.
func (s *Service) autoStart(ctx context.Context, tx pgx.Tx, x *session) (*jobs.Job, error) {
	j, _, err := s.starter.StartInTx(ctx, tx, x.UserID, x.MeetingID, *x.TranscribeKey, *x.TranscribeLang)
	if apperr.CodeOf(err) != "" {
		return nil, nil
	}
	return j, err
}
