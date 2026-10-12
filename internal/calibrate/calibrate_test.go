package calibrate_test

import (
	"bytes"
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/calibrate"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	rtfake "github.com/xiaolai/recite/internal/realtime/fake"
	"github.com/xiaolai/recite/internal/stt"
	"github.com/xiaolai/recite/internal/verify"
)

const seed = 20261011

func corpus(t *testing.T) []string {
	t.Helper()
	f, err := os.Open("testdata/passages-en.txt")
	if err != nil {
		t.Fatal(err)
	}
	p, err := calibrate.ReadPassages(f)
	_ = f.Close() // read-only: a close error cannot lose data
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCorruptEveryKindChangesTheMeaningDeterministically(t *testing.T) {
	const p = "The Senate of the United States shall be composed of two Senators from each State, chosen by the Legislature thereof, for six Years; and each Senator shall have one Vote."
	for _, k := range calibrate.Corruptions {
		a, err := calibrate.Corrupt(p, k, rand.New(rand.NewPCG(1, 2)))
		if err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		b, _ := calibrate.Corrupt(p, k, rand.New(rand.NewPCG(1, 2)))
		if a != b {
			t.Fatalf("%s is not deterministic: %q, %q", k, a, b)
		}
		if verify.Equivalent(a, p) {
			t.Fatalf("%s left the passage equivalent: %q", k, a)
		}
		t.Logf("%s: %s", k, a)
	}
}

func TestCorruptKinds(t *testing.T) {
	rng := func() *rand.Rand { return rand.New(rand.NewPCG(3, 4)) }
	cases := []struct {
		k        calibrate.Kind
		in, want string
	}{
		{calibrate.ClauseDrop, "First part, second part.", "First part."},
		{calibrate.ClauseDrop, "One, two; three four.", ""}, // any but the first clause
		{calibrate.NumberChange, "It took 41 days.", ""},
		{calibrate.WordSwap, "The quick fox.", "The fox quick."},
	}
	for _, c := range cases {
		got, err := calibrate.Corrupt(c.in, c.k, rng())
		if err != nil {
			t.Fatalf("%s %q: %v", c.k, c.in, err)
		}
		if c.want != "" && got != c.want {
			t.Fatalf("%s %q = %q, want %q", c.k, c.in, got, c.want)
		}
		if !strings.HasPrefix(got, strings.Fields(c.in)[0]) || verify.Equivalent(got, c.in) {
			t.Fatalf("%s %q = %q", c.k, c.in, got)
		}
	}
	for k, in := range map[calibrate.Kind]string{
		calibrate.ClauseDrop:   "No comma here.",
		calibrate.NumberChange: "No numbers here.",
		calibrate.NameChange:   "nothing capitalised here.",
		calibrate.WordSwap:     "Two.",
	} {
		if _, err := calibrate.Corrupt(in, k, rng()); !errors.Is(err, calibrate.ErrNotApplicable) {
			t.Fatalf("%s %q: %v, want ErrNotApplicable", k, in, err)
		}
	}
}

func TestBuildMiniCalibrationCandidates(t *testing.T) {
	p := corpus(t)
	s, err := calibrate.Build(p, 20, 20, seed)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := calibrate.Build(p, 20, 20, seed)
	if !slices.Equal(s, again) {
		t.Fatal("Build is not deterministic")
	}
	if len(s) != 40 {
		t.Fatalf("%d samples", len(s))
	}
	kinds := map[calibrate.Kind]int{}
	ids := map[string]bool{}
	for _, x := range s {
		kinds[x.Kind]++
		ids[x.ID] = true
		if x.Corrupted() == verify.Equivalent(x.Rendered, x.Original) {
			t.Fatalf("%s %s: rendered %q, original %q", x.ID, x.Kind, x.Rendered, x.Original)
		}
	}
	want := map[calibrate.Kind]int{calibrate.Good: 20, calibrate.WordSwap: 4, calibrate.ClauseDrop: 4,
		calibrate.Preamble: 3, calibrate.NumberChange: 3, calibrate.NameChange: 3, calibrate.ContentWord: 3}
	if len(ids) != 40 || len(kinds) != len(want) {
		t.Fatalf("ids %d, kinds %v", len(ids), kinds)
	}
	for k, n := range want {
		if kinds[k] != n {
			t.Fatalf("kinds %v, want %v", kinds, want)
		}
	}
	// The anonymised order mixes the kinds: the first ten IDs are not all one construction.
	first := map[bool]int{}
	for _, x := range s[:10] {
		first[x.Corrupted()]++
	}
	if len(first) != 2 {
		t.Fatalf("IDs follow construction: %v", first)
	}
	if _, err := calibrate.Build(p, 30, 0, seed); err == nil {
		t.Fatal("more good candidates than passages was accepted")
	}
}

func TestSplitStratified6040(t *testing.T) {
	s, err := calibrate.Build(corpus(t), 20, 20, seed)
	if err != nil {
		t.Fatal(err)
	}
	prop, held := calibrate.Split(s, seed)
	prop2, held2 := calibrate.Split(slices.Clone(s), seed)
	if !slices.Equal(prop, prop2) || !slices.Equal(held, held2) {
		t.Fatal("Split is not deterministic")
	}
	if len(held) != 16 || len(prop) != 24 {
		t.Fatalf("held %d, proposal %d", len(held), len(prop))
	}
	per := map[calibrate.Kind]int{}
	for _, x := range held {
		per[x.Kind]++
	}
	want := map[calibrate.Kind]int{calibrate.Good: 8, calibrate.WordSwap: 2, calibrate.ClauseDrop: 2,
		calibrate.Preamble: 1, calibrate.NumberChange: 1, calibrate.NameChange: 1, calibrate.ContentWord: 1}
	for k, n := range want {
		if per[k] != n {
			t.Fatalf("held out per kind %v, want %v", per, want)
		}
	}
	all := append(slices.Clone(prop), held...)
	slices.SortFunc(all, func(a, b calibrate.Sample) int { return strings.Compare(a.ID, b.ID) })
	if !slices.Equal(all, s) {
		t.Fatal("the parts are not a partition of the samples")
	}
}

func TestWilson(t *testing.T) {
	cases := []struct {
		k, n   int
		lo, hi float64
	}{
		{0, 8, 0, 0.3244},
		{5, 10, 0.2366, 0.7634},
		{8, 8, 0.6756, 1},
		{0, 0, 0, 1},
	}
	for _, c := range cases {
		lo, hi := calibrate.Wilson(c.k, c.n)
		if math.Abs(lo-c.lo) > 1e-4 || math.Abs(hi-c.hi) > 1e-4 {
			t.Fatalf("Wilson(%d, %d) = %.4f–%.4f, want %.4f–%.4f", c.k, c.n, lo, hi, c.lo, c.hi)
		}
	}
}

// synth renders a take as long as its estimate that says exactly what it was asked to.
type synth struct{}

func (synth) Open(context.Context, realtime.SessionSpec) (realtime.SpeechSession, error) {
	return session{}, nil
}

type session struct{}

func (session) Render(_ context.Context, r realtime.RenderRequest) (realtime.Take, error) {
	return realtime.Take{PCM: rtfake.TaggedPCM(r.ChunkID, r.Attempt, r.Estimate), Said: r.Text, Status: realtime.StatusCompleted}, nil
}
func (session) Age() time.Duration { return 0 }
func (session) Close() error       { return nil }

// hears transcribes a take as the text mapped to its chunk ID.
type hears struct {
	mu    sync.Mutex
	texts map[string]string
}

func (h *hears) Transcribe(_ context.Context, pcm []byte, _ stt.Hint) (stt.Transcript, error) {
	id, _, ok := rtfake.PCMTag(pcm)
	if !ok {
		return stt.Transcript{}, errors.New("untagged take")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return stt.Transcript{Text: h.texts[id]}, nil
}

func noSleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }

func harness(t *testing.T) ([]calibrate.Sample, []pipeline.Task, pipeline.Config) {
	t.Helper()
	s, err := calibrate.Build(corpus(t), 20, 20, seed)
	if err != nil {
		t.Fatal(err)
	}
	identity := func(s string) (string, error) { return s, nil }
	tasks, err := calibrate.Tasks(s, realtime.SessionSpec{Model: "m", Voice: "v", Speed: 1}, identity)
	if err != nil {
		t.Fatal(err)
	}
	cfg := pipeline.Config{Jobs: 2, Verify: verify.OptionsFor(verify.ModeNormal), Synth: synth{}, Retry: openai.NewRetrier(1, noSleep)}
	return s, tasks, cfg
}

// An honest transcriber hears what was rendered: every corrupted take is rejected, every good
// take accepted, and each was transcribed.
func TestRunHonestTranscriber(t *testing.T) {
	s, tasks, cfg := harness(t)
	h := &hears{texts: map[string]string{}}
	for _, x := range s {
		h.texts[x.ID] = x.Rendered
	}
	cfg.Transcriber = h
	kept := 0
	recs, err := calibrate.Run(context.Background(), nil, cfg, s, tasks, func(_ calibrate.Sample, r verify.ChunkResult) error {
		if len(r.PCM) > 0 {
			kept++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if kept != len(s) {
		t.Fatalf("audio of %d takes kept, want %d", kept, len(s))
	}
	for _, r := range recs {
		if !r.Reached || r.Accepted == r.Corrupted() || len(r.Heard) == 0 {
			t.Fatalf("%s %s: reached %v accepted %v heard %q (%s)", r.ID, r.Kind, r.Reached, r.Accepted, r.Heard, r.Rejection)
		}
		if r.SaidMismatch != r.Corrupted() {
			t.Fatalf("%s %s: said mismatch %v", r.ID, r.Kind, r.SaidMismatch)
		}
	}
}

// accept: VER-07#1
func TestRunFailsWithPerfectMatchStubSTT(t *testing.T) {
	s, tasks, cfg := harness(t)
	stub := &hears{texts: map[string]string{}}
	for _, x := range s {
		stub.texts[x.ID] = x.Original // a "perfect match" whatever was rendered
	}
	cfg.Transcriber = stub
	_, err := calibrate.Run(context.Background(), nil, cfg, s, tasks, nil)
	if !errors.Is(err, calibrate.ErrNotReached) {
		t.Fatalf("a stub STT reporting a perfect match: %v, want ErrNotReached", err)
	}
	for _, x := range s {
		if x.Corrupted() && !strings.Contains(err.Error(), x.ID+" ") {
			t.Fatalf("the error does not name corrupted sample %s: %v", x.ID, err)
		}
	}

	// With the said guard back in place, corrupted takes never reach STT: that fails too.
	honest := &hears{texts: map[string]string{}}
	for _, x := range s {
		honest.texts[x.ID] = x.Rendered
	}
	cfg.Transcriber = honest
	saidOn := func(ctx context.Context, c pipeline.Config, ts []pipeline.Task) (pipeline.Result, error) {
		c.Verify.BypassSaid = false
		return pipeline.Run(ctx, c, ts)
	}
	if _, err := calibrate.Run(context.Background(), saidOn, cfg, s, tasks, nil); !errors.Is(err, calibrate.ErrNotReached) {
		t.Fatalf("said guard not bypassed: %v, want ErrNotReached", err)
	}
}

func TestShardFitsBudget(t *testing.T) {
	_, tasks, _ := harness(t)
	if got := calibrate.Shard(tasks, 30*time.Minute); len(got) != 1 || len(got[0]) != 40 {
		t.Fatalf("the mini calibration should fit one 30-minute shard: %d shards", len(got))
	}
	parts := calibrate.Shard(tasks, time.Minute)
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	if len(parts) < 2 || n != 40 {
		t.Fatalf("%d shards holding %d tasks", len(parts), n)
	}
}

func TestLabelsTemplateAndParse(t *testing.T) {
	held := []calibrate.Sample{{ID: "s01", Kind: calibrate.Good, Original: "A passage."}, {ID: "s02", Kind: calibrate.WordSwap, Original: "Another passage."}}
	var b bytes.Buffer
	if err := calibrate.WriteLabelsTemplate(&b, held, "/clips", ""); err != nil {
		t.Fatal(err)
	}
	tpl := b.String()
	if !strings.HasPrefix(tpl, "# recite calibration labels (VER-07)\n") {
		t.Fatalf("mini-run header:\n%s", tpl)
	}
	var full bytes.Buffer
	if err := calibrate.WriteLabelsTemplate(&full, held, "/clips", "VER-11 full calibration, han"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(full.String(), "# recite calibration labels (VER-11 full calibration, han)\n") {
		t.Fatalf("full-run header:\n%s", full.String())
	}
	if strings.Contains(tpl, string(calibrate.WordSwap)) || !strings.Contains(tpl, "# s02: Another passage.") {
		t.Fatalf("template:\n%s", tpl)
	}
	if _, err := calibrate.ParseLabels(strings.NewReader(tpl)); err == nil {
		t.Fatal("an unfilled template parsed")
	}
	filled := strings.Replace(strings.Replace(tpl, "s01\t", "s01\tgood", 1), "s02\t", "s02 bad", 1)
	got, err := calibrate.ParseLabels(strings.NewReader(filled))
	if err != nil {
		t.Fatal(err)
	}
	if got.ByID["s01"] != calibrate.LabelGood || got.ByID["s02"] != calibrate.LabelCorrupted || len(got.ByID) != 2 || got.Labeller != "" {
		t.Fatalf("labels %+v", got)
	}
	machine, err := calibrate.ParseLabels(strings.NewReader("#! labeller: whisper.cpp large-v3-turbo\n" + filled))
	if err != nil {
		t.Fatal(err)
	}
	if machine.Labeller != "whisper.cpp large-v3-turbo" || len(machine.ByID) != 2 {
		t.Fatalf("machine labels %+v", machine)
	}
	for _, bad := range []string{
		"s01 maybe\n", "s01 good\ns01 bad\n", "s01 good extra\n",
		"#! labeller: a\n#! labeller: b\ns01 good\n", "#! labeller:   \ns01 good\n",
	} {
		if _, err := calibrate.ParseLabels(strings.NewReader(bad)); err == nil {
			t.Fatalf("%q parsed", bad)
		}
	}
}

func rec(id string, k calibrate.Kind, accepted bool, score float64) calibrate.Record {
	return calibrate.Record{Sample: calibrate.Sample{ID: id, Kind: k, Original: "o", Rendered: "r"}, Reached: true, Accepted: accepted, Score: score, SaidMismatch: k != calibrate.Good}
}

func TestEvaluateCriteria(t *testing.T) {
	var held []calibrate.Record
	labels := map[string]calibrate.Label{}
	for i := range 20 {
		id := string(rune('a' + i))
		if i < 10 {
			held = append(held, rec(id, calibrate.Good, true, 1))
			labels[id] = calibrate.LabelGood
		} else {
			held = append(held, rec(id, calibrate.ClauseDrop, false, 0.7))
			labels[id] = calibrate.LabelCorrupted
		}
	}
	ev, err := calibrate.Evaluate(held, labels)
	if err != nil || ev.Check() != nil || len(ev.Disagreements()) != 0 {
		t.Fatalf("clean run: %v, %v", err, ev.Check())
	}

	// A good clip rejected: 1 of 10 is over 5%.
	held[0].Accepted = false
	ev, _ = calibrate.Evaluate(held, labels)
	if ev.FalseRejects != 1 || !errors.Is(ev.Check(), calibrate.ErrCriteria) {
		t.Fatalf("false reject: %+v %v", ev.FalseRejects, ev.Check())
	}
	held[0].Accepted = true

	// A corrupted clip the listener heard as good is a disagreement, not a false accept when
	// it is accepted; a corrupted-labelled accepted clip is a false accept.
	labels["k"] = calibrate.LabelGood
	held[10].Accepted = true
	ev, _ = calibrate.Evaluate(held, labels)
	if ev.FalseAccepts != 0 || len(ev.Disagreements()) != 1 || ev.Check() != nil {
		t.Fatalf("disagreement: FA %d, %d disagreements, %v", ev.FalseAccepts, len(ev.Disagreements()), ev.Check())
	}
	labels["k"] = calibrate.LabelCorrupted
	ev, _ = calibrate.Evaluate(held, labels)
	if ev.FalseAccepts != 1 || !errors.Is(ev.Check(), calibrate.ErrCriteria) {
		t.Fatalf("false accept: %d, %v", ev.FalseAccepts, ev.Check())
	}

	delete(labels, "a")
	if _, err := calibrate.Evaluate(held, labels); err == nil {
		t.Fatal("an unlabelled clip was accepted")
	}
	labels["a"], labels["zz"] = calibrate.LabelGood, calibrate.LabelGood
	if _, err := calibrate.Evaluate(held, labels); err == nil {
		t.Fatal("a label for an unknown clip was accepted")
	}
	if err := (calibrate.Evaluation{}).Check(); err == nil {
		t.Fatal("an empty evaluation passed")
	}
}

func reportEvaluation(t *testing.T) calibrate.Evaluation {
	t.Helper()
	held := []calibrate.Record{rec("s01", calibrate.Good, true, 1), rec("s02", calibrate.NameChange, false, 0.8), rec("s03", calibrate.WordSwap, true, 0.96)}
	labels := map[string]calibrate.Label{"s01": calibrate.LabelGood, "s02": calibrate.LabelCorrupted, "s03": calibrate.LabelGood}
	ev, err := calibrate.Evaluate(held, labels)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func TestWriteReport(t *testing.T) {
	ev := reportEvaluation(t)
	date := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	var b bytes.Buffer
	err := calibrate.WriteReport(&b, calibrate.Report{
		Date: date, Source: "test corpus", Seed: 7, Mode: verify.ModeNormal, Thresholds: verify.ModeNormal.Thresholds(),
		Propose: []calibrate.Record{rec("s04", calibrate.Good, true, 0.99)}, HeldOut: ev,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if calibrate.ReportName(date) != "calibration-20261011.md" {
		t.Fatal(calibrate.ReportName(date))
	}
	for _, want := range []string{
		"**Held-out verdict: PASS**", "## Method", "test corpus", "seed 7", "## Human labels",
		"| s02 | name-change | corrupted | rejected | 0.8000 |", "## Per-type results (held out)",
		"| good | 1 of 1 | 100.0% | 0.21–1.00 |", "## Said guard", "## Construction vs human disagreements",
		"| s03 | word-swap | good | r |", "| False rejects (of clips labelled good) | 0 of 2 | 0.0% | 0.00–0.66 |",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("report lacks %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"machine", "Machine"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("a human-labelled report mentions %q:\n%s", unwanted, out)
		}
	}
}

// A machine-labelled report (design D22) says so and never claims listening.
func TestWriteReportMachineLabelled(t *testing.T) {
	var b bytes.Buffer
	err := calibrate.WriteReport(&b, calibrate.Report{
		Date: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), Source: "test corpus", Seed: 7, Mode: verify.ModeNormal,
		Thresholds: verify.ModeNormal.Thresholds(), HeldOut: reportEvaluation(t), Labeller: "whisper.cpp large-v3-turbo",
	})
	if err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"**Held-out verdict (machine-labelled): PASS**",
		"machine labels from whisper.cpp large-v3-turbo", "weaker evidence than human listening", "a human labelling pass supersedes them",
		"## Machine labels (held out)\n\n| Clip | Construction | Machine label |",
		"## Construction vs machine-label disagreements\n\n| Clip | Construction | Machine label | Rendered text |",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("report lacks %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"Human label", "come from listening", "**Held-out verdict: "} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("a machine-labelled report contains %q:\n%s", unwanted, out)
		}
	}
}

// A real recogniser sometimes hears a corrupted take as the original: the live mini run had
// "only four the of nine" heard as "four of the nine", and the model read famous passages from
// memory. Those samples are what calibration measures (the owner's labels settle them), so one
// does not fail the run. A transcriber that does not listen hears every corrupted take as the
// original, so the run fails when more than half of them are.
func TestHeardAsOriginalFailsOnlyWhenMostAre(t *testing.T) {
	rec := func(id string, heardOriginal bool) calibrate.Record {
		r := calibrate.Record{
			Sample:  calibrate.Sample{ID: id, Kind: calibrate.WordSwap, Original: "only four of the nine", Rendered: "only four the of nine"},
			Reached: true, Heard: []string{"only four the of nine"},
		}
		if heardOriginal {
			r.Heard = []string{"only four of the nine"}
		}
		return r
	}
	some := []calibrate.Record{rec("s01", true), rec("s02", false), rec("s03", false)}
	if err := calibrate.CheckReached(some); err != nil {
		t.Fatalf("one of three heard as the original: %v", err)
	}
	most := []calibrate.Record{rec("s01", true), rec("s02", true), rec("s03", false)}
	if err := calibrate.CheckReached(most); !errors.Is(err, calibrate.ErrNotReached) {
		t.Fatalf("two of three heard as the original: %v, want ErrNotReached", err)
	}
}
