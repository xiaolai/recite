package logx

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/xiaolai/sayso/go/secret"
)

// droppedKeys are attribute names whose values are secrets by construction.
var droppedKeys = map[string]bool{"authorization": true, "token": true, "secret": true, "value": true}

// Handler wraps another slog.Handler and scrubs every record before it is passed on.
type Handler struct{ inner slog.Handler }

// NewHandler returns a redacting wrapper around inner.
func NewHandler(inner slog.Handler) *Handler { return &Handler{inner: inner} }

// Enabled reports whether the inner handler handles level.
func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle scrubs the message and every attribute, then delegates.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, secret.Scrub(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		if s, ok := scrubAttr(a); ok {
			out.AddAttrs(s)
		}
		return true
	})
	if err := h.inner.Handle(ctx, out); err != nil {
		return fmt.Errorf("logx: %w", err)
	}
	return nil
}

// WithAttrs scrubs attrs before the inner handler stores them.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{inner: h.inner.WithAttrs(scrubAttrs(attrs))}
}

// WithGroup scrubs the group name, which is user-visible text too.
func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{inner: h.inner.WithGroup(secret.Scrub(name))}
}

func scrubAttrs(attrs []slog.Attr) []slog.Attr {
	out := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		if s, ok := scrubAttr(a); ok {
			out = append(out, s)
		}
	}
	return out
}

// scrubAttr returns the scrubbed attribute, or false when it must be dropped.
// Every value is rendered to a string or recursed into, so no value reaches the
// inner handler in a form that could format a secret later (LogValuer, error, any).
func scrubAttr(a slog.Attr) (slog.Attr, bool) {
	if droppedKeys[strings.ToLower(a.Key)] {
		return slog.Attr{}, false
	}
	key := secret.Scrub(a.Key)
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindGroup:
		return slog.Attr{Key: key, Value: slog.GroupValue(scrubAttrs(v.Group())...)}, true
	case slog.KindString:
		return slog.String(key, secret.Scrub(v.String())), true
	case slog.KindInt64, slog.KindUint64, slog.KindFloat64, slog.KindBool, slog.KindDuration, slog.KindTime:
		return slog.Attr{Key: key, Value: v}, true
	}
	if err, ok := v.Any().(error); ok {
		return slog.String(key, secret.Scrub(err.Error())), true
	}
	return slog.String(key, secret.Scrub(fmt.Sprintf("%+v", v.Any()))), true
}
