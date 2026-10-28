package telemetry

import (
	"context"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

// secretKeys are attribute names whose values never reach logs or log export.
var secretKeys = map[string]bool{"code": true, "token": true, "apikey": true, "api_key": true,
	"authorization": true, "cookie": true, "password": true, "secret": true, "session": true}

const redacted = "[REDACTED]"

func redact(a slog.Attr) slog.Attr {
	if secretKeys[strings.ToLower(a.Key)] {
		return slog.String(a.Key, redacted)
	}
	if a.Value.Kind() == slog.KindGroup {
		g := a.Value.Group()
		out := make([]any, len(g))
		for i, x := range g {
			out[i] = redact(x)
		}
		return slog.Group(a.Key, out...)
	}
	return a
}

// Handler redacts secret attributes, adds trace_id/span_id from the context
// and fans records out to every child (stdout JSON, OTLP export).
type Handler struct{ children []slog.Handler }

func NewHandler(children ...slog.Handler) *Handler { return &Handler{children} }

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, c := range h.children {
		if c.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool { out.AddAttrs(redact(a)); return true })
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		out.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	for _, c := range h.children {
		if c.Enabled(ctx, r.Level) {
			if err := c.Handle(ctx, out.Clone()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	red := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		red[i] = redact(a)
	}
	cs := make([]slog.Handler, len(h.children))
	for i, c := range h.children {
		cs[i] = c.WithAttrs(red)
	}
	return &Handler{cs}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	cs := make([]slog.Handler, len(h.children))
	for i, c := range h.children {
		cs[i] = c.WithGroup(name)
	}
	return &Handler{cs}
}
