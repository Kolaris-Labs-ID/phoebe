package httpapi

import (
	"net/http"

	"phoebe/internal/export"
	"phoebe/internal/jsonx"
)

type FilesService = *export.Files

func (s *Server) routesFiles() []Route {
	return []Route{
		{Method: "GET", Path: "/v1/meetings/{id}/audio/url", Handle: s.getAudioURL},
		{Method: "GET", Path: "/v1/files/{kind}/{id}", Public: true, Handle: s.serveFile},
	}
}

type audioURLBody struct {
	URL       string     `json:"url"`
	ExpiresAt jsonx.Time `json:"expires_at"`
}

func (s *Server) getAudioURL(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	url, exp, err := s.Files.AudioURL(r.Context(), userID(r), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, audioURLBody{url, jsonx.NewTime(exp)})
	return nil
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	f, err := s.Files.Open(r.Context(), r.PathValue("kind"), r.PathValue("id"), q.Get("exp"), q.Get("sig"))
	if err != nil {
		return err
	}
	defer f.Body.Close()
	h := w.Header()
	h.Set("Cache-Control", "private, no-store")
	h.Set("Content-Type", f.ContentType)
	if f.Filename != "" {
		h.Set("Content-Disposition", `attachment; filename="`+f.Filename+`"`)
	}
	http.ServeContent(w, r, "", f.ModTime, f.Body)
	return nil
}
