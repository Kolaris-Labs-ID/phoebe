package httpapi

import (
	"net/http"
	"strconv"

	"phoebe/internal/apperr"
	"phoebe/internal/documents"
	"phoebe/internal/meetings"
)

type DocumentsService = *documents.Service

func (s *Server) routesDocuments() []Route {
	const base = "/v1/meetings/{id}/documents"
	return []Route{
		{Method: "GET", Path: base + "/{kind}", Handle: s.getDocument},
		{Method: "PUT", Path: base + "/{kind}", Handle: s.putDocument},
		{Method: "GET", Path: base + "/{kind}/revisions", Handle: s.listDocumentRevisions},
		{Method: "GET", Path: base + "/{kind}/revisions/{n}", Handle: s.getDocumentRevision},
		{Method: "POST", Path: base + "/{kind}/restore", Handle: s.restoreDocument},
		{Method: "PUT", Path: base + "/{kind}/review", Handle: s.reviewDocument},
		{Method: "POST", Path: base + "/ai_notes/generate", Handle: s.generateDocument(documents.KindAINotes)},
		{Method: "POST", Path: base + "/mom/generate", Handle: s.generateDocument(documents.KindMoM)},
	}
}

// docPath parses the meeting id and kind; an unknown kind names no resource → 404.
func docPath(r *http.Request) (string, documents.Kind, error) {
	id, err := pathUUID(r, "id")
	if err != nil {
		return "", "", err
	}
	kind, ok := documents.ParseKind(r.PathValue("kind"))
	if !ok {
		return "", "", apperr.ErrNotFound
	}
	return id, kind, nil
}

func writeDocStatus(w http.ResponseWriter, created bool, v any) {
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, v)
}

func (s *Server) getDocument(w http.ResponseWriter, r *http.Request) error {
	id, kind, err := docPath(r)
	if err != nil {
		return err
	}
	v, err := s.Documents.Get(r.Context(), userID(r), id, kind)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, v)
	return nil
}

func (s *Server) putDocument(w http.ResponseWriter, r *http.Request) error {
	id, kind, err := docPath(r)
	if err != nil {
		return err
	}
	var in documents.PutInput
	raw, err := decodeJSON(r, &in)
	if err != nil {
		return err
	}
	v, created, err := s.Documents.Put(r.Context(), userID(r), id, kind, raw, in)
	if err != nil {
		return err
	}
	writeDocStatus(w, created, v)
	return nil
}

type docRevisionCursor struct {
	Before int `json:"b"`
}

func (s *Server) listDocumentRevisions(w http.ResponseWriter, r *http.Request) error {
	id, kind, err := docPath(r)
	if err != nil {
		return err
	}
	var c docRevisionCursor
	limit, hasCursor, err := pageParams(r, &c)
	if err != nil {
		return err
	}
	if hasCursor && c.Before < 1 {
		return apperr.Field("cursor", apperr.ReasonInvalid)
	}
	items, next, err := s.Documents.ListRevisions(r.Context(), userID(r), id, kind, limit, c.Before)
	if err != nil {
		return err
	}
	var cur *string
	if next != nil {
		cur = encodeCursor(docRevisionCursor{*next})
	}
	writeJSON(w, http.StatusOK, NewPage(items, cur))
	return nil
}

type docRevisionBody struct {
	Revision *documents.Revision `json:"revision"`
}

func (s *Server) getDocumentRevision(w http.ResponseWriter, r *http.Request) error {
	id, kind, err := docPath(r)
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 {
		return apperr.ErrNotFound
	}
	rev, err := s.Documents.GetRevision(r.Context(), userID(r), id, kind, n)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, docRevisionBody{rev})
	return nil
}

func (s *Server) restoreDocument(w http.ResponseWriter, r *http.Request) error {
	id, kind, err := docPath(r)
	if err != nil {
		return err
	}
	var in documents.RestoreInput
	raw, err := decodeJSON(r, &in)
	if err != nil {
		return err
	}
	v, created, err := s.Documents.Restore(r.Context(), userID(r), id, kind, raw, in)
	if err != nil {
		return err
	}
	writeDocStatus(w, created, v)
	return nil
}

type docSummaryBody struct {
	Document *meetings.DocumentSummary `json:"document"`
}

func (s *Server) reviewDocument(w http.ResponseWriter, r *http.Request) error {
	id, kind, err := docPath(r)
	if err != nil {
		return err
	}
	var in documents.ReviewInput
	if _, err := decodeJSON(r, &in); err != nil {
		return err
	}
	d, err := s.Documents.Review(r.Context(), userID(r), id, kind, in)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, docSummaryBody{d})
	return nil
}

type docJobBody struct {
	Job *meetings.Job `json:"job"`
}

// generateDocument: api.md §1.4 — key check, then ownership/key lookup in the service.
func (s *Server) generateDocument(kind documents.Kind) HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		id, err := pathUUID(r, "id")
		if err != nil {
			return err
		}
		key, err := idempotencyKey(r)
		if err != nil {
			return err
		}
		var in documents.GenerateInput
		raw, err := decodeJSON(r, &in)
		if err != nil {
			return err
		}
		j, created, err := s.Documents.Generate(r.Context(), userID(r), id, kind, key, raw, in)
		if err != nil {
			return err
		}
		status := http.StatusOK
		if created {
			status = http.StatusAccepted
		}
		writeJSON(w, status, docJobBody{j})
		return nil
	}
}
