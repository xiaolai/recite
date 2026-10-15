package openai

import (
	"crypto/x509"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/xiaolai/sayso/go/openaihttp"

	"github.com/xiaolai/recite/internal/testguard"
)

// Per-class timeouts (design §5.2). Callers apply them with context deadlines;
// the client itself has no overall timeout because it also carries WebSocket
// sessions, and websocket.Dial rejects a client with Timeout set.
const (
	MintTimeout          = 20 * time.Second
	WebSocketOpenTimeout = 20 * time.Second
	ResponseIdleTimeout  = 60 * time.Second
)

// STTTimeout is the HTTP transcription timeout: 60 s plus 1 s per audio second
// (a started second counts in full).
func STTTimeout(audio time.Duration) time.Duration {
	if audio < 0 {
		audio = 0
	}
	secs := (audio + time.Second - 1) / time.Second
	return 60*time.Second + secs*time.Second
}

// ResponseHardCap is the per-response cap: 3 × the estimated duration + 30 s.
func ResponseHardCap(estimated time.Duration) time.Duration {
	return 3*estimated + 30*time.Second
}

// Guarded reports whether c came from NewClient, so a bearer sent through it passes the origin
// policy, the redirect refusal and the test guard.
func Guarded(c *http.Client) bool { return openaihttp.Guarded(c) }

// Option configures NewClient (openaihttp.Option).
type Option = openaihttp.Option

// WithBaseTransport replaces the transport below the origin policy. For tests: fakes go below
// the policy, so the policy code path is always exercised.
func WithBaseTransport(rt http.RoundTripper) Option { return openaihttp.WithBaseTransport(rt) }

// WithRootCAs makes the production transport trust pool instead of the system roots.
func WithRootCAs(pool *x509.CertPool) Option { return openaihttp.WithRootCAs(pool) }

// WithTestOrigin additionally allows https://hostport, a reserved test host, for bearer requests.
func WithTestOrigin(hostport string) Option { return openaihttp.WithTestOrigin(hostport) }

// NewClient returns the one http.Client used for REST calls and WebSocket dials: the shared
// client with its origin policy, no redirects and the proxy from the environment, plus recite's
// test guard, which refuses the real API over the real network in a non-live test run.
func NewClient(opts ...Option) (*http.Client, error) {
	// A fresh slice: appending to opts could overwrite the caller's backing array.
	all := append(slices.Clip(opts), openaihttp.WithRealNetworkGuard(realNetworkGuard))
	return openaihttp.NewClient(all...)
}

func realNetworkGuard() error {
	if testguard.Active() {
		// A unit test was about to reach the real API: it must use a fake. Only the live suites
		// (RECITE_LIVE=1) may spend real usage.
		return errors.New("real OpenAI origin over the real network in a non-live test run")
	}
	return nil
}
