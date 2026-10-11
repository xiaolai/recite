package openai

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"

	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/logx"
)

// Class is the failure class of design §5.3. It decides retry and exit code.
type Class string

const (
	ClassAuthExpired    Class = "auth-expired"
	ClassAuthMode       Class = "auth-mode"
	ClassCodexError     Class = "codex-error"
	ClassRouteDenied    Class = "route-denied"
	ClassInvalidRequest Class = "invalid-request"
	ClassRateLimited    Class = "rate-limited"
	ClassServer         Class = "server"
	ClassNetwork        Class = "network"
	ClassProtocol       Class = "protocol"
	ClassVerifyFailed   Class = "verify-failed"
	ClassCancelled      Class = "cancelled"
	ClassInternal       Class = "internal" // programming errors only
)

// Route names the call a route-denied failure is tracked against (design §5.3).
type Route string

const (
	RouteSpeechMint  Route = "speech-mint"
	RouteHTTPSTT     Route = "http-stt"
	RouteRealtimeSTT Route = "realtime-stt"
)

// Error is a classified failure. Code, Param and Message come from the OpenAI
// error body and are scrubbed; no header or bearer is ever stored.
type Error struct {
	Class     Class
	Status    int    // HTTP status, 0 if none
	Type      string // OpenAI error type
	Code      string // OpenAI error code
	Param     string // OpenAI error param, named for invalid-request
	Message   string
	RequestID string // x-request-id
	Route     Route
	UserInput bool  // invalid-request caused by user input (exit 2) rather than our bug (exit 1)
	Err       error // underlying cause, if any
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("openai: ")
	b.WriteString(string(e.Class))
	if e.Route != "" {
		fmt.Fprintf(&b, " (%s)", e.Route)
	}
	if e.Status != 0 {
		fmt.Fprintf(&b, ": HTTP %d", e.Status)
	}
	if e.Code != "" {
		fmt.Fprintf(&b, " code=%s", e.Code)
	}
	if e.Param != "" {
		fmt.Fprintf(&b, " param=%s", e.Param)
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(logx.Scrub(e.Err.Error()))
	}
	if e.RequestID != "" {
		fmt.Fprintf(&b, " (request id %s)", e.RequestID)
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// ExitCode maps a class to its exit code (design §5.3, §12.3). For
// invalid-request, userInput selects 2 (user input) over 1 (our bug).
func (c Class) ExitCode(userInput bool) int {
	switch c {
	case ClassAuthExpired, ClassAuthMode, ClassCodexError:
		return exitcode.Auth
	case ClassRouteDenied:
		return exitcode.RouteDenied
	case ClassInvalidRequest:
		if userInput {
			return exitcode.Usage
		}
		return exitcode.Internal
	case ClassRateLimited:
		return exitcode.RateLimited
	case ClassServer, ClassNetwork, ClassProtocol:
		return exitcode.Failure
	case ClassVerifyFailed:
		return exitcode.VerifyFailed
	case ClassCancelled:
		return exitcode.Interrupted
	default:
		return exitcode.Internal
	}
}

// ExitCode returns the exit code for err; nil is success.
func ExitCode(err error) int {
	if err == nil {
		return exitcode.OK
	}
	var e *Error
	userInput := errors.As(err, &e) && e.UserInput
	return Classify(err).ExitCode(userInput)
}

// Classify returns the class of err. A classified *Error anywhere in the chain
// wins; then cancellation; then transport failures. Anything else is internal.
func Classify(err error) Class {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Class
	}
	if c := classifyWebSocket(err); c != "" {
		return c
	}
	if errors.Is(err, context.Canceled) {
		return ClassCancelled
	}
	if isNetwork(err) {
		return ClassNetwork
	}
	return ClassInternal
}

// isNetwork reports DNS, TLS, connection and timeout failures.
func isNetwork(err error) bool {
	var (
		netErr  net.Error
		recErr  tls.RecordHeaderError
		alert   tls.AlertError
		verify  *tls.CertificateVerificationError
		unknown x509.UnknownAuthorityError
		host    x509.HostnameError
		invalid x509.CertificateInvalidError
	)
	return errors.Is(err, context.DeadlineExceeded) ||
		errors.As(err, &netErr) ||
		errors.As(err, &recErr) || errors.As(err, &alert) || errors.As(err, &verify) ||
		errors.As(err, &unknown) || errors.As(err, &host) || errors.As(err, &invalid) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EPIPE)
}

// maxErrorBody bounds how much of an error response is read.
const maxErrorBody = 64 << 10

// apiErrorBody is the OpenAI error shape {"error":{"message","type","code","param"}}.
type apiErrorBody struct {
	Error struct {
		Message string          `json:"message"`
		Type    string          `json:"type"`
		Code    json.RawMessage `json:"code"` // string or number in the wild
		Param   *string         `json:"param"`
	} `json:"error"`
}

// CheckResponse returns nil for a 2xx response and a classified *Error
// otherwise, reading (at most 64 KiB of) and closing the body in that case.
// The caller sets UserInput on an invalid-request it knows came from the user.
func CheckResponse(resp *http.Response, route Route) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	_ = resp.Body.Close()
	e := &Error{
		Status:    resp.StatusCode,
		RequestID: resp.Header.Get("x-request-id"),
		Route:     route,
	}
	var parsed apiErrorBody
	if json.Unmarshal(body, &parsed) == nil {
		e.Message = logx.Scrub(parsed.Error.Message)
		e.Type = logx.Scrub(parsed.Error.Type)
		e.Code = logx.Scrub(rawString(parsed.Error.Code))
		if parsed.Error.Param != nil {
			e.Param = logx.Scrub(*parsed.Error.Param)
		}
	}
	e.Class = classifyStatus(resp.StatusCode, e.Code)
	if e.Class != ClassRouteDenied {
		e.Route = ""
	}
	return e
}

func rawString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	return string(raw)
}

// classifyStatus holds the HTTP rows of design §5.3. The WebSocket rows (close
// codes and error events) are beside it in wsclass.go.
func classifyStatus(status int, code string) Class {
	switch {
	case status == http.StatusUnauthorized && code == "missing_scope":
		return ClassRouteDenied
	case status == http.StatusUnauthorized:
		return ClassAuthExpired
	case status == http.StatusForbidden, status == http.StatusNotFound:
		return ClassRouteDenied
	case status == http.StatusTooManyRequests:
		return ClassRateLimited
	case status >= 500:
		return ClassServer
	case status >= 400:
		return ClassInvalidRequest
	default: // 1xx/3xx where a final 2xx was expected
		return ClassProtocol
	}
}
