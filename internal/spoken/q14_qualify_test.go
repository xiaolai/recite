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
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/livebudget"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/verify"
)

// q14Condition is one delivery condition of the Q14 experiment: the style text after the rules
// and the effort it renders at.
type q14Condition struct {
	Name   string
	Style  string // the free-text style, as DeliveryParagraph receives it; "" for none
	Effort realtime.Effort
}

// q14Conditions compare each direction as recite sends it today (the bare words of a script's
// `(direction)`) with the same direction as a sentence in the shape of OpenAI's voice prompting
// guide ("Deliver your audio response fast, but do not sound rushed"), and two sentence forms
// again at effort low, where the guide starts gpt-realtime-2.
var q14Conditions = []q14Condition{
	{"none", "", realtime.DefaultEffort},
	{"hushed-bare", "hushed, slower", realtime.DefaultEffort},
	{"hushed-sentence", "Deliver this hushed and slower. Do not change the words.", realtime.DefaultEffort},
	{"fast-bare", "fast", realtime.DefaultEffort},
	{"fast-sentence", "Deliver this fast, but do not sound rushed. Do not change the words.", realtime.DefaultEffort},
	{"loud-bare", "loud, fast, excited", realtime.DefaultEffort},
	{"loud-sentence", "Deliver this loud, fast and excited, but do not sound rushed. Do not change the words.", realtime.DefaultEffort},
	{"hushed-sentence-low", "Deliver this hushed and slower. Do not change the words.", "low"},
	{"fast-sentence-low", "Deliver this fast, but do not sound rushed. Do not change the words.", "low"},
}

// q14Sounds are passages with written sound words, for whether the model adds sound effects or
// drops the words (field report 2026-10-17, item 5). They render with no direction.
var q14Sounds = []string{
	"Crash! The plates hit the floor, and the cat shot under the table with a hiss.",
	"Bang, bang, bang went the door. Then a hiss: the kettle was boiling over.",
	"Splash! Tom jumped into the pond. Woof, woof, barked the dog from the bank.",
}

// q14Takes is the number of takes of each condition on each passage.
const q14Takes = 3

// q14Record is one take.
type q14Record struct {
	Condition string       `json:"condition"` // a q14Conditions name, or "sounds"
	Passage   int          `json:"passage"`   // 1-based, within its passage list
	Take      int          `json:"take"`
	Verdict   verify.State `json:"verdict"`
	Attempts  int          `json:"attempts"`
	Score     float64      `json:"score"`
	Seconds   float64      `json:"seconds"`  // the kept take, trimmed
	RMSdBFS   *float64     `json:"rms_dbfs"` // the kept take; null without audio or for digital silence
	Rejection string       `json:"rejection,omitempty"`
}

type q14Shard struct {
	Evidence string      `json:"evidence"`
	Records  []q14Record `json:"records"`
}

// q14Row is one condition's row of the summary.
type q14Row struct {
	Condition     string   `json:"condition"`
	Takes         int      `json:"takes"`
	Verified      int      `json:"verified"`
	FirstAttempt  int      `json:"first_attempt"`
	MedianSeconds float64  `json:"median_seconds"`
	MedianRMSdBFS float64  `json:"median_rms_dbfs"`
	DurationVs    *float64 `json:"duration_vs_none"` // median of per-passage ratios - 1; null for "sounds"
	RMSVs         *float64 `json:"rms_db_vs_none"`   // median of per-passage differences; null for "sounds"
}

// TestQ14DirectionPhrasing is the Q14 experiment: does a direction phrased as a sentence move
// pace and level more than the bare words recite sends, does effort low follow it better, and
// do written sound words verify. 9 conditions × 3 SPK-07 passages × 3 takes, plus 3 sound
// passages × 3 takes (90 takes). Each take's audio is written as a WAV beside the records, for
// listening. It reports and never gates: it settles a question, not a feature.
//
// It spends real usage, once per shard:
//
//	RECITE_LIVE=1 RECITE_SHARD=k go test -tags qualify -run TestQ14DirectionPhrasing -count=1 -v ./internal/spoken
//
// Results go to $TMPDIR/recite-live/q14-<evidence ID>/.
func TestQ14DirectionPhrasing(t *testing.T) {
	set := realtime.DefaultSettings()
	base, err := set.SessionSpec()
	if err != nil {
		t.Fatal(err)
	}
	opts := verify.OptionsFor(verify.ModeNormal)
	type key struct {
		cond          string
		passage, take int
	}
	keys := map[string]key{}
	var tasks []pipeline.Task
	add := func(cond string, p int, passage, style string, effort realtime.Effort) {
		for take := 1; take <= q14Takes; take++ {
			id := fmt.Sprintf("p%d-%s-t%d", p, cond, take)
			spec := base
			spec.Effort = effort
			tk := corpusTask(t, id, passage, spec)
			tk.Style = realtime.DeliveryParagraph(realtime.DefaultEmotion, style)
			tasks = append(tasks, tk)
			keys[id] = key{cond, p, take}
		}
	}
	// Passage-major order, so every shard compares the conditions on the same text.
	for p, passage := range spk07Passages {
		for _, c := range q14Conditions {
			add(c.Name, p+1, passage, c.Style, c.Effort)
		}
	}
	for p, passage := range q14Sounds {
		add("sounds", p+1, passage, "", realtime.DefaultEffort)
	}
	if want := (len(q14Conditions)*len(spk07Passages) + len(q14Sounds)) * q14Takes; len(tasks) != want {
		t.Fatalf("%d takes planned, want %d", len(tasks), want)
	}
	shards := shard(tasks, opts.MaxAttempts, liveBudget)
	t.Logf("%d takes in %d shards under the %v budget", len(tasks), len(shards), liveBudget)
	if os.Getenv("RECITE_LIVE") == "" {
		t.Skip("set RECITE_LIVE=1 to run the Q14 experiment against the real service")
	}
	k, err := strconv.Atoi(os.Getenv("RECITE_SHARD"))
	if err != nil || k < 1 || k > len(shards) {
		t.Fatalf("run each of the %d shards in its own invocation: RECITE_SHARD=1..%d", len(shards), len(shards))
	}
	part := shards[k-1]
	var est []verify.Chunk
	for _, tk := range part {
		est = append(est, tk.Chunk)
	}
	if err := livebudget.Preflight(est, opts.MaxAttempts, liveBudget); err != nil {
		t.Fatal(err)
	}
	// The chunk IDs name the condition, but not its style text, so the styles join the evidence.
	var styles []string
	for _, c := range q14Conditions {
		styles = append(styles, c.Name+"="+c.Style+"@"+string(c.Effort))
	}
	evidence := evidenceID(tasks, opts, base, revision()+"\n"+strings.Join(styles, "\n"))
	dir := filepath.Join(os.TempDir(), "recite-live", "q14-"+evidence)
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
	var records []q14Record
	for _, r := range res.Chunks {
		kk := keys[r.ChunkID]
		pcm := kept.Of(t, r)
		rec := q14Record{Condition: kk.cond, Passage: kk.passage, Take: kk.take, Verdict: r.State,
			Attempts: len(r.Attempts), Score: r.Score, Seconds: pcm.Duration().Seconds(), RMSdBFS: rmsDBFS(pcm)}
		for _, a := range r.Attempts {
			if a.Rejection != nil {
				rec.Rejection = string(a.Rejection.Reason) + ": " + a.Rejection.Detail
			}
		}
		if len(pcm) > 0 {
			q14WriteWAV(t, filepath.Join(dir, r.ChunkID+".wav"), pcm)
		}
		records = append(records, rec)
	}
	if len(records) != len(part) {
		t.Fatalf("shard %d has %d records, want %d", k, len(records), len(part))
	}
	writeJSON(t, filepath.Join(dir, fmt.Sprintf("shard-%d-of-%d.json", k, len(shards))), q14Shard{Evidence: evidence, Records: records})

	var all []q14Record
	for j := 1; j <= len(shards); j++ {
		data, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("shard-%d-of-%d.json", j, len(shards))))
		if errors.Is(err, os.ErrNotExist) {
			t.Logf("shard %d of %d has not run yet; the summary is written once every shard has (results in %s)", j, len(shards), dir)
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		var sf q14Shard
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
		kk := key{r.Condition, r.Passage, r.Take}
		if _, ok := keys[fmt.Sprintf("p%d-%s-t%d", r.Passage, r.Condition, r.Take)]; !ok || seen[kk] {
			t.Fatalf("foreign or duplicate record %+v", kk)
		}
		seen[kk] = true
	}
	if len(all) != len(tasks) {
		t.Fatalf("the shards hold %d records, want %d", len(all), len(tasks))
	}
	rows := q14Summarize(all)
	writeJSON(t, filepath.Join(dir, "records.json"), all)
	writeJSON(t, filepath.Join(dir, "summary.json"), rows)
	table := q14Table(rows)
	if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("Q14 results in %s\n%s", dir, table)
}

// q14Summarize makes one row per condition, in q14Conditions order, then "sounds". Medians are
// over every take with audio, verified or not, so nothing is cherry-picked. The comparison with
// "none" pairs passages: each passage's median ratio (or difference) against none's median on
// the same passage, then the median over the passages, so a long passage cannot dominate.
func q14Summarize(records []q14Record) []q14Row {
	type cell struct{ secs, rms []float64 }
	cells := map[string]map[int]*cell{}
	names := []string{}
	for _, c := range q14Conditions {
		names = append(names, c.Name)
	}
	names = append(names, "sounds")
	rows := make([]q14Row, 0, len(names))
	for _, n := range names {
		row := q14Row{Condition: n}
		cells[n] = map[int]*cell{}
		var secs, rms []float64
		for _, r := range records {
			if r.Condition != n {
				continue
			}
			row.Takes++
			if r.Verdict == verify.StateVerified {
				row.Verified++
				if r.Attempts == 1 {
					row.FirstAttempt++
				}
			}
			if r.Seconds > 0 && r.RMSdBFS != nil {
				secs, rms = append(secs, r.Seconds), append(rms, *r.RMSdBFS)
				c := cells[n][r.Passage]
				if c == nil {
					c = &cell{}
					cells[n][r.Passage] = c
				}
				c.secs, c.rms = append(c.secs, r.Seconds), append(c.rms, *r.RMSdBFS)
			}
		}
		row.MedianSeconds, row.MedianRMSdBFS = median(secs), median(rms)
		rows = append(rows, row)
	}
	for i := range rows {
		if rows[i].Condition == "sounds" {
			continue // no baseline: the sound passages render with no direction only
		}
		var ratios, diffs []float64
		for p, c := range cells[rows[i].Condition] {
			n := cells["none"][p]
			if n == nil {
				continue
			}
			ratios = append(ratios, median(c.secs)/median(n.secs)-1)
			diffs = append(diffs, median(c.rms)-median(n.rms))
		}
		dv, rv := median(ratios), median(diffs)
		rows[i].DurationVs, rows[i].RMSVs = &dv, &rv
	}
	return rows
}

func q14Table(rows []q14Row) string {
	var b strings.Builder
	b.WriteString("| Condition | Takes | Verified | First attempt | Median s | vs none | Median RMS dBFS | vs none |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|\n")
	for _, r := range rows {
		if r.DurationVs == nil || r.RMSVs == nil {
			fmt.Fprintf(&b, "| %s | %d | %d | %d | %.2f | — | %.2f | — |\n",
				r.Condition, r.Takes, r.Verified, r.FirstAttempt, r.MedianSeconds, r.MedianRMSdBFS)
			continue
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %.2f | %+.1f%% | %.2f | %+.2f dB |\n",
			r.Condition, r.Takes, r.Verified, r.FirstAttempt, r.MedianSeconds, 100**r.DurationVs, r.MedianRMSdBFS, *r.RMSVs)
	}
	return b.String()
}

// q14WriteWAV keeps a take for listening.
func q14WriteWAV(t *testing.T, path string, p audio.PCM) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := audio.WriteWAV(f, p); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
