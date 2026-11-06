package httpapi

import (
	"net/http"

	"phoebe/internal/meetings"
)

// FoldersTagsService is implemented by the meetings service (folders and tags live in internal/meetings).
type FoldersTagsService = *meetings.Service

func (s *Server) routesFoldersTags() []Route {
	var out []Route
	for _, l := range []struct {
		path, key string
		kind      meetings.LabelKind
	}{{"/v1/folders", "folder", meetings.Folders}, {"/v1/tags", "tag", meetings.Tags}} {
		out = append(out,
			Route{Method: "GET", Path: l.path, Handle: s.listLabels(l.kind)},
			Route{Method: "POST", Path: l.path, Handle: s.createLabel(l.kind, l.key)},
			Route{Method: "PATCH", Path: l.path + "/{id}", Handle: s.renameLabel(l.kind, l.key)},
			Route{Method: "DELETE", Path: l.path + "/{id}", Handle: s.deleteLabel(l.kind)},
		)
	}
	return out
}

func (s *Server) listLabels(k meetings.LabelKind) HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		items, err := s.FoldersTags.ListLabels(r.Context(), k, userID(r))
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
		return nil
	}
}

func (s *Server) createLabel(k meetings.LabelKind, key string) HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		var in meetings.LabelInput
		if _, err := decodeJSON(r, &in); err != nil {
			return err
		}
		l, created, err := s.FoldersTags.CreateLabel(r.Context(), k, userID(r), in)
		if err != nil {
			return err
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, status, map[string]any{key: l})
		return nil
	}
}

func (s *Server) renameLabel(k meetings.LabelKind, key string) HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		id, err := pathUUID(r, "id")
		if err != nil {
			return err
		}
		var in meetings.LabelInput
		if _, err := decodeJSON(r, &in); err != nil {
			return err
		}
		l, err := s.FoldersTags.RenameLabel(r.Context(), k, userID(r), id, in.Name)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{key: l})
		return nil
	}
}

func (s *Server) deleteLabel(k meetings.LabelKind) HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		id, err := pathUUID(r, "id")
		if err != nil {
			return err
		}
		if err := s.FoldersTags.DeleteLabel(r.Context(), k, userID(r), id); err != nil {
			return err
		}
		writeNoContent(w)
		return nil
	}
}
