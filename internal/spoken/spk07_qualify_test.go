//go:build qualify

package spoken_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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

// spk07Passages are the three passages of the SPK-07 delivery sample.
var spk07Passages = []string{
	"The train left the station just after nine, and the platform was empty again by the time the rain began.",
	"She opened the letter slowly, read the first line twice, and then folded it back into its envelope.",
	"Every evening the old lighthouse keeper climbed the stairs, lit the lamp, and watched the ships pass by.",
}

// spk07Takes is the number of takes of each preset on each passage.
const spk07Takes = 2

// spk07Record is one take of one preset on one passage.
type spk07Record struct {
	Preset    string       `json:"preset"`
	Passage   int          `json:"passage"` // 1-based
	Take      int          `json:"take"`
	Verdict   verify.State `json:"verdict"`
	Attempts  int          `json:"attempts"`
	Score     float64      `json:"score"`
	Seconds   float64      `json:"seconds"`  // the kept take, trimmed
	RMSdBFS   *float64     `json:"rms_dbfs"` // the kept take; null without audio or for digital silence
	Rejection string       `json:"rejection,omitempty"`
}

type spk07Shard struct {
	Evidence string        `json:"evidence"`
	Records  []spk07Record `json:"records"`
}

// spk07Row is one preset's row of the summary.
type spk07Row struct {
	Preset          string  `json:"preset"`
	Takes           int     `json:"takes"`
	Verified        int     `json:"verified"`
	MedianSeconds   float64 `json:"median_seconds"`
	MedianRMSdBFS   float64 `json:"median_rms_dbfs"`
	DurationVsNeutr float64 `json:"duration_vs_neutral"` // median ratio - 1
	RMSVsNeutral    float64 `json:"rms_db_vs_neutral"`
}

// TestQualifyDeliveryPresets is the SPK-07 qualification: every preset × 3 passages × 2 takes
// (54 takes), each verified within the default attempt limit. It reports every take, then
// gates: at least 50 of 54 verify, and over its 6 takes `whisper` and `sad` each have a median
// duration at least 20% longer and a median RMS at least 1 dB lower than `neutral`.
//
// It spends real usage, once per shard:
//
//	RECITE_LIVE=1 RECITE_SHARD=k go test -tags qualify -run TestQualifyDeliveryPresets -count=1 -v ./internal/spoken
//
// Results go to $TMPDIR/recite-live/spk07-<evidence ID>/: shard-k-of-n.json, then
// records.json, summary.json and summary.md once every shard has run.
// accept: SPK-07#3
func TestQualifyDeliveryPresets(t *testing.T) {
	set := realtime.DefaultSettings()
	spec, err := set.SessionSpec()
	if err != nil {
		t.Fatal(err)
	}
	opts := verify.OptionsFor(verify.ModeNormal)
	type key struct {
		preset        string
		passage, take int
	}
	keys := map[string]key{}
	var tasks []pipeline.Task
	// Passage-major order, so every shard compares the presets on the same text.
	for p, passage := range spk07Passages {
		for _, preset := range realtime.Emotions() {
			for take := 1; take <= spk07Takes; take++ {
				id := fmt.Sprintf("p%d-%s-t%d", p+1, preset, take)
				tk := corpusTask(t, id, passage, spec)
				tk.Style = realtime.DeliveryParagraph(preset, "")
				tasks = append(tasks, tk)
				keys[id] = key{preset, p + 1, take}
			}
		}
	}
	if len(tasks) != 54 {
		t.Fatalf("%d takes planned, want 54", len(tasks))
	}
	shards := shard(tasks, opts.MaxAttempts, liveBudget)
	t.Logf("%d takes in %d shards under the %v budget", len(tasks), len(shards), liveBudget)
	if os.Getenv("RECITE_LIVE") == "" {
		t.Skip("set RECITE_LIVE=1 to run the SPK-07 qualification against the real service")
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
	if err := livebudget.Preflight(est, opts.MaxAttempts, liveBudget); err != nil {
		t.Fatal(err)
	}
	// The chunk IDs name the preset; the preset sentences are versioned.
	evidence := evidenceID(tasks, opts, spec, revision()) + "-e" + strconv.Itoa(realtime.EmotionVersion)
	dir := filepath.Join(os.TempDir(), "recite-live", "spk07-"+evidence)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	synth, tr, gate := liveServices(t)
	budget := livebudget.New(liveBudget)
	kept := newDeliveredAudio()
	res, err := budget.Pipeline(pipeline.Run)(context.Background(), pipeline.Config{
		Jobs: 3, Verify: opts, Synth: synth, Transcriber: tr, Gate: gate,
		Deliver: kept.Deliver,
	}, part)
	if err != nil {
		t.Fatalf("run: %v (audio received %v)", err, budget.Used().Round(time.Second))
	}
	t.Logf("shard %d of %d: audio received %v of the %v budget", k, len(shards), budget.Used().Round(time.Second), liveBudget)
	var records []spk07Record
	for _, r := range res.Chunks {
		kk := keys[r.ChunkID]
		rec := spk07Record{Preset: kk.preset, Passage: kk.passage, Take: kk.take, Verdict: r.State,
			Attempts: len(r.Attempts), Score: r.Score, Seconds: kept.Of(t, r).Duration().Seconds(), RMSdBFS: rmsDBFS(kept.Of(t, r))}
		for _, a := range r.Attempts {
			if a.Rejection != nil {
				rec.Rejection = string(a.Rejection.Reason) + ": " + a.Rejection.Detail
			}
		}
		records = append(records, rec)
	}
	if len(records) != len(part) {
		t.Fatalf("shard %d has %d records, want %d", k, len(records), len(part))
	}
	writeJSON(t, filepath.Join(dir, fmt.Sprintf("shard-%d-of-%d.json", k, len(shards))), spk07Shard{Evidence: evidence, Records: records})

	var all []spk07Record
	for j := 1; j <= len(shards); j++ {
		data, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("shard-%d-of-%d.json", j, len(shards))))
		if errors.Is(err, os.ErrNotExist) {
			t.Logf("shard %d of %d has not run yet; the summary is written once every shard has (results in %s)", j, len(shards), dir)
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		var sf spk07Shard
		if err := json.Unmarshal(data, &sf); err != nil {
			t.Fatal(err)
		}
		if sf.Evidence != evidence {
			t.Fatalf("shard %d of %d belongs to evidence %q, not %q", j, len(shards), sf.Evidence, evidence)
		}
		all = append(all, sf.Records...)
	}
	seen := map[key]bool{}
	for _, r := range all {
		kk := key{r.Preset, r.Passage, r.Take}
		if _, ok := keys[fmt.Sprintf("p%d-%s-t%d", r.Passage, r.Preset, r.Take)]; !ok || seen[kk] {
			t.Fatalf("foreign or duplicate record %+v", kk)
		}
		seen[kk] = true
	}
	if len(all) != len(tasks) {
		t.Fatalf("the shards hold %d records, want %d", len(all), len(tasks))
	}
	rows := spk07Summarize(all)
	writeJSON(t, filepath.Join(dir, "records.json"), all)
	writeJSON(t, filepath.Join(dir, "summary.json"), rows)
	table := spk07Table(rows)
	if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("SPK-07 results in %s\n%s", dir, table)

	// The gate, after everything is reported.
	verified := 0
	for _, r := range rows {
		verified += r.Verified
	}
	if verified < 50 {
		t.Errorf("%d of %d takes verified, want at least 50", verified, len(all))
	}
	for _, r := range rows {
		if r.Preset != "whisper" && r.Preset != "sad" {
			continue
		}
		if r.DurationVsNeutr < 0.20 {
			t.Errorf("%s: median duration %+.1f%% vs neutral, want at least +20%%", r.Preset, 100*r.DurationVsNeutr)
		}
		if r.RMSVsNeutral > -1 {
			t.Errorf("%s: median RMS %+.2f dB vs neutral, want at most -1 dB", r.Preset, r.RMSVsNeutral)
		}
	}
}

// spk07Summarize makes one row per preset, in realtime.Emotions order. Medians are over every
// take of the preset with audio, verified or not, so nothing is cherry-picked.
func spk07Summarize(records []spk07Record) []spk07Row {
	var rows []spk07Row
	for _, p := range realtime.Emotions() {
		row := spk07Row{Preset: p}
		var secs, rms []float64
		for _, r := range records {
			if r.Preset != p {
				continue
			}
			row.Takes++
			if r.Verdict == verify.StateVerified {
				row.Verified++
			}
			if r.Seconds > 0 && r.RMSdBFS != nil {
				secs, rms = append(secs, r.Seconds), append(rms, *r.RMSdBFS)
			}
		}
		row.MedianSeconds, row.MedianRMSdBFS = median(secs), median(rms)
		rows = append(rows, row)
	}
	neutral := rows[0] // realtime.Emotions starts with neutral
	for i := range rows {
		if neutral.MedianSeconds > 0 {
			rows[i].DurationVsNeutr = rows[i].MedianSeconds/neutral.MedianSeconds - 1
		}
		rows[i].RMSVsNeutral = rows[i].MedianRMSdBFS - neutral.MedianRMSdBFS
	}
	return rows
}

func spk07Table(rows []spk07Row) string {
	var b strings.Builder
	b.WriteString("| Preset | Takes | Verified | Median s | vs neutral | Median RMS dBFS | vs neutral |\n")
	b.WriteString("|---|---|---|---|---|---|---|\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %d | %d | %.2f | %+.1f%% | %.2f | %+.2f dB |\n",
			r.Preset, r.Takes, r.Verified, r.MedianSeconds, 100*r.DurationVsNeutr, r.MedianRMSdBFS, r.RMSVsNeutral)
	}
	return b.String()
}

// median of xs; NaN for none.
func median(xs []float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	if n := len(s); n%2 == 1 {
		return s[n/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

// rmsDBFS is the RMS level of 16-bit little-endian PCM in dB relative to full scale, or nil
// when there is no audio or only digital silence: -Inf has no JSON encoding.
func rmsDBFS(p audio.PCM) *float64 {
	n := len(p) / 2
	if n == 0 {
		return nil
	}
	var sum float64
	for i := 0; i < n; i++ {
		v := float64(int16(binary.LittleEndian.Uint16(p[2*i:]))) / 32768
		sum += v * v
	}
	if sum == 0 {
		return nil
	}
	db := 10 * math.Log10(sum/float64(n))
	return &db
}

// liveServices signs in through the local Codex login and builds the real synthesizer and the
// verifier's transcriber chain, as the Q10 experiment does.
func liveServices(t *testing.T) (realtime.Synthesizer, stt.Transcriber, *openai.Gate) {
	t.Helper()
	st := auth.Probe(context.Background(), auth.ProbeConfig{Source: auth.SourceAuto, Getenv: os.Getenv})
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
	return synth, &stt.Chain{Primary: &stt.HTTP{Client: hc, BaseURL: openai.APIBase, Creds: st.Creds}, Fallback: rt}, gate
}
