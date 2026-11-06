package httpapi

import (
	"net/http"

	"phoebe/internal/apperr"
	"phoebe/internal/events"
)

type EventsService = *events.Service

func (s *Server) routesEvents() []Route {
	return []Route{{Method: http.MethodPost, Path: "/v1/events", Handle: s.postEvents}}
}

func (s *Server) postEvents(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Events *[]events.ClientEvent `json:"events"`
	}
	if _, err := decodeJSON(r, &body); err != nil {
		return err
	}
	if body.Events == nil {
		return apperr.Field("events", apperr.ReasonRequired)
	}
	n, err := s.Events.Ingest(r.Context(), userID(r), *body.Events)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"accepted": n})
	return nil
}
