package obs

import (
	"context"
	"io"
	"log/slog"
)

type ctxKey int

const (
	correlationKey ctxKey = iota
	userKey
)

func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationKey, id)
}

func CorrelationID(ctx context.Context) string {
	s, _ := ctx.Value(correlationKey).(string)
	return s
}

func WithUserID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, userKey, id)
}

func UserID(ctx context.Context) string {
	s, _ := ctx.Value(userKey).(string)
	return s
}

// NewLogger returns a JSON logger that stamps correlation_id (and user_id when known)
// from the context on every *Context call. Callers log only the allowed fields of
// backend.md §15; never content, tokens, URLs with query strings, or secrets.
func NewLogger(w io.Writer, level slog.Leveler) *slog.Logger {
	return slog.New(ctxHandler{slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})})
}

type ctxHandler struct{ slog.Handler }

func (h ctxHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := CorrelationID(ctx); id != "" {
		r.AddAttrs(slog.String("correlation_id", id))
	}
	if id := UserID(ctx); id != "" {
		r.AddAttrs(slog.String("user_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h ctxHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return ctxHandler{h.Handler.WithAttrs(as)}
}

func (h ctxHandler) WithGroup(name string) slog.Handler {
	return ctxHandler{h.Handler.WithGroup(name)}
}
