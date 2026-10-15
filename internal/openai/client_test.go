package openai

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

const canaryBearer = "Bearer eyJcanary.canary.canary"

// recorder is a fake transport below the policy. It records every request it
// receives and answers with respond.
type recorder struct {
	mu      sync.Mutex
	got     []*http.Request
	respond func(*http.Request) *http.Response
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.got = append(r.got, req)
	r.mu.Unlock()
	if req.Body != nil {
		_ = req.Body.Close()
	}
	if r.respond != nil {
		return r.respond(req), nil
	}
	return reply(req, http.StatusOK, nil, "{}"), nil
}

func (r *recorder) requests() []*http.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*http.Request(nil), r.got...)
}

func reply(req *http.Request, status int, h http.Header, body string) *http.Response {
	if h == nil {
		h = http.Header{}
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: req}
}

// call shapes recite makes with a bearer: mint, HTTP STT and a WebSocket upgrade.
type shape struct {
	name, method, path string
	header             http.Header
}

var shapes = []shape{
	{"mint", http.MethodPost, "/v1/realtime/client_secrets", http.Header{"Content-Type": {"application/json"}}},
	{"stt", http.MethodPost, "/v1/audio/transcriptions", http.Header{"Content-Type": {"multipart/form-data; boundary=x"}}},
	{"websocket", http.MethodGet, "/v1/realtime?model=gpt-realtime-2.1", http.Header{
		"Connection": {"Upgrade"}, "Upgrade": {"websocket"}, "Sec-Websocket-Version": {"13"},
		"Sec-Websocket-Key": {"dGhlIHNhbXBsZSBub25jZQ=="},
	}},
}

func newReq(t *testing.T, s shape, rawURL string) *http.Request {
	t.Helper()
	var body io.Reader
	if s.method == http.MethodPost {
		body = strings.NewReader("{}")
	}
	req, err := http.NewRequest(s.method, rawURL, body)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range s.header {
		req.Header[k] = v
	}
	req.Header.Set("Authorization", canaryBearer)
	return req
}

// accept: NET-01#1
func TestOriginPolicyRefusesBeforeDialling(t *testing.T) {
	cases := []struct {
		name   string
		origin string
		host   string // req.Host override
	}{
		{"other host", "https://api.example.org", ""},
		{"lookalike host", "https://api.openai.com.evil.test", ""},
		{"http scheme", "http://api.openai.com", ""},
		{"port 8443", "https://api.openai.com:8443", ""},
		{"userinfo", "https://user:pw@api.openai.com", ""},
		{"Host override", "https://api.openai.com", "evil.test"},
	}
	for _, s := range shapes {
		for _, tc := range cases {
			t.Run(s.name+"/"+tc.name, func(t *testing.T) {
				base := &recorder{}
				c, err := NewClient(WithBaseTransport(base))
				if err != nil {
					t.Fatal(err)
				}
				req := newReq(t, s, tc.origin+s.path)
				req.Host = tc.host
				resp, err := c.Do(req)
				if err == nil {
					_ = resp.Body.Close()
					t.Fatal("request allowed, want refusal")
				}
				if n := len(base.requests()); n != 0 {
					t.Fatalf("base transport saw %d requests, want 0", n)
				}
				if got := Classify(err); got != ClassInternal {
					t.Errorf("class %q, want %q", got, ClassInternal)
				}
				if strings.Contains(err.Error(), "eyJcanary") {
					t.Errorf("error leaks bearer: %v", err)
				}
			})
		}
	}
}

// accept: NET-01#1
func TestRedirectsAreNeverFollowed(t *testing.T) {
	targets := map[string]string{
		"same host":  "https://api.openai.com/v1/elsewhere",
		"other host": "https://second.example.org/v1/realtime/client_secrets",
	}
	for _, s := range shapes {
		for name, loc := range targets {
			for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
				base := &recorder{respond: func(req *http.Request) *http.Response {
					return reply(req, status, http.Header{"Location": {loc}}, "")
				}}
				c, err := NewClient(WithBaseTransport(base))
				if err != nil {
					t.Fatal(err)
				}
				resp, err := c.Do(newReq(t, s, APIBase+s.path))
				if err == nil {
					_ = resp.Body.Close()
					t.Fatalf("%s %s %d: redirect followed", s.name, name, status)
				}
				got := base.requests()
				if len(got) != 1 {
					t.Fatalf("%s %s %d: base saw %d requests, want 1", s.name, name, status, len(got))
				}
				if got[0].URL.Hostname() != "api.openai.com" {
					t.Fatalf("first request went to %q", got[0].URL.Hostname())
				}
				var e *Error
				if !errors.As(err, &e) || e.Class != ClassProtocol || e.Status != status {
					t.Errorf("%s %s %d: error %v, want protocol with status", s.name, name, status, err)
				}
			}
		}
	}
}

func TestTimeouts(t *testing.T) {
	if MintTimeout != 20*time.Second || WebSocketOpenTimeout != 20*time.Second || ResponseIdleTimeout != 60*time.Second {
		t.Error("fixed timeouts differ from design §5.2")
	}
	for in, want := range map[time.Duration]time.Duration{
		0:                       60 * time.Second,
		-time.Second:            60 * time.Second,
		10 * time.Second:        70 * time.Second,
		1500 * time.Millisecond: 62 * time.Second,
	} {
		if got := STTTimeout(in); got != want {
			t.Errorf("STTTimeout(%v) = %v, want %v", in, got, want)
		}
	}
	if got := ResponseHardCap(10 * time.Second); got != 60*time.Second {
		t.Errorf("ResponseHardCap(10s) = %v, want 60s", got)
	}
}
