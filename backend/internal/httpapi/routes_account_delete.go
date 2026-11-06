package httpapi

import "net/http"

// deleteMe is DELETE /v1/me (api.md §4.1); registered in routesUsers.
func (s *Server) deleteMe(w http.ResponseWriter, r *http.Request) error {
	at, err := s.Users.Delete(r.Context(), userID(r))
	if err != nil {
		return err
	}
	writePurge(w, at)
	return nil
}
