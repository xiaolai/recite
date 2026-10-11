package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/stt"
)

// mishearing hears what was said, except that a text containing "Misheard" comes back as
// something else, so that output fails verification (exit 5).
type mishearing struct{ said *lastSaid }

func (m mishearing) Transcribe(context.Context, []byte, stt.Hint) (stt.Transcript, error) {
	text := m.said.get()
	if strings.Contains(text, "Misheard") {
		text = "Completely different words were heard here instead."
	}
	return stt.Transcript{Text: text, Languages: []stt.DetectedLanguage{}}, nil
}

// renderEnv is a sayEnv with a project of four outputs: 1 and 4 verify, 2 fails verification
// (exit 5) and 3 names a missing input (exit 2).
func renderEnv(t *testing.T) *sayEnv {
	t.Helper()
	e := newSayEnv(t)
	e.newTr = func(*auth.Credentials) stt.Transcriber { return mishearing{e.said} }
	write(t, e.path("ch01.txt"), "The first chapter is short.")
	write(t, e.path("ch02.txt"), "The Misheard chapter fails.")
	write(t, e.path("ch04.txt"), "The fourth chapter is fine.")
	write(t, e.path("book.toml"), `[defaults]
voice = "marin"
format = "wav"

[[output]]
input = "ch01.txt"
path = "out/01.wav"

[[output]]
input = "ch02.txt"
path = "out/02.wav"
voice = "cedar"

[[output]]
input = "ch03.txt"
path = "out/03.wav"

[[output]]
input = "ch04.txt"
path = "out/04.wav"
`)
	if err := os.Mkdir(e.path("out"), 0o755); err != nil {
		t.Fatal(err)
	}
	return e
}

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// A failed output does not stop the later ones; the exit code is the most severe by the
// CLI-03 order (5 over 2), and a summary table is printed.
// accept: RUN-07#1
// accept: CLI-13#1
func TestRenderContinuesPastFailuresAndReportsMostSevere(t *testing.T) {
	e := renderEnv(t)
	r := e.command("render", "book.toml", "--max-attempts", "1", "--jobs", "1")
	if r.code != exitcode.VerifyFailed {
		t.Fatalf("code %d, want %d\nstdout %s\nstderr %s", r.code, exitcode.VerifyFailed, r.stdout, r.stderr)
	}
	for _, name := range []string{"out/01.wav", "out/01.wav.json", "out/02.wav", "out/04.wav", "out/04.wav.json"} {
		if !exists(e.path(name)) {
			t.Errorf("%s not written", name)
		}
	}
	if exists(e.path("out/03.wav")) {
		t.Error("out/03.wav written for a missing input")
	}
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	want := [][]string{
		{"#", "input", "output", "exit", "result"},
		{"1", "ch01.txt", "out/01.wav", "0", "ok"},
		{"2", "ch02.txt", "out/02.wav", "5", "Verification failed"},
		{"3", "ch03.txt", "out/03.wav", "2", "Usage or input error"},
		{"4", "ch04.txt", "out/04.wav", "0", "ok"},
	}
	for i, w := range want {
		if i >= len(lines) || !strings.HasPrefix(strings.Join(strings.Fields(lines[i]), " "), strings.Join(w, " ")) {
			t.Fatalf("summary row %d: want %q in\n%s", i, w, r.stdout)
		}
	}
	if !strings.Contains(r.stdout, "exit:       5\n") {
		t.Errorf("summary lacks the exit code:\n%s", r.stdout)
	}
}

// --fail-fast stops at the first failed output; the later ones are skipped and not written.
// accept: RUN-07#1
func TestRenderFailFast(t *testing.T) {
	e := renderEnv(t)
	r := e.command("render", "--fail-fast", "book.toml", "--max-attempts", "1", "--json")
	if r.code != exitcode.VerifyFailed {
		t.Fatalf("code %d\nstderr %s", r.code, r.stderr)
	}
	if exists(e.path("out/04.wav")) {
		t.Error("out/04.wav written after --fail-fast stopped the project")
	}
	ev := renderEvents(t, r)
	outs := ev["outputs"].([]any)
	if len(outs) != 4 {
		t.Fatalf("outputs %v", outs)
	}
	for i, wantExit := range []any{0.0, 5.0, nil, nil} {
		o := outs[i].(map[string]any)
		if o["exit"] != wantExit || o["skipped"] != (wantExit == nil) {
			t.Errorf("output %d: %v", i+1, o)
		}
	}
}

// renderEvents validates every stdout line against the event schema, checks that the last is
// the render event with the exit code, and returns it. Each output's events end in its done.
func renderEvents(t *testing.T, r result) map[string]any {
	t.Helper()
	sch := eventsSchema(t)
	lines := strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n")
	var last map[string]any
	for _, line := range lines {
		validate(t, sch, line)
		last = nil
		if err := json.Unmarshal([]byte(line), &last); err != nil {
			t.Fatal(err)
		}
	}
	if last["event"] != "render" || last["exit"] != float64(r.code) {
		t.Fatalf("last event %v, want render with exit %d", last, r.code)
	}
	return last
}

// Under --json each output is a say stream ending in done, and the render event is last.
func TestRenderJSON(t *testing.T) {
	e := renderEnv(t)
	r := e.command("render", "book.toml", "--max-attempts", "1", "--json")
	ev := renderEvents(t, r)
	if ev["exit"] != 5.0 || len(ev["outputs"].([]any)) != 4 {
		t.Fatalf("render event %v", ev)
	}
	if n := strings.Count(r.stdout, `"event":"done"`); n != 4 {
		t.Fatalf("%d done events, want 4:\n%s", n, r.stdout)
	}
}

// render --dry-run sums the estimates of every output, renders nothing and needs no login.
// accept: CLI-13#1
func TestRenderDryRunAggregates(t *testing.T) {
	e := newSayEnv(t)
	e.env = map[string]string{"RECITE_CACHE_DIR": e.path("cache")}
	write(t, e.path("a.txt"), threeParagraphs)
	write(t, e.path("b.txt"), "One more paragraph.\n\nAnd another one.")
	write(t, e.path("p.toml"), "[[output]]\ninput = \"a.txt\"\npath = \"a.wav\"\n\n[[output]]\ninput = \"b.txt\"\npath = \"b.wav\"\n")
	sayA := e.run("-f", "a.txt", "--dry-run", "--json")
	sayB := e.run("-f", "b.txt", "--dry-run", "--json")
	planA, planB := events(t, sayA)[0], events(t, sayB)[0]

	r := e.command("render", "p.toml", "--dry-run", "--json")
	if r.code != exitcode.OK {
		t.Fatalf("code %d stderr %s", r.code, r.stderr)
	}
	ev := renderEvents(t, r)
	plan := ev["plan"].(map[string]any)
	if plan["chunks"] != planA["chunks"].(float64)+planB["chunks"].(float64) ||
		plan["est_seconds"] != round3(planA["est_seconds"].(float64)+planB["est_seconds"].(float64)) ||
		plan["est_wall_seconds"] != round3(planA["est_wall_seconds"].(float64)+planB["est_wall_seconds"].(float64)) {
		t.Fatalf("plan %v from %v and %v", plan, planA, planB)
	}
	if got, want := plan["estimate_usd"].(float64), planA["estimate_usd"].(float64)+planB["estimate_usd"].(float64); got-want > 1e-9 || want-got > 1e-9 {
		t.Fatalf("estimate %v, want %v", got, want)
	}
	if ev["dry_run"] != true || len(e.srv.MintRequests()) != 0 {
		t.Fatalf("dry run %v, %d mints", ev["dry_run"], len(e.srv.MintRequests()))
	}

	r = e.command("render", "p.toml", "--dry-run")
	if r.code != exitcode.OK || !strings.Contains(r.stdout, "total:      5 chunks, 0 cached\n") || !strings.Contains(r.stdout, "price:      $") {
		t.Fatalf("code %d stdout:\n%s", r.code, r.stdout)
	}
	for _, name := range []string{"a.wav", "b.wav", "cache"} {
		if exists(e.path(name)) {
			t.Errorf("the dry run wrote %s", name)
		}
	}
}

// Settings resolve as defaults < [[output]] < flags, and front matter still applies.
func TestRenderSettingPrecedence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	write(t, filepath.Join(dir, "a.txt"), "A.")
	write(t, filepath.Join(dir, "b.txt"), "B.")
	proj := filepath.Join(dir, "p.toml")
	write(t, proj, `[defaults]
voice = "marin"
speed = 1.2
format = "mp3"

[[output]]
input = "a.txt"
path = "a.wav"

[[output]]
input = "b.txt"
path = "sub/b.mp3"
voice = "cedar"
format = "wav"
`)
	r := run("render", proj)
	if r.code != ExitOK || len(r.says) != 2 {
		t.Fatalf("code %d, %d says, stderr %s", r.code, len(r.says), r.stderr)
	}
	a, b := r.says[0], r.says[1]
	if a.Settings.Voice != "marin" || b.Settings.Voice != "cedar" || a.Settings.Speed != 1.2 || b.Settings.Speed != 1.2 {
		t.Fatalf("settings %+v / %+v", a.Settings, b.Settings)
	}
	if a.File != filepath.Join(dir, "a.txt") || a.Output != filepath.Join(dir, "a.wav") || b.Output != filepath.Join(dir, "sub", "b.mp3") {
		t.Fatalf("paths %q %q %q", a.File, a.Output, b.Output)
	}
	if a.Format != "mp3" || b.Format != "wav" || a.Command != "render" {
		t.Fatalf("formats %q %q, command %q", a.Format, b.Format, a.Command)
	}
	r = run("render", proj, "--voice", "ash", "--format", "flac")
	if r.code != ExitOK || r.says[0].Settings.Voice != "ash" || r.says[1].Settings.Voice != "ash" || r.says[1].Format != "flac" {
		t.Fatalf("flags do not win: %+v", r)
	}
}

// A project file is checked whole before anything runs: unknown keys, invalid values, keys
// only the environment may set, and paths that leave the project's directory are exit 2.
func TestRenderRejectsBadProjects(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	write(t, filepath.Join(dir, "a.txt"), "A.")
	out := "[[output]]\ninput = \"a.txt\"\npath = \"a.wav\"\n"
	cases := map[string]struct{ body, want string }{
		"unknown top key":      {"title = \"x\"\n" + out, `unknown key "title"`},
		"unknown default key":  {"[defaults]\nvoise = \"marin\"\n" + out, `unknown key "voise"`},
		"unknown output key":   {"[[output]]\ninput = \"a.txt\"\npath = \"a.wav\"\nchapter = 1\n", `unknown key "chapter"`},
		"bad value":            {"[defaults]\nvoice = \"nobody\"\n" + out, "voice"},
		"env-only key":         {"[defaults]\ncache_dir = \"/tmp/c\"\n" + out, `unknown key "cache_dir"`},
		"input in defaults":    {"[defaults]\ninput = \"a.txt\"\n" + out, `"input" belongs in each [[output]]`},
		"input escapes":        {"[[output]]\ninput = \"../a.txt\"\npath = \"a.wav\"\n", "inside the project's directory"},
		"absolute output":      {"[[output]]\ninput = \"a.txt\"\npath = \"/tmp/a.wav\"\n", "inside the project's directory"},
		"hidden escape":        {"[[output]]\ninput = \"a.txt\"\npath = \"x/../../a.wav\"\n", "inside the project's directory"},
		"missing path":         {"[[output]]\ninput = \"a.txt\"\n", `missing "path"`},
		"duplicate output":     {out + out, "also the path of output 1"},
		"no outputs":           {"[defaults]\nvoice = \"marin\"\n", "at least one output"},
		"bad format":           {"[defaults]\nformat = \"aiff\"\n" + out, "format"},
		"output not array":     {"[output]\ninput = \"a.txt\"\n", "array of [[output]] tables"},
		"not toml":             {"[[output]\n", "p.toml"},
		"profile not accepted": {"[profile.x]\nvoice = \"marin\"\n" + out, `unknown key "profile"`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			proj := filepath.Join(dir, "p.toml")
			write(t, proj, tc.body)
			r := run("render", proj)
			if r.code != ExitUsage || len(r.says) != 0 || !strings.Contains(r.stderr, tc.want) {
				t.Fatalf("code %d, %d says, stderr %q, want %q", r.code, len(r.says), r.stderr, tc.want)
			}
		})
	}
	// Symbolic links are resolved: a link out of the project is refused like a written "../",
	// so a downloaded project cannot read a secret into the request or write outside itself.
	outside := t.TempDir()
	write(t, filepath.Join(outside, "id_rsa"), "secret")
	for _, l := range [][2]string{{filepath.Join(outside, "id_rsa"), "notes.md"}, {outside, "out"}, {filepath.Join(dir, "a.txt"), "alias.txt"}} {
		if err := os.Symlink(l[0], filepath.Join(dir, l[1])); err != nil {
			t.Fatal(err)
		}
	}
	for _, body := range []string{"[[output]]\ninput = \"notes.md\"\npath = \"a.wav\"\n", "[[output]]\ninput = \"a.txt\"\npath = \"out/a.wav\"\n"} {
		proj := filepath.Join(dir, "p.toml")
		write(t, proj, body)
		if r := run("render", proj); r.code != ExitUsage || len(r.says) != 0 || !strings.Contains(r.stderr, "outside the project's directory") {
			t.Fatalf("%q: code %d, %d says, stderr %q", body, r.code, len(r.says), r.stderr)
		}
	}
	proj := filepath.Join(dir, "p.toml")
	write(t, proj, "[[output]]\ninput = \"alias.txt\"\npath = \"a.wav\"\n")
	if r := run("render", "--dry-run", proj); r.code != ExitOK {
		t.Fatalf("a link inside the project: code %d, stderr %q", r.code, r.stderr)
	}
	for _, args := range [][]string{{"render"}, {"render", "a.toml", "b.toml"}, {"render", "-o", "x.wav", "p.toml"}, {"render", filepath.Join(dir, "none.toml")}} {
		if r := run(args...); r.code != ExitUsage || len(r.says) != 0 {
			t.Errorf("%v: code %d", args, r.code)
		}
	}
}

// A usage error under --json still ends with a schema-valid render event.
func TestRenderJSONUsageError(t *testing.T) {
	r := run("render", "--json", "--bogus", "p.toml")
	if r.code != ExitUsage {
		t.Fatalf("code %d", r.code)
	}
	renderEvents(t, r)
	r = run("render", "--json", filepath.Join(t.TempDir(), "none.toml"))
	if ev := renderEvents(t, r); ev["exit"] != 2.0 {
		t.Fatalf("event %v", ev)
	}
}
