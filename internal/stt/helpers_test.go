package stt_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/openai"
)

const canaryBearer = "eyJcanary.canary.canary"

// plainHTTP sends what the production client addressed to https://<test origin> to a
// plain-HTTP server. It sits below the origin policy, so the policy still checks every request.
type plainHTTP struct{ rt http.RoundTripper }

func (p plainHTTP) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.URL.Scheme = "http"
	return p.rt.RoundTrip(r)
}

// client returns the production client wired to a server at rawURL, and the https base URL.
func client(t testing.TB, rawURL string) (*http.Client, string) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	tr := &http.Transport{DisableKeepAlives: true}
	t.Cleanup(tr.CloseIdleConnections)
	c, err := openai.NewClient(openai.WithTestOrigin(u.Host), openai.WithBaseTransport(plainHTTP{tr}))
	if err != nil {
		t.Fatal(err)
	}
	return c, "https://" + u.Host
}

// staticCreds hands out one canary token and cannot refresh.
type staticCreds struct{}

func (staticCreds) Token(context.Context) (auth.Token, error) {
	return auth.NewToken(canaryBearer, time.Unix(1800000000, 0), "", "test"), nil
}

func (staticCreds) RefreshAfter(context.Context, auth.Token) (auth.Token, error) {
	return auth.Token{}, &openai.Error{Class: openai.ClassAuthExpired, Message: "no refresh in this test"}
}
