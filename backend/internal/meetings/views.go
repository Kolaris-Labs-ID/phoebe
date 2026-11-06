package meetings

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"phoebe/internal/db"
	"phoebe/internal/jobs"
	"phoebe/internal/jsonx"
)

// API shapes of api.md §2 / §4.2 shared by the meeting object and other domains' responses.

type Meeting struct {
	ID               string      `json:"id"`
	Source           string      `json:"source"`
	Title            string      `json:"title"`
	StartedAt        *jsonx.Time `json:"started_at"`
	Timezone         string      `json:"timezone"`
	Language         string      `json:"language"`
	Participants     []string    `json:"participants"`
	MetadataRevision int         `json:"metadata_revision"`
	FolderID         *string     `json:"folder_id"`
	TagIDs           []string    `json:"tag_ids"`
	PipelineState    string      `json:"pipeline_state"`
	CreatedAt        jsonx.Time  `json:"created_at"`
	UpdatedAt        jsonx.Time  `json:"updated_at"`

	Audio      *AudioAsset    `json:"audio"`
	Upload     *UploadSession `json:"upload"`
	Transcript *TranscriptRef `json:"transcript"`
	Documents  Documents      `json:"documents"`
	Jobs       MeetingJobs    `json:"jobs"`
}

type TranscriptRef struct {
	CurrentRevisionID     string `json:"current_revision_id"`
	CurrentRevisionNumber int    `json:"current_revision_number"`
}

type Documents struct {
	ManualNotes *DocumentSummary `json:"manual_notes"`
	AINotes     *DocumentSummary `json:"ai_notes"`
	MoM         *DocumentSummary `json:"mom"`
}

type MeetingJobs struct {
	Transcribe    *Job `json:"transcribe"`
	GenerateNotes *Job `json:"generate_notes"`
	GenerateMoM   *Job `json:"generate_mom"`
}

type ListItem struct {
	ID                         string      `json:"id"`
	Source                     string      `json:"source"`
	Title                      string      `json:"title"`
	StartedAt                  *jsonx.Time `json:"started_at"`
	Timezone                   string      `json:"timezone"`
	FolderID                   *string     `json:"folder_id"`
	TagIDs                     []string    `json:"tag_ids"`
	PipelineState              string      `json:"pipeline_state"`
	DurationMS                 *int64      `json:"duration_ms"`
	AudioAvailable             bool        `json:"audio_available"`
	MoMReviewStatus            *string     `json:"mom_review_status"`
	ActiveJobTypes             []string    `json:"active_job_types"`
	FailedStage                *string     `json:"failed_stage"`
	TranscriptionErrorCategory *string     `json:"transcription_error_category"`
	CreatedAt                  jsonx.Time  `json:"created_at"`
	UpdatedAt                  jsonx.Time  `json:"updated_at"`
}

type AudioAsset struct {
	ID            string      `json:"id"`
	ContentType   string      `json:"content_type"`
	SizeBytes     int64       `json:"size_bytes"`
	DurationMS    int64       `json:"duration_ms"`
	SHA256        string      `json:"sha256"`
	Available     bool        `json:"available"`
	DeletedReason *string     `json:"deleted_reason"`
	RetainUntil   *jsonx.Time `json:"retain_until"`
	CreatedAt     jsonx.Time  `json:"created_at"`
}

type UploadSession struct {
	ID                string     `json:"id"`
	MeetingID         string     `json:"meeting_id"`
	Status            string     `json:"status"`
	ContentType       string     `json:"content_type"`
	SizeBytes         int64      `json:"size_bytes"`
	ReceivedBytes     int64      `json:"received_bytes"`
	SHA256            string     `json:"sha256"`
	DurationMS        int64      `json:"duration_ms"`
	ChunkMaxBytes     int64      `json:"chunk_max_bytes"`
	FailureReason     *string    `json:"failure_reason"`
	AutoTranscription bool       `json:"auto_transcription"`
	ExpiresAt         jsonx.Time `json:"expires_at"`
	CreatedAt         jsonx.Time `json:"created_at"`
}

type Job struct {
	ID            string          `json:"id"`
	MeetingID     *string         `json:"meeting_id"`
	Type          string          `json:"type"`
	Trigger       string          `json:"trigger"`
	Status        string          `json:"status"`
	Attempts      int             `json:"attempts"`
	MaxAttempts   int             `json:"max_attempts"`
	ErrorCategory *string         `json:"error_category"`
	Result        json.RawMessage `json:"result"`
	CreatedAt     jsonx.Time      `json:"created_at"`
	StartedAt     *jsonx.Time     `json:"started_at"`
	FinishedAt    *jsonx.Time     `json:"finished_at"`
}

type DocumentSummary struct {
	ID                    string     `json:"id"`
	Kind                  string     `json:"kind"`
	CurrentRevisionNumber int        `json:"current_revision_number"`
	CurrentRevisionID     string     `json:"current_revision_id"`
	ReviewStatus          string     `json:"review_status"`
	IsStale               bool       `json:"is_stale"`
	StaleReasons          []string   `json:"stale_reasons"`
	UpdatedAt             jsonx.Time `json:"updated_at"`
}

// JobView maps a job row to the api.md Job object (nil → nil).
func JobView(j *jobs.Job) *Job {
	if j == nil {
		return nil
	}
	return &Job{
		ID: j.ID, MeetingID: j.MeetingID, Type: j.Type, Trigger: j.Trigger, Status: j.Status,
		Attempts: j.Attempts, MaxAttempts: j.MaxAttempts, ErrorCategory: j.LastErrorCategory,
		Result: j.Result, CreatedAt: jsonx.NewTime(j.CreatedAt),
		StartedAt: jsonx.TimePtr(j.StartedAt), FinishedAt: jsonx.TimePtr(j.FinishedAt),
	}
}

// LatestJob returns the newest job of jobType for the meeting, nil when none.
func LatestJob(ctx context.Context, q db.Querier, meetingID, jobType string) (*Job, error) {
	j, err := jobs.Latest(ctx, q, meetingID, jobType)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return JobView(j), nil
}

// LoadAudio returns the meeting's audio asset (also when deleted: available=false), nil when none.
func LoadAudio(ctx context.Context, q db.Querier, meetingID string) (*AudioAsset, error) {
	var a AudioAsset
	var retain *time.Time
	var created time.Time
	err := q.QueryRow(ctx, `
		SELECT id, content_type, size_bytes, duration_ms, sha256, deleted_at IS NULL, deleted_reason, retain_until, created_at
		FROM audio_assets WHERE meeting_id = $1`, meetingID).
		Scan(&a.ID, &a.ContentType, &a.SizeBytes, &a.DurationMS, &a.SHA256, &a.Available, &a.DeletedReason, &retain, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.RetainUntil = jsonx.TimePtr(retain)
	a.CreatedAt = jsonx.NewTime(created)
	return &a, nil
}

// LoadLiveUpload returns the meeting's open/verifying upload session, nil when none.
func LoadLiveUpload(ctx context.Context, q db.Querier, meetingID string, chunkMaxBytes int64) (*UploadSession, error) {
	var u UploadSession
	var expires, created time.Time
	err := q.QueryRow(ctx, `
		SELECT id, meeting_id, status, declared_content_type, size_bytes, received_bytes, sha256, declared_duration_ms,
		       failure_reason, transcribe_idempotency_key IS NOT NULL, expires_at, created_at
		FROM upload_sessions WHERE meeting_id = $1 AND status IN ('open', 'verifying')
		ORDER BY created_at DESC LIMIT 1`, meetingID).
		Scan(&u.ID, &u.MeetingID, &u.Status, &u.ContentType, &u.SizeBytes, &u.ReceivedBytes, &u.SHA256, &u.DurationMS,
			&u.FailureReason, &u.AutoTranscription, &expires, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.ChunkMaxBytes = chunkMaxBytes
	u.ExpiresAt = jsonx.NewTime(expires)
	u.CreatedAt = jsonx.NewTime(created)
	return &u, nil
}

func loadTranscriptRef(ctx context.Context, q db.Querier, meetingID string) (*TranscriptRef, error) {
	var t TranscriptRef
	err := q.QueryRow(ctx, `
		SELECT id, revision_number FROM transcript_revisions WHERE meeting_id = $1
		ORDER BY revision_number DESC LIMIT 1`, meetingID).Scan(&t.CurrentRevisionID, &t.CurrentRevisionNumber)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// LoadDocumentSummaries returns the api.md DocumentSummary of each kind (nil when the
// document has no revision). Staleness is computed here, at read time (backend.md §10).
func LoadDocumentSummaries(ctx context.Context, q db.Querier, meetingID string) (Documents, error) {
	rows, err := q.Query(ctx, `
		WITH d AS (
			SELECT d.id, d.kind, d.current_revision_number, d.updated_at, r.id AS rid, r.reviewed_at,
			       r.source_transcript_revision_id AS st, r.source_manual_notes_revision_id AS sm,
			       r.source_ai_notes_revision_id AS sa
			FROM documents d
			JOIN document_revisions r ON r.document_id = d.id AND r.revision_number = d.current_revision_number
			WHERE d.meeting_id = $1
		)
		SELECT d.id, d.kind, d.current_revision_number, d.updated_at, d.rid, d.reviewed_at IS NOT NULL,
		       d.st IS NULL AND d.sm IS NULL AND d.sa IS NULL,
		       d.st IS DISTINCT FROM (SELECT t.id FROM transcript_revisions t WHERE t.meeting_id = $1
		                              ORDER BY t.revision_number DESC LIMIT 1),
		       d.sm IS DISTINCT FROM (SELECT rid FROM d WHERE kind = 'manual_notes'),
		       d.sa IS DISTINCT FROM (SELECT rid FROM d WHERE kind = 'ai_notes')
		FROM d`, meetingID)
	if err != nil {
		return Documents{}, err
	}
	defer rows.Close()
	var out Documents
	for rows.Next() {
		var s DocumentSummary
		var updated time.Time
		var reviewed, noSources, staleT, staleM, staleA bool
		if err := rows.Scan(&s.ID, &s.Kind, &s.CurrentRevisionNumber, &updated, &s.CurrentRevisionID, &reviewed,
			&noSources, &staleT, &staleM, &staleA); err != nil {
			return Documents{}, err
		}
		s.UpdatedAt = jsonx.NewTime(updated)
		s.ReviewStatus = "draft"
		if reviewed {
			s.ReviewStatus = "reviewed"
		}
		s.StaleReasons = []string{}
		if s.Kind != "manual_notes" && !noSources {
			if staleT {
				s.StaleReasons = append(s.StaleReasons, "transcript")
			}
			if staleM {
				s.StaleReasons = append(s.StaleReasons, "manual_notes")
			}
			if s.Kind == "mom" && staleA {
				s.StaleReasons = append(s.StaleReasons, "ai_notes")
			}
		}
		s.IsStale = len(s.StaleReasons) > 0
		switch s.Kind {
		case "manual_notes":
			out.ManualNotes = &s
		case "ai_notes":
			out.AINotes = &s
		case "mom":
			out.MoM = &s
		}
	}
	return out, rows.Err()
}
