package stt

import (
	"context"
	"log/slog"
	"sync"

	"github.com/xiaolai/recite/internal/openai"
)

// Chain verifies with Primary (HTTP) until Primary is route-denied, then with Fallback
// (realtime) for the rest of the run (VER-02, design §5.3: route-denied is tracked per route).
// The take that met the refusal is transcribed by Fallback at once. A route-denied Fallback is
// returned as is: every verifier route is then denied, which the caller turns into exit 4.
type Chain struct {
	Primary, Fallback Transcriber
	Logger            *slog.Logger // nil discards

	// OnSwitch, if set, is called once, when the run switches to Fallback, with the refusal
	// that caused it; the run result (and later the manifest) records it from there.
	OnSwitch func(cause error)

	mu       sync.Mutex
	switched bool
}

var _ Transcriber = (*Chain)(nil)

// Switched reports whether the run has switched to Fallback.
func (c *Chain) Switched() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.switched
}

// Route names the verifier route in use and its STT model; cached verification records it
// (RUN-04).
func (c *Chain) Route() (route, model string) {
	if c.Switched() {
		return "realtime", RealtimeModel
	}
	return "http", HTTPModel
}

// Transcribe implements Transcriber.
func (c *Chain) Transcribe(ctx context.Context, pcm []byte, hint Hint) (Transcript, error) {
	if !c.Switched() {
		t, err := c.Primary.Transcribe(ctx, pcm, hint)
		if openai.Classify(err) != openai.ClassRouteDenied {
			return t, err
		}
		c.switchOnce(err)
	}
	return c.Fallback.Transcribe(ctx, pcm, hint)
}

// switchOnce records the switch; concurrent refusals log and report it only once.
func (c *Chain) switchOnce(cause error) {
	c.mu.Lock()
	first := !c.switched
	c.switched = true
	c.mu.Unlock()
	if !first {
		return
	}
	logger := c.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	logger.Warn("HTTP transcription is not available to this sign-in; verifying with realtime transcription for the rest of the run", "cause", cause.Error())
	if c.OnSwitch != nil {
		c.OnSwitch(cause)
	}
}
