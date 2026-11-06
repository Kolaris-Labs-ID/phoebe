package httpapi

import (
	"net/http"

	"phoebe/internal/usage"
)

type UsageService = *usage.Service

func (s *Server) routesUsage() []Route {
	return []Route{{Method: http.MethodGet, Path: "/v1/usage", Handle: s.getUsage}}
}

func (s *Server) getUsage(w http.ResponseWriter, r *http.Request) error {
	u, err := s.Usage.Get(r.Context(), userID(r))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, u)
	return nil
}
