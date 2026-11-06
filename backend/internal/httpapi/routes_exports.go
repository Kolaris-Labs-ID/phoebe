package httpapi

import (
	"net/http"

	"phoebe/internal/export"
)

type ExportsService = *export.Service

func (s *Server) routesExports() []Route {
	return []Route{
		{Method: "POST", Path: "/v1/exports", Handle: s.createExport},
		{Method: "GET", Path: "/v1/exports/{id}", Handle: s.getExport},
	}
}

func (s *Server) createExport(w http.ResponseWriter, r *http.Request) error {
	var in export.CreateInput
	if _, err := decodeJSON(r, &in); err != nil {
		return err
	}
	res, err := s.Exports.Create(r.Context(), userID(r), in)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, res)
	return nil
}

func (s *Server) getExport(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	res, err := s.Exports.Get(r.Context(), userID(r), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}
