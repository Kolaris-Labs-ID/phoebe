package httpapi

import (
	"net/http"

	"phoebe/internal/users"
)

type UsersService = *users.Service

func (s *Server) routesUsers() []Route {
	return []Route{
		{Method: "GET", Path: "/v1/me", Handle: s.getMe},
		{Method: "PATCH", Path: "/v1/me", Handle: s.patchMe},
		{Method: "DELETE", Path: "/v1/me", Handle: s.deleteMe},
	}
}

type userBody struct {
	User *users.User `json:"user"`
}

func (s *Server) getMe(w http.ResponseWriter, r *http.Request) error {
	u, err := s.Users.Get(r.Context(), userID(r))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, userBody{u})
	return nil
}

func (s *Server) patchMe(w http.ResponseWriter, r *http.Request) error {
	var p users.Patch
	if _, err := decodeJSON(r, &p); err != nil {
		return err
	}
	u, err := s.Users.Update(r.Context(), userID(r), p)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, userBody{u})
	return nil
}
