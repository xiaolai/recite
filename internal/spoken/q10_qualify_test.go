//go:build qualify

package spoken_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/livebudget"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/stt"
	"github.com/xiaolai/recite/internal/verify"
)

// q10Takes is the number of independent takes of each chunk at each effort (design Q10).
const q10Takes = 3

// q10Record is one take: one render at one effort, verified once by the normal verifier.
type q10Record struct {
	Chunk      int          `json:"chunk"` // 1-based line of testdata/q10-hard.txt
	Effort     string       `json:"effort"`
	Take       int          `json:"take"`
	Verdict    verify.State `json:"verdict"`
	Score      float64      `json:"score"`
	RenderSecs float64      `json:"render_seconds"`
	AudioSecs  float64      `json:"audio_seconds"`
	Rejection  string       `json:"rejection,omitempty"`
	Heard      []string     `json:"heard,omitempty"`
	Spoken     string       `json:"spoken"`
}

// q10Shard is one shard's records with the identity of the evidence they belong to.
type q10Shard struct {
	Evidence string      `json:"evidence"`
	Records  []q10Record `json:"records"`
}

// q10Summary is one effort's row of the summary table.
type q10Summary struct {
	Effort        string  `json:"effort"`
	Takes         int     `json:"takes"`
	FirstTakePass float64 `json:"first_take_pass_rate"` // take 1 of each chunk
	AllTakesPass  float64 `json:"all_takes_pass_rate"`
	MeanScore     float64 `json:"mean_score"`
	MeanRenderSec float64 `json:"mean_render_seconds"`
	MeanAudioSec  float64 `json:"mean_audio_seconds"`
}

// TestQ10EffortExperiment is the Q10 experiment (SPK-11, design Q10): the 40 hard chunks of
// testdata/q10-hard.txt (numbers, symbols, alphanumerics, mixed scripts) × every effort ×
// q10Takes takes. Each take is one render, verified once with the default options, so its
// verdict is a first-attempt verdict. It measures and does not gate: the records and the
// per-effort summary feed dev-docs/spikes/q10-effort.md. Sessions of a shard are minted at
// each effort in turn, as `--effort E` would, so no take carries a per-response override.
//
// It spends real usage, once per shard (the count is logged and in the error without
// RECITE_SHARD):
//
//	RECITE_LIVE=1 RECITE_SHARD=k go test -tags qualify -run TestQ10EffortExperiment -count=1 -v ./internal/spoken
//
// Results go to $TMPDIR/recite-live/q10-<evidence ID>/: shard-k-of-n.json per shard, then
// records.json, summary.json and summary.md once every shard has run. The committed report
// built from them is dev-docs/spikes/q10-effort.md.
// accept: SPK-11#6
func TestQ10EffortExperiment(t *testing.T) {
	raw, err := os.ReadFile("testdata/q10-hard.txt")
	if err != nil {
		t.Fatal(err)
	}
	corpus := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(corpus) != 40 {
		t.Fatalf("corpus has %d chunks, want 40", len(corpus))
	}
	set := realtime.DefaultSettings()
	base, err := set.SessionSpec()
	if err != nil {
		t.Fatal(err)
	}
	opts := verify.DefaultOptions()
	opts.MaxAttempts, opts.RequestExtraAttempts = 1, 0

	// Chunk-major order: every shard compares all efforts on its chunks, so the takes of one
	// chunk run close together in time and a partial run is still a fair comparison.
	type key struct {
		chunk, take int
		effort      realtime.Effort
	}
	var tasks []pipeline.Task
	keys := map[string]key{}
	for i, c := range corpus {
		for _, e := range realtime.Efforts {
			spec := base
			spec.Effort = e
			for take := 1; take <= q10Takes; take++ {
				id := fmt.Sprintf("c%04d-%s-t%d", i+1, e, take)
				tasks = append(tasks, corpusTask(t, id, c, spec))
				keys[id] = key{i + 1, take, e}
			}
		}
	}
	shards := shard(tasks, 1, liveBudget)
	t.Logf("%d takes in %d shards under the %v budget", len(tasks), len(shards), liveBudget)
	if os.Getenv("RECITE_LIVE") == "" {
		t.Skip("set RECITE_LIVE=1 to run the Q10 experiment against the real service")
	}
	k, err := strconv.Atoi(os.Getenv("RECITE_SHARD"))
	if err != nil || k < 1 || k > len(shards) {
		t.Fatalf("run each of the %d shards in its own invocation: RECITE_SHARD=1..%d", len(shards), len(shards))
	}
	part := shards[k-1]
	var est time.Duration
	for _, tk := range part {
		est += tk.Chunk.Estimate
	}
	if err := livebudget.Preflight(est, 1, liveBudget); err != nil {
		t.Fatal(err)
	}
	evidence := evidenceID(tasks, opts, base, revision())
	dir := filepath.Join(os.TempDir(), "recite-live", "q10-"+evidence)
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
	timed := &timedSynth{Synthesizer: &realtime.Synth{Minter: minter, Client: hc, BaseURL: openai.APIBase, OnRateLimits: pipeline.PauseOnExhausted(gate)}, takes: map[string]takeTime{}}
	rt := &stt.Realtime{Minter: minter, Client: hc, BaseURL: openai.APIBase}
	t.Cleanup(func() { _ = rt.Close() })
	tr := &stt.Chain{Primary: &stt.HTTP{Client: hc, BaseURL: openai.APIBase, Creds: st.Creds}, Fallback: rt}

	// One run per effort: the pool key ignores effort, so a mixed run would serve one
	// effort's chunks on sessions minted at another.
	budget := livebudget.New(liveBudget)
	run := budget.Pipeline(pipeline.Run)
	var records []q10Record
	for _, e := range realtime.Efforts {
		var group []pipeline.Task
		for _, tk := range part {
			if tk.Spec.Effort == e {
				group = append(group, tk)
			}
		}
		if len(group) == 0 {
			continue
		}
		res, err := run(ctx, pipeline.Config{
			Jobs: 3, Verify: opts, Synth: timed, Transcriber: tr, Gate: gate,
			Deliver: func(int, verify.ChunkResult) error { return nil },
		}, group)
		if err != nil {
			t.Fatalf("effort %s: run: %v (audio received %v)", e, err, budget.Used().Round(time.Second))
		}
		for i, r := range res.Chunks {
			kk := keys[r.ChunkID]
			tt := timed.get(r.ChunkID)
			rec := q10Record{Chunk: kk.chunk, Effort: string(kk.effort), Take: kk.take, Verdict: r.State, Score: r.Score,
				RenderSecs: tt.render.Seconds(), AudioSecs: tt.audio.Seconds(), Spoken: group[i].Chunk.Spoken}
			for _, a := range r.Attempts {
				if a.Rejection != nil {
					rec.Rejection = string(a.Rejection.Reason) + ": " + a.Rejection.Detail
				}
				for _, x := range a.Transcriptions {
					rec.Heard = append(rec.Heard, x.Text)
				}
			}
			records = append(records, rec)
		}
	}
	t.Logf("shard %d of %d: audio received %v of the %v budget", k, len(shards), budget.Used().Round(time.Second), liveBudget)
	if len(records) != len(part) {
		t.Fatalf("shard %d has %d records, want %d", k, len(records), len(part))
	}
	writeJSON(t, filepath.Join(dir, fmt.Sprintf("shard-%d-of-%d.json", k, len(shards))), q10Shard{Evidence: evidence, Records: records})

	var all []q10Record
	for j := 1; j <= len(shards); j++ {
		data, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("shard-%d-of-%d.json", j, len(shards))))
		if errors.Is(err, os.ErrNotExist) {
			t.Logf("shard %d of %d has not run yet; the summary is written once every shard has (results in %s)", j, len(shards), dir)
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		var sf q10Shard
		if err := json.Unmarshal(data, &sf); err != nil {
			t.Fatal(err)
		}
		if sf.Evidence != evidence {
			t.Fatalf("shard %d of %d belongs to evidence %q, not %q", j, len(shards), sf.Evidence, evidence)
		}
		all = append(all, sf.Records...)
	}
	// Exactly the planned takes, each once: no missing, duplicate or foreign record.
	seen := map[key]bool{}
	for _, r := range all {
		kk := key{r.Chunk, r.Take, realtime.Effort(r.Effort)}
		if _, ok := keys[fmt.Sprintf("c%04d-%s-t%d", r.Chunk, r.Effort, r.Take)]; !ok || seen[kk] {
			t.Fatalf("foreign or duplicate record %+v", kk)
		}
		seen[kk] = true
	}
	if len(all) != len(tasks) {
		t.Fatalf("the shards hold %d records, want %d", len(all), len(tasks))
	}
	summary := q10Summarize(all)
	writeJSON(t, filepath.Join(dir, "records.json"), all)
	writeJSON(t, filepath.Join(dir, "summary.json"), summary)
	table := q10Table(summary)
	if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("Q10 results in %s\n%s", dir, table)
}

// q10Summarize makes one row per effort, in realtime.Efforts order.
func q10Summarize(records []q10Record) []q10Summary {
	var out []q10Summary
	for _, e := range realtime.Efforts {
		s := q10Summary{Effort: string(e)}
		firsts, firstPass, pass := 0, 0, 0
		for _, r := range records {
			if r.Effort != string(e) {
				continue
			}
			s.Takes++
			ok := r.Verdict == verify.StateVerified
			if ok {
				pass++
			}
			if r.Take == 1 {
				firsts++
				if ok {
					firstPass++
				}
			}
			s.MeanScore += r.Score
			s.MeanRenderSec += r.RenderSecs
			s.MeanAudioSec += r.AudioSecs
		}
		if s.Takes == 0 {
			continue
		}
		n := float64(s.Takes)
		s.AllTakesPass, s.MeanScore, s.MeanRenderSec, s.MeanAudioSec = float64(pass)/n, s.MeanScore/n, s.MeanRenderSec/n, s.MeanAudioSec/n
		if firsts > 0 {
			s.FirstTakePass = float64(firstPass) / float64(firsts)
		}
		out = append(out, s)
	}
	return out
}

// q10Table renders the summary as a Markdown table.
func q10Table(rows []q10Summary) string {
	var b strings.Builder
	b.WriteString("| Effort | Takes | Pass, take 1 | Pass, all takes | Mean score | Mean render s | Mean audio s |\n")
	b.WriteString("|---|---|---|---|---|---|---|\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %d | %.1f%% | %.1f%% | %.3f | %.2f | %.2f |\n",
			r.Effort, r.Takes, 100*r.FirstTakePass, 100*r.AllTakesPass, r.MeanScore, r.MeanRenderSec, r.MeanAudioSec)
	}
	return b.String()
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// takeTime is one render's wall time and the audio it returned.
type takeTime struct{ render, audio time.Duration }

// timedSynth records each render's wall time and raw audio length by chunk ID; the pipeline
// result keeps neither.
type timedSynth struct {
	realtime.Synthesizer
	mu    sync.Mutex
	takes map[string]takeTime
}

func (s *timedSynth) Open(ctx context.Context, spec realtime.SessionSpec) (realtime.SpeechSession, error) {
	ss, err := s.Synthesizer.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	return timedSession{ss, s}, nil
}

func (s *timedSynth) get(id string) takeTime {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.takes[id]
}

type timedSession struct {
	realtime.SpeechSession
	s *timedSynth
}

func (ts timedSession) Render(ctx context.Context, req realtime.RenderRequest) (realtime.Take, error) {
	start := time.Now()
	take, err := ts.SpeechSession.Render(ctx, req)
	if err == nil {
		ts.s.mu.Lock()
		ts.s.takes[req.ChunkID] = takeTime{time.Since(start), audio.PCM(take.PCM).Duration()}
		ts.s.mu.Unlock()
	}
	return take, err
}
