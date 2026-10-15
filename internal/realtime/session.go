package realtime

import (
	"context"
	"log/slog"
	"net/http"

	sr "github.com/xiaolai/sayso/go/realtime"

	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/openai"
)

// ReadLimit is the largest message accepted (design §5.2).
const ReadLimit = 16 << 20

// ErrClosed is returned by Next and Send after Close.
var ErrClosed = sr.ErrClosed

// Session is one realtime WebSocket (NET-03): one reader, serialised writes, no client pings.
type Session = sr.Session

// WebSocketURL is the realtime endpoint for model under base (openai.APIBase in production).
func WebSocketURL(base, model string) string { return sr.WebSocketURL(base, model) }

// DialConfig describes one WebSocket session.
type DialConfig struct {
	Client *http.Client // the production client: proxy and origin policy apply (spike S0.2)
	URL    string
	Secret auth.ClientSecret
	Route  openai.Route // set on a route-denied handshake refusal
	Logger *slog.Logger // nil discards
	// Trace, if set, receives every message as sent or received ("client" or "server"),
	// before decoding, for the event dumps of OBS-02. It must not keep raw.
	Trace func(dir string, raw []byte)
}

// Dial opens the session with `Authorization: Bearer ek_…`, within the WebSocket open timeout
// and with recite's read limit.
func Dial(ctx context.Context, cfg DialConfig) (*Session, error) {
	return sr.Dial(ctx, sr.DialConfig{
		Client: cfg.Client, URL: cfg.URL, Secret: cfg.Secret, HandshakeTimeout: openai.WebSocketOpenTimeout,
		ReadLimit: ReadLimit, Route: cfg.Route, Logger: cfg.Logger, Trace: cfg.Trace,
	})
}
