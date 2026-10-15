package openai

import (
	"net/http"

	"github.com/xiaolai/sayso/go/openaihttp"

	"github.com/xiaolai/recite/internal/exitcode"
)

// Class is the failure class of design §5.3. It decides retry and exit code.
type Class = openaihttp.Class

// Error is a classified failure (openaihttp.Error).
type Error = openaihttp.Error

// The classes of the shared taxonomy, and recite's own verify-failed.
const (
	ClassAuthExpired    = openaihttp.ClassAuthExpired
	ClassAuthMode       = openaihttp.ClassAuthMode
	ClassCodexError     = openaihttp.ClassCodexError
	ClassRouteDenied    = openaihttp.ClassRouteDenied
	ClassInvalidRequest = openaihttp.ClassInvalidRequest
	ClassRateLimited    = openaihttp.ClassRateLimited
	ClassServer         = openaihttp.ClassServer
	ClassNetwork        = openaihttp.ClassNetwork
	ClassProtocol       = openaihttp.ClassProtocol
	ClassCancelled      = openaihttp.ClassCancelled
	ClassInternal       = openaihttp.ClassInternal

	ClassVerifyFailed Class = "verify-failed"
)

// Route names the call a route-denied failure is tracked against (design §5.3).
type Route = string

// The routes recite tracks.
const (
	RouteSpeechMint  Route = "speech-mint"
	RouteHTTPSTT     Route = "http-stt"
	RouteRealtimeSTT Route = "realtime-stt"
)

// APIBase is the only origin a bearer may be sent to.
const APIBase = openaihttp.APIBase

// Classify returns the class of err (openaihttp.Classify).
func Classify(err error) Class { return openaihttp.Classify(err) }

// CheckResponse returns nil for a 2xx response and a classified *Error otherwise; route is kept on a route-denied
// error.
func CheckResponse(resp *http.Response, route Route) error {
	return openaihttp.CheckResponse(resp, route)
}

// ConnClosed classifies a WebSocket close received while recite expected more events.
func ConnClosed(code int, reason string, cause error) *Error {
	return openaihttp.ConnClosed(code, reason, cause)
}

// ErrorEvent classifies a realtime `error` event.
func ErrorEvent(typ, code, param, message string) *Error {
	return openaihttp.ErrorEvent(typ, code, param, message)
}

// ClassExitCode maps a class to its exit code (design §5.3, §12.3). An invalid request reaching
// the API is recite's bug (exit 1): user input is validated before any request is made.
func ClassExitCode(c Class) int {
	switch c {
	case ClassAuthExpired, ClassAuthMode, ClassCodexError:
		return exitcode.Auth
	case ClassRouteDenied:
		return exitcode.RouteDenied
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
	return ClassExitCode(Classify(err))
}
