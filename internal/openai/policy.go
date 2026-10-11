package openai

import (
	"fmt"
	"github.com/xiaolai/recite/internal/testguard"
	"net/http"
)

const (
	apiHost = "api.openai.com"
	// APIBase is the only origin a bearer may be sent to.
	APIBase = "https://" + apiHost
)

// originPolicy refuses, before dialling, any request carrying a bearer whose
// origin is not exactly https://api.openai.com (port 443 or empty, no userinfo,
// no Host override). Requests without Authorization pass unchecked.
type originPolicy struct {
	base        http.RoundTripper
	testOrigins map[string]bool
	realNetwork bool // base is the production transport, not an injected fake
}

func (p *originPolicy) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("Authorization") != "" {
		if err := p.check(req); err != nil {
			if req.Body != nil {
				_ = req.Body.Close() // RoundTripper contract: close the body even on error
			}
			return nil, err
		}
	}
	return p.base.RoundTrip(req)
}

func (p *originPolicy) check(req *http.Request) error {
	u := req.URL
	switch {
	case u == nil:
		return policyError("request without URL")
	case u.User != nil:
		return policyError("userinfo in URL")
	case req.Host != "" && req.Host != u.Host:
		return policyError("Host header override")
	case u.Scheme != "https":
		return policyError(fmt.Sprintf("scheme %q", u.Scheme))
	case p.testOrigins[u.Host]:
		return nil
	case u.Hostname() != apiHost:
		return policyError(fmt.Sprintf("host %q", u.Hostname()))
	case u.Port() != "" && u.Port() != "443":
		return policyError(fmt.Sprintf("port %q", u.Port()))
	case p.realNetwork && testguard.Active():
		// A unit test was about to reach the real API over the network: it must use a
		// fake. Only the live suites (RECITE_LIVE=1) may spend real usage.
		return policyError("real OpenAI origin over the real network in a non-live test run")
	}
	return nil
}

// policyError is a programming error: recite built a bearer request for a
// forbidden origin. It maps to ClassInternal (exit 1).
func policyError(why string) error {
	return &Error{Class: ClassInternal, Message: "origin policy: bearer request refused: " + why}
}
