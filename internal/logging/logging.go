// Package logging wires structured logging (slog) with request-ID context
// plumbing shared across the API and internal services.
package logging

import (
	"context"
	"log/slog"
	"os"
)

type ctxKey struct{}

// New builds a logger writing to stderr in text or JSON form.
func New(level slog.Level, json bool) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler = slog.NewTextHandler(os.Stderr, opts)
	if json {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

// WithRequestID returns a context carrying the given request ID.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// RequestID extracts the request ID from ctx, or "" if absent.
func RequestID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}
