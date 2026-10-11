package cli

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/guard"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/text"
	"github.com/xiaolai/recite/internal/verify"
)

var update = flag.Bool("update", false, "rewrite golden files")

type result struct {
	code           int
	stdout, stderr string
	says           []SayRequest
}

func run(args ...string) result {
	var out, errb bytes.Buffer
	c := New(&out, &errb)
	var r result
	c.Say = func(req SayRequest) int {
		r.says = append(r.says, req)
		return ExitOK
	}
	r.code = c.Run(args)
	r.stdout, r.stderr = out.String(), errb.String()
	return r
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	goldenAt(t, filepath.Join("testdata", name+".golden"), got)
}

// goldenAt compares got with the golden file at path; tests that change
// directory pass an absolute path.
func goldenAt(t *testing.T, path, got string) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run go test -update): %v", err)
	}
	if got != string(want) {
		t.Errorf("%s mismatch (run go test ./internal/cli -update)\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}

// accept: CLI-01#1
func TestHelpGolden(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"help", []string{"help"}},
		{"help", []string{"-h"}},
		{"help", []string{"--help"}},
		{"help-say", []string{"help", "say"}},
		{"help-say", []string{"say", "--help"}},
		{"help-version", []string{"help", "version"}},
		{"help-version", []string{"version", "-h"}},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, "_"), func(t *testing.T) {
			r := run(tc.args...)
			if r.code != ExitOK || r.stderr != "" {
				t.Fatalf("code %d, stderr %q", r.code, r.stderr)
			}
			golden(t, tc.name, r.stdout)
		})
	}
	// Every registered command has help, and `mcp` (post-1.0) is not registered.
	for _, cmd := range New(nil, nil).commands() {
		if r := run("help", cmd.name); r.code != ExitOK || !strings.HasPrefix(r.stdout, "Usage: recite "+cmd.name) {
			t.Errorf("help %s: code %d, %q", cmd.name, r.code, r.stdout)
		}
	}
	if r := run("help", "mcp"); r.code != ExitUsage {
		t.Errorf("help mcp: code %d, want %d", r.code, ExitUsage)
	}
}

// sayDefaults sets the M2 rendering defaults of a SayRequest.
func sayDefaults(r *SayRequest) {
	r.ChunkSeconds = text.DefaultChunkSeconds
	r.InputFormat = formatText
	r.Gaps = audio.DefaultGaps()
	r.SampleRate = audio.SampleRate
	r.Jobs, r.SessionChunks = pipeline.DefaultJobs, realtime.DefaultSessionChunks
	r.Verify = verify.DefaultOptions()
	r.CacheDir = defaultCacheDir(os.Getenv) // the process environment, which run passes on
}

// accept: CLI-01#1
func TestDefaultCommandResolution(t *testing.T) {
	cases := []struct {
		args []string
		want SayRequest
	}{
		{[]string{"Hello"}, SayRequest{Text: []string{"Hello"}}},
		{[]string{"say", "Hello"}, SayRequest{Text: []string{"Hello"}}},
		{[]string{"-f", "x.txt"}, SayRequest{File: "x.txt"}},
		{[]string{"say", "-f", "x.txt"}, SayRequest{File: "x.txt"}},
		{[]string{"say", "check"}, SayRequest{Text: []string{"check"}}},
		{[]string{"--", "check"}, SayRequest{Text: []string{"check"}}},
		{[]string{"say", "--", "-f"}, SayRequest{Text: []string{"-f"}}},
		{[]string{"Hello world"}, SayRequest{Text: []string{"Hello world"}}},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, "_"), func(t *testing.T) {
			r := run(tc.args...)
			if r.code != ExitOK || len(r.says) != 1 {
				t.Fatalf("code %d, says %d, stderr %q", r.code, len(r.says), r.stderr)
			}
			tc.want.Globals = defaultGlobals()
			tc.want.Settings = realtime.DefaultSettings()
			tc.want.MaxInput = text.DefaultMaxInput
			tc.want.Guard = guard.Options{Policy: guard.PolicyFix}
			tc.want.Spoken = spoken.Options{Mode: spoken.ModeAuto, DateStyle: spoken.DateUS}
			sayDefaults(&tc.want)
			if got := r.says[0]; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
	// Identical plans for the implicit and explicit forms.
	a, b := run("Hello", "--json"), run("say", "Hello", "--json")
	if !reflect.DeepEqual(a.says, b.says) {
		t.Errorf("recite Hello %+v != recite say Hello %+v", a.says, b.says)
	}
	// A first argument naming a command is that command, not text.
	if r := run("check", "x.md"); len(r.says) != 0 || !strings.Contains(r.stderr, "recite check: cannot read input file") {
		t.Errorf("check routed to say: %+v", r)
	}
}

// accept: CLI-01#1
func TestUnknownCommandSuggestion(t *testing.T) {
	r := run("sya")
	if r.code != ExitUsage || len(r.says) != 0 {
		t.Fatalf("code %d, says %v", r.code, r.says)
	}
	if !strings.Contains(r.stderr, "did you mean say?") || !strings.Contains(r.stderr, "To speak it, run: recite say sya") {
		t.Errorf("stderr %q", r.stderr)
	}
	if r := run("verison"); !strings.Contains(r.stderr, "did you mean version?") {
		t.Errorf("verison: stderr %q", r.stderr)
	}
	// Ordinary words are text for the default command, not typos.
	for _, w := range []string{"hey", "Sya", "hello"} {
		if r := run(w); len(r.says) != 1 {
			t.Errorf("%q not spoken: %+v", w, r)
		}
	}
}

// accept: CLI-01#1
func TestInterspersedFlags(t *testing.T) {
	r := run("say", "Hello", "-f", "x.txt", "world", "--json", "-v", "-v", "--auth-source", "codex")
	if r.code != ExitOK || len(r.says) != 1 {
		t.Fatalf("code %d, stderr %q", r.code, r.stderr)
	}
	got := r.says[0]
	want := SayRequest{File: "x.txt", Text: []string{"Hello", "world"}}
	want.Globals = defaultGlobals()
	want.Settings = realtime.DefaultSettings()
	want.MaxInput = text.DefaultMaxInput
	want.Guard = guard.Options{Policy: guard.PolicyFix}
	want.Spoken = spoken.Options{Mode: spoken.ModeAuto, DateStyle: spoken.DateUS}
	sayDefaults(&want)
	want.Globals.JSON, want.Globals.Verbose, want.Globals.AuthSource = true, 2, "codex"
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	// Global flags before the command survive the command's own parse.
	cfg := writeConfig(t, "")
	if r := run("--config", cfg, "say", "Hi"); len(r.says) != 1 || r.says[0].Globals.Config != cfg {
		t.Errorf("global lost: %+v", r.says[0])
	}
	if r := run("say", "Hi", "--auth-source", "nope"); r.code != ExitUsage {
		t.Errorf("bad auth-source: code %d", r.code)
	}
	if r := run("say", "Hi", "--bogus"); r.code != ExitUsage || !strings.Contains(r.stderr, "bogus") {
		t.Errorf("unknown flag: %+v", r)
	}
}

func TestStubs(t *testing.T) {
	for _, cmd := range New(nil, nil).commands() {
		if cmd.name == "say" || cmd.name == "version" || cmd.name == "help" || cmd.name == "auth" || cmd.name == "check" || cmd.name == "config" || cmd.name == "prepare" ||
			cmd.name == "doctor" || cmd.name == "voices" || cmd.name == "usage" || cmd.name == "cache" {
			continue
		}
		r := run(cmd.name)
		if r.code != ExitUsage || r.stderr != "recite "+cmd.name+": not implemented yet\n" {
			t.Errorf("%s: code %d, %q", cmd.name, r.code, r.stderr)
		}
	}
}

func TestDistance(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		d    int
	}{{"sya", "say", 1}, {"", "say", 3}, {"say", "say", 0}, {"kitten", "sitting", 3}} {
		if got := distance(tc.a, tc.b); got != tc.d {
			t.Errorf("distance(%q,%q)=%d want %d", tc.a, tc.b, got, tc.d)
		}
	}
}
