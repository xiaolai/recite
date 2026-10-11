package cli

import (
	"encoding/json"
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

const testLexicon = `"SQL" = { say = "sequel" }
"Okafor" = { hint = "oh-KAH-for" }
"重庆" = { hint = "chóng qìng" }
`

func writeLexicon(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "lexicon.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A say entry rewrites the spoken form the model gets, and the verifier checks the take
// against that rewrite. The entry declares term and rewrite equal, so a transcript that
// writes the term back (STT inverse normalisation) verifies too; another word does not.
// accept: SPK-08#2
func TestSayLexiconSayVerifiedAgainstRewrite(t *testing.T) {
	lex := writeLexicon(t, testLexicon)
	m := newMultiEnv(t)
	r := m.run("Query the SQL table, not MySQL.", "--lexicon", lex, "-o", "out.wav", "--jobs", "1")
	if r.code != exitcode.OK {
		t.Fatalf("code %d stderr %q", r.code, r.stderr)
	}
	calls := m.synth.Calls()
	if len(calls) != 1 || calls[0].Request.Text != "Query the sequel table, not MySQL." {
		t.Fatalf("sent %+v", calls)
	}
	tk := m.lastTasks()[0]
	if len(tk.Chunk.Protected) != 1 || tk.Chunk.Spoken[tk.Chunk.Protected[0].Start:tk.Chunk.Protected[0].End] != "sequel" {
		t.Errorf("protected %+v of %q", tk.Chunk.Protected, tk.Chunk.Spoken)
	}
	if calls[0].Request.Style != "" {
		t.Errorf("a say entry added instructions %q", calls[0].Request.Style)
	}

	if eq := tk.Chunk.Equivalents; len(eq) != 1 || eq[0].Written != "SQL" || eq[0].Spoken != "sequel" {
		t.Errorf("equivalents %+v", eq)
	}

	// STT writes the term back: the take is correct and verifies.
	m = newMultiEnv(t)
	m.tr.mishear("c0001", "Query the sql table, not MySQL.")
	if r := m.run("Query the SQL table, not MySQL.", "--lexicon", lex, "-o", "back.wav", "--jobs", "1", "--max-attempts", "1"); r.code != exitcode.OK {
		t.Fatalf("a transcript spelling the term failed: %+v", r)
	}

	// Another word is still a wrong take.
	m = newMultiEnv(t)
	m.tr.mishear("c0001", "Query the sickle table, not MySQL.")
	r = m.run("Query the SQL table, not MySQL.", "--lexicon", lex, "-o", "bad.wav", "--jobs", "1", "--max-attempts", "1")
	if r.code == exitcode.OK {
		t.Fatalf("a wrong word verified: %+v", r)
	}
}

// A hint entry leaves the text alone and adds the pronunciation line to that chunk's
// instructions only; the pairs a chunk uses are part of its cache key.
// accept: SPK-08#1
func TestSayLexiconHintInstructions(t *testing.T) {
	lex := writeLexicon(t, testLexicon)
	m := newMultiEnv(t)
	in := "Dr. Okafor arrived.\n\nShe left for 重庆 later.\n\nNothing here."
	r := m.run(in, "--lexicon", lex, "-o", "out.wav", "--jobs", "1")
	if r.code != exitcode.OK {
		t.Fatalf("code %d stderr %q", r.code, r.stderr)
	}
	calls := m.synth.Calls()
	if len(calls) != 3 {
		t.Fatalf("%d renders", len(calls))
	}
	byID := map[string]string{}
	for _, c := range calls {
		byID[c.Request.ChunkID] = c.Request.Style
		if strings.Contains(c.Request.Text, "oh-KAH") || strings.Contains(c.Request.Text, "chóng") {
			t.Errorf("a hint changed the text: %q", c.Request.Text)
		}
	}
	if byID["c0001"] != `Pronounce "Okafor" as "oh-KAH-for".` || byID["c0002"] != `Pronounce "重庆" as "chóng qìng".` || byID["c0003"] != "" {
		t.Errorf("instructions %q", byID)
	}
	keyed := m.lastTasks()

	// Without the lexicon the chunks with a hint get other keys; the plain one keeps its key.
	m = newMultiEnv(t)
	if r := m.run(in, "-o", "out2.wav", "--jobs", "1"); r.code != exitcode.OK {
		t.Fatalf("%+v", r)
	}
	plain := m.lastTasks()
	if keyed[0].CacheKey == plain[0].CacheKey || keyed[1].CacheKey == plain[1].CacheKey || keyed[2].CacheKey != plain[2].CacheKey {
		t.Errorf("cache keys do not follow the pairs used")
	}
	// The hint term reaches verification as a protected keyword.
	if got := keyed[0].Chunk.Lexicon; len(got) != 1 || got[0] != "Okafor" {
		t.Errorf("verify lexicon %q", got)
	}
}

// Entries inside speech marks are ignored: the mark decides the span.
// accept: SPK-08#1
func TestSayLexiconIgnoresMarks(t *testing.T) {
	lex := writeLexicon(t, testLexicon)
	m := newMultiEnv(t)
	r := m.run("Keep {{verbatim|SQL}} and {{hint:oh-ka-FOR|Okafor}}, then SQL.", "--marks", "--lexicon", lex, "-o", "out.wav", "--jobs", "1")
	if r.code != exitcode.OK {
		t.Fatalf("code %d stderr %q", r.code, r.stderr)
	}
	c := m.synth.Calls()[0]
	if c.Request.Text != "Keep SQL and Okafor, then sequel." {
		t.Errorf("sent %q", c.Request.Text)
	}
	if c.Request.Style != "Pronounce Okafor as oh-ka-FOR." {
		t.Errorf("instructions %q: the lexicon hint must not apply inside a mark", c.Request.Style)
	}
}

// The plan lists the lexicon pairs each chunk used, in text order.
func TestPlanListsLexiconPairs(t *testing.T) {
	lex, err := spoken.ParseLexiconText(`{"Okafor":{"hint":"oh-KAH-for"},"SQL":{"say":"sequel"},"Unused":{"say":"x"}}`)
	if err != nil {
		t.Fatal(err)
	}
	st, err := runTextStages("Okafor runs SQL.", guard.Options{}, spoken.Options{Lexicon: lex}, 25, realtime.DefaultSettings())
	if err != nil || len(st.plan.Chunks) != 1 {
		t.Fatalf("%v %+v", err, st.plan)
	}
	want := []pipeline.Hint{{Term: "Okafor", Hint: "oh-KAH-for"}, {Term: "SQL", Say: "sequel"}}
	if got := st.plan.Chunks[0].Hints; !reflect.DeepEqual(got, want) {
		t.Errorf("hints %+v", got)
	}
}

// The lexicon comes from the config file, a profile, front matter or --lexicon, each layer
// replacing the one below; a bad entry is exit 2 naming the term.
func TestLexiconSources(t *testing.T) {
	userConfig(t, "[lexicon]\nSQL = { say = \"ess queue ell\" }\n\n[profile.dev.lexicon]\nSQL = { say = \"sequel\" }\n")
	show := func(args ...string) settingRow {
		t.Helper()
		r := run(append([]string{"config", "show", "--json"}, args...)...)
		if r.code != exitcode.OK {
			t.Fatalf("%v: %+v", args, r)
		}
		var out struct{ Settings []settingRow }
		if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
			t.Fatal(err)
		}
		for _, row := range out.Settings {
			if row.Key == "lexicon" {
				return row
			}
		}
		t.Fatal("no lexicon row")
		return settingRow{}
	}
	if row := show(); row.Value != `{"SQL":{"say":"ess queue ell"}}` || row.Layer != "user" {
		t.Errorf("user: %+v", row)
	}
	if row := show("--profile", "dev"); row.Value != `{"SQL":{"say":"sequel"}}` || row.Layer != "profile" {
		t.Errorf("profile: %+v", row)
	}
	lex := writeLexicon(t, "Okafor = { hint = \"oh-KAH-for\" }\n")
	if row := show("--lexicon", lex); row.Value != `{"Okafor":{"hint":"oh-KAH-for"}}` || row.Layer != "flag" || row.Origin != "--lexicon" {
		t.Errorf("flag: %+v", row)
	}
	for _, bad := range []string{
		"SQL = \"sequel\"\n",
		"SQL = { say = \"sequel\", hint = \"x\" }\n",
		"SQL = { sya = \"sequel\" }\n",
		"\"\" = { say = \"x\" }\n",
		"Okafor = { hint = \"oh \\\"KAH\\\"\" }\n",
		"Xi = { hint = \"shee\", lang = \"??\" }\n",
	} {
		r := run("config", "show", "--lexicon", writeLexicon(t, bad))
		if r.code != exitcode.Usage || !strings.Contains(r.stderr, "lexicon") {
			t.Errorf("%q: %+v", bad, r)
		}
	}
	if r := run("config", "show", "--lexicon", filepath.Join(t.TempDir(), "missing.toml")); r.code != exitcode.Usage {
		t.Errorf("missing file: %+v", r)
	}
}

// A prepared spoken script already holds the say rewrites, so rendering it with the same
// lexicon applies only the hints, and plans the same spoken forms, instructions and keys.
func TestPreparedScriptKeepsLexiconPlan(t *testing.T) {
	lex, err := spoken.ParseLexiconText(`{"Okafor":{"hint":"oh-KAH-for"},"SQL":{"say":"sequel"},"sequel":{"say":"WRONG"}}`)
	if err != nil {
		t.Fatal(err)
	}
	so := spoken.Options{Lexicon: lex}
	st, err := runTextStages("Okafor runs SQL.\n\nThen SQL again.", guard.Options{}, so, 25, realtime.DefaultSettings())
	if err != nil || !st.planned {
		t.Fatal(err)
	}
	script, err := spokenScript(st, false)
	if err != nil {
		t.Fatal(err)
	}
	back, _, err := spokenScriptPlan(script, true, so, planSettings(realtime.DefaultSettings()), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Chunks) != len(st.plan.Chunks) {
		t.Fatalf("%d vs %d chunks", len(back.Chunks), len(st.plan.Chunks))
	}
	for i, c := range st.plan.Chunks {
		b := back.Chunks[i]
		if c.Spoken != b.Spoken || c.CacheKey != b.CacheKey || !reflect.DeepEqual(c.Instructions, b.Instructions) {
			t.Errorf("chunk %d: %q %q %v vs %q %q %v", i, c.Spoken, c.CacheKey, c.Instructions, b.Spoken, b.CacheKey, b.Instructions)
		}
	}
	if got := st.plan.Chunks[0]; got.Spoken != "Okafor runs sequel." || len(got.Instructions) != 1 || got.Instructions[0] != `Pronounce "Okafor" as "oh-KAH-for".` {
		t.Errorf("chunk 1 %q %q", got.Spoken, got.Instructions)
	}
}
