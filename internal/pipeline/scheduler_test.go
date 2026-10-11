package pipeline_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	rtfake "github.com/xiaolai/recite/internal/realtime/fake"
	"github.com/xiaolai/recite/internal/stt"
	sttfake "github.com/xiaolai/recite/internal/stt/fake"
	"github.com/xiaolai/recite/internal/verify"
)

// spokenOf is the text of chunk i: distinct per chunk, so a take of the wrong chunk fails.
func spokenOf(i int) string {
	return fmt.Sprintf("The quick brown fox number %d jumps over the lazy dog.", i+1)
}

func chunkID(i int) string { return fmt.Sprintf("c%04d", i+1) }

// tasks makes n tasks of one second each; voices are assigned round robin.
func tasks(n int, voices ...string) []pipeline.Task {
	if len(voices) == 0 {
		voices = []string{"marin"}
	}
	out := make([]pipeline.Task, n)
	for i := range out {
		out[i] = pipeline.Task{
			Chunk: verify.Chunk{ID: chunkID(i), Spoken: spokenOf(i), Estimate: time.Second},
			Spec:  realtime.SessionSpec{Model: "gpt-realtime-2.1", Voice: voices[i%len(voices)], Speed: 1, Instructions: "read", Effort: "low"},
		}
	}
	return out
}

// echo transcribes a fake take as exactly the text of its chunk.
type echo struct {
	mu    sync.Mutex
	texts map[string]string
	calls map[string]int
}

func newEcho(ts []pipeline.Task) *echo {
	e := &echo{texts: map[string]string{}, calls: map[string]int{}}
	for _, t := range ts {
		e.texts[t.Chunk.ID] = t.Chunk.Spoken
	}
	return e
}

func (e *echo) Transcribe(ctx context.Context, pcm []byte, _ stt.Hint) (stt.Transcript, error) {
	if err := ctx.Err(); err != nil {
		return stt.Transcript{}, err
	}
	id, _, ok := rtfake.PCMTag(pcm)
	e.mu.Lock()
	defer e.mu.Unlock()
	if !ok {
		return stt.Transcript{}, errors.New("echo: untagged take")
	}
	e.calls[id]++
	return stt.Transcript{Text: e.texts[id], Languages: []stt.DetectedLanguage{}}, nil
}

func (e *echo) chunks() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var ids []string
	for id := range e.calls {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// noSleep makes backoffs instant; the waits are not under test here.
func noSleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }

// run is one scheduler run; it records the delivery order.
type run struct {
	cfg       pipeline.Config
	mu        sync.Mutex
	delivered []int
}

func newRun(synth realtime.Synthesizer, tr stt.Transcriber, jobs int) *run {
	r := &run{}
	r.cfg = pipeline.Config{
		Jobs:        jobs,
		Verify:      verify.DefaultOptions(),
		Synth:       synth,
		Transcriber: tr,
		Retry:       openai.NewRetrier(1, noSleep),
		Deliver: func(i int, res verify.ChunkResult) error {
			if res.ChunkID != chunkID(i) {
				return fmt.Errorf("delivered %s at %d", res.ChunkID, i)
			}
			r.mu.Lock()
			r.delivered = append(r.delivered, i)
			r.mu.Unlock()
			return nil
		},
	}
	return r
}

func (r *run) start(t *testing.T, ts []pipeline.Task) (pipeline.Result, int, error) {
	t.Helper()
	type out struct {
		res pipeline.Result
		err error
	}
	done := make(chan out, 1)
	go func() {
		res, err := pipeline.Run(context.Background(), r.cfg, ts)
		done <- out{res, err}
	}()
	select {
	case o := <-done:
		code, _ := pipeline.ExitCode(o.res, o.err, verify.OutputPolicy{})
		return o.res, code, o.err
	case <-time.After(60 * time.Second):
		t.Fatal("the run did not terminate")
		return pipeline.Result{}, 0, nil
	}
}

func (r *run) inOrder(t *testing.T, n int) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.delivered) != n {
		t.Fatalf("delivered %d chunks, want %d", len(r.delivered), n)
	}
	for i, d := range r.delivered {
		if d != i {
			t.Fatalf("delivery %d was chunk %d", i, d)
		}
	}
}

// accept: RUN-02#1
func TestRun500ChunksRandomLatencyInOrder(t *testing.T) {
	defer goleak.VerifyNone(t)
	ts := tasks(500, "marin", "cedar")
	synth := rtfake.NewScriptedSynthesizer()
	var mu sync.Mutex
	rng := rand.New(rand.NewPCG(5, 8))
	synth.Hook = func(ctx context.Context, _ rtfake.RenderCall) error {
		mu.Lock()
		d := time.Duration(rng.IntN(1500)) * time.Microsecond
		mu.Unlock()
		select {
		case <-time.After(d):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	r := newRun(synth, newEcho(ts), 3)
	res, code, err := r.start(t, ts)
	if err != nil || code != exitcode.OK {
		t.Fatalf("err %v, exit %d", err, code)
	}
	r.inOrder(t, 500)
	for _, c := range res.Chunks {
		if c.State != verify.StateVerified {
			t.Fatalf("%s is %s", c.ChunkID, c.State)
		}
	}
	if res.MaxHeld > pipeline.WindowPerJob*3 {
		t.Fatalf("held %d > W", res.MaxHeld)
	}
	if synth.OpenNow() != 0 {
		t.Fatalf("%d sessions left open", synth.OpenNow())
	}
}

// accept: RUN-02#2
func TestRunStalledFirstChunkBoundsHeld(t *testing.T) {
	defer goleak.VerifyNone(t)
	const jobs, n = 3, 100
	w := pipeline.WindowPerJob * jobs
	ts := tasks(n)
	synth := rtfake.NewScriptedSynthesizer()
	var started atomic.Int32
	var beyond atomic.Int32 // renders of chunks at or past the window while chunk 1 is unresolved
	var resolved atomic.Bool
	allIn := make(chan struct{})
	var once sync.Once
	synth.Hook = func(ctx context.Context, c rtfake.RenderCall) error {
		var idx int
		_, _ = fmt.Sscanf(c.Request.ChunkID, "c%d", &idx)
		if idx-1 >= w && !resolved.Load() {
			beyond.Add(1)
		}
		if c.Request.ChunkID != "c0001" {
			if started.Add(1) == int32(w-1) {
				once.Do(func() { close(allIn) })
			}
			return nil
		}
		// Chunk 1 stalls until the rest of the window has rendered, then times out.
		select {
		case <-allIn:
		case <-ctx.Done():
			return ctx.Err()
		}
		if c.Attempt == openai.MaxAttempts(openai.ClassNetwork) {
			resolved.Store(true)
		}
		return &openai.Error{Class: openai.ClassNetwork, Message: "realtime: no event within the per-response idle timeout"}
	}
	r := newRun(synth, newEcho(ts), jobs)
	res, code, err := r.start(t, ts)
	if code != exitcode.Failure || err == nil {
		t.Fatalf("err %v, exit %d; want chunk 1's failure, exit 7", err, code)
	}
	if !strings.Contains(err.Error(), "chunk c0001:") {
		t.Fatalf("error %q does not report chunk c0001", err)
	}
	if res.MaxHeld > w || beyond.Load() != 0 {
		t.Fatalf("held %d (W %d), %d renders past the window", res.MaxHeld, w, beyond.Load())
	}
	if len(res.Chunks) != 0 {
		t.Fatalf("%d chunks delivered past a failed chunk 1", len(res.Chunks))
	}
}

// accept: RUN-02#3
func TestRunAllRejectTerminatesExit5(t *testing.T) {
	defer goleak.VerifyNone(t)
	ts := tasks(5)
	tr := sttfake.NewScriptedTranscriber()
	tr.Default = sttfake.Text("something else entirely was heard here")
	r := newRun(rtfake.NewScriptedSynthesizer(), tr, 1)
	r.cfg.VerifyJobs = 1
	res, code, err := r.start(t, ts)
	if err != nil || code != exitcode.VerifyFailed {
		t.Fatalf("err %v, exit %d; want exit 5", err, code)
	}
	for _, c := range res.Chunks {
		if c.State != verify.StateUnverified || len(c.Attempts) != verify.DefaultMaxAttempts {
			t.Fatalf("%s: %s after %d attempts", c.ChunkID, c.State, len(c.Attempts))
		}
	}
}

// accept: RUN-02#4
func TestRunJobsLimits(t *testing.T) {
	defer goleak.VerifyNone(t)
	synth := rtfake.NewScriptedSynthesizer()
	ts := tasks(60, "alloy", "ash", "ballad", "cedar", "coral", "echo", "marin", "sage")
	r := newRun(synth, newEcho(ts), 7)
	_, code, err := r.start(t, ts)
	if code != exitcode.Usage || err == nil {
		t.Fatalf("--jobs 7: err %v, exit %d; want 2", err, code)
	}
	if len(synth.Opens()) != 0 || len(synth.Calls()) != 0 {
		t.Fatalf("--jobs 7 reached the synthesizer")
	}
	r = newRun(synth, newEcho(ts), 6)
	if _, code, err := r.start(t, ts); err != nil || code != exitcode.OK {
		t.Fatalf("--jobs 6: err %v, exit %d", err, code)
	}
	if synth.MaxOpen() > 6 {
		t.Fatalf("%d sessions open at once with --jobs 6", synth.MaxOpen())
	}
}

// accept: SPK-06#1
func TestRunTwoVoicesThreeJobsRightVoice(t *testing.T) {
	defer goleak.VerifyNone(t)
	ts := tasks(60, "marin", "cedar")
	synth := rtfake.NewScriptedSynthesizer()
	r := newRun(synth, newEcho(ts), 3)
	if _, code, err := r.start(t, ts); err != nil || code != exitcode.OK {
		t.Fatalf("err %v, exit %d", err, code)
	}
	if synth.MaxOpen() > 3 {
		t.Fatalf("%d sessions open at once", synth.MaxOpen())
	}
	want := map[string]string{}
	for _, tk := range ts {
		want[tk.Chunk.ID] = tk.Spec.Voice
	}
	for _, c := range synth.Calls() {
		if c.Spec.Voice != want[c.Request.ChunkID] {
			t.Fatalf("%s rendered on a %s session, want %s", c.Request.ChunkID, c.Spec.Voice, want[c.Request.ChunkID])
		}
	}
}

// accept: SPK-06#2
func TestRunOneJobAlternatingVoices(t *testing.T) {
	defer goleak.VerifyNone(t)
	ts := tasks(8, "marin", "cedar")
	synth := rtfake.NewScriptedSynthesizer()
	r := newRun(synth, newEcho(ts), 1)
	if _, code, err := r.start(t, ts); err != nil || code != exitcode.OK {
		t.Fatalf("err %v, exit %d", err, code)
	}
	calls := synth.Calls()
	if len(synth.Opens()) != 8 || synth.MaxOpen() != 1 || synth.OpenNow() != 0 {
		t.Fatalf("%d opens, max open %d, open now %d; want a close and an open at each of 8 switches", len(synth.Opens()), synth.MaxOpen(), synth.OpenNow())
	}
	for i, c := range calls {
		if c.Session != i+1 {
			t.Fatalf("call %d on session %d", i, c.Session)
		}
	}
}

func deniedSpeech() error {
	return &openai.Error{Class: openai.ClassRouteDenied, Status: 403, Code: "missing_scope", Route: openai.RouteSpeechMint}
}

// accept: NET-06#3
func TestRunSpeechDeniedStopsWithAtMostOneMoreRequest(t *testing.T) {
	defer goleak.VerifyNone(t)
	ts := tasks(10)
	synth := rtfake.NewScriptedSynthesizer()
	var denied atomic.Bool
	var after atomic.Int32
	synth.Hook = func(_ context.Context, c rtfake.RenderCall) error {
		if denied.Load() {
			after.Add(1)
		}
		if c.Request.ChunkID == "c0002" {
			denied.Store(true)
			return deniedSpeech()
		}
		return nil
	}
	r := newRun(synth, newEcho(ts), 2)
	_, code, err := r.start(t, ts)
	if code != exitcode.RouteDenied {
		t.Fatalf("err %v, exit %d; want 4", err, code)
	}
	if n := after.Load(); n > 1 {
		t.Fatalf("%d requests after the denial, want at most 1", n)
	}
	if synth.Attempts("c0002") != 1 {
		t.Fatalf("a denied route was retried")
	}
}

// accept: NET-06#1
func TestRunHTTPSTTDeniedFallsBack(t *testing.T) {
	defer goleak.VerifyNone(t)
	ts := tasks(10)
	primary := sttfake.NewScriptedTranscriber()
	primary.Default = sttfake.Fail(&openai.Error{Class: openai.ClassRouteDenied, Status: 401, Code: "missing_scope", Route: openai.RouteHTTPSTT})
	fallback := newEcho(ts)
	var switches atomic.Int32
	chain := &stt.Chain{Primary: primary, Fallback: fallback, OnSwitch: func(error) { switches.Add(1) }}
	r := newRun(rtfake.NewScriptedSynthesizer(), chain, 2)
	if _, code, err := r.start(t, ts); err != nil || code != exitcode.OK {
		t.Fatalf("err %v, exit %d; want 0", err, code)
	}
	if !chain.Switched() || switches.Load() != 1 {
		t.Fatalf("switched %v, %d switch records; want one", chain.Switched(), switches.Load())
	}
	if got := fallback.chunks(); len(got) != 10 {
		t.Fatalf("the fallback verified %v, want all 10 chunks", got)
	}
}

func TestRunEveryVerifierDeniedExit4(t *testing.T) {
	defer goleak.VerifyNone(t)
	ts := tasks(4)
	deny := func(route openai.Route) *sttfake.ScriptedTranscriber {
		s := sttfake.NewScriptedTranscriber()
		s.Default = sttfake.Fail(&openai.Error{Class: openai.ClassRouteDenied, Status: 401, Code: "missing_scope", Route: route})
		return s
	}
	chain := &stt.Chain{Primary: deny(openai.RouteHTTPSTT), Fallback: deny(openai.RouteRealtimeSTT)}
	r := newRun(rtfake.NewScriptedSynthesizer(), chain, 2)
	if _, code, err := r.start(t, ts); code != exitcode.RouteDenied {
		t.Fatalf("err %v, exit %d; want 4", err, code)
	}
}

// accept: NET-07#2
func TestRunMidResponseDropRetriedOnFreshSession(t *testing.T) {
	defer goleak.VerifyNone(t)
	ts := tasks(3)
	synth := rtfake.NewScriptedSynthesizer()
	synth.On("c0002", rtfake.Fail(openai.ConnClosed(1006, "", errors.New("connection reset mid-response"))))
	r := newRun(synth, newEcho(ts), 1)
	if _, code, err := r.start(t, ts); err != nil || code != exitcode.OK {
		t.Fatalf("err %v, exit %d", err, code)
	}
	var sessions []int
	for _, c := range synth.Calls() {
		if c.Request.ChunkID == "c0002" {
			sessions = append(sessions, c.Session)
		}
	}
	if len(sessions) != 2 || sessions[0] == sessions[1] {
		t.Fatalf("c0002 rendered on sessions %v, want a retry on a fresh one", sessions)
	}
}

// accept: CLI-03#1
func TestRunRateLimitedExhaustedExit6(t *testing.T) {
	defer goleak.VerifyNone(t)
	ts := tasks(3)
	synth := rtfake.NewScriptedSynthesizer()
	synth.Default = rtfake.Fail(&openai.Error{Class: openai.ClassRateLimited, Status: 429})
	r := newRun(synth, newEcho(ts), 1)
	_, code, err := r.start(t, ts)
	if code != exitcode.RateLimited {
		t.Fatalf("err %v, exit %d; want 6", err, code)
	}
	if n := synth.Attempts("c0001"); n != openai.MaxAttempts(openai.ClassRateLimited) {
		t.Fatalf("%d attempts, want 5", n)
	}
}

// accept: CLI-03#1
func TestRunStopDrainsAndExits130(t *testing.T) {
	defer goleak.VerifyNone(t)
	ts := tasks(50)
	synth := rtfake.NewScriptedSynthesizer()
	stop := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	synth.Hook = func(ctx context.Context, c rtfake.RenderCall) error {
		if c.Request.ChunkID == "c0005" {
			once.Do(func() { close(stop) })
			select { // in flight when the interrupt arrives; it is allowed to finish
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	r := newRun(synth, newEcho(ts), 2)
	r.cfg.Stop = stop
	r.cfg.After = func(time.Duration) <-chan time.Time {
		close(release) // the drain timer starts: let the in-flight chunk finish
		return make(chan time.Time)
	}
	res, code, err := r.start(t, ts)
	if code != exitcode.Interrupted || !res.Interrupted {
		t.Fatalf("err %v, exit %d; want 130", err, code)
	}
	if n := len(synth.Calls()); n >= 50 {
		t.Fatalf("all %d chunks rendered after the interrupt", n)
	}
	if synth.Attempts("c0005") != 1 {
		t.Fatalf("the in-flight chunk did not finish")
	}
}

func TestRunDrainTimeoutCancelsInFlight(t *testing.T) {
	defer goleak.VerifyNone(t)
	ts := tasks(4)
	synth := rtfake.NewScriptedSynthesizer()
	stop := make(chan struct{})
	synth.Hook = func(ctx context.Context, c rtfake.RenderCall) error {
		if c.Request.ChunkID == "c0002" {
			close(stop)
			<-ctx.Done() // never finishes on its own
			return ctx.Err()
		}
		return nil
	}
	r := newRun(synth, newEcho(ts), 1)
	r.cfg.Stop = stop
	fire := make(chan time.Time, 1)
	r.cfg.After = func(d time.Duration) <-chan time.Time {
		if d != pipeline.DrainTimeout {
			t.Errorf("drain %v, want 15s", d)
		}
		fire <- time.Time{}
		return fire
	}
	if _, code, err := r.start(t, ts); code != exitcode.Interrupted {
		t.Fatalf("err %v, exit %d; want 130", err, code)
	}
}

func TestPauseOnExhaustedClosesGate(t *testing.T) {
	now := time.Unix(0, 0)
	var slept time.Duration
	g := openai.NewGate(func() time.Time { return now }, func(_ context.Context, d time.Duration) error {
		slept += d
		now = now.Add(d)
		return nil
	})
	pipeline.PauseOnExhausted(g)(realtime.RateLimitsUpdated{Limits: []realtime.RateLimit{
		{Name: "requests", Remaining: 3, ResetSeconds: 9},
		{Name: "tokens", Remaining: 0, ResetSeconds: 2.5},
	}})
	if err := g.Wait(context.Background()); err != nil || slept != 2500*time.Millisecond {
		t.Fatalf("slept %v (%v), want 2.5s", slept, err)
	}
}

func TestExitCodeSeverityWhateverTheOrder(t *testing.T) {
	errs := []error{
		&openai.Error{Class: openai.ClassAuthExpired},
		&openai.Error{Class: openai.ClassRouteDenied},
		&openai.Error{Class: openai.ClassNetwork},
	}
	for range 6 {
		errs = append(errs[1:], errs[0])
		code, write := pipeline.ExitCode(pipeline.Result{Chunks: []verify.ChunkResult{{State: verify.StateUnverified}}}, &pipeline.RunError{Errs: errs}, verify.OutputPolicy{})
		if code != exitcode.Auth || write {
			t.Fatalf("exit %d write %v, want 3 and no output", code, write)
		}
	}
}

func TestWatchSignalsTwoStages(t *testing.T) {
	defer goleak.VerifyNone(t)
	sigs := make(chan os.Signal, 2)
	timer := make(chan time.Time)
	in, ctx, release := pipeline.WatchSignals(context.Background(), sigs, 0, func(d time.Duration) <-chan time.Time {
		if d != pipeline.DrainTimeout {
			t.Errorf("drain %v", d)
		}
		return timer
	})
	defer release()
	if in.Interrupted() {
		t.Fatal("interrupted before any signal")
	}
	sigs <- os.Interrupt
	<-in.Stop()
	if ctx.Err() != nil {
		t.Fatal("the first signal cancelled in-flight work")
	}
	sigs <- os.Interrupt // the second signal aborts
	<-ctx.Done()

	sigs2, timer2 := make(chan os.Signal, 1), make(chan time.Time)
	in2, ctx2, release2 := pipeline.WatchSignals(context.Background(), sigs2, time.Second, func(time.Duration) <-chan time.Time { return timer2 })
	defer release2()
	sigs2 <- os.Interrupt
	<-in2.Stop()
	timer2 <- time.Time{} // the drain timeout cancels
	<-ctx2.Done()
}
