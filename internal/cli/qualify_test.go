//go:build qualify

package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/livebudget"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/verify"
)

// m2Text is the M2 exit text: Abraham Lincoln, Second Inaugural Address, 4 March 1865, public
// domain (published 1865, and a work of the US government). Source: the Avalon Project, Yale
// Law School (https://avalon.law.yale.edu/19th_century/lincoln2.asp). Abridged to 596 words:
// the "Woe unto the world" passage is left out and one dash is a comma; about 4 minutes of
// audio (`recite check` estimates 239 s in 16 chunks).
const m2Text = "../../testdata/live/lincoln-second-inaugural.txt"

// liveBudget is the per-invocation cap of `make qualify` (implementation plan §1.4).
const liveBudget = 30 * time.Minute

// m2Run is one in-process `recite say` over the real service, observed through RunPipeline.
type m2Run struct {
	code    int
	stderr  string
	out     string
	results []verify.ChunkResult
	tasks   int
}

// sayM2 renders m2Text with --jobs 3 --max-attempts 2. With interrupt set, one SIGINT is sent
// once half the chunks have been delivered.
func sayM2(t *testing.T, dir string, interrupt bool) m2Run {
	t.Helper()
	if os.Getenv("RECITE_LIVE") == "" {
		t.Skip("set RECITE_LIVE=1 to run against the real service")
	}
	raw, err := os.ReadFile(m2Text)
	if err != nil {
		t.Fatal(err)
	}
	if err := livebudget.Preflight(realtime.EstimateDuration(string(raw), 1), 2, liveBudget); err != nil {
		t.Fatal(err)
	}
	budget := livebudget.New(liveBudget)
	run := budget.Pipeline(pipeline.Run)
	out := filepath.Join(dir, "m2.wav")
	var stderr bytes.Buffer
	c := New(io.Discard, io.MultiWriter(&stderr, os.Stderr))
	sigs := make(chan os.Signal, 1)
	c.Signals = func() (<-chan os.Signal, func()) { return sigs, func() {} }
	var r m2Run
	var mu sync.Mutex
	c.RunPipeline = func(ctx context.Context, cfg pipeline.Config, tasks []pipeline.Task) (pipeline.Result, error) {
		r.tasks = len(tasks)
		interruptAt := 0
		if interrupt {
			interruptAt = len(tasks) / 2
		}
		deliver := cfg.Deliver
		delivered := 0
		cfg.Deliver = func(i int, cr verify.ChunkResult) error {
			mu.Lock()
			delivered++
			if delivered == interruptAt {
				sigs <- os.Interrupt
			}
			mu.Unlock()
			return deliver(i, cr)
		}
		res, err := run(ctx, cfg, tasks)
		r.results = res.Chunks
		return res, err
	}
	r.code = c.Run([]string{"say", "-f", m2Text, "--jobs", "3", "--max-attempts", "2", "-o", out, "-v"})
	r.stderr, r.out = stderr.String(), out
	t.Logf("audio received: %v of the %v budget", budget.Used().Round(time.Second), liveBudget)
	if budget.Exceeded() {
		t.Fatalf("the run reached the audio budget and is incomplete (exit %d)", r.code)
	}
	return r
}

func liveDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(os.TempDir(), "recite-live", time.Now().UTC().Format("20060102-150405"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Logf("artifacts in %s", dir)
	return dir
}

// checkEveryChunk asserts the run finished with every chunk verified or reported.
func checkEveryChunk(t *testing.T, r m2Run) {
	t.Helper()
	if r.code != exitcode.OK && r.code != exitcode.VerifyFailed {
		t.Fatalf("exit %d, want 0 or 5\n%s", r.code, r.stderr)
	}
	if len(r.results) != r.tasks || r.tasks < 10 {
		t.Fatalf("%d of %d chunks delivered", len(r.results), r.tasks)
	}
	for _, cr := range r.results {
		if cr.State != verify.StateVerified && !strings.Contains(r.stderr, "chunk "+cr.ChunkID) {
			t.Errorf("chunk %s is %s and not reported", cr.ChunkID, cr.State)
		}
	}
	if fi, err := os.Stat(r.out); err != nil || fi.Size() < 44+48000*120 {
		t.Fatalf("output %s: %v (want about 4 minutes of audio)", r.out, err)
	}
}

// The M2 exit: the 600-word text renders, verifies and assembles live with --jobs 3
// --max-attempts 2; every chunk verifies or is reported.
// accept: CLI-02#1
func TestQualifyM2Render(t *testing.T) {
	checkEveryChunk(t, sayM2(t, liveDir(t), false))
}

// The M2 exit, second part: a run interrupted at about 50% exits 130 and writes nothing; a
// re-run then completes (without the M3 cache it re-renders everything).
func TestQualifyM2InterruptThenRerun(t *testing.T) {
	dir := liveDir(t)
	r := sayM2(t, dir, true)
	if r.code != exitcode.Interrupted {
		t.Fatalf("interrupted run: exit %d, want 130\n%s", r.code, r.stderr)
	}
	if _, err := os.Stat(r.out); !os.IsNotExist(err) {
		t.Fatalf("interrupted run wrote %s: %v", r.out, err)
	}
	if n := len(r.results); n == 0 || n >= r.tasks {
		t.Fatalf("interrupted after %d of %d chunks, want part way", n, r.tasks)
	}
	t.Logf("interrupted after %d of %d chunks", len(r.results), r.tasks)
	checkEveryChunk(t, sayM2(t, dir, false))
}
