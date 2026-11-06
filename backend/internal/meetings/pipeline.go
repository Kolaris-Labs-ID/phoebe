package meetings

import (
	"context"

	"phoebe/internal/db"
)

// pipeline_state values (backend.md §3.1).
const (
	StateAwaitingAudio       = "awaiting_audio"
	StateUploading           = "uploading"
	StateUploaded            = "uploaded"
	StateTranscribing        = "transcribing"
	StateTranscriptionFailed = "transcription_failed"
	StateTranscriptReady     = "transcript_ready"
	StateGeneratingNotes     = "generating_notes"
	StateNotesFailed         = "notes_failed"
	StateNotesReady          = "notes_ready"
)

// Event is a backend.md §3.1 transition trigger.
type Event int

const (
	UploadCreated        Event = iota // awaiting_audio|uploading → uploading
	UploadVerified                    // uploading → uploaded
	UploadAbandoned                   // uploading → awaiting_audio, only when no open/verifying session is left
	TranscriptionStarted              // uploaded|transcription_failed → transcribing
	TranscriptWritten                 // transcribing → generating_notes (notes "auto")
	TranscriptOnly                    // transcribing → transcript_ready (notes "skip")
	TranscriptionFailed               // transcribing → transcription_failed
	NotesRequested                    // transcript_ready|notes_ready|notes_failed → generating_notes
	NotesWritten                      // generating_notes → notes_ready
	NotesFailed                       // generating_notes → notes_failed
)

var transitions = map[Event]struct {
	from []string
	to   string
}{
	UploadCreated:        {[]string{StateAwaitingAudio, StateUploading}, StateUploading},
	UploadVerified:       {[]string{StateUploading}, StateUploaded},
	UploadAbandoned:      {[]string{StateUploading}, StateAwaitingAudio},
	TranscriptionStarted: {[]string{StateUploaded, StateTranscriptionFailed}, StateTranscribing},
	TranscriptWritten:    {[]string{StateTranscribing}, StateGeneratingNotes},
	TranscriptOnly:       {[]string{StateTranscribing}, StateTranscriptReady},
	TranscriptionFailed:  {[]string{StateTranscribing}, StateTranscriptionFailed},
	NotesRequested:       {[]string{StateTranscriptReady, StateNotesReady, StateNotesFailed}, StateGeneratingNotes},
	NotesWritten:         {[]string{StateGeneratingNotes}, StateNotesReady},
	NotesFailed:          {[]string{StateGeneratingNotes}, StateNotesFailed},
}

// Transition applies ev inside the caller's tx (which already holds the meeting lock) and
// bumps updated_at when the state changed. changed=false means the meeting was not in a
// from-state; nothing is written. For UploadAbandoned, update the session status first.
func Transition(ctx context.Context, q db.Querier, meetingID string, ev Event) (changed bool, err error) {
	t, ok := transitions[ev]
	if !ok {
		panic("meetings: unknown pipeline event")
	}
	sql := `UPDATE meetings SET pipeline_state = $2, updated_at = now()
		WHERE id = $1 AND pipeline_state = ANY($3) AND pipeline_state <> $2`
	if ev == UploadAbandoned {
		sql += ` AND NOT EXISTS (SELECT 1 FROM upload_sessions s WHERE s.meeting_id = $1 AND s.status IN ('open', 'verifying'))`
	}
	tag, err := q.Exec(ctx, sql, meetingID, t.to, t.from)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
