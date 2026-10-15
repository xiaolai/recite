package realtime_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/realtime/fake"
)

// fakeClock is a settable clock.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func voiceSpec(voice string) realtime.SessionSpec {
	return realtime.SessionSpec{Model: "gpt-realtime-2.1", Voice: voice, Speed: 1, Instructions: "x", Effort: "low"}
}

func newPool(t *testing.T, synth realtime.Synthesizer, max int, clock *fakeClock) *realtime.Pool {
	t.Helper()
	cfg := realtime.PoolConfig{Synth: synth, Max: max}
	if clock != nil {
		cfg.Now = clock.Now
	}
	p, err := realtime.NewPool(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func poolRender(t *testing.T, p *realtime.Pool, spec realtime.SessionSpec, chunk string) {
	t.Helper()
	l, err := p.Acquire(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Render(context.Background(), realtime.RenderRequest{ChunkID: chunk, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	l.Release()
}

func TestPoolReusesIdleSessionOfSameKey(t *testing.T) {
	synth := fake.NewScriptedSynthesizer()
	p := newPool(t, synth, 3, nil)
	for i := range 5 {
		poolRender(t, p, voiceSpec("marin"), "c"+string(rune('1'+i)))
	}
	if n := len(synth.Opens()); n != 1 {
		t.Fatalf("%d opens, want 1", n)
	}
}

// accept: SPK-06#3
func TestPoolFourKeysWithThreeSlotsReplacesLRUIdle(t *testing.T) {
	synth := fake.NewScriptedSynthesizer()
	p := newPool(t, synth, 3, nil)
	for i, v := range []string{"alloy", "ash", "cedar", "marin"} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			poolRender(t, p, voiceSpec(v), "c"+string(rune('1'+i)))
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("key %s waited on an idle session", v)
		}
	}
	opens := synth.Opens()
	if len(opens) != 4 || synth.OpenNow() != 3 || synth.MaxOpen() != 3 {
		t.Fatalf("%d opens, %d open now, max %d; want 4, 3, 3", len(opens), synth.OpenNow(), synth.MaxOpen())
	}
	// The least recently used key (alloy) was the one closed: ash renders without opening.
	poolRender(t, p, voiceSpec("ash"), "c5")
	if len(synth.Opens()) != 4 {
		t.Fatalf("ash was closed instead of the LRU alloy")
	}
	poolRender(t, p, voiceSpec("alloy"), "c6")
	if len(synth.Opens()) != 5 {
		t.Fatalf("alloy should have been reopened")
	}
}

func TestPoolExhaustedWhenAllBusy(t *testing.T) {
	synth := fake.NewScriptedSynthesizer()
	p := newPool(t, synth, 1, nil)
	l, err := p.Acquire(context.Background(), voiceSpec("marin"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if _, err := p.Acquire(context.Background(), voiceSpec("ash")); !errors.Is(err, realtime.ErrPoolExhausted) {
		t.Fatalf("err %v, want ErrPoolExhausted", err)
	}
}

// accept: NET-07#1
func TestPoolRecyclesAtAgeWithoutFailingInFlight(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	synth := fake.NewScriptedSynthesizer()
	p := newPool(t, synth, 1, clock)
	// The clock passes the 50-minute limit while a chunk is rendering.
	synth.Hook = func(_ context.Context, c fake.RenderCall) error {
		if c.Request.ChunkID == "c2" {
			clock.Advance(realtime.DefaultSessionMaxAge + time.Minute)
		}
		return nil
	}
	poolRender(t, p, voiceSpec("marin"), "c1")
	poolRender(t, p, voiceSpec("marin"), "c2") // must not fail
	if synth.OpenNow() != 0 {
		t.Fatalf("the aged session is still open after its in-flight chunk")
	}
	poolRender(t, p, voiceSpec("marin"), "c3")
	calls := synth.Calls()
	if len(synth.Opens()) != 2 || calls[1].Session != 1 || calls[2].Session != 2 {
		t.Fatalf("opens %d, sessions %d %d; want a new session for c3", len(synth.Opens()), calls[1].Session, calls[2].Session)
	}
}

func TestPoolRecyclesAfterChunkLimit(t *testing.T) {
	synth := fake.NewScriptedSynthesizer()
	p, err := realtime.NewPool(realtime.PoolConfig{Synth: synth, Max: 1, MaxChunks: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	for _, c := range []string{"c1", "c2", "c3", "c4", "c5"} {
		poolRender(t, p, voiceSpec("marin"), c)
	}
	if n := len(synth.Opens()); n != 3 {
		t.Fatalf("%d opens, want 3", n)
	}
}

func TestPoolDiscardsSessionAfterServerError(t *testing.T) {
	synth := fake.NewScriptedSynthesizer()
	synth.On("c1", fake.Fail(&openai.Error{Class: openai.ClassServer}))
	synth.On("c2", fake.Fail(&openai.Error{Class: openai.ClassRateLimited}))
	p := newPool(t, synth, 1, nil)
	for _, c := range []string{"c1", "c2", "c3"} {
		l, err := p.Acquire(context.Background(), voiceSpec("marin"))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = l.Render(context.Background(), realtime.RenderRequest{ChunkID: c, Text: "hi"})
		l.Release()
	}
	calls := synth.Calls()
	// c1's server error closes session 1; c2's rate limit keeps session 2 for c3.
	if calls[0].Session != 1 || calls[1].Session != 2 || calls[2].Session != 2 {
		t.Fatalf("sessions %d %d %d, want 1 2 2", calls[0].Session, calls[1].Session, calls[2].Session)
	}
}

// A fatal error ends the session whatever its class: session_expired as invalid-request too.
func TestPoolDiscardsSessionAfterFatalError(t *testing.T) {
	synth := fake.NewScriptedSynthesizer()
	synth.On("c1", fake.Fail(&openai.Error{Class: openai.ClassInvalidRequest, Code: "session_expired", Fatal: true}))
	p := newPool(t, synth, 1, nil)
	for _, c := range []string{"c1", "c2"} {
		l, err := p.Acquire(context.Background(), voiceSpec("marin"))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = l.Render(context.Background(), realtime.RenderRequest{ChunkID: c, Text: "hi"})
		l.Release()
	}
	if calls := synth.Calls(); calls[0].Session != 1 || calls[1].Session != 2 {
		t.Fatalf("sessions %d %d, want 1 2", calls[0].Session, calls[1].Session)
	}
}

// ctlSession is a session whose Close can be held open and that can report itself unusable.
type ctlSession struct {
	id       int
	err      error
	unusable bool
	hold     chan struct{} // Close waits on it when non-nil
	entered  chan struct{} // closed when Close starts, if non-nil
	closed   chan struct{}
	onClose  func() // the synth's count, updated before Close returns
}

func (s *ctlSession) Render(context.Context, realtime.RenderRequest) (realtime.Take, error) {
	return realtime.Take{}, s.err
}
func (s *ctlSession) Age() time.Duration { return 0 }
func (s *ctlSession) Unusable() bool     { return s.unusable }
func (s *ctlSession) Close() error {
	if s.entered != nil {
		close(s.entered)
	}
	if s.hold != nil {
		<-s.hold
	}
	close(s.closed)
	if s.onClose != nil {
		s.onClose()
	}
	return nil
}

// ctlSynth opens ctlSessions and counts the connections open at once.
type ctlSynth struct {
	mu       sync.Mutex
	next     func(id int) *ctlSession
	opened   []*ctlSession
	open     int
	maxOpen  int
	lastSpec realtime.SessionSpec
}

func (y *ctlSynth) Open(_ context.Context, spec realtime.SessionSpec) (realtime.SpeechSession, error) {
	y.mu.Lock()
	defer y.mu.Unlock()
	s := &ctlSession{id: len(y.opened) + 1, closed: make(chan struct{})}
	if y.next != nil {
		s = y.next(len(y.opened) + 1)
		s.closed = make(chan struct{})
	}
	y.opened = append(y.opened, s)
	y.open++
	y.maxOpen = max(y.maxOpen, y.open)
	y.lastSpec = spec
	s.onClose = func() {
		y.mu.Lock()
		y.open--
		y.mu.Unlock()
	}
	return s, nil
}

func (y *ctlSynth) count() int {
	y.mu.Lock()
	defer y.mu.Unlock()
	return len(y.opened)
}

// A session broken mid-response is discarded even when its error is a rate limit (audit: a
// rate-limited session kept by the pool replayed its stored error to every retry).
func TestPoolDiscardsUnusableSessionWhateverTheClass(t *testing.T) {
	synth := &ctlSynth{next: func(id int) *ctlSession {
		if id == 1 {
			return &ctlSession{id: id, err: &openai.Error{Class: openai.ClassRateLimited}, unusable: true}
		}
		return &ctlSession{id: id}
	}}
	p := newPool(t, synth, 1, nil)
	for range 2 {
		l, err := p.Acquire(context.Background(), voiceSpec("marin"))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = l.Render(context.Background(), realtime.RenderRequest{ChunkID: "c1", Text: "hi"})
		l.Release()
	}
	if n := synth.count(); n != 2 {
		t.Fatalf("%d sessions opened, want 2: the unusable session was reused", n)
	}
}

// A session being closed is never lent again, and it counts against Max until its Close
// returns, so no more than Max connections are ever open.
func TestPoolNeverLendsOrOverfillsWhileClosing(t *testing.T) {
	hold, entered := make(chan struct{}), make(chan struct{})
	synth := &ctlSynth{next: func(id int) *ctlSession {
		if id == 1 {
			return &ctlSession{id: id, err: &openai.Error{Class: openai.ClassServer}, hold: hold, entered: entered}
		}
		return &ctlSession{id: id}
	}}
	p := newPool(t, synth, 1, nil)
	l, err := p.Acquire(context.Background(), voiceSpec("marin"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = l.Render(context.Background(), realtime.RenderRequest{ChunkID: "c1", Text: "hi"})
	released := make(chan struct{})
	go func() { l.Release(); close(released) }() // blocks in Close
	<-entered
	got := make(chan *realtime.Lease, 1)
	go func() {
		l2, err := p.Acquire(context.Background(), voiceSpec("marin"))
		if err != nil {
			t.Error(err)
		}
		got <- l2
	}()
	select {
	case <-got:
		t.Fatal("Acquire returned while the only slot's session was still closing")
	case <-time.After(200 * time.Millisecond):
	}
	close(hold)
	<-released
	l2 := <-got
	if _, err := l2.Render(context.Background(), realtime.RenderRequest{ChunkID: "c2", Text: "hi"}); err != nil {
		t.Fatalf("the new lease rendered on a closed session: %v", err)
	}
	l2.Release()
	synth.mu.Lock()
	defer synth.mu.Unlock()
	if synth.maxOpen > 1 || len(synth.opened) != 2 {
		t.Fatalf("max %d open at once over %d opens, want 1 over 2", synth.maxOpen, len(synth.opened))
	}
}

// Expired sessions closing in one Acquire still count against Max for a concurrent one.
func TestPoolCountsClosingExpiredSessions(t *testing.T) {
	hold := make(chan struct{})
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	synth := &ctlSynth{next: func(id int) *ctlSession {
		if id <= 2 {
			return &ctlSession{id: id, hold: hold}
		}
		return &ctlSession{id: id}
	}}
	p := newPool(t, synth, 2, clock)
	a, _ := p.Acquire(context.Background(), voiceSpec("marin"))
	b, _ := p.Acquire(context.Background(), voiceSpec("cedar"))
	a.Release()
	b.Release()
	clock.Advance(realtime.DefaultSessionMaxAge)
	done := make(chan struct{}, 2)
	for _, v := range []string{"marin", "cedar"} {
		go func() {
			l, err := p.Acquire(context.Background(), voiceSpec(v))
			if err != nil {
				t.Error(err)
			} else {
				l.Release()
			}
			done <- struct{}{}
		}()
	}
	time.Sleep(200 * time.Millisecond)
	close(hold)
	<-done
	<-done
	synth.mu.Lock()
	defer synth.mu.Unlock()
	if synth.maxOpen > 2 {
		t.Fatalf("%d connections open at once, Max 2", synth.maxOpen)
	}
}

// A session is reused only for the instructions it was opened with: instructions are sent with
// each response from the session's spec.
func TestPoolDoesNotReuseAcrossInstructions(t *testing.T) {
	synth := &ctlSynth{}
	p := newPool(t, synth, 2, nil)
	for _, instr := range []string{"a", "b"} {
		spec := voiceSpec("marin")
		spec.Instructions = instr
		l, err := p.Acquire(context.Background(), spec)
		if err != nil {
			t.Fatal(err)
		}
		l.Release()
	}
	if n := synth.count(); n != 2 {
		t.Fatalf("%d sessions opened, want 2", n)
	}
}
