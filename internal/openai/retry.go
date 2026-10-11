package openai

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"
)

// Backoff parameters of design §5.3 (NET-06): exponential with full jitter.
const (
	BackoffBase = time.Second
	BackoffCap  = 16 * time.Second
)

// MaxAttempts is how many attempts in total a failure of class c allows (NET-06): 5 for
// rate-limited, 3 for server, network and protocol, and 1 (no retry) for every other class.
func MaxAttempts(c Class) int {
	switch c {
	case ClassRateLimited:
		return 5
	case ClassServer, ClassNetwork, ClassProtocol:
		return 3
	}
	return 1
}

// BackoffCeiling is the upper bound of the wait after the n-th failure (1-based):
// min(BackoffCap, BackoffBase·2^(n-1)).
func BackoffCeiling(n int) time.Duration {
	d := BackoffBase
	for i := 1; i < n && d < BackoffCap; i++ {
		d *= 2
	}
	return min(d, BackoffCap)
}

// Sleeper waits d or until ctx ends, returning ctx's error in that case.
type Sleeper func(ctx context.Context, d time.Duration) error

// SleepContext is the real-time Sleeper.
func SleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Retrier runs an operation again after a retryable failure, waiting a full-jitter backoff
// between attempts (NET-06). Safe for concurrent use.
type Retrier struct {
	mu   sync.Mutex
	rand *rand.Rand

	sleep Sleeper
	// OnRetry, if set, is called before each wait with the failed attempt and the wait.
	OnRetry func(attempt int, err error, wait time.Duration)
}

// NewRetrier returns a retrier drawing its jitter from a PCG seeded with seed, and waiting
// through sleep (nil means SleepContext).
func NewRetrier(seed uint64, sleep Sleeper) *Retrier {
	if sleep == nil {
		sleep = SleepContext
	}
	return &Retrier{rand: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)), sleep: sleep}
}

// Delay draws the wait after the n-th failure: uniform in [0, BackoffCeiling(n)].
func (r *Retrier) Delay(n int) time.Duration {
	ceil := BackoffCeiling(n)
	r.mu.Lock()
	defer r.mu.Unlock()
	return time.Duration(r.rand.Int64N(int64(ceil) + 1))
}

// Do calls op with attempt = 1, 2, … until it succeeds, fails with a class that is not
// retried, or the attempts allowed for the class of its latest failure run out. The last
// error is returned unchanged, so its class decides the exit code.
func (r *Retrier) Do(ctx context.Context, op func(ctx context.Context, attempt int) error) error {
	for n := 1; ; n++ {
		err := op(ctx, n)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil || n >= MaxAttempts(Classify(err)) {
			return err
		}
		wait := r.Delay(n)
		if r.OnRetry != nil {
			r.OnRetry(n, err, wait)
		}
		if serr := r.sleep(ctx, wait); serr != nil {
			return err
		}
	}
}

// Gate pauses new requests while a rate limit is exhausted (NET-06: rate_limits.updated with
// remaining 0 pauses new renders until reset_seconds). Safe for concurrent use.
type Gate struct {
	mu    sync.Mutex
	until time.Time
	now   func() time.Time
	sleep Sleeper
}

// NewGate returns a gate over the clock now and sleep (nil means time.Now and SleepContext).
func NewGate(now func() time.Time, sleep Sleeper) *Gate {
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = SleepContext
	}
	return &Gate{now: now, sleep: sleep}
}

// PauseFor closes the gate for d from now; a later deadline already set is kept.
func (g *Gate) PauseFor(d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if until := g.now().Add(d); until.After(g.until) {
		g.until = until
	}
}

// Wait returns once the gate is open, or ctx's error.
func (g *Gate) Wait(ctx context.Context) error {
	for {
		g.mu.Lock()
		left := g.until.Sub(g.now())
		g.mu.Unlock()
		if left <= 0 {
			return ctx.Err()
		}
		if err := g.sleep(ctx, left); err != nil {
			return err
		}
	}
}
