package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/stt"
	"github.com/xiaolai/recite/internal/verify"
)

// Scheduler limits (design §10, RUN-02, RUN-03).
const (
	DefaultJobs  = 3
	MaxJobs      = 6
	WindowPerJob = 8 // W = 8 × jobs
	DrainTimeout = 15 * time.Second
)

// MemoryLimit is the soft Go memory limit the CLI sets unless GOMEMLIMIT is given. The
// admission window bounds the live heap; without a limit the GC pacer lets garbage grow to
// the size of the live heap again, which took a 10 MB input to a 301 MB peak instead of the
// design's < 200 MB (§14.4); with it the peak stays under 200 MB (dev-docs/bench/).
const MemoryLimit = 160 << 20

// UsageError is a bad scheduler setting from the command line (exit 2).
type UsageError struct{ msg string }

func (e *UsageError) Error() string { return e.msg }

// ExitCode is exitcode.Usage.
func (e *UsageError) ExitCode() int { return exitcode.Usage }

// ValidateJobs checks --jobs (1…6) and --verify-jobs (0 means the default, 2 × jobs).
func ValidateJobs(jobs, verifyJobs int) error {
	if jobs < 1 || jobs > MaxJobs {
		return &UsageError{fmt.Sprintf("--jobs %d: want 1 to %d", jobs, MaxJobs)}
	}
	if verifyJobs < 0 {
		return &UsageError{fmt.Sprintf("--verify-jobs %d: want at least 1", verifyJobs)}
	}
	return nil
}

// Task is one chunk to render and verify.
type Task struct {
	Chunk  verify.Chunk
	Spec   realtime.SessionSpec // its session key is the pool key
	Style  string
	Effort realtime.Effort // the chunk's effective effort; "" means the session's
	// Render, when set, is sent to the synthesizer instead of Chunk.Spoken, which stays the
	// reference the take is verified against. Only the calibration harness sets it (VER-07):
	// it renders an edited passage and verifies it against the original.
	Render string
	// CacheKey is the chunk's take cache key (Plan CacheKey); "" keeps the chunk out of the
	// cache.
	CacheKey string
}

// Config configures Run. Synth, Transcriber (unless verification is off) and Deliver are
// required; every other zero value takes its default.
type Config struct {
	Jobs       int // render workers and pooled sessions; 0 means DefaultJobs
	VerifyJobs int // verify workers; 0 means 2 × Jobs
	Window     int // admission window W; 0 means WindowPerJob × Jobs

	Verify      verify.Options
	Synth       realtime.Synthesizer
	Transcriber stt.Transcriber

	Retry *openai.Retrier // nil means a time-seeded retrier with real sleeps
	Gate  *openai.Gate    // pauses new renders while a rate limit is exhausted; nil means none

	Now           func() time.Time // the session clock; nil means time.Now
	SessionMaxAge time.Duration    // 0 means realtime.DefaultSessionMaxAge
	SessionChunks int              // --session-chunks; 0 means realtime.DefaultSessionChunks

	// Deliver receives every finished chunk in index order, as soon as the prefix before it
	// is complete (ordered assembly). An error stops the run.
	Deliver func(i int, r verify.ChunkResult) error

	// Stop is the first interrupt: no new work is dispatched, in-flight work may finish for
	// DrainTimeout, then it is cancelled. Cancelling Run's ctx aborts at once (the second
	// interrupt).
	Stop         <-chan struct{}
	DrainTimeout time.Duration                        // 0 means DrainTimeout
	After        func(time.Duration) <-chan time.Time // nil means time.After

	Cache *TakeCache // the take cache (RUN-04); nil is --no-cache

	DebugDir string       // --debug-dir: each STT result is appended to its render's event dump
	Logger   *slog.Logger // nil discards
}

// Result is the record of a run.
type Result struct {
	// Chunks holds the verdict of every delivered chunk in index order, audio dropped.
	Chunks []verify.ChunkResult
	// MaxHeld is the largest number of chunks that were in flight or held at once.
	MaxHeld int
	// Interrupted is set when Stop or ctx ended the run early.
	Interrupted bool
}

// RunError holds every failure of a run; its exit code follows the fixed severity order
// whatever the order of arrival (CLI-03).
type RunError struct{ Errs []error }

func (e *RunError) Error() string {
	msgs := make([]string, len(e.Errs))
	for i, err := range e.Errs {
		msgs[i] = err.Error()
	}
	return strings.Join(msgs, "; ")
}

// Unwrap returns the failures.
func (e *RunError) Unwrap() []error { return e.Errs }

// ExitCode is the most severe exit code of the failures.
func (e *RunError) ExitCode() int {
	codes := make([]int, len(e.Errs))
	for i, err := range e.Errs {
		codes[i] = errorCode(err)
	}
	return exitcode.Worst(codes...)
}

type coder interface{ ExitCode() int }

func errorCode(err error) int {
	var c coder
	if errors.As(err, &c) {
		return c.ExitCode()
	}
	return openai.ExitCode(err)
}

// ExitCode decides the exit code of a run and whether its output may be written: a failed or
// interrupted run writes nothing; otherwise verify.Outcome decides (VER-09).
func ExitCode(res Result, err error, p verify.OutputPolicy) (code int, write bool) {
	code, write = verify.Outcome(res.Chunks, p)
	if err != nil {
		return exitcode.Worst(code, errorCode(err)), false
	}
	return code, write
}

// ErrInterrupted is the failure recorded for an interrupted run (exit 130).
var ErrInterrupted = &openai.Error{Class: openai.ClassCancelled, Message: "interrupted"}

// errSpeechDenied stops renders already queued in a worker once the speech route is denied.
var errSpeechDenied = &openai.Error{Class: openai.ClassRouteDenied, Route: openai.RouteSpeechMint,
	Message: "the speech route was denied earlier in this run"}

type renderJob struct{ i, n int }

type renderOut struct {
	i, n  int
	take  realtime.Take
	reuse *verify.ChunkResult // a cached take reused without verification
	err   error
}

type verifyOut struct {
	i     int
	a     verify.Attempt
	calls int
	err   error
}

type verifyJob struct {
	i, n int
	take realtime.Take
}

// sched is the shared, read-only part of a run plus the speech-route breaker.
type sched struct {
	cfg    Config
	tasks  []Task
	pool   *realtime.Pool
	tr     stt.Transcriber
	retry  *openai.Retrier
	logger *slog.Logger
	denied atomic.Bool
}

// Run renders and verifies tasks (design §10, RUN-02). One coordinator goroutine — the caller's
// — owns every piece of scheduling state; render and verify workers receive one job at a time
// and send one result back, and never enqueue work. Re-renders go before new chunks, and a
// new chunk is admitted only inside the window [low, low+W), low being the lowest chunk not yet
// delivered, so at most W chunks are ever in flight or held.
func Run(ctx context.Context, cfg Config, tasks []Task) (Result, error) {
	s, err := newSched(cfg, tasks)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = s.pool.Close() }()

	work, cancelWork := context.WithCancel(context.Background())
	defer cancelWork()
	renderCh, verifyCh := make(chan renderJob), make(chan verifyJob)
	renderRes, verifyRes := make(chan renderOut), make(chan verifyOut)
	var wg sync.WaitGroup
	for range s.cfg.Jobs {
		wg.Go(func() {
			for j := range renderCh {
				renderRes <- s.renderOrReuse(work, j)
			}
		})
	}
	for range s.cfg.VerifyJobs {
		wg.Go(func() {
			for j := range verifyCh {
				a, calls, err := s.check(work, j)
				verifyRes <- verifyOut{i: j.i, a: a, calls: calls, err: err}
			}
		})
	}
	defer wg.Wait()
	defer close(verifyCh)
	defer close(renderCh)

	c := coordinator{s: s, n: len(tasks), states: make([]verify.ChunkResult, len(tasks)),
		held: map[int]verify.ChunkResult{}, takes: map[int]map[int]realtime.Take{}}
	stop, ctxDone := s.cfg.Stop, ctx.Done()
	var drain <-chan time.Time
	for c.low < c.n || c.inflightR+c.inflightV > 0 {
		rch, rj, fromQueue := c.nextRender(renderCh)
		var vch chan verifyJob
		var vj verifyJob
		if len(c.verifyQ) > 0 && work.Err() == nil {
			vch, vj = verifyCh, c.verifyQ[0]
		}
		if rch == nil && vch == nil && c.inflightR+c.inflightV == 0 {
			break // stopped or failed, and nothing is left in flight
		}
		select {
		case rch <- rj:
			c.inflightR++
			if fromQueue {
				c.rerender = c.rerender[1:]
			} else {
				c.next++
			}
		case vch <- vj:
			c.inflightV++
			c.verifyQ = c.verifyQ[1:]
		case o := <-renderRes:
			c.inflightR--
			c.rendered(o)
		case o := <-verifyRes:
			c.inflightV--
			c.verified(o)
		case <-stop:
			stop, c.stopping = nil, true
			drain = s.cfg.After(s.cfg.DrainTimeout)
			s.logger.Warn("interrupted: finishing the chunks in flight; interrupt again to abort")
		case <-drain:
			drain = nil
			cancelWork()
		case <-ctxDone:
			ctxDone, c.stopping = nil, true
			cancelWork()
		}
		if work.Err() != nil {
			c.verifyQ = nil // takes not yet verified are abandoned with the run
		}
		c.maxHeld = max(c.maxHeld, c.next-c.low)
	}

	res := Result{Chunks: c.results, MaxHeld: c.maxHeld, Interrupted: c.stopping}
	errs := c.errs
	if c.stopping {
		errs = append(errs, ErrInterrupted)
	}
	if len(errs) > 0 {
		return res, &RunError{Errs: errs}
	}
	return res, nil
}

func newSched(cfg Config, tasks []Task) (*sched, error) {
	if cfg.Jobs == 0 {
		cfg.Jobs = DefaultJobs
	}
	if err := ValidateJobs(cfg.Jobs, cfg.VerifyJobs); err != nil {
		return nil, err
	}
	if cfg.VerifyJobs == 0 {
		cfg.VerifyJobs = 2 * cfg.Jobs
	}
	if cfg.Window == 0 {
		cfg.Window = WindowPerJob * cfg.Jobs
	}
	if cfg.Window < 1 {
		return nil, fmt.Errorf("pipeline: window %d: want at least 1", cfg.Window)
	}
	if cfg.Synth == nil || cfg.Deliver == nil || (cfg.Transcriber == nil && cfg.Verify.Mode != verify.ModeOff) {
		return nil, errors.New("pipeline: Run needs a Synth, a Transcriber and Deliver")
	}
	for _, t := range tasks {
		if err := cfg.Verify.Validate(t.Chunk); err != nil {
			return nil, err
		}
	}
	if cfg.Retry == nil {
		cfg.Retry = openai.NewRetrier(uint64(time.Now().UnixNano()), nil)
	}
	if cfg.DrainTimeout == 0 {
		cfg.DrainTimeout = DrainTimeout
	}
	if cfg.After == nil {
		cfg.After = time.After
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	pool, err := realtime.NewPool(realtime.PoolConfig{
		Synth: cfg.Synth, Max: cfg.Jobs, MaxAge: cfg.SessionMaxAge, MaxChunks: cfg.SessionChunks,
		Now: cfg.Now, Logger: logger,
	})
	if err != nil {
		return nil, err
	}
	s := &sched{cfg: cfg, tasks: tasks, pool: pool, retry: cfg.Retry, logger: logger}
	if cfg.Transcriber != nil {
		s.tr = retryingTranscriber{tr: cfg.Transcriber, retry: cfg.Retry}
	}
	return s, nil
}

// coordinator is the scheduling state; only Run's goroutine touches it.
type coordinator struct {
	s        *sched
	n        int
	next     int // the next new chunk to admit
	low      int // the lowest chunk not yet delivered
	rerender []renderJob
	verifyQ  []verifyJob
	states   []verify.ChunkResult          // attempts so far, per chunk
	held     map[int]verify.ChunkResult    // finished, waiting for the prefix before them
	takes    map[int]map[int]realtime.Take // raw takes of unfinished chunks, by attempt, for the cache
	results  []verify.ChunkResult

	inflightR, inflightV int
	maxHeld              int
	stopping             bool
	errs                 []error
}

// nextRender picks the next render job: a re-render first, then the next chunk inside the
// window. Nothing is dispatched once the run is stopping or has failed.
func (c *coordinator) nextRender(ch chan renderJob) (chan renderJob, renderJob, bool) {
	if c.stopping || len(c.errs) > 0 || c.s.denied.Load() {
		return nil, renderJob{}, false
	}
	if len(c.rerender) > 0 {
		return ch, c.rerender[0], true
	}
	if c.next < c.n && c.next < c.low+c.s.cfg.Window {
		return ch, renderJob{i: c.next, n: 1}, false
	}
	return nil, renderJob{}, false
}

func (c *coordinator) fail(i int, err error) {
	c.errs = append(c.errs, fmt.Errorf("chunk %s: %w", c.s.tasks[i].Chunk.ID, err))
}

func (c *coordinator) rendered(o renderOut) {
	if o.err != nil {
		c.fail(o.i, o.err)
		return
	}
	if o.reuse != nil {
		c.finish(o.i, *o.reuse)
		return
	}
	if c.s.cfg.Cache != nil {
		if c.takes[o.i] == nil {
			c.takes[o.i] = map[int]realtime.Take{}
		}
		c.takes[o.i][o.n] = o.take
	}
	c.verifyQ = append(c.verifyQ, verifyJob{i: o.i, n: o.n, take: o.take})
}

func (c *coordinator) verified(o verifyOut) {
	st := &c.states[o.i]
	st.STTCalls += o.calls
	if o.err != nil {
		c.fail(o.i, o.err)
		return
	}
	if !st.Fold(o.a, c.s.cfg.Verify, c.s.tasks[o.i].Chunk) {
		c.rerender = append(c.rerender, renderJob{i: o.i, n: o.a.N + 1})
		return
	}
	r := *st
	c.states[o.i] = verify.ChunkResult{}
	if r.Final > 0 {
		if take, ok := c.takes[o.i][r.Final]; ok {
			c.s.store(o.i, r, take)
		}
	}
	delete(c.takes, o.i)
	c.finish(o.i, r)
}

// finish holds chunk i's final result and delivers every chunk whose prefix is complete.
func (c *coordinator) finish(i int, r verify.ChunkResult) {
	c.held[i] = r
	for {
		r, ok := c.held[c.low]
		if !ok || len(c.errs) > 0 {
			return
		}
		delete(c.held, c.low)
		if err := c.s.cfg.Deliver(c.low, r); err != nil {
			c.fail(c.low, err)
			return
		}
		c.results = append(c.results, stripAudio(r))
		c.low++
	}
}

// stripAudio drops a delivered chunk's PCM, so memory stays bounded by the window.
func stripAudio(r verify.ChunkResult) verify.ChunkResult {
	r.PCM = nil
	r.Attempts = append([]verify.Attempt(nil), r.Attempts...)
	for i := range r.Attempts {
		r.Attempts[i].PCM = nil
	}
	return r
}

// renderOrReuse serves a chunk's first attempt from the cache when it can (RUN-04), and
// renders otherwise.
func (s *sched) renderOrReuse(ctx context.Context, j renderJob) renderOut {
	if j.n == 1 {
		if take, reuse, hit := s.lookup(j.i); hit {
			return renderOut{i: j.i, n: j.n, take: take, reuse: reuse}
		}
	}
	take, err := s.render(ctx, j)
	return renderOut{i: j.i, n: j.n, take: take, err: err}
}

// render runs one render job with retries (NET-06) on a pooled session (SPK-06).
func (s *sched) render(ctx context.Context, j renderJob) (realtime.Take, error) {
	t := s.tasks[j.i]
	var take realtime.Take
	err := s.retry.Do(ctx, func(ctx context.Context, _ int) error {
		if s.denied.Load() {
			return errSpeechDenied
		}
		if s.cfg.Gate != nil {
			if err := s.cfg.Gate.Wait(ctx); err != nil {
				return err
			}
		}
		lease, err := s.pool.Acquire(ctx, t.Spec)
		if err != nil {
			s.noteDenied(err)
			return err
		}
		defer lease.Release()
		say := t.Chunk.Spoken
		if t.Render != "" {
			say = t.Render
		}
		tk, err := lease.Render(ctx, realtime.RenderRequest{
			ChunkID: t.Chunk.ID, Text: say, Style: t.Style, Effort: t.Effort,
			Attempt: j.n, Estimate: t.Chunk.Estimate,
		})
		if err != nil {
			s.noteDenied(err)
			s.logger.Debug("render failed", "chunk", t.Chunk.ID, "attempt", j.n, "class", openai.Classify(err))
			return err
		}
		take = tk
		return nil
	})
	return take, err
}

// noteDenied opens the run-wide breaker when the speech route is refused (NET-06).
func (s *sched) noteDenied(err error) {
	if openai.Classify(err) == openai.ClassRouteDenied {
		s.denied.Store(true)
	}
}

// check verifies one take on a verify worker.
func (s *sched) check(ctx context.Context, j verifyJob) (verify.Attempt, int, error) {
	t := s.tasks[j.i]
	a, calls, err := verify.Check(ctx, t.Chunk, s.cfg.Verify, j.n, j.take, s.tr)
	if err == nil && s.cfg.DebugDir != "" {
		s.dumpSTT(t.Chunk.ID, a)
	}
	return a, calls, err
}

// dumpSTT appends the attempt's transcriptions to its event dump (OBS-02).
func (s *sched) dumpSTT(chunkID string, a verify.Attempt) {
	if len(a.Transcriptions) == 0 {
		return
	}
	var b []byte
	for _, tr := range a.Transcriptions {
		line, err := json.Marshal(struct {
			Dir      string  `json:"dir"`
			Text     string  `json:"text"`
			Language string  `json:"language"`
			Score    float64 `json:"score"`
		}{"stt", tr.Text, tr.Language, tr.Result.Score})
		if err != nil {
			continue
		}
		b = append(append(b, line...), '\n')
	}
	if err := os.MkdirAll(s.cfg.DebugDir, 0o700); err != nil {
		s.logger.Warn("debug dump", "err", err)
		return
	}
	f, err := os.OpenFile(filepath.Join(s.cfg.DebugDir, realtime.DumpName(chunkID, a.N)), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		s.logger.Warn("debug dump", "err", err)
		return
	}
	_, werr := f.Write(b)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		s.logger.Warn("debug dump", "err", werr)
	}
}

// retryingTranscriber retries a transcription after a retryable failure (NET-06).
type retryingTranscriber struct {
	tr    stt.Transcriber
	retry *openai.Retrier
}

func (r retryingTranscriber) Transcribe(ctx context.Context, pcm []byte, hint stt.Hint) (stt.Transcript, error) {
	var out stt.Transcript
	err := r.retry.Do(ctx, func(ctx context.Context, _ int) error {
		t, err := r.tr.Transcribe(ctx, pcm, hint)
		out = t
		return err
	})
	return out, err
}

// PauseOnExhausted returns an OnRateLimits handler that closes g until the latest reset of an
// exhausted limit (NET-06: rate_limits.updated with remaining 0 pauses new renders).
func PauseOnExhausted(g *openai.Gate) func(realtime.RateLimitsUpdated) {
	return func(ev realtime.RateLimitsUpdated) {
		for _, l := range ev.Limits {
			if l.Remaining <= 0 && l.ResetSeconds > 0 {
				g.PauseFor(time.Duration(l.ResetSeconds * float64(time.Second)))
			}
		}
	}
}
