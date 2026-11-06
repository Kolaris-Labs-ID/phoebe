package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"phoebe/internal/apperr"
	"phoebe/internal/ids"
	"phoebe/internal/obs"
)

type ctxKey int

const (
	reqInfoKey ctxKey = iota
	authKey
)

// reqInfo is shared by pointer so the access log sees the user resolved further in.
type reqInfo struct {
	userID string
}

type authInfo struct {
	userID    string
	sessionID string
}

// userID returns the authenticated caller; "" on public routes.
func userID(r *http.Request) string {
	a, _ := r.Context().Value(authKey).(authInfo)
	return a.userID
}

func sessionID(r *http.Request) string {
	a, _ := r.Context().Value(authKey).(authInfo)
	return a.sessionID
}

// requestID takes a valid X-Request-ID UUID from the client or generates one, echoes it,
// and stores it as the correlation id.
func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := ids.Parse(r.Header.Get("X-Request-ID"))
		if !ok {
			id = ids.New()
		}
		w.Header().Set("X-Request-ID", id)
		ctx := obs.WithCorrelationID(r.Context(), id)
		ctx = context.WithValue(ctx, reqInfoKey, &reqInfo{})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer (read deadlines, flush).
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// accessLog logs http.request with the route pattern only: no path, no query string,
// no headers, no bodies.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		route := r.Pattern
		if route == "/" {
			route = "unmatched"
		}
		attrs := []any{"method", r.Method, "route", route, "http_status", status,
			"duration_ms", time.Since(start).Milliseconds()}
		if ri, _ := r.Context().Value(reqInfoKey).(*reqInfo); ri != nil && ri.userID != "" {
			attrs = append(attrs, "user_id", ri.userID)
		}
		s.Log.InfoContext(r.Context(), "http.request", attrs...)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				s.writeError(w, r, fmt.Errorf("panic: %v", p))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func bodyLimit(limit int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
			s.writeError(w, r, apperr.New(apperr.Unauthenticated))
			return
		}
		uid, sid, err := s.Sessions.ResolveSession(r.Context(), strings.TrimSpace(token))
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if ri, _ := r.Context().Value(reqInfoKey).(*reqInfo); ri != nil {
			ri.userID = uid
		}
		ctx := context.WithValue(r.Context(), authKey, authInfo{userID: uid, sessionID: sid})
		ctx = obs.WithUserID(ctx, uid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
