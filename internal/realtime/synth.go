package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
	"unicode"

	"github.com/xiaolai/recite/internal/openai"
)

// Synth is the production Synthesizer (SPK-01): it mints a client secret for the
// session spec and dials the realtime WebSocket.
type Synth struct {
	Minter  *Minter      // mints with the TokenSource's token, refreshing once on auth-expired
	Client  *http.Client // the production client
	BaseURL string       // openai.APIBase; tests use a reserved test origin
	Logger  *slog.Logger // nil discards

	// Now measures FirstAudio, Wall and Age; nil means time.Now.
	Now func() time.Time
	// IdleTimeout is the per-response idle limit; 0 means openai.ResponseIdleTimeout.
	IdleTimeout time.Duration
	// HardCap maps an estimate to the per-response cap; nil means openai.ResponseHardCap.
	HardCap func(estimate time.Duration) time.Duration
	// OnRateLimits receives every rate_limits.updated event (consumed by WP-2.8). It is
	// called on the rendering goroutine and must not block.
	OnRateLimits func(RateLimitsUpdated)
	// Dumps, if set, writes event dumps (OBS-02).
	Dumps *Dumps
}

var _ Synthesizer = (*Synth)(nil)

// Open mints a secret for spec and opens its session.
func (s *Synth) Open(ctx context.Context, spec SessionSpec) (SpeechSession, error) {
	if s.Minter == nil || s.Client == nil || s.BaseURL == "" {
		return nil, errors.New("realtime: Synth needs a Minter, a Client and a BaseURL")
	}
	secret, err := s.Minter.MintSpeech(ctx, spec)
	if err != nil {
		return nil, err
	}
	cfg := DialConfig{
		Client: s.Client,
		URL:    WebSocketURL(s.BaseURL, spec.Model),
		Secret: secret,
		Route:  openai.RouteSpeechMint,
		Logger: s.Logger,
	}
	var events *eventLog
	if s.Dumps != nil {
		events = &eventLog{}
		cfg.Trace = events.record
	}
	conn, err := Dial(ctx, cfg)
	if err != nil {
		return nil, err
	}
	now := s.Now
	if now == nil {
		now = time.Now
	}
	idle := s.IdleTimeout
	if idle <= 0 {
		idle = openai.ResponseIdleTimeout
	}
	hardCap := s.HardCap
	if hardCap == nil {
		hardCap = openai.ResponseHardCap
	}
	return &liveSession{
		conn: conn, spec: spec, now: now, opened: now(),
		idle: idle, hardCap: hardCap, onRateLimits: s.OnRateLimits,
		dumps: s.Dumps, events: events,
	}, nil
}

// liveSession renders one response at a time on one WebSocket.
type liveSession struct {
	conn         *Session
	spec         SessionSpec
	now          func() time.Time
	opened       time.Time
	idle         time.Duration
	hardCap      func(time.Duration) time.Duration
	onRateLimits func(RateLimitsUpdated)
	dumps        *Dumps
	events       *eventLog

	mu     sync.Mutex // one response in flight
	broken error      // set when a response was abandoned mid-flight; the session is unusable
}

var (
	errIdle    = errors.New("realtime: no event within the per-response idle timeout")
	errHardCap = errors.New("realtime: no response.done within the per-response hard cap")
)

func (ss *liveSession) Age() time.Duration { return ss.now().Sub(ss.opened) }

func (ss *liveSession) Close() error { return ss.conn.Close() }

// CreatedAwaiter is a session that can wait for the server's session.created. A WebSocket
// upgrade alone does not prove the session exists: the server may answer it with an error event
// or close at once.
type CreatedAwaiter interface {
	AwaitCreated(ctx context.Context) error
}

// AwaitCreated waits, within the idle timeout, for session.created. An error event, a closed
// connection or the timeout fails it and leaves the session unusable.
func (ss *liveSession) AwaitCreated(ctx context.Context) error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.broken != nil {
		return ss.broken
	}
	wctx, cancel := context.WithTimeout(ctx, ss.idle)
	defer cancel()
	for {
		ev, err := ss.conn.Next(wctx)
		if err != nil {
			if ev == nil && wctx.Err() != nil && ctx.Err() == nil {
				err = &openai.Error{Class: openai.ClassNetwork, Message: "realtime: no session.created within the idle timeout", Err: err}
			}
			ss.broken = err
			return err
		}
		switch e := ev.(type) {
		case SessionCreated:
			return nil
		case ErrorEvent:
			ss.broken = e.Err
			return e.Err
		}
		// Anything else before session.created (session.updated, an unknown type) is skipped.
	}
}

// Render sends one out-of-band response.create and collects its audio and
// transcript until response.done (design §6.2). With Dumps, the response's events are
// written as OBS-02 says.
func (ss *liveSession) Render(ctx context.Context, req RenderRequest) (Take, error) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.broken != nil {
		return Take{}, fmt.Errorf("realtime: session unusable after an abandoned response: %w", ss.broken)
	}
	attempt := req.Attempt
	if attempt == 0 {
		attempt = 1
	}
	if ss.events != nil {
		ss.events.reset()
	}
	take, err := ss.render(ctx, req, attempt)
	return take, dumpRender(ss.dumps, ss.events, req.ChunkID, attempt, err)
}

func (ss *liveSession) render(ctx context.Context, req RenderRequest, attempt int) (Take, error) {
	effort := req.Effort
	if effort == "" {
		effort = ss.spec.Effort
	}
	msg, err := ResponseCreate(ChunkRequest{
		ChunkID: req.ChunkID, Attempt: attempt, Text: req.Text,
		Instructions: ResponseInstructions(ss.spec.Instructions, req.Style),
		Effort:       effort, SessionEffort: ss.spec.Effort,
	})
	if err != nil {
		return Take{}, err
	}
	estimate := req.Estimate
	if estimate <= 0 {
		estimate = EstimateDuration(req.Text, ss.spec.Speed)
	}
	rctx, cancel := context.WithTimeoutCause(ctx, ss.hardCap(estimate), errHardCap)
	defer cancel()

	start := ss.now()
	if err := ss.conn.Send(rctx, json.RawMessage(msg)); err != nil {
		return Take{}, ss.abandon(ctx, rctx, err)
	}
	var take Take
	var said []byte
	gotAudio := false
	for {
		ectx, ecancel := context.WithTimeoutCause(rctx, ss.idle, errIdle)
		ev, err := ss.conn.Next(ectx)
		cause := context.Cause(ectx)
		ecancel()
		if err != nil {
			if ev == nil && ectx.Err() != nil {
				return Take{}, ss.abandon(ctx, rctx, cause)
			}
			var oe *openai.Error
			if errors.As(err, &oe) && oe.Class == openai.ClassProtocol {
				ss.broken = err // a malformed event mid-response leaves its state unknown
			}
			return Take{}, err
		}
		switch e := ev.(type) {
		case AudioDelta:
			if !gotAudio {
				take.FirstAudio = ss.now().Sub(start)
				gotAudio = true
			}
			take.PCM = append(take.PCM, e.PCM...)
		case TranscriptDelta:
			said = append(said, e.Delta...)
		case RateLimitsUpdated:
			if ss.onRateLimits != nil {
				ss.onRateLimits(e)
			}
		case ErrorEvent:
			return Take{}, e.Err
		case ResponseDone:
			if got := e.Metadata["chunk"]; got != req.ChunkID {
				return Take{}, &openai.Error{Class: openai.ClassProtocol,
					Message: fmt.Sprintf("response.done for chunk %q while rendering %q", got, req.ChunkID)}
			}
			if e.Status == StatusCompleted && !gotAudio {
				return Take{}, &openai.Error{Class: openai.ClassProtocol, Message: "response.done completed without audio"}
			}
			take.Said = string(said)
			take.Status = e.Status
			take.Usage = e.Usage
			take.Wall = ss.now().Sub(start)
			return take, nil
		}
		// session.created and response.created carry nothing a take needs.
	}
}

// abandon classifies a response cut short by a deadline or a send failure and
// marks the session unusable: the server may still be streaming the response.
func (ss *liveSession) abandon(parent, rctx context.Context, err error) error {
	switch {
	case parent.Err() != nil:
		err = parent.Err()
	case errors.Is(err, errIdle):
		err = &openai.Error{Class: openai.ClassNetwork, Message: errIdle.Error(), Err: err}
	case errors.Is(context.Cause(rctx), errHardCap):
		err = &openai.Error{Class: openai.ClassProtocol, Message: errHardCap.Error(), Err: errHardCap}
	}
	ss.broken = err
	return err
}

// Speaking rates at speed 1.0 measured by the probes (design §7.2). A per-script
// table replaces this when the segmenter lands; until then CJK runes count at the
// CJK rate and everything else at the Latin rate.
const (
	latinCharsPerSecond = 14.0
	cjkCharsPerSecond   = 4.3
)

// EstimateDuration is the provisional speech-duration estimate of text at speed
// (design §7.2: chars / rate(script) / speed).
func EstimateDuration(text string, speed float64) time.Duration {
	if speed <= 0 {
		speed = DefaultSpeed
	}
	var cjk, other int
	for _, r := range text {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			cjk++
		} else {
			other++
		}
	}
	secs := float64(cjk)/cjkCharsPerSecond + float64(other)/latinCharsPerSecond
	return time.Duration(secs / speed * float64(time.Second))
}
