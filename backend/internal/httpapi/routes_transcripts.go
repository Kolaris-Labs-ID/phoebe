package httpapi

import (
	"net/http"
	"strconv"

	"phoebe/internal/apperr"
	"phoebe/internal/meetings"
	"phoebe/internal/transcripts"
)

type TranscriptsService = *transcripts.Service

func (s *Server) routesTranscripts() []Route {
	return []Route{
		{Method: "POST", Path: "/v1/meetings/{id}/transcription", Handle: s.startTranscription},
		{Method: "GET", Path: "/v1/meetings/{id}/transcript", Handle: s.getTranscript},
		{Method: "GET", Path: "/v1/meetings/{id}/transcript/revisions", Handle: s.listTranscriptRevisions},
		{Method: "GET", Path: "/v1/meetings/{id}/transcript/revisions/{n}", Handle: s.getTranscriptRevision},
		{Method: "POST", Path: "/v1/meetings/{id}/transcript/revisions", Handle: s.correctTranscript},
		{Method: "GET", Path: "/v1/meetings/{id}/jobs", Handle: s.listMeetingJobs},
		{Method: "GET", Path: "/v1/jobs/{job_id}", Handle: s.getJob},
	}
}

type jobBody struct {
	Job *meetings.Job `json:"job"`
}

type transcriptRevisionBody struct {
	Revision *transcripts.Revision `json:"revision"`
}

func (s *Server) startTranscription(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	key, err := idempotencyKey(r)
	if err != nil {
		return err
	}
	var in transcripts.StartInput
	raw, err := decodeJSON(r, &in)
	if err != nil {
		return err
	}
	j, created, err := s.Transcripts.Start(r.Context(), userID(r), id, key, in, raw)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	writeJSON(w, status, jobBody{j})
	return nil
}

func (s *Server) getTranscript(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	rev, err := s.Transcripts.Current(r.Context(), userID(r), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, transcriptRevisionBody{rev})
	return nil
}

func (s *Server) listTranscriptRevisions(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	var c transcripts.Cursor
	limit, hasCursor, err := pageParams(r, &c)
	if err != nil {
		return err
	}
	var after *transcripts.Cursor
	if hasCursor {
		after = &c
	}
	items, next, err := s.Transcripts.ListRevisions(r.Context(), userID(r), id, limit, after)
	if err != nil {
		return err
	}
	var cur *string
	if next != nil {
		cur = encodeCursor(next)
	}
	writeJSON(w, http.StatusOK, NewPage(items, cur))
	return nil
}

func (s *Server) getTranscriptRevision(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 {
		return apperr.ErrNotFound
	}
	rev, err := s.Transcripts.GetRevision(r.Context(), userID(r), id, n)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, transcriptRevisionBody{rev})
	return nil
}

func (s *Server) correctTranscript(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	var in transcripts.CorrectionInput
	raw, err := decodeJSON(r, &in)
	if err != nil {
		return err
	}
	rev, created, err := s.Transcripts.Correct(r.Context(), userID(r), id, in, raw)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, transcriptRevisionBody{rev})
	return nil
}

func (s *Server) listMeetingJobs(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	items, err := s.Transcripts.MeetingJobs(r.Context(), userID(r), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, struct {
		Items []*meetings.Job `json:"items"`
	}{items})
	return nil
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "job_id")
	if err != nil {
		return err
	}
	j, err := s.Transcripts.Job(r.Context(), userID(r), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, jobBody{j})
	return nil
}
