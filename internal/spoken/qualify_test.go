//go:build qualify

package spoken_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/guard"
	"github.com/xiaolai/recite/internal/livebudget"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/stt"
	"github.com/xiaolai/recite/internal/text"
	"github.com/xiaolai/recite/internal/verify"
)

// probeCorpus is the 21 items of dev-docs/probe_textnorm.py, Chinese included.
var probeCorpus = []string{
	"The deal was worth $3.5M, or about €3.2 million.",
	"The meeting is on 2026-10-11 at 3:45pm.",
	"Take 1/2 cup on 1/2.",
	"Growth hit 50% (up from 12.5%).",
	"Read pages 3–5 and chapters 10-12.",
	"Upgrade to v1.2.3 before Friday.",
	"Write it in C++ or C#, not F#.",
	"Visit https://example.com/docs?id=7 for details.",
	"Mail ops@example.com today.",
	"Call +1 (415) 555-0134.",
	"NASA and the FBI met at the UN; the CEO used SQL.",
	"Dr. Smith lives on St. James St., No. 5.",
	"A → B, x ≥ 3, & 2 × 4 = 8.",
	"Henry VIII ruled in the 16th century; see Chapter IV.",
	"Great job 🎉👍 see you soon 😀",
	"This was proven [1] and later disputed.[2]",
	"Ths   is  ,,a  sentnce with typos!!!  and​ zero-width chars",
	"It weighs 5kg, runs at 120km/h, and costs 3¥.",
	"2026年10月11日下午3点45分，价格是¥1,280.50，增长了12.5%。",
	"请在iPhone 17上打开App Store，下载版本v2.0。",
	"第1/2季度，温度是-5℃到30℃。",
}

// Qualification (TXT-11#3): the 51 chunks, normalised, one take each: at least 49 verify on
// the first attempt and all 51 within the attempt limit (verify.DefaultMaxAttempts). Each item
// is its own chunk. It spends real usage: RECITE_LIVE=1 go test -tags qualify -run
// TestQualifyEnglishSpokenForm -count=1 ./internal/spoken, once per shard with RECITE_SHARD=k
//
// accept: TXT-11#3
func TestQualifyEnglishSpokenForm(t *testing.T) {
	raw, err := os.ReadFile("testdata/qualify-en.txt")
	if err != nil {
		t.Fatal(err)
	}
	corpus := append(append([]string(nil), probeCorpus...), strings.Split(strings.TrimRight(string(raw), "\n"), "\n")...)
	if len(corpus) != 51 {
		t.Fatalf("corpus has %d chunks, want 51", len(corpus))
	}
	set := realtime.DefaultSettings()
	spec, err := set.SessionSpec()
	if err != nil {
		t.Fatal(err)
	}
	opts := verify.DefaultOptions()
	tasks := make([]pipeline.Task, 0, len(corpus))
	for i, c := range corpus {
		g, err := guard.Run(c, guard.Options{})
		if err != nil {
			t.Fatalf("%q: guard: %v", c, err)
		}
		lang := text.Resolve(g.Text, text.ResolveOptions{})
		form, err := spoken.Normalize(g.Text, lang.Code, spoken.Options{})
		if err != nil {
			t.Fatalf("%q: %v", c, err)
		}
		requestLike := false
		for _, f := range g.Findings {
			requestLike = requestLike || f.Code == guard.Request
		}
		d := realtime.EstimateDuration(form.Spoken, set.Speed)
		tasks = append(tasks, pipeline.Task{
			Chunk: verify.Chunk{ID: fmt.Sprintf("c%04d", i+1), Spoken: form.Spoken, Language: lang, Estimate: d, RequestLike: requestLike},
			Spec:  spec,
		})
	}
	if os.Getenv("RECITE_LIVE") == "" {
		t.Skip("set RECITE_LIVE=1 to render and verify the corpus against the real service")
	}
	// The worst case of all 51 chunks is over one invocation's budget (plan §1.4), so the gate
	// runs in shards, one per invocation, each resumable; the last shard aggregates.
	attempts := opts.MaxAttempts + opts.RequestExtraAttempts
	shards := shard(tasks, attempts, liveBudget)
	k, err := strconv.Atoi(os.Getenv("RECITE_SHARD"))
	if err != nil || k < 1 || k > len(shards) {
		t.Fatalf("run each of the %d shards in its own invocation: RECITE_SHARD=1..%d", len(shards), len(shards))
	}
	part := shards[k-1]
	var est time.Duration
	for _, tk := range part {
		est += tk.Chunk.Estimate
	}
	if err := livebudget.Preflight(est, attempts, liveBudget); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	dir := filepath.Join(os.TempDir(), "recite-live", fmt.Sprintf("txt11-%x", sum[:6]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	st := auth.Probe(ctx, auth.ProbeConfig{Source: auth.SourceAuto, Getenv: os.Getenv})
	if st.Creds == nil {
		t.Fatalf("auth not ready: %s", st.State)
	}
	t.Cleanup(func() { _ = st.Close() })
	hc, err := openai.NewClient()
	if err != nil {
		t.Fatal(err)
	}
	minter := &realtime.Minter{Client: hc, BaseURL: openai.APIBase, Creds: st.Creds}
	gate := openai.NewGate(nil, nil)
	synth := &realtime.Synth{Minter: minter, Client: hc, BaseURL: openai.APIBase, OnRateLimits: pipeline.PauseOnExhausted(gate)}
	rt := &stt.Realtime{Minter: minter, Client: hc, BaseURL: openai.APIBase}
	t.Cleanup(func() { _ = rt.Close() })
	tr := &stt.Chain{Primary: &stt.HTTP{Client: hc, BaseURL: openai.APIBase, Creds: st.Creds}, Fallback: rt}

	budget := livebudget.New(liveBudget)
	res, err := budget.Pipeline(pipeline.Run)(ctx, pipeline.Config{
		Jobs: 3, Verify: opts, Synth: synth, Transcriber: tr, Gate: gate,
		Deliver: func(int, verify.ChunkResult) error { return nil },
	}, part)
	t.Logf("shard %d of %d: audio received %v of the %v budget", k, len(shards), budget.Used().Round(time.Second), liveBudget)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	outcomes := make([]outcome, len(res.Chunks))
	for i, r := range res.Chunks {
		o := outcome{ID: r.ChunkID, State: r.State, Final: r.Final, Score: r.Score, Spoken: part[i].Chunk.Spoken}
		for _, a := range r.Attempts {
			at := attempt{Score: a.Score}
			if a.Rejection != nil {
				at.Rejection = string(a.Rejection.Reason) + ": " + a.Rejection.Detail
			}
			for _, tr := range a.Transcriptions {
				at.Heard = append(at.Heard, tr.Text)
			}
			o.Attempts = append(o.Attempts, at)
		}
		outcomes[i] = o
	}
	data, err := json.Marshal(outcomes)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("shard-%d-of-%d.json", k, len(shards))), data, 0o600); err != nil {
		t.Fatal(err)
	}

	var all []outcome
	for j := 1; j <= len(shards); j++ {
		data, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("shard-%d-of-%d.json", j, len(shards))))
		if errors.Is(err, os.ErrNotExist) {
			t.Logf("shard %d of %d has not run yet; the gate is decided once every shard has (results in %s)", j, len(shards), dir)
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		var o []outcome
		if err := json.Unmarshal(data, &o); err != nil {
			t.Fatal(err)
		}
		all = append(all, o...)
	}
	if len(all) != len(corpus) {
		t.Fatalf("the shards hold %d results, want %d", len(all), len(corpus))
	}
	first, verified := 0, 0
	for i, o := range all {
		if o.State == verify.StateVerified {
			verified++
			if o.Final == 1 {
				first++
			}
		}
		if o.State != verify.StateVerified || o.Final != 1 {
			t.Logf("%s %s, kept attempt %d, score %.3f: %q", o.ID, o.State, o.Final, o.Score, corpus[i])
		}
	}
	t.Logf("%d/51 verified on the first attempt, %d/51 within the limit", first, verified)
	if first < 49 || verified != 51 {
		t.Fatalf("TXT-11#3 fails: %d/51 first-attempt (want ≥ 49), %d/51 verified (want 51)", first, verified)
	}
}

// liveBudget is the per-invocation cap of `make qualify` (implementation plan §1.4).
const liveBudget = 30 * time.Minute

// outcome is one chunk's result as a shard records it.
type outcome struct {
	ID    string       `json:"id"`
	State verify.State `json:"state"`
	Final int          `json:"final"`
	Score float64      `json:"score"`
	// The evidence a failure needs: what was meant, said and heard, and why it was rejected.
	Spoken   string    `json:"spoken"`
	Attempts []attempt `json:"attempts"`
}

type attempt struct {
	Heard     []string `json:"heard"`
	Score     float64  `json:"score"`
	Rejection string   `json:"rejection,omitempty"`
}

// shard splits tasks, in order, into the fewest runs whose worst case fits the budget.
func shard(tasks []pipeline.Task, attempts int, budget time.Duration) [][]pipeline.Task {
	var out [][]pipeline.Task
	var cur []pipeline.Task
	var est time.Duration
	for _, tk := range tasks {
		if len(cur) > 0 && livebudget.Preflight(est+tk.Chunk.Estimate, attempts, budget) != nil {
			out, cur, est = append(out, cur), nil, 0
		}
		cur, est = append(cur, tk), est+tk.Chunk.Estimate
	}
	return append(out, cur)
}
