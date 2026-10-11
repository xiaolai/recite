package logx

import (
	"io"
	"log/slog"
	"strings"
	"time"
)

// Level maps the CLI's quiet flag and -v count to a slog level (design §14.3):
// errors only with -q, warnings by default, info at -v, debug at -vv.
func Level(quiet bool, verbose int) slog.Level {
	switch {
	case quiet:
		return slog.LevelError
	case verbose >= 2:
		return slog.LevelDebug
	case verbose == 1:
		return slog.LevelInfo
	}
	return slog.LevelWarn
}

// New returns a redacting logger: text to w, or JSON when json is set (--log-file).
func New(w io.Writer, level slog.Level, json bool) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	var inner slog.Handler
	if json {
		inner = slog.NewJSONHandler(w, opts)
	} else {
		inner = slog.NewTextHandler(w, opts)
	}
	return slog.New(NewHandler(inner))
}

// Call describes one network call. It deliberately has no field for headers or
// bodies, so they cannot be logged through it (OBS-01).
type Call struct {
	Class     string // error class (§5.3), or "ok"
	Path      string // URL path only, never the query or host
	Status    int
	Latency   time.Duration
	RequestID string // x-request-id
	ChunkID   string
}

// LogCall logs c at info level as a "network call" record.
func LogCall(l *slog.Logger, c Call) {
	path, _, _ := strings.Cut(c.Path, "?")
	l.Info("network call",
		slog.String("class", c.Class),
		slog.String("path", path),
		slog.Int("status", c.Status),
		slog.Duration("latency", c.Latency),
		slog.String("request_id", c.RequestID),
		slog.String("chunk_id", c.ChunkID),
	)
}
