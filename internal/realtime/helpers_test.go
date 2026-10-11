package realtime_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/realtime/fake"
)

// plainHTTP sends what the production client addressed to https://<test origin>
// to the plain-HTTP fake. It sits below the origin policy, so the policy still
// checks every bearer request.
type plainHTTP struct{ rt http.RoundTripper }

func (p plainHTTP) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.URL.Scheme = "http"
	return p.rt.RoundTrip(r)
}

// client returns the production client wired to a server at rawURL, and the
// https base URL recite uses for it.
func client(t testing.TB, rawURL string) (*http.Client, string) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	c, err := openai.NewClient(openai.WithTestOrigin(u.Host), openai.WithBaseTransport(plainHTTP{tr}))
	if err != nil {
		t.Fatal(err)
	}
	return c, "https://" + u.Host
}

// staticCreds hands out one token and cannot refresh.
type staticCreds struct{ tok auth.Token }

func newStatic() staticCreds {
	return staticCreds{auth.NewToken("eyJcanary.canary.canary", time.Unix(1800000000, 0), "", "test")}
}

func (s staticCreds) Token(context.Context) (auth.Token, error) { return s.tok, nil }
func (s staticCreds) RefreshAfter(context.Context, auth.Token) (auth.Token, error) {
	return auth.Token{}, &openai.Error{Class: openai.ClassAuthExpired, Message: "no refresh in this test"}
}

func loadScenario(t testing.TB, name string) fake.Scenario {
	t.Helper()
	sc, err := fake.LoadScenario(name)
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

// dial mints nothing: it opens a session to srv with the fixture secret.
func dial(t testing.TB, srv *fake.Server) *realtime.Session {
	t.Helper()
	c, base := client(t, srv.URL)
	s, err := realtime.Dial(context.Background(), realtime.DialConfig{
		Client: c,
		URL:    realtime.WebSocketURL(base, "gpt-realtime-2.1"),
		Secret: auth.NewClientSecret("ek_fixture", time.Unix(1800000000, 0)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func wantClass(t testing.TB, err error, class openai.Class) {
	t.Helper()
	var e *openai.Error
	if !errors.As(err, &e) || openai.Classify(err) != class {
		t.Fatalf("err = %v, want class %s", err, class)
	}
}
