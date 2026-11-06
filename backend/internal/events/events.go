package events

import (
	"context"
	"fmt"
	"time"

	"phoebe/internal/apperr"
	"phoebe/internal/db"
	"phoebe/internal/ids"
)

const (
	OnboardingCompleted  = "onboarding_completed"
	RecordingStarted     = "recording_started"
	RecordingInterrupted = "recording_interrupted"
	RecordingSaved       = "recording_saved"
	DocumentSaved        = "document_saved"
	MoMReviewed          = "mom_reviewed"
	ExportCompleted      = "export_completed"
	ShareSheetOpened     = "share_sheet_opened"
	QuotaBlocked         = "quota_blocked"

	UploadVerified         = "upload_verified"
	TranscriptionCompleted = "transcription_completed"
	NotesReady             = "notes_ready"
	MoMGenerated           = "mom_generated"
)

const MaxBatch = 100

var clientNames = map[string]bool{
	OnboardingCompleted: true, RecordingStarted: true, RecordingInterrupted: true, RecordingSaved: true,
	DocumentSaved: true, MoMReviewed: true, ExportCompleted: true, ShareSheetOpened: true, QuotaBlocked: true,
}

var serverNames = map[string]bool{UploadVerified: true, TranscriptionCompleted: true, NotesReady: true, MoMGenerated: true}

// Emit records a server event inside the caller's finalize/complete tx. eventID is the
// deterministic id of the produced row (asset / transcript revision / document revision),
// so a rerun after lease expiry inserts nothing. meetingID "" = NULL.
func Emit(ctx context.Context, q db.Querier, userID, eventID, name, meetingID string) error {
	if !serverNames[name] {
		return fmt.Errorf("events: %q is not a server event", name)
	}
	var mid *string
	if meetingID != "" {
		mid = &meetingID
	}
	_, err := q.Exec(ctx, `
		INSERT INTO events (user_id, event_id, name, source, meeting_id, occurred_at)
		VALUES ($1, $2, $3, 'server', $4, now())
		ON CONFLICT DO NOTHING`, userID, eventID, name, mid)
	return err
}

type Service struct{ db db.Querier }

func New(pool db.Querier) *Service { return &Service{db: pool} }

// ClientEvent is one item of POST /v1/events as received.
type ClientEvent struct {
	EventID    string  `json:"event_id"`
	Name       string  `json:"name"`
	OccurredAt string  `json:"occurred_at"`
	MeetingID  *string `json:"meeting_id"`
}

// Ingest validates and stores a client batch; returns the newly stored count. Duplicate
// event_ids (in the batch or already stored) are skipped. A meeting_id the caller does not
// own (or tombstoned) is stored as NULL.
func (s *Service) Ingest(ctx context.Context, userID string, evs []ClientEvent) (int, error) {
	if len(evs) == 0 {
		return 0, apperr.Field("events", apperr.ReasonTooShort)
	}
	if len(evs) > MaxBatch {
		return 0, apperr.Field("events", apperr.ReasonTooLong)
	}
	var fields []apperr.FieldError
	bad := func(i int, f, reason string) {
		fields = append(fields, apperr.FieldError{Field: fmt.Sprintf("events[%d].%s", i, f), Reason: reason})
	}
	seen := map[string]bool{}
	var eventIDs, names []string
	var meetingIDs []*string
	var occurred []time.Time
	for i, e := range evs {
		id, idOK := ids.Parse(e.EventID)
		switch {
		case e.EventID == "":
			bad(i, "event_id", apperr.ReasonRequired)
		case !idOK:
			bad(i, "event_id", apperr.ReasonInvalid)
		}
		switch {
		case e.Name == "":
			bad(i, "name", apperr.ReasonRequired)
		case !clientNames[e.Name]:
			bad(i, "name", apperr.ReasonInvalid)
		}
		var at time.Time
		if e.OccurredAt == "" {
			bad(i, "occurred_at", apperr.ReasonRequired)
		} else if t, err := time.Parse(time.RFC3339Nano, e.OccurredAt); err != nil {
			bad(i, "occurred_at", apperr.ReasonInvalid)
		} else {
			at = t.UTC().Truncate(time.Millisecond)
		}
		var mid *string
		if e.MeetingID != nil {
			m, ok := ids.Parse(*e.MeetingID)
			if !ok {
				bad(i, "meeting_id", apperr.ReasonInvalid)
			}
			mid = &m
		}
		if len(fields) > 0 || seen[id] {
			continue
		}
		seen[id] = true
		eventIDs = append(eventIDs, id)
		names = append(names, e.Name)
		meetingIDs = append(meetingIDs, mid)
		occurred = append(occurred, at)
	}
	if len(fields) > 0 {
		return 0, apperr.Validation(fields...)
	}
	tag, err := s.db.Exec(ctx, `
		INSERT INTO events (user_id, event_id, name, source, meeting_id, occurred_at)
		SELECT $1, x.event_id, x.name, 'client', m.id, x.occurred_at
		FROM unnest($2::uuid[], $3::text[], $4::text[]::uuid[], $5::timestamptz[]) AS x(event_id, name, meeting_id, occurred_at)
		LEFT JOIN meetings m ON m.id = x.meeting_id AND m.user_id = $1 AND m.deleted_at IS NULL
		ON CONFLICT DO NOTHING`, userID, eventIDs, names, meetingIDs, occurred)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
