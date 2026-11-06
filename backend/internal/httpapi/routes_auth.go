package httpapi

import (
	"net/http"

	"phoebe/internal/auth"
)

type AuthService = *auth.Service

func (s *Server) routesAuth() []Route {
	rs := []Route{
		{Method: "POST", Path: "/v1/auth/apple", Public: true, Handle: s.authApple},
		{Method: "POST", Path: "/v1/auth/logout", Handle: s.authLogout},
	}
	if s.Config.DevLoginEnabled {
		rs = append(rs, Route{Method: "POST", Path: "/v1/auth/dev", Public: true, Handle: s.authDev})
	}
	return rs
}

func (s *Server) authApple(w http.ResponseWriter, r *http.Request) error {
	var in auth.AppleSignIn
	if _, err := decodeJSON(r, &in); err != nil {
		return err
	}
	res, err := s.Auth.SignInApple(r.Context(), in)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

func (s *Server) authDev(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Subject string `json:"subject"`
	}
	if _, err := decodeJSON(r, &in); err != nil {
		return err
	}
	res, err := s.Auth.SignInDev(r.Context(), in.Subject)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) error {
	if err := s.Auth.Logout(r.Context(), sessionID(r)); err != nil {
		return err
	}
	writeNoContent(w)
	return nil
}
