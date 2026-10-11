//go:build calibrate

package calibrate_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/calibrate"
	"github.com/xiaolai/recite/internal/livebudget"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/stt"
	"github.com/xiaolai/recite/internal/verify"
)

// liveBudget is the per-invocation audio cap of `make calibrate` (implementation plan §1.4).
const liveBudget = 30 * time.Minute

const source = "internal/calibrate/testdata/passages-en.txt: 22 passages written for this project, " +
	"so the model cannot read any of them from memory"

// mini is the VER-07 mini calibration: its samples, split, tasks and result directory.
type mini struct {
	samples, propose, heldOut []calibrate.Sample
	tasks                     []pipeline.Task
	shards                    [][]pipeline.Task
	spec                      realtime.SessionSpec
	dir                       string
}

func setup(t *testing.T) mini {
	t.Helper()
	raw, err := os.ReadFile("testdata/passages-en.txt")
	if err != nil {
		t.Fatal(err)
	}
	var m mini
	if m.samples, err = calibrate.Build(corpus(t), 20, 20, seed); err != nil {
		t.Fatal(err)
	}
	m.propose, m.heldOut = calibrate.Split(m.samples, seed)
	if len(m.heldOut) != 16 {
		t.Fatalf("%d held-out clips, want 16", len(m.heldOut))
	}
	if m.spec, err = realtime.DefaultSettings().SessionSpec(); err != nil {
		t.Fatal(err)
	}
	spokenForm := func(s string) (string, error) {
		r, err := spoken.Normalize(s, "en", spoken.Options{})
		return r.Spoken, err
	}
	if m.tasks, err = calibrate.Tasks(m.samples, m.spec, spokenForm); err != nil {
		t.Fatal(err)
	}
	for i, tk := range m.tasks {
		if m.samples[i].Corrupted() == verify.Equivalent(tk.Render, tk.Chunk.Spoken) {
			t.Fatalf("%s (%s): the spoken form undid the construction: %q vs %q", tk.Chunk.ID, m.samples[i].Kind, tk.Render, tk.Chunk.Spoken)
		}
	}
	m.shards = calibrate.Shard(m.tasks, liveBudget)
	sum := sha256.Sum256(append(raw, strconv.Itoa(seed)...))
	m.dir = filepath.Join(os.TempDir(), "recite-live", fmt.Sprintf("ver07-%x", sum[:6]))
	return m
}

func (m mini) shardFile(k int) string {
	return filepath.Join(m.dir, fmt.Sprintf("shard-%d-of-%d.json", k, len(m.shards)))
}

func (m mini) labelsFile() string {
	if p := os.Getenv("RECITE_CALIBRATION_LABELS"); p != "" {
		return p
	}
	return filepath.Join(m.dir, "labels.txt")
}

// Step 1 renders the 40 candidates once each, with the said guard bypassed, and writes every
// take's WAV under an anonymised ID (held-out clips in clips/, the rest in propose/), the
// records, and the labels template for the owner. A shard already recorded is not rendered
// again. It spends real usage: make calibrate (RECITE_SHARD=k when there is more than one shard).
func TestCalibrateRender(t *testing.T) {
	m := setup(t)
	if os.Getenv("RECITE_LIVE") == "" {
		t.Skip("set RECITE_LIVE=1 to render the calibration candidates against the real service")
	}
	k := 1
	if len(m.shards) > 1 {
		var err error
		k, err = strconv.Atoi(os.Getenv("RECITE_SHARD"))
		if err != nil || k < 1 || k > len(m.shards) {
			t.Fatalf("run each of the %d shards in its own invocation: RECITE_SHARD=1..%d", len(m.shards), len(m.shards))
		}
	}
	held := map[string]bool{}
	for _, s := range m.heldOut {
		held[s.ID] = true
	}
	for _, d := range []string{"clips", "propose"} {
		if err := os.MkdirAll(filepath.Join(m.dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(m.shardFile(k)); err == nil {
		t.Logf("shard %d of %d is already recorded in %s; not rendering it again", k, len(m.shards), m.dir)
	} else {
		renderShard(t, m, k, held)
	}

	for j := 1; j <= len(m.shards); j++ {
		if _, err := os.Stat(m.shardFile(j)); err != nil {
			t.Logf("shard %d of %d has not run yet (results in %s)", j, len(m.shards), m.dir)
			return
		}
	}
	if _, err := os.Stat(m.labelsFile()); errors.Is(err, os.ErrNotExist) {
		f, err := os.OpenFile(m.labelsFile(), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := calibrate.WriteLabelsTemplate(f, m.heldOut, filepath.Join(m.dir, "clips")); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("listen to %s and fill %s", filepath.Join(m.dir, "clips"), m.labelsFile())
}

func renderShard(t *testing.T, m mini, k int, held map[string]bool) {
	t.Helper()
	part := m.shards[k-1]
	var est time.Duration
	for _, tk := range part {
		est += tk.Chunk.Estimate
	}
	if err := livebudget.Preflight(est, 1, liveBudget); err != nil {
		t.Fatal(err)
	}
	first := 0
	for i := 0; i < k-1; i++ {
		first += len(m.shards[i])
	}
	samples := m.samples[first : first+len(part)]

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

	keep := func(s calibrate.Sample, r verify.ChunkResult) error {
		sub := "propose"
		if held[s.ID] {
			sub = "clips"
		}
		pcm := r.PCM
		if len(pcm) == 0 && len(r.Attempts) > 0 {
			pcm = r.Attempts[0].PCM
		}
		f, err := os.OpenFile(filepath.Join(m.dir, sub, s.ID+".wav"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		if err := audio.WriteWAV(f, pcm); err != nil {
			_ = f.Close()
			return err
		}
		return f.Close()
	}
	budget := livebudget.New(liveBudget)
	cfg := pipeline.Config{Jobs: 3, Verify: verify.DefaultOptions(), Synth: synth, Transcriber: tr, Gate: gate}
	recs, err := calibrate.Run(ctx, budget.Pipeline(pipeline.Run), cfg, samples, part, keep)
	t.Logf("shard %d of %d: audio received %v of the %v budget", k, len(m.shards), budget.Used().Round(time.Second), liveBudget)
	if recs == nil {
		t.Fatal(err)
	}
	// The records are the evidence of a failed check too, so they are written first.
	data, merr := json.MarshalIndent(recs, "", "  ")
	if merr != nil {
		t.Fatal(merr)
	}
	if err != nil {
		if werr := os.WriteFile(m.shardFile(k)+".failed", data, 0o600); werr != nil {
			t.Error(werr)
		}
		t.Fatalf("%v\n(records in %s.failed)", err, m.shardFile(k))
	}
	for _, r := range calibrate.HeardAsOriginal(recs) {
		t.Logf("listen closely: %s (%s) was heard as the original passage", r.ID, r.Kind)
	}
	if err := os.WriteFile(m.shardFile(k), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Step 2 reads the records and the owner's labels, writes dev-docs/calibration-YYYYMMDD.md and
// applies the held-out criteria. It fails until every held-out clip is labelled.
//
// accept: VER-07#2
// accept: VER-07#3
func TestCalibrateReport(t *testing.T) {
	m := setup(t)
	var recs []calibrate.Record
	for j := 1; j <= len(m.shards); j++ {
		data, err := os.ReadFile(m.shardFile(j))
		if err != nil {
			t.Fatalf("shard %d of %d is not recorded; run the render step first: %v", j, len(m.shards), err)
		}
		var r []calibrate.Record
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatal(err)
		}
		recs = append(recs, r...)
	}
	if len(recs) != len(m.samples) {
		t.Fatalf("the shards hold %d records, want %d", len(recs), len(m.samples))
	}
	if err := calibrate.CheckReached(recs); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(m.labelsFile())
	if err != nil {
		t.Fatalf("labels: %v", err)
	}
	labels, err := calibrate.ParseLabels(f)
	_ = f.Close()
	if err != nil {
		t.Fatalf("%v (fill %s after listening to %s)", err, m.labelsFile(), filepath.Join(m.dir, "clips"))
	}
	held := map[string]bool{}
	for _, s := range m.heldOut {
		held[s.ID] = true
	}
	var propose, heldOut []calibrate.Record
	for _, r := range recs {
		if held[r.ID] {
			heldOut = append(heldOut, r)
		} else {
			propose = append(propose, r)
		}
	}
	ev, err := calibrate.Evaluate(heldOut, labels)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	path := filepath.Join("..", "..", "dev-docs", calibrate.ReportName(now))
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	opts := calibrate.Options(verify.DefaultOptions())
	err = calibrate.WriteReport(out, calibrate.Report{
		Date: now, Source: source, Seed: seed, Mode: opts.Mode, Thresholds: opts.Mode.Thresholds(),
		Propose: propose, HeldOut: ev,
	})
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("report written to %s; %d construction-vs-human disagreements", path, len(ev.Disagreements()))
	if err := ev.Check(); err != nil {
		t.Fatalf("VER-07#3: %v", err)
	}
}
