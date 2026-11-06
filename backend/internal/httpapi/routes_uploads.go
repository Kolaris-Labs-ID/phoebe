package httpapi

import (
	"io"
	"net/http"
	"strconv"
	"time"

	"phoebe/internal/apperr"
	"phoebe/internal/meetings"
	"phoebe/internal/uploads"
)

type UploadsService = *uploads.Service

const chunkIdleTimeout = 60 * time.Second

func (s *Server) routesUploads() []Route {
	return []Route{
		{Method: "POST", Path: "/v1/meetings/{id}/uploads", Handle: s.createUpload},
		{Method: "GET", Path: "/v1/uploads/{id}", Handle: s.getUpload},
		{Method: "PUT", Path: "/v1/uploads/{id}", BodyLimit: NoBodyLimit, Handle: s.putUploadChunk},
		{Method: "POST", Path: "/v1/uploads/{id}/complete", Handle: s.completeUpload},
	}
}

type uploadBody struct {
	Upload *meetings.UploadSession `json:"upload"`
}

func (s *Server) createUpload(w http.ResponseWriter, r *http.Request) error {
	mid, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	var in uploads.CreateInput
	if _, err := decodeJSON(r, &in); err != nil {
		return err
	}
	u, created, err := s.Uploads.Create(r.Context(), userID(r), mid, in)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, uploadBody{u})
	return nil
}

func (s *Server) getUpload(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	u, err := s.Uploads.Get(r.Context(), userID(r), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, uploadBody{u})
	return nil
}

// idleReader extends the connection read deadline before every read (backend.md §7.2).
type idleReader struct {
	r  io.Reader
	rc *http.ResponseController
}

func (i idleReader) Read(p []byte) (int, error) {
	_ = i.rc.SetReadDeadline(time.Now().Add(chunkIdleTimeout))
	return i.r.Read(p)
}

func (s *Server) putUploadChunk(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	var fields []apperr.FieldError
	offset, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if r.Header.Get("Upload-Offset") == "" {
		fields = append(fields, apperr.FieldError{Field: "Upload-Offset", Reason: apperr.ReasonRequired})
	} else if err != nil || offset < 0 {
		fields = append(fields, apperr.FieldError{Field: "Upload-Offset", Reason: apperr.ReasonInvalid})
	}
	if r.ContentLength < 0 {
		fields = append(fields, apperr.FieldError{Field: "Content-Length", Reason: apperr.ReasonRequired})
	}
	if len(fields) > 0 {
		return apperr.Validation(fields...)
	}
	rc := http.NewResponseController(w)
	res, err := s.Uploads.PutChunk(r.Context(), userID(r), id, offset, r.ContentLength, idleReader{r.Body, rc})
	_ = rc.SetReadDeadline(time.Time{})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

func (s *Server) completeUpload(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	res, err := s.Uploads.Complete(r.Context(), userID(r), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}
