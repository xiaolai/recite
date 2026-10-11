package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/text"
)

func writeNamedInput(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const frontMatterDoc = `+++
title = "Field notes"
voice = "marin"
speed = 1.2
effort = "low"
lang = "fr"
+++
# Field notes

Bonjour.
`

// config show --input reports each front-matter value with the source "front matter", at
// its line of the input; the environment still wins over it, and it wins over the user file.
// accept: TXT-08#1
func TestConfigShowFrontMatter(t *testing.T) {
	userConfig(t, "voice = \"alloy\"\nspeed = 0.9\n")
	t.Setenv("RECITE_EFFORT", "medium")
	doc := writeNamedInput(t, "notes.md", frontMatterDoc)
	r := run("config", "show", "--input", doc, "--json")
	if r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	var out struct{ Settings []settingRow }
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
		t.Fatal(err)
	}
	got := map[string]settingRow{}
	for _, row := range out.Settings {
		got[row.Key] = row
	}
	for _, want := range []settingRow{
		{Key: "title", Value: "Field notes", Layer: "front matter", Origin: doc + ":2"},
		{Key: "voice", Value: "marin", Layer: "front matter", Origin: doc + ":3"},
		{Key: "speed", Value: "1.2", Layer: "front matter", Origin: doc + ":4"},
		{Key: "lang", Value: "fr", Layer: "front matter", Origin: doc + ":6"},
		{Key: "effort", Value: "medium", Layer: "env", Origin: "RECITE_EFFORT"},
	} {
		if got[want.Key] != want {
			t.Errorf("%s: %+v, want %+v", want.Key, got[want.Key], want)
		}
	}
	// The text table names the source the same way.
	r = run("config", "show", "--input", doc)
	if !strings.Contains(r.stdout, "front matter ("+doc+":3)") {
		t.Errorf("text table:\n%s", r.stdout)
	}
}

// Front matter reaches the request `say` builds: voice, speed and effort as settings, lang as
// the language of the text.
func TestFrontMatterReachesSay(t *testing.T) {
	doc := writeNamedInput(t, "notes.md", frontMatterDoc)
	r := run("say", "-f", doc, "-o", "x.wav")
	if r.code != ExitOK || len(r.says) != 1 {
		t.Fatalf("%+v", r)
	}
	got := r.says[0]
	if got.Settings.Voice != "marin" || got.Settings.Speed != 1.2 || got.Settings.Effort != "low" || got.Lang != "fr" || got.InputFormat != formatMarkdown {
		t.Fatalf("%+v", got)
	}
	// A flag still wins.
	if r := run("say", "-f", doc, "--voice", "ash"); r.says[0].Settings.Voice != "ash" {
		t.Fatalf("flag lost: %+v", r.says[0].Settings)
	}
	// Plain text has no front matter: the block is just text.
	txt := writeNamedInput(t, "notes.txt", frontMatterDoc)
	if r := run("say", "-f", txt); r.says[0].Settings.Voice != "cedar" || r.says[0].InputFormat != formatText {
		t.Fatalf("text input read front matter: %+v", r.says[0])
	}
}

func TestFrontMatterErrorsExit2(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"+++\njobs = 4\n+++\nHi.\n", `notes.md:2: key "jobs" cannot be set in front matter (allowed: title, voice, effort, speed, lang)`},
		{"+++\nstyle = \"warm\"\n+++\nHi.\n", `notes.md:2: key "style" is not supported by this version`},
		{"+++\nlexicon = {}\n+++\nHi.\n", `key "lexicon" is not supported by this version`},
		{"+++\nvoices = {}\n+++\nHi.\n", `key "voices" is not supported by this version`},
		{"+++\n\nvoice = \"nobody\"\n+++\nHi.\n", `notes.md:3: voice:`},
		{"+++\nlang = \"???\"\n+++\nHi.\n", `want a BCP 47 language tag`},
		{"+++\nvoice = \"marin\"\nHi.\n", "never closed"},
	} {
		doc := writeNamedInput(t, "notes.md", tc.body)
		for _, cmd := range [][]string{{"config", "show", "--input", doc}, {"say", "-f", doc}, {"check", doc}} {
			r := run(cmd...)
			if r.code != ExitUsage || !strings.Contains(r.stderr, tc.want) || len(r.says) != 0 {
				t.Errorf("%v %q: %+v", cmd[0], tc.body, r)
			}
		}
	}
	if r := run("config", "show", "--input", writeNamedInput(t, "a.txt", "Hi.")); r.code != ExitUsage || !strings.Contains(r.stderr, "not Markdown") {
		t.Errorf("text input: %+v", r)
	}
}

const markdownSample = "# Setup\n\nInstall the tool.\n\n```sh\nbrew install recite\n```\n\n- First step.\n- Second step.\n\n<div>raw</div>\n\n## Use\n\nRun it.\n"

// check plans a Markdown file by its blocks: headings are sections, list items list joins, and
// the code and HTML are skipped with a counted warning.
// accept: TXT-05#1
func TestCheckMarkdownPlan(t *testing.T) {
	doc := writeNamedInput(t, "guide.md", markdownSample)
	r := run("check", doc, "--json")
	if r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(r.stderr, "recite check: markdown: skipped 1 code block, 1 HTML element") {
		t.Errorf("stderr %q", r.stderr)
	}
	var rep CheckReport
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range rep.Plan.Items {
		got = append(got, it.Display+" | "+it.Boundary)
	}
	want := []string{
		"Setup | paragraph", "Install the tool. | paragraph", "First step. | list-item",
		"Second step. | section", "Use | paragraph", "Run it. | end",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("plan:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// -q silences the warning; --code announce speaks the placeholder instead.
	if r := run("-q", "check", doc); strings.Contains(r.stderr, "skipped") {
		t.Errorf("-q: %q", r.stderr)
	}
	r = run("check", doc, "--json", "--code", "announce")
	if !strings.Contains(r.stdout, text.CodeAnnouncement) {
		t.Errorf("announce: %s", r.stdout)
	}
	if r := run("check", doc, "--code", "read"); r.code != ExitUsage {
		t.Errorf("--code read: %+v", r)
	}
}

// A Markdown file renders end to end: only the speech text reaches the model, and the
// manifest records the format.
// accept: TXT-05#1
func TestSayMarkdownRenders(t *testing.T) {
	m := newMultiEnv(t)
	if err := os.WriteFile(m.path("guide.md"), []byte(markdownSample), 0o600); err != nil {
		t.Fatal(err)
	}
	r := m.run("-f", "guide.md", "-o", "a.wav")
	if r.code != exitcode.OK {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(r.stderr, "markdown: skipped 1 code block, 1 HTML element") {
		t.Errorf("stderr %q", r.stderr)
	}
	var said []string
	for _, c := range m.synth.Calls() {
		said = append(said, c.Request.Text)
	}
	joined := strings.Join(said, "|")
	if strings.Contains(joined, "brew") || strings.Contains(joined, "raw") || strings.Contains(joined, "#") || !strings.Contains(joined, "Second step.") {
		t.Errorf("rendered %q", said)
	}
	b, err := os.ReadFile(m.path("a.wav.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"format":"markdown"`) && !strings.Contains(string(b), `"format": "markdown"`) {
		t.Errorf("manifest input format: %s", b)
	}
}

// Markdown with nothing to say (only code) is an input error before the network.
func TestSayMarkdownNothingToSpeak(t *testing.T) {
	m := newMultiEnv(t)
	if err := os.WriteFile(m.path("code.md"), []byte("```\nx\n```\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := m.run("-f", "code.md", "-o", "a.wav")
	if r.code != exitcode.Usage || !strings.Contains(r.stderr, "nothing to speak") || !strings.Contains(r.stderr, "skipped 1 code block") {
		t.Fatalf("%+v", r)
	}
	m.untouched()
}
