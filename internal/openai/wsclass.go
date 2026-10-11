package openai

import (
	"errors"
	"fmt"

	"github.com/coder/websocket"

	"github.com/xiaolai/recite/internal/logx"
)

// The WebSocket rows of design §5.3 live here, beside the HTTP rows of
// classifyStatus; Classify dispatches to both, so there is one classification.

// classifyClose maps a close code received from the peer. 1006 (abnormal
// closure) never travels on the wire: the library reports it for a dropped
// connection, which is a network failure.
func classifyClose(code websocket.StatusCode) Class {
	switch code {
	case websocket.StatusAbnormalClosure:
		return ClassNetwork
	case websocket.StatusInternalError, // 1011, e.g. "keepalive ping timeout"
		websocket.StatusServiceRestart, websocket.StatusTryAgainLater, websocket.StatusBadGateway,
		websocket.StatusGoingAway:
		return ClassServer
	case websocket.StatusPolicyViolation:
		return ClassInvalidRequest
	case websocket.StatusMessageTooBig, websocket.StatusProtocolError, websocket.StatusUnsupportedData,
		websocket.StatusInvalidFramePayloadData, websocket.StatusNormalClosure, websocket.StatusNoStatusRcvd:
		// The peer ended the session while recite still expected events.
		return ClassProtocol
	default:
		return ClassServer
	}
}

// ConnClosed classifies a WebSocket close received while recite expected
// more events. The close reason is scrubbed.
func ConnClosed(code int, reason string, cause error) *Error {
	return &Error{
		Class:   classifyClose(websocket.StatusCode(code)),
		Code:    fmt.Sprint(code),
		Message: "realtime connection closed: " + logx.Scrub(reason),
		Err:     cause,
	}
}

// ErrorEvent classifies a realtime `error` event by its error.type and
// error.code (design §5.3). All strings are scrubbed.
func ErrorEvent(typ, code, param, message string) *Error {
	e := &Error{
		Type:    logx.Scrub(typ),
		Code:    logx.Scrub(code),
		Param:   logx.Scrub(param),
		Message: logx.Scrub(message),
	}
	switch {
	case code == "rate_limit_exceeded" || typ == "rate_limit_error" || typ == "insufficient_quota" || code == "insufficient_quota":
		e.Class = ClassRateLimited
	case code == "missing_scope":
		e.Class = ClassRouteDenied // the caller sets Route: it knows which session this was
	case typ == "invalid_request_error":
		e.Class = ClassInvalidRequest
	default: // server_error and anything a later API adds
		e.Class = ClassServer
	}
	return e
}

// classifyWebSocket returns the class of a library error carrying a close
// frame, or "" when err holds none.
func classifyWebSocket(err error) Class {
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return classifyClose(ce.Code)
	}
	return ""
}
