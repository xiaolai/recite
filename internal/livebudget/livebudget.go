// Package livebudget enforces the audio budget of the live suites (implementation plan §1.4):
// a preflight on the estimate before rendering, and a running count of the audio actually
// received that stops the run when the budget is reached. Only live and qualify tests use it.
package livebudget

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/verify"
)

// Errors of the two checks.
var (
	ErrOverBudget = errors.New("livebudget: the worst case is over the budget; shard the run")
	ErrExceeded   = errors.New("livebudget: the audio budget was reached; the run is incomplete")
)

// Preflight refuses a run whose worst case, the longest take the duration guard accepts for each
// chunk (verify.MaxTakeDuration) × the attempt limit, is over the budget.
func Preflight(chunks []verify.Chunk, attempts int, budget time.Duration) error {
	var each time.Duration
	for _, c := range chunks {
		each += verify.MaxTakeDuration(c.Estimate, c.Spoken)
	}
	worst := each * time.Duration(attempts)
	if worst > budget {
		return fmt.Errorf("%w: up to %v of audio, budget %v", ErrOverBudget, worst.Round(time.Second), budget)
	}
	return nil
}

// Budget counts the audio of every take received, rejected takes included, and cancels the
// run once the count reaches the limit. In-flight renders are cancelled with it, so the
// overshoot is at most one take per render job. Safe for concurrent use.
type Budget struct {
	limit time.Duration

	mu     sync.Mutex
	used   time.Duration
	cancel context.CancelCauseFunc
}

// New returns a budget of limit.
func New(limit time.Duration) *Budget { return &Budget{limit: limit} }

// Used is the audio received so far.
func (b *Budget) Used() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

// Exceeded reports whether the budget was reached.
func (b *Budget) Exceeded() bool { return b.Used() >= b.limit }

// RunFunc is the shape of pipeline.Run.
type RunFunc func(context.Context, pipeline.Config, []pipeline.Task) (pipeline.Result, error)

// Pipeline wraps run so that its synthesizer is counted and the run is cancelled at the
// budget. The returned error wraps ErrExceeded when the budget stopped the run.
func (b *Budget) Pipeline(run RunFunc) RunFunc {
	return func(ctx context.Context, cfg pipeline.Config, tasks []pipeline.Task) (pipeline.Result, error) {
		ctx, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)
		b.mu.Lock()
		b.cancel = cancel
		b.mu.Unlock()
		cfg.Synth = synth{cfg.Synth, b}
		res, err := run(ctx, cfg, tasks)
		if errors.Is(context.Cause(ctx), ErrExceeded) {
			return res, fmt.Errorf("%w (%v received, budget %v): %v", ErrExceeded, b.Used().Round(time.Second), b.limit, err)
		}
		return res, err
	}
}

func (b *Budget) add(d time.Duration) {
	b.mu.Lock()
	b.used += d
	over, cancel := b.used >= b.limit, b.cancel
	b.mu.Unlock()
	if over && cancel != nil {
		cancel(ErrExceeded)
	}
}

type synth struct {
	realtime.Synthesizer
	b *Budget
}

func (s synth) Open(ctx context.Context, spec realtime.SessionSpec) (realtime.SpeechSession, error) {
	ss, err := s.Synthesizer.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	return session{ss, s.b}, nil
}

type session struct {
	realtime.SpeechSession
	b *Budget
}

func (s session) Render(ctx context.Context, req realtime.RenderRequest) (realtime.Take, error) {
	take, err := s.SpeechSession.Render(ctx, req)
	s.b.add(audio.PCM(take.PCM).Duration())
	return take, err
}
