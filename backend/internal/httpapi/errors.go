package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"phoebe/internal/apperr"
	"phoebe/internal/db"
	"phoebe/internal/obs"
)

type codeInfo struct {
	status  int
	message string
}

// codeTable is the api.md §1.2 mapping: code → HTTP status + English debug message.
var codeTable = map[apperr.Code]codeInfo{
	apperr.InvalidJSON:           {http.StatusBadRequest, "Request body is not valid JSON or has wrong types."},
	apperr.MissingIdempotencyKey: {http.StatusBadRequest, "Idempotency-Key header is missing or not a UUID."},
	apperr.Unauthenticated:       {http.StatusUnauthorized, "Authentication required."},
	apperr.QuotaExceeded:         {http.StatusPaymentRequired, "Not enough remaining quota."},
	apperr.EntitlementInactive:   {http.StatusPaymentRequired, "Entitlement is inactive or outside its period."},
	apperr.InvalidSignature:      {http.StatusForbidden, "Signed URL signature is invalid."},
	apperr.NotFound:              {http.StatusNotFound, "Not found."},
	apperr.RevisionConflict:      {http.StatusConflict, "Document was changed by another device."},
	apperr.JobInProgress:         {http.StatusConflict, "A job of this type is already in progress."},
	apperr.AlreadyTranscribed:    {http.StatusConflict, "Meeting already has a transcript."},
	apperr.AudioExists:           {http.StatusConflict, "Meeting already has verified audio."},
	apperr.AudioNotReady:         {http.StatusConflict, "No verified audio yet."},
	apperr.TranscriptNotReady:    {http.StatusConflict, "Operation needs a transcript."},
	apperr.AINotesNotReady:       {http.StatusConflict, "MoM generation needs an AI notes revision."},
	apperr.UploadOffsetMismatch:  {http.StatusConflict, "Upload-Offset does not match received bytes."},
	apperr.UploadBusy:            {http.StatusConflict, "Upload session is busy; retry shortly."},
	apperr.UploadNotOpen:         {http.StatusConflict, "Upload session is not open."},
	apperr.UploadIncomplete:      {http.StatusConflict, "Upload is incomplete."},
	apperr.NameTaken:             {http.StatusConflict, "Name already exists."},
	apperr.MeetingIDConflict:     {http.StatusConflict, "Meeting id cannot be used."},
	apperr.IDConflict:            {http.StatusConflict, "Id cannot be used."},
	apperr.MetadataConflict:      {http.StatusConflict, "Meeting metadata was changed by another device."},
	apperr.UploadExpired:         {http.StatusGone, "Upload session expired."},
	apperr.AudioUnavailable:      {http.StatusGone, "Audio is no longer available."},
	apperr.URLExpired:            {http.StatusGone, "Signed URL expired."},
	apperr.PayloadTooLarge:       {http.StatusRequestEntityTooLarge, "Request body too large."},
	apperr.ChunkTooLarge:         {http.StatusRequestEntityTooLarge, "Chunk too large."},
	apperr.UploadTooLarge:        {http.StatusRequestEntityTooLarge, "Upload too large."},
	apperr.ValidationFailed:      {http.StatusUnprocessableEntity, "Validation failed."},
	apperr.InvalidContent:        {http.StatusUnprocessableEntity, "Content is invalid."},
	apperr.IdempotencyKeyReused:  {http.StatusUnprocessableEntity, "Idempotency key was used with a different request."},
	apperr.ChecksumMismatch:      {http.StatusUnprocessableEntity, "Checksum does not match."},
	apperr.AudioUnsupported:      {http.StatusUnprocessableEntity, "Audio format is not supported."},
	apperr.AudioCorrupt:          {http.StatusUnprocessableEntity, "Audio file is corrupt."},
	apperr.AudioEmpty:            {http.StatusUnprocessableEntity, "Audio is too short."},
	apperr.AudioTooLong:          {http.StatusUnprocessableEntity, "Audio is too long."},
	apperr.RateLimited:           {http.StatusTooManyRequests, "AI generation limit reached."},
	apperr.Internal:              {http.StatusInternalServerError, "Internal error."},
	apperr.Unavailable:           {http.StatusServiceUnavailable, "Service unavailable."},
}

var errNotFound = apperr.ErrNotFound

type errorBody struct {
	Error errorObj `json:"error"`
}

type errorObj struct {
	Code      apperr.Code    `json:"code"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details"`
	RequestID string         `json:"request_id"`
}

// toAppErr maps any error to an apperr: apperr as-is, body limit → payload_too_large,
// DB unreachable → unavailable, everything else → internal.
func toAppErr(err error) *apperr.Error {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		if _, ok := codeTable[ae.Code]; ok {
			return ae
		}
		return apperr.New(apperr.Internal)
	}
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return apperr.New(apperr.PayloadTooLarge)
	}
	if db.IsUnavailable(err) {
		return apperr.New(apperr.Unavailable)
	}
	return apperr.New(apperr.Internal)
}

// writeError writes the error envelope; 5xx are logged with the error (never request
// content). Retry-After is set for rate_limited.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	ae := toAppErr(err)
	info := codeTable[ae.Code]
	if info.status >= 500 && !errors.Is(r.Context().Err(), context.Canceled) {
		s.Log.ErrorContext(r.Context(), "http.error", "route", r.Pattern, "http_status", info.status, "error", err.Error())
	}
	details := ae.Details
	if details == nil {
		details = map[string]any{}
	}
	if ae.Code == apperr.RateLimited {
		if v, ok := details["retry_after_seconds"].(int64); ok {
			w.Header().Set("Retry-After", strconv.FormatInt(v, 10))
		}
	}
	writeJSON(w, info.status, errorBody{Error: errorObj{
		Code:      ae.Code,
		Message:   info.message,
		Details:   details,
		RequestID: obs.CorrelationID(r.Context()),
	}})
}
