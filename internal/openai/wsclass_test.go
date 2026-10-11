package openai

import (
	"fmt"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/xiaolai/recite/internal/exitcode"
)

// One row per WebSocket signal of design §5.3: close codes and `error` events.
// accept: NET-05#1
func TestClassifyWebSocketRows(t *testing.T) {
	closeErr := func(code websocket.StatusCode) error {
		return fmt.Errorf("failed to get reader: %w", websocket.CloseError{Code: code, Reason: "keepalive ping timeout"})
	}
	rows := []struct {
		name  string
		err   error
		class Class
		exit  int
	}{
		{"close 1011", closeErr(websocket.StatusInternalError), ClassServer, exitcode.Failure},
		{"close 1012", closeErr(websocket.StatusServiceRestart), ClassServer, exitcode.Failure},
		{"close 1013", closeErr(websocket.StatusTryAgainLater), ClassServer, exitcode.Failure},
		{"close 1014", closeErr(websocket.StatusBadGateway), ClassServer, exitcode.Failure},
		{"close 1001", closeErr(websocket.StatusGoingAway), ClassServer, exitcode.Failure},
		{"close 1006 abnormal", closeErr(websocket.StatusAbnormalClosure), ClassNetwork, exitcode.Failure},
		{"close 1008 policy", closeErr(websocket.StatusPolicyViolation), ClassInvalidRequest, exitcode.Internal},
		{"close 1009 too big", closeErr(websocket.StatusMessageTooBig), ClassProtocol, exitcode.Failure},
		{"close 1002", closeErr(websocket.StatusProtocolError), ClassProtocol, exitcode.Failure},
		{"close 1000 mid-response", closeErr(websocket.StatusNormalClosure), ClassProtocol, exitcode.Failure},
		{"close 4000 unknown", closeErr(4000), ClassServer, exitcode.Failure},
		{"ConnClosed 1011", ConnClosed(1011, "keepalive ping timeout", nil), ClassServer, exitcode.Failure},
		{"ConnClosed 1006", ConnClosed(1006, "", nil), ClassNetwork, exitcode.Failure},
		{"event server_error", ErrorEvent("server_error", "server_error", "", "boom"), ClassServer, exitcode.Failure},
		{"event invalid_value", ErrorEvent("invalid_request_error", "invalid_value", "response.audio.output.voice", "bad voice"), ClassInvalidRequest, exitcode.Internal},
		{"event rate_limit_exceeded", ErrorEvent("invalid_request_error", "rate_limit_exceeded", "", "slow"), ClassRateLimited, exitcode.RateLimited},
		{"event insufficient_quota", ErrorEvent("insufficient_quota", "", "", "quota"), ClassRateLimited, exitcode.RateLimited},
		{"event missing_scope", ErrorEvent("invalid_request_error", "missing_scope", "", "scopes"), ClassRouteDenied, exitcode.RouteDenied},
		{"event unknown type", ErrorEvent("brand_new_error", "", "", "?"), ClassServer, exitcode.Failure},
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
}

func TestErrorEventScrubsAndNamesParam(t *testing.T) {
	e := ErrorEvent("invalid_request_error", "invalid_value", "response.audio.output.voice", "bad ek_canary value")
	if strings.Contains(e.Error(), "ek_canary") || !strings.Contains(e.Error(), "param=response.audio.output.voice") {
		t.Fatalf("error = %q", e.Error())
	}
	c := ConnClosed(1011, "reason eyJcanary.canary.canary", nil)
	if strings.Contains(c.Error(), "canary") || c.Code != "1011" {
		t.Fatalf("close error = %q", c.Error())
	}
}
