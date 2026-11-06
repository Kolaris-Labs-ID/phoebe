package apperr

import "errors"

type Code string

// api.md §1.2. HTTP status and message per code live in httpapi's mapping table.
const (
	InvalidJSON           Code = "invalid_json"
	MissingIdempotencyKey Code = "missing_idempotency_key"
	Unauthenticated       Code = "unauthenticated"
	AccessTokenExpired    Code = "access_token_expired"
	SessionRevoked        Code = "session_revoked"
	SessionExpired        Code = "session_expired"
	QuotaExceeded         Code = "quota_exceeded"
	EntitlementInactive   Code = "entitlement_inactive"
	InvalidSignature      Code = "invalid_signature"
	NotFound              Code = "not_found"
	RevisionConflict      Code = "revision_conflict"
	JobInProgress         Code = "job_in_progress"
	AlreadyTranscribed    Code = "already_transcribed"
	AudioExists           Code = "audio_exists"
	AudioNotReady         Code = "audio_not_ready"
	TranscriptNotReady    Code = "transcript_not_ready"
	AINotesNotReady       Code = "ai_notes_not_ready"
	UploadOffsetMismatch  Code = "upload_offset_mismatch"
	UploadBusy            Code = "upload_busy"
	UploadNotOpen         Code = "upload_not_open"
	UploadIncomplete      Code = "upload_incomplete"
	NameTaken             Code = "name_taken"
	MeetingIDConflict     Code = "meeting_id_conflict"
	IDConflict            Code = "id_conflict"
	MetadataConflict      Code = "metadata_conflict"
	UploadExpired         Code = "upload_expired"
	AudioUnavailable      Code = "audio_unavailable"
	URLExpired            Code = "url_expired"
	PayloadTooLarge       Code = "payload_too_large"
	ChunkTooLarge         Code = "chunk_too_large"
	UploadTooLarge        Code = "upload_too_large"
	ValidationFailed      Code = "validation_failed"
	InvalidContent        Code = "invalid_content"
	IdempotencyKeyReused  Code = "idempotency_key_reused"
	ChecksumMismatch      Code = "checksum_mismatch"
	AudioUnsupported      Code = "audio_unsupported"
	AudioCorrupt          Code = "audio_corrupt"
	AudioEmpty            Code = "audio_empty"
	AudioTooLong          Code = "audio_too_long"
	AIGenerationLimit     Code = "ai_generation_limit"
	Internal              Code = "internal"
	Unavailable           Code = "unavailable"
)

// Validation reasons (api.md §1.1).
const (
	ReasonRequired   = "required"
	ReasonTooLong    = "too_long"
	ReasonTooShort   = "too_short"
	ReasonInvalid    = "invalid"
	ReasonNotFound   = "not_found"
	ReasonDuplicate  = "duplicate"
	ReasonOutOfRange = "out_of_range"
)

// Details must never carry meeting content (api.md §1.1).
type Error struct {
	Code    Code
	Details map[string]any
}

func (e *Error) Error() string { return string(e.Code) }

// Is matches by code, so errors.Is(err, apperr.ErrNotFound) works for any not_found error.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

var ErrNotFound = &Error{Code: NotFound}

func New(code Code) *Error { return &Error{Code: code} }

func WithDetails(code Code, details map[string]any) *Error {
	return &Error{Code: code, Details: details}
}

type FieldError struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

func Validation(fields ...FieldError) *Error {
	return &Error{Code: ValidationFailed, Details: map[string]any{"fields": fields}}
}

func Field(field, reason string) *Error {
	return Validation(FieldError{Field: field, Reason: reason})
}

// path is a JSON pointer into the submitted content.
func Content(path, reason string) *Error {
	return &Error{Code: InvalidContent, Details: map[string]any{"path": path, "reason": reason}}
}

func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func Is(err error, code Code) bool { return CodeOf(err) == code }
