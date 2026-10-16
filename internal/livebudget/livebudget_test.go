package livebudget_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/livebudget"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	rtfake "github.com/xiaolai/recite/internal/realtime/fake"
	"github.com/xiaolai/recite/internal/verify"
)

const take = 10 * time.Second

// tasks are n chunks whose every take lasts take.
func tasks(n int) []pipeline.Task {
	ts := make([]pipeline.Task, n)
	for i := range ts {
		ts[i] = pipeline.Task{Chunk: verify.Chunk{ID: fmt.Sprintf("c%04d", i+1), Spoken: "Some words to say.", Estimate: take}}
	}
	return ts
}

func config(synth realtime.Synthesizer) pipeline.Config {
	return pipeline.Config{
		Jobs: 2, Verify: verify.Options{Mode: verify.ModeOff, MaxAttempts: 1}, Synth: synth,
		Deliver: func(int, verify.ChunkResult) error { return nil },
	}
}

func fakeSynth() *rtfake.ScriptedSynthesizer {
	s := rtfake.NewScriptedSynthesizer()
	s.Default = rtfake.Pass(take)
	return s
}

func TestPreflight(t *testing.T) {
	// 5 min of estimated audio × 2.2 × 2 attempts = 22 min.
	if err := livebudget.Preflight([]verify.Chunk{{Estimate: 5 * time.Minute}}, 2, 30*time.Minute); err != nil {
		t.Fatalf("22 min under a 30 min budget: %v", err)
	}
	err := livebudget.Preflight([]verify.Chunk{{Estimate: 5 * time.Minute}}, 3, 30*time.Minute)
	if !errors.Is(err, livebudget.ErrOverBudget) {
		t.Fatalf("33 min over a 30 min budget: %v", err)
	}
	// Short chunks are bounded by the guard's slack, not its ratio: 10 chunks of 0.5s may each run
	// 1.5s, so 15s an attempt, where 2.2 × the 5s total would claim 11s. A budget of 12s is short.
	short := make([]verify.Chunk, 10)
	for i := range short {
		short[i] = verify.Chunk{Spoken: "Six", Estimate: 500 * time.Millisecond}
	}
	if err := livebudget.Preflight(short, 1, 12*time.Second); !errors.Is(err, livebudget.ErrOverBudget) {
		t.Fatalf("15s of worst case under a 12s budget passed: %v", err)
	}
	if err := livebudget.Preflight(short, 1, 15*time.Second); err != nil {
		t.Fatalf("15s of worst case under a 15s budget: %v", err)
	}
}

// The in-run check of plan §1.4: once the audio received reaches the budget, no new render
// starts and the run reports itself incomplete; the overshoot is at most one take per job.
func TestRunStopsAtTheBudget(t *testing.T) {
	synth := fakeSynth()
	b := livebudget.New(35 * time.Second)
	_, err := b.Pipeline(pipeline.Run)(context.Background(), config(synth), tasks(20))
	if !errors.Is(err, livebudget.ErrExceeded) {
		t.Fatalf("run error %v, want ErrExceeded", err)
	}
	if !b.Exceeded() {
		t.Fatal("Exceeded is false after the run stopped at the budget")
	}
	jobs := 2
	if used := b.Used(); used < 35*time.Second || used > 35*time.Second+time.Duration(jobs)*take {
		t.Fatalf("used %v, want between the budget and the budget plus %d takes", used, jobs)
	}
	if n := len(synth.Calls()); n >= 20 {
		t.Fatalf("%d renders: the run went on past the budget", n)
	}
}

func TestRunUnderTheBudgetIsUntouched(t *testing.T) {
	b := livebudget.New(time.Hour)
	res, err := b.Pipeline(pipeline.Run)(context.Background(), config(fakeSynth()), tasks(5))
	if err != nil || len(res.Chunks) != 5 {
		t.Fatalf("%d chunks, err %v", len(res.Chunks), err)
	}
	if b.Exceeded() || b.Used() != 5*take {
		t.Fatalf("exceeded %v, used %v, want false and %v", b.Exceeded(), b.Used(), 5*take)
	}
}
