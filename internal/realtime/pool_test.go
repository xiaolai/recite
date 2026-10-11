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
