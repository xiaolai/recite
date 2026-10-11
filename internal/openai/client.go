package openai

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
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

// Transport-level limits. They bound a stuck connection; the per-class
// deadlines above are the real limits for each call.
const (
	dialTimeout           = 10 * time.Second
	tlsHandshakeTimeout   = 10 * time.Second
	responseHeaderTimeout = 5 * time.Minute // long STT uploads answer only after transcription
	idleConnTimeout       = 90 * time.Second
)

// Option configures NewClient.
type Option func(*config) error

type config struct {
	base        http.RoundTripper
	rootCAs     *x509.CertPool
	testOrigins map[string]bool
}

// WithBaseTransport replaces the transport below the origin policy. For tests:
// fakes go below the policy, so the policy code path is always exercised.
func WithBaseTransport(rt http.RoundTripper) Option {
	return func(c *config) error {
		if rt == nil {
			return fmt.Errorf("openai: nil base transport")
		}
		c.base = rt
		return nil
	}
}

// WithRootCAs makes the production transport trust pool instead of the system
// roots. For tests against an httptest TLS server.
func WithRootCAs(pool *x509.CertPool) Option {
	return func(c *config) error {
		c.rootCAs = pool
		return nil
	}
}

// WithTestOrigin additionally allows https://hostport for bearer requests. For
// tests only; hostport must name a reserved host (loopback, localhost,
// example.com, or a .test name), so a misuse can never send a bearer to a real
// third party.
func WithTestOrigin(hostport string) Option {
	return func(c *config) error {
		host, port, err := net.SplitHostPort(hostport)
		if err != nil || port == "" {
			return fmt.Errorf("openai: test origin %q: want host:port", hostport)
		}
		if !reservedHost(host) {
			return fmt.Errorf("openai: test origin %q is not a reserved test host", hostport)
		}
		if c.testOrigins == nil {
			c.testOrigins = map[string]bool{}
		}
		c.testOrigins[hostport] = true
		return nil
	}
}

func reservedHost(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	h := strings.ToLower(host)
	return h == "localhost" || h == "example.com" || strings.HasSuffix(h, ".test")
}

// NewClient returns the one http.Client used for REST calls and WebSocket dials
// (pass it as websocket.DialOptions.HTTPClient, spike S0.2). Its transport is
// wrapped by the origin policy, and it follows no redirect.
func NewClient(opts ...Option) (*http.Client, error) {
	var c config
	for _, o := range opts {
		if err := o(&c); err != nil {
			return nil, err
		}
	}
	// A test origin may name a real host (example.com). Only allow it when the system trust
	// store is replaced (custom roots or a whole fake transport), so a bearer can never reach
	// the real host: its certificate would not verify against a test-only pool.
	if len(c.testOrigins) > 0 && c.base == nil && c.rootCAs == nil {
		return nil, fmt.Errorf("openai: a test origin requires WithRootCAs or WithBaseTransport")
	}
	base := c.base
	if base == nil {
		base = newTransport(c.rootCAs)
	}
	return &http.Client{
		Transport:     &originPolicy{base: base, testOrigins: c.testOrigins},
		CheckRedirect: refuseRedirect,
	}, nil
}

// newTransport is the production transport. HTTP/2 stays off: WebSocket
// upgrades need HTTP/1.1, and one client serves both.
func newTransport(roots *x509.CertPool) *http.Transport {
	d := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           d.DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ResponseHeaderTimeout: responseHeaderTimeout,
		IdleConnTimeout:       idleConnTimeout,
		ExpectContinueTimeout: time.Second,
		MaxIdleConnsPerHost:   4,
	}
}

// refuseRedirect stops every redirect, same host or not, so a bearer never
// follows a 3xx.
func refuseRedirect(req *http.Request, _ []*http.Request) error {
	status := 0
	if req.Response != nil {
		status = req.Response.StatusCode
	}
	return &Error{Class: ClassProtocol, Status: status, Message: "redirect refused"}
}
