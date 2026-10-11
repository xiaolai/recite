package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/guard"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/spoken"
)

// prepareSample has a guard finding, spoken-form rewrites, every kind of mark that survives
// into a script, a pause, and a paragraph long enough to be cut at sentences.
const prepareSample = "Paid $3.5M on 2026-10-11 🎉. Read {{spell|API}} as {{say:sequel|SQL}}, code {{digits|42}}." +
	"{{pause:600ms}} Then {{emph|stop}} and say {{lang:fr|bonjour}} to {{hint:oh-KAH-for|Okafor}}.\n\n" +
	"Take {{fraction|1/2}} of the cake and {{verbatim|R2-D2}}. This sentence is long enough that a small budget " +
	"cuts the paragraph here. Another sentence follows it, and then the paragraph ends.\n\n" +
	"The third paragraph is short."

// prepareArgs are the text flags both runs of the round trip share.
var prepareArgs = []string{"--marks", "--chunk-seconds", "6"}

func prepareFile(t *testing.T, dir, in string, args ...string) (string, result) {
	t.Helper()
	src := filepath.Join(dir, "in.txt")
	if err := os.WriteFile(src, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "in.spoken.txt")
	r := run(append(append([]string{"prepare"}, args...), "-o", out, src)...)
	if r.code != exitcode.OK {
		t.Fatalf("prepare: %+v", r)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(b), r
}

// The spoken script is a pure function of the input: golden, and identical on a re-run.
// accept: CLI-15#1
func TestPrepareGolden(t *testing.T) {
	dir := t.TempDir()
	got, _ := prepareFile(t, dir, prepareSample, prepareArgs...)
	golden(t, "prepare", got)
	if err := os.Remove(filepath.Join(dir, "in.spoken.txt")); err != nil {
		t.Fatal(err)
	}
	again, _ := prepareFile(t, dir, prepareSample, prepareArgs...)
	if again != got {
		t.Fatal("a second prepare differs")
	}
	withF, _ := prepareFile(t, t.TempDir(), prepareSample, append([]string{"--with-findings"}, prepareArgs...)...)
	golden(t, "prepare-findings", withF)
	// -o - writes to stdout; an existing output needs --force.
	src := filepath.Join(dir, "in.txt")
	if r := run("prepare", "--marks", "-o", "-", src); r.code != exitcode.OK || !strings.HasPrefix(r.stdout, scriptHeader) {
		t.Fatalf("stdout: %+v", r)
	}
	if r := run("prepare", "--marks", "-o", filepath.Join(dir, "in.spoken.txt"), src); r.code != exitcode.Usage || !strings.Contains(r.stderr, "--force") {
		t.Fatalf("existing output: %+v", r)
	}
	// Without --marks the braces are text, which a script would read back as marks: refused.
	if r := run("prepare", "-o", filepath.Join(dir, "y.txt"), src); r.code != exitcode.Usage || !strings.Contains(r.stderr, "would read as a speech mark") {
		t.Fatalf("braces without --marks: %+v", r)
	}
	if r := run("prepare", src); r.code != exitcode.Usage || !strings.Contains(r.stderr, "-o PATH is required") {
		t.Fatalf("no -o: %+v", r)
	}
	if r := run("prepare", "--guard", "strict", "-o", filepath.Join(dir, "x.txt"), src); r.code != exitcode.Usage {
		t.Fatalf("strict with findings: %+v", r)
	}
}

// The round trip: `prepare`, then `say --input-format spoken` on the unedited script, plans
// the same chunks as `say` on the original — the same spoken forms, cache keys, instructions,
// protected spans, joins and pauses.
// accept: TXT-15#1
// accept: CLI-15#1
func TestPrepareRoundTripSamePlan(t *testing.T) {
	dir := t.TempDir()
	script, _ := prepareFile(t, dir, prepareSample, prepareArgs...)

	g := guard.Options{Marks: true}
	st, err := runTextStages(prepareSample, g, spoken.Options{}, 6, realtime.DefaultSettings())
	if err != nil || !st.planned {
		t.Fatal(err)
	}
	orig := st.plan
	back, _, err := spokenScriptPlan(script, true, spoken.Options{}, planSettings(realtime.DefaultSettings()), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(orig.Chunks) < 5 {
		t.Fatalf("the sample makes only %d chunks; it must exercise sentence cuts and a pause", len(orig.Chunks))
	}
	type key struct {
		Spoken, Display, CacheKey string
		Instructions              []string
		Protected                 []spoken.Range
		Boundary                  string
		Pause                     int64
	}
	keys := func(p pipeline.Plan) []key {
		var out []key
		for _, c := range p.Chunks {
			out = append(out, key{c.Spoken, c.Display, c.CacheKey, c.Instructions, c.Protected, string(c.Boundary), int64(c.Pause)})
		}
		return out
	}
	if a, b := keys(orig), keys(back); !reflect.DeepEqual(a, b) {
		t.Fatalf("plans differ:\noriginal %+v\nscript   %+v", a, b)
	}

	// End to end: the tasks `say` hands the scheduler are the same both ways.
	m := newMultiEnv(t)
	src, spk := m.path("in.txt"), m.path("in.spoken.txt")
	for p, body := range map[string]string{src: prepareSample, spk: script} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if r := m.run(append([]string{"-f", src, "-o", "a.wav"}, prepareArgs...)...); r.code != exitcode.OK {
		t.Fatalf("say original: %+v", r)
	}
	a := m.lastTasks()
	if r := m.run("-f", spk, "--input-format", "spoken", "-o", "b.wav"); r.code != exitcode.OK {
		t.Fatalf("say spoken: %+v", r)
	}
	b := m.lastTasks()
	if len(a) != len(b) {
		t.Fatalf("%d tasks, then %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Chunk.Spoken != b[i].Chunk.Spoken || a[i].CacheKey != b[i].CacheKey || a[i].Style != b[i].Style ||
			!reflect.DeepEqual(a[i].Chunk.Protected, b[i].Chunk.Protected) {
			t.Errorf("task %d: %+v\nvs %+v", i, a[i], b[i])
		}
	}
}

// A hand edit to one paragraph of the script re-renders only that chunk: every other chunk
// keeps its cache key and comes from the take cache.
// accept: TXT-15#2
func TestSpokenScriptEditRerendersOneChunk(t *testing.T) {
	script, _ := prepareFile(t, t.TempDir(), prepareSample, prepareArgs...)
	m := newMultiEnv(t)
	m.env = map[string]string{"RECITE_CACHE_DIR": m.path("cache")}
	spk := m.path("in.spoken.txt")
	if err := os.WriteFile(spk, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := m.run("-f", spk, "--input-format", "spoken", "-o", "a.wav"); r.code != exitcode.OK {
		t.Fatalf("first: %+v", r)
	}
	first := m.lastTasks()
	renders := len(m.synth.Calls())
	if renders != len(first) {
		t.Fatalf("%d renders for %d chunks", renders, len(first))
	}
	// The text line, not its display comment, is what is spoken.
	edited := strings.Replace(script, "\nThe third paragraph is short.\n", "\nThe third paragraph is shorter now.\n", 1)
	if edited == script {
		t.Fatal("the edit did not apply")
	}
	if err := os.WriteFile(spk, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := m.run("-f", spk, "--input-format", "spoken", "-o", "b.wav"); r.code != exitcode.OK {
		t.Fatalf("second: %+v", r)
	}
	calls := m.synth.Calls()[renders:]
	last := first[len(first)-1].Chunk.ID
	if len(calls) != 1 || calls[0].Request.ChunkID != last || calls[0].Request.Text != "The third paragraph is shorter now." {
		t.Fatalf("re-rendered %+v, want only %s", calls, last)
	}
}

// check reads a spoken script too: its plan, with no guard findings.
func TestCheckSpokenScript(t *testing.T) {
	script, _ := prepareFile(t, t.TempDir(), prepareSample, prepareArgs...)
	run := offline(t)
	in := writeInput(t, script)
	r := run("--input-format", "spoken", in)
	if r.code != exitcode.OK || !strings.Contains(r.stdout, "findings:\n  none\n") || !strings.Contains(r.stdout, "pause") {
		t.Fatalf("%+v", r)
	}
	bad := writeInput(t, "#| colour: red\nHello.\n")
	if r := run("--input-format", "spoken", bad); r.code != exitcode.Usage || !strings.Contains(r.stderr, "unknown comment key") {
		t.Fatalf("bad script: %+v", r)
	}
}
