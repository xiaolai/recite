//go:build qualify

package spoken_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
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

// probeCorpus is the 21 items of dev-docs/probe_textnorm.py, Chinese included, with three
// replaced by owner decision (2026-10-11): "Take 1/2 cup on 1/2." and "第1/2季度…" end in an
// N-AMBIGUOUS span, which TXT-11#2 requires and which can never verify, and the deliberately
// misspelled sentence is auto-corrected by STT. Their behaviour stays covered by the
// deterministic tests (en_test.go, zh_test.go, the guard tests).
var probeCorpus = []string{
	"The deal was worth $3.5M, or about €3.2 million.",
	"The meeting is on 2026-10-11 at 3:45pm.",
	"Measure 1/2 cup of milk and 3/4 cup of water.",
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
	"The 2nd shipment contains 48 boxes of 250ml bottles and costs $1,200.",
	"It weighs 5kg, runs at 120km/h, and costs 3¥.",
	"2026年10月11日下午3点45分，价格是¥1,280.50，增长了12.5%。",
	"请在iPhone 17上打开App Store，下载版本v2.0。",
	"In the 2nd quarter, the temperature ranged from -5℃ to 30℃.",
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
	qualifyCorpus(t, "TXT-11#3", "txt11", corpus, 49)
}

// qualifyCorpus renders and verifies corpus, one chunk per item and one take each, in
// budgeted shards (RECITE_SHARD); once every shard has run it requires minFirst items
// verified on the first attempt and all of them within the attempt limit. Shard results
// and transcripts go to $TMPDIR/recite-live/<tag>-<evidence ID>/.
func qualifyCorpus(t *testing.T, bullet, tag string, corpus []string, minFirst int) {
	t.Helper()
	total := len(corpus)
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
	// The worst case of all the chunks is over one invocation's budget (plan §1.4), so the gate
	// runs in shards, one per invocation, each resumable; the last shard aggregates.
	attempts := opts.MaxAttempts + opts.RequestExtraAttempts
	shards := shard(tasks, attempts, liveBudget)
	shard := os.Getenv("RECITE_SHARD")
	if shard == "" && len(shards) == 1 {
		shard = "1"
	}
	k, err := strconv.Atoi(shard)
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
	evidence := evidenceID(tasks, opts, spec, revision())
	dir := filepath.Join(os.TempDir(), "recite-live", tag+"-"+evidence)
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
	data, err := json.Marshal(shardFile{Evidence: evidence, Outcomes: outcomes})
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
		var sf shardFile
		if err := json.Unmarshal(data, &sf); err != nil {
			t.Fatal(err)
		}
		if sf.Evidence != evidence {
			t.Fatalf("shard %d of %d belongs to evidence %q, not %q", j, len(shards), sf.Evidence, evidence)
		}
		all = append(all, sf.Outcomes...)
	}
	// Exactly the planned chunks, each once and in order: no missing, duplicate or foreign result.
	if len(all) != len(tasks) {
		t.Fatalf("the shards hold %d results, want %d", len(all), len(tasks))
	}
	for i, o := range all {
		if o.ID != tasks[i].Chunk.ID {
			t.Fatalf("result %d is %s, want %s", i+1, o.ID, tasks[i].Chunk.ID)
		}
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
	t.Logf("%d/%d verified on the first attempt, %d/%d within the limit", first, total, verified, total)
	if first < minFirst || verified != total {
		t.Fatalf("%s fails: %d/%d first-attempt (want ≥ %d), %d/%d verified (want %d)",
			bullet, first, total, minFirst, verified, total, total)
	}
}

// liveBudget is the per-invocation cap of `make qualify` (implementation plan §1.4).
const liveBudget = 30 * time.Minute

// shardFile is one shard's results with the identity of the evidence they belong to.
type shardFile struct {
	Evidence string    `json:"evidence"`
	Outcomes []outcome `json:"outcomes"`
}

// revision is the implementation revision for the evidence identity; a dirty tree is marked
// so, since its results do not belong to any commit.
func revision() string {
	out, err := exec.Command("git", "describe", "--always", "--dirty", "--abbrev=12").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

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
