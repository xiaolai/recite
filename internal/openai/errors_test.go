package openai

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/xiaolai/recite/internal/exitcode"
)

func errBody(code, param, msg string) string {
	p := "null"
	if param != "" {
		p = fmt.Sprintf("%q", param)
	}
	return fmt.Sprintf(`{"error":{"message":%q,"type":"invalid_request_error","code":%q,"param":%s}}`, msg, code, p)
}

// One row per HTTP signal of design §5.3. The WebSocket rows join in WP-1.4.
// accept: NET-05#1
func TestCheckResponseHTTPRows(t *testing.T) {
	rows := []struct {
		name   string
		status int
		body   string
		class  Class
		exit   int
		route  bool // route kept on the error
	}{
		{"400 bad voice", 400, errBody("invalid_value", "session.audio.output.voice", "Invalid voice"), ClassInvalidRequest, exitcode.Internal, false},
		{"401 invalid_token", 401, errBody("invalid_token", "", "token expired"), ClassAuthExpired, exitcode.Auth, false},
		{"401 no body", 401, "", ClassAuthExpired, exitcode.Auth, false},
		{"401 missing_scope", 401, errBody("missing_scope", "", "Missing scopes: api.model.audio.request"), ClassRouteDenied, exitcode.RouteDenied, true},
		{"403", 403, errBody("missing_scope", "", "forbidden"), ClassRouteDenied, exitcode.RouteDenied, true},
		{"403 no code", 403, "", ClassRouteDenied, exitcode.RouteDenied, true},
		{"404 endpoint", 404, errBody("", "", "Not found"), ClassRouteDenied, exitcode.RouteDenied, true},
		{"413", 413, "", ClassInvalidRequest, exitcode.Internal, false},
		{"429", 429, errBody("rate_limit_exceeded", "", "slow down"), ClassRateLimited, exitcode.RateLimited, false},
		{"500", 500, errBody("server_error", "", "oops"), ClassServer, exitcode.Failure, false},
		{"502 html", 502, "<html>bad gateway</html>", ClassServer, exitcode.Failure, false},
		{"503", 503, "", ClassServer, exitcode.Failure, false},
		{"304", 304, "", ClassProtocol, exitcode.Failure, false},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			resp := reply(nil, r.status, http.Header{"X-Request-Id": {"req_123"}}, r.body)
			err := CheckResponse(resp, RouteSpeechMint)
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("got %v, want *Error", err)
			}
			if e.Class != r.class || Classify(err) != r.class {
				t.Errorf("class %q, want %q", e.Class, r.class)
			}
			if got := ExitCode(fmt.Errorf("wrapped: %w", err)); got != r.exit {
				t.Errorf("exit %d, want %d", got, r.exit)
			}
			if e.Status != r.status || e.RequestID != "req_123" {
				t.Errorf("status %d id %q", e.Status, e.RequestID)
			}
			if (e.Route == RouteSpeechMint) != r.route {
				t.Errorf("route %q, want kept=%v", e.Route, r.route)
			}
		})
	}
}

// accept: NET-05#1
func TestInvalidRequestNamesParamAndSplitsUserFromBug(t *testing.T) {
	resp := reply(nil, 400, nil, errBody("invalid_value", "session.audio.output.speed", "speed out of range"))
	err := CheckResponse(resp, RouteSpeechMint)
	var e *Error
	if !errors.As(err, &e) || e.Param != "session.audio.output.speed" || e.Code != "invalid_value" {
		t.Fatalf("got %#v", err)
	}
	if !strings.Contains(err.Error(), "param=session.audio.output.speed") {
		t.Errorf("message does not name the param: %v", err)
	}
	if ExitCode(err) != exitcode.Internal {
		t.Error("an unflagged 400 is our bug, exit 1")
	}
	e.UserInput = true
	if ExitCode(err) != exitcode.Usage {
		t.Error("a 400 from user input is exit 2")
	}
}

func TestCheckResponseScrubsAndAcceptsSuccess(t *testing.T) {
	if err := CheckResponse(reply(nil, 200, nil, "{}"), RouteHTTPSTT); err != nil {
		t.Fatalf("2xx: %v", err)
	}
	body := `{"error":{"message":"bad token eyJcanary.canary.canary and ek_canary","type":"x","code":401,"param":null}}`
	err := CheckResponse(reply(nil, 401, nil, body), RouteHTTPSTT)
	if s := err.Error(); strings.Contains(s, "canary") {
		t.Errorf("error leaks secret: %s", s)
	}
	var e *Error
	if errors.As(err, &e) && e.Code != "401" {
		t.Errorf("numeric code %q, want \"401\"", e.Code)
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// accept: NET-05#1
func TestClassifyTransportAndOtherRows(t *testing.T) {
	urlErr := func(err error) error { return &url.Error{Op: "Post", URL: APIBase, Err: err} }
	rows := []struct {
		name  string
		err   error
		class Class
		exit  int
	}{
		{"DNS", urlErr(&net.DNSError{Err: "no such host", Name: "api.openai.com", IsNotFound: true}), ClassNetwork, exitcode.Failure},
		{"connection reset", urlErr(&net.OpError{Op: "read", Err: os.NewSyscallError("read", syscall.ECONNRESET)}), ClassNetwork, exitcode.Failure},
		{"bare reset", syscall.ECONNRESET, ClassNetwork, exitcode.Failure},
		{"refused", syscall.ECONNREFUSED, ClassNetwork, exitcode.Failure},
		{"TLS unknown authority", urlErr(&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}), ClassNetwork, exitcode.Failure},
		{"TLS hostname", urlErr(x509.HostnameError{Host: "x"}), ClassNetwork, exitcode.Failure},
		{"TLS record header", urlErr(tls.RecordHeaderError{Msg: "bad"}), ClassNetwork, exitcode.Failure},
		{"TLS alert", urlErr(tls.AlertError(40)), ClassNetwork, exitcode.Failure},
		{"timeout", urlErr(timeoutErr{}), ClassNetwork, exitcode.Failure},
		{"deadline", urlErr(context.DeadlineExceeded), ClassNetwork, exitcode.Failure},
		{"unexpected EOF", urlErr(io.ErrUnexpectedEOF), ClassNetwork, exitcode.Failure},
		{"cancelled", urlErr(context.Canceled), ClassCancelled, exitcode.Interrupted},
		{"auth-mode", &Error{Class: ClassAuthMode}, ClassAuthMode, exitcode.Auth},
		{"codex-error", fmt.Errorf("codex: %w", &Error{Class: ClassCodexError}), ClassCodexError, exitcode.Auth},
		{"protocol", &Error{Class: ClassProtocol}, ClassProtocol, exitcode.Failure},
		{"verify-failed", &Error{Class: ClassVerifyFailed}, ClassVerifyFailed, exitcode.VerifyFailed},
		{"unknown is internal", errors.New("nil map"), ClassInternal, exitcode.Internal},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			if got := Classify(r.err); got != r.class {
				t.Errorf("Classify = %q, want %q", got, r.class)
			}
			if got := ExitCode(r.err); got != r.exit {
				t.Errorf("ExitCode = %d, want %d", got, r.exit)
			}
		})
	}
	if Classify(nil) != "" || ExitCode(nil) != exitcode.OK {
		t.Error("nil error must be unclassified, exit 0")
	}
}

// A real dial failure through the client is network-class.
func TestClassifyRealDialFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	c, err := NewClient(WithTestOrigin(addr), WithRootCAs(x509.NewCertPool()))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(newReq(t, shapes[0], "https://"+addr+shapes[0].path))
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("dial to closed port succeeded")
	}
	if got := Classify(err); got != ClassNetwork {
		t.Errorf("class %q, want network (%v)", got, err)
	}
}

func TestEveryClassHasAnExitCode(t *testing.T) {
	want := map[Class]int{
		ClassAuthExpired: 3, ClassAuthMode: 3, ClassCodexError: 3, ClassRouteDenied: 4,
		ClassRateLimited: 6, ClassServer: 7, ClassNetwork: 7, ClassProtocol: 7,
		ClassVerifyFailed: 5, ClassCancelled: 130, ClassInternal: 1,
	}
	for c, code := range want {
		if got := c.ExitCode(false); got != code {
			t.Errorf("%s: exit %d, want %d", c, got, code)
		}
	}
}
