package httpapi

import (
	"net/http"
	"time"

	"phoebe/internal/jsonx"
	"phoebe/internal/meetings"
)

type MeetingsService = *meetings.Service

func (s *Server) routesMeetings() []Route {
	return []Route{
		{Method: "POST", Path: "/v1/meetings", Handle: s.createMeeting},
		{Method: "GET", Path: "/v1/meetings", Handle: s.listMeetings},
		{Method: "GET", Path: "/v1/meetings/{id}", Handle: s.getMeeting},
		{Method: "PATCH", Path: "/v1/meetings/{id}", Handle: s.patchMeeting},
		{Method: "DELETE", Path: "/v1/meetings/{id}", Handle: s.deleteMeeting},
		{Method: "DELETE", Path: "/v1/meetings/{id}/audio", Handle: s.deleteMeetingAudio},
	}
}

type meetingBody struct {
	Meeting *meetings.Meeting `json:"meeting"`
}

type purgeBody struct {
	PurgeScheduledAt jsonx.Time `json:"purge_scheduled_at"`
}

func (s *Server) createMeeting(w http.ResponseWriter, r *http.Request) error {
	var in meetings.CreateInput
	if _, err := decodeJSON(r, &in); err != nil {
		return err
	}
	m, created, err := s.Meetings.Create(r.Context(), userID(r), in)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, meetingBody{m})
	return nil
}

func (s *Server) listMeetings(w http.ResponseWriter, r *http.Request) error {
	var c meetings.Cursor
	limit, hasCursor, err := pageParams(r, &c)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	lq := meetings.ListQuery{Q: q.Get("q"), FolderID: q.Get("folder_id"), TagID: q.Get("tag_id"), Limit: limit}
	if hasCursor {
		lq.After = &c
	}
	items, next, err := s.Meetings.List(r.Context(), userID(r), lq)
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

func (s *Server) getMeeting(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	m, err := s.Meetings.Get(r.Context(), userID(r), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, meetingBody{m})
	return nil
}

func (s *Server) patchMeeting(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	var p meetings.Patch
	if _, err := decodeJSON(r, &p); err != nil {
		return err
	}
	m, err := s.Meetings.Update(r.Context(), userID(r), id, p)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, meetingBody{m})
	return nil
}

func (s *Server) deleteMeeting(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	at, err := s.Meetings.Delete(r.Context(), userID(r), id)
	if err != nil {
		return err
	}
	writePurge(w, at)
	return nil
}

func (s *Server) deleteMeetingAudio(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	if err := s.Meetings.DeleteAudio(r.Context(), userID(r), id); err != nil {
		return err
	}
	writeNoContent(w)
	return nil
}

func writePurge(w http.ResponseWriter, at time.Time) {
	writeJSON(w, http.StatusAccepted, purgeBody{jsonx.NewTime(at)})
}
