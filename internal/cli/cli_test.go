package cli

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
	path := filepath.Join("testdata", name+".golden")
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

// accept: CLI-01#1
func TestDefaultCommandResolution(t *testing.T) {
	cases := []struct {
		args []string
		want SayRequest
	}{
		{[]string{"Hello"}, SayRequest{Text: []string{"Hello"}}},
		{[]string{"say", "Hello"}, SayRequest{Text: []string{"Hello"}}},
		{[]string{"-f", "x.md"}, SayRequest{File: "x.md"}},
		{[]string{"say", "-f", "x.md"}, SayRequest{File: "x.md"}},
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
	if r := run("check", "x.md"); len(r.says) != 0 || !strings.Contains(r.stderr, "recite check: not implemented yet") {
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
	r := run("say", "Hello", "-f", "x.md", "world", "--json", "-v", "-v", "--auth-source", "codex")
	if r.code != ExitOK || len(r.says) != 1 {
		t.Fatalf("code %d, stderr %q", r.code, r.stderr)
	}
	got := r.says[0]
	want := SayRequest{File: "x.md", Text: []string{"Hello", "world"}}
	want.Globals = defaultGlobals()
	want.Globals.JSON, want.Globals.Verbose, want.Globals.AuthSource = true, 2, "codex"
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	// Global flags before the command survive the command's own parse.
	if r := run("--config", "c.toml", "say", "Hi"); r.says[0].Globals.Config != "c.toml" {
		t.Errorf("global lost: %+v", r.says[0])
	}
	if r := run("say", "Hi", "--auth-source", "nope"); r.code != ExitUsage {
		t.Errorf("bad auth-source: code %d", r.code)
	}
	if r := run("say", "Hi", "--bogus"); r.code != ExitUsage || !strings.Contains(r.stderr, "bogus") {
		t.Errorf("unknown flag: %+v", r)
	}
}

func TestStubsAndDefaultSay(t *testing.T) {
	var out, errb bytes.Buffer
	if code := New(&out, &errb).Run([]string{"Hello"}); code != ExitUsage ||
		errb.String() != "recite say: not implemented yet\n" {
		t.Errorf("say: code %d, %q", code, errb.String())
	}
	for _, cmd := range New(nil, nil).commands() {
		if cmd.name == "say" || cmd.name == "version" || cmd.name == "help" || cmd.name == "auth" {
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
