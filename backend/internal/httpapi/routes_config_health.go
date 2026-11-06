package httpapi

import (
	"context"
	"net/http"
	"time"

	"phoebe/internal/apperr"
)

func (s *Server) routesConfigHealth() []Route {
	return []Route{
		{Method: "GET", Path: "/healthz", Public: true, Handle: s.handleHealth},
		{Method: "GET", Path: "/v1/config", Public: true, Handle: s.handleConfig},
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.DB.Ping(ctx); err != nil {
		return apperr.New(apperr.Unavailable)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	return nil
}

type configResponse struct {
	MaxAudioDurationMS    int64    `json:"max_audio_duration_ms"`
	MaxUploadBytes        int64    `json:"max_upload_bytes"`
	UploadChunkMaxBytes   int64    `json:"upload_chunk_max_bytes"`
	SupportedContentTypes []string `json:"supported_content_types"`
	AudioRetentionDays    int      `json:"audio_retention_days"`
	Languages             []string `json:"languages"`
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) error {
	c := s.Config
	writeJSON(w, http.StatusOK, configResponse{
		MaxAudioDurationMS:    c.MaxAudioSeconds * 1000,
		MaxUploadBytes:        c.MaxUploadBytes,
		UploadChunkMaxBytes:   c.UploadChunkMaxBytes,
		SupportedContentTypes: []string{"audio/mp4", "audio/mpeg", "audio/wav"},
		AudioRetentionDays:    c.AudioRetentionDays,
		Languages:             []string{"id", "en"},
	})
	return nil
}
