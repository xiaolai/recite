package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/fsx"
)

// A "--" that is a flag's value is a value, not the end of the flags: `--style -- Hello
// --dry-run` still reads --dry-run as a flag.
func TestDoubleDashAsFlagValue(t *testing.T) {
	for _, tc := range []struct {
		args       []string
		style      string
		dry        bool
		positional []string
	}{
		{[]string{"--style", "--", "Hello", "--dry-run"}, "--", true, []string{"Hello"}},
		{[]string{"--style", "x", "--", "--dry-run"}, "x", false, []string{"--dry-run"}},
		{[]string{"--dry-run", "--", "--style"}, "", true, []string{"--style"}},
	} {
		fs := newFlagSet("t")
		style := fs.String("style", "", "")
		dry := fs.Bool("dry-run", false, "")
		got, err := parseInterspersed(fs, tc.args)
		if err != nil || *style != tc.style || *dry != tc.dry || !slices.Equal(got, tc.positional) {
			t.Errorf("%q: style %q dry %v positional %q err %v", tc.args, *style, *dry, got, err)
		}
	}
}

// A stacked -vv is a verbosity flag only where a flag is read; as a flag's value it stays the
// value (`-f -vv` reads the file "-vv").
func TestStackedVerbosityKeepsValues(t *testing.T) {
	var g Globals
	fs := newFlagSet("t")
	g.register(fs)
	g.registerStacked(fs)
	file := fs.String("f", "", "")
	if err := fs.Parse([]string{"-f", "-vv", "-vvv"}); err != nil {
		t.Fatal(err)
	}
	if *file != "-vv" || g.Verbose != 3 {
		t.Fatalf("file %q verbose %d, want -vv and 3", *file, g.Verbose)
	}
}

// A malformed global flag before say still ends the --json stream with say's final event.
// accept: CLI-06#2
func TestMalformedGlobalFlagEndsTheJSONStream(t *testing.T) {
	var out, errb bytes.Buffer
	c := New(&out, &errb)
	c.Getenv = func(string) string { return "" }
	code := c.Run([]string{"--json", "--auth-source", "invalid", "say", "Hi"})
	if code != ExitUsage || !strings.Contains(out.String(), `"event"`) {
		t.Fatalf("code %d stdout %q", code, out.String())
	}
}

// The final event belongs to the command the line names, found past the global flags: a
// flag's value is never taken for a command, and text or an unknown flag means say (audit).
// accept: CLI-06#2
func TestMalformedGlobalFlagFinalEventNamesTheCommand(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string // the final event
	}{
		{[]string{"--json", "--profile", "render", "--auth-source", "invalid", "say", "Hi"}, "done"},
		{[]string{"--json", "--auth-source", "invalid", "--profile", "render", "say", "Hi"}, "done"},
		{[]string{"--json", "--profile", "auth", "--auth-source", "invalid", "say", "Hi"}, "done"},
		{[]string{"--json", "--auth-source=invalid", "render", "x.md"}, "render"},
		{[]string{"--json", "--codex-home", "say", "--auth-source", "invalid", "render", "x.md"}, "render"},
		{[]string{"--json", "--auth-source", "invalid", "Hello", "render"}, "done"},
		{[]string{"--json", "--auth-source", "invalid"}, "done"},
		{[]string{"--json", "--log-file"}, "done"},
	} {
		var out, errb bytes.Buffer
		c := New(&out, &errb)
		c.Getenv = func(string) string { return "" }
		code := c.Run(tc.args)
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		var ev struct {
			Event string `json:"event"`
			Exit  int    `json:"exit"`
		}
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &ev); err != nil || code != ExitUsage || ev.Event != tc.want || ev.Exit != ExitUsage {
			t.Errorf("%q: code %d, final event %q (%v), want %s", tc.args, code, out.String(), err, tc.want)
		}
	}
}

// The debug log is private: an existing world-readable file is tightened to 0600, and a
// symlink is refused.
func TestLogFileIsPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "debug.log")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := New(&bytes.Buffer{}, &bytes.Buffer{})
	c.globals = defaultGlobals()
	c.globals.LogFile = path
	closeLog, err := c.setupLogging()
	if err != nil {
		t.Fatal(err)
	}
	closeLog()
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
	}
	if b, _ := os.ReadFile(path); !strings.HasPrefix(string(b), "old\n") {
		t.Fatal("the existing log was not appended to")
	}
	link := filepath.Join(dir, "link.log")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	c.globals.LogFile = link
	if _, err := c.setupLogging(); !errors.Is(err, fsx.ErrUnsafePath) {
		t.Fatalf("symlinked log file: %v", err)
	}
}

// Provider text that reaches the terminal cannot drive it: controls and bidi overrides are
// escaped, newlines and tabs kept.
func TestTerminalSafe(t *testing.T) {
	in := "refused\x1b[2J\x07 ‮gnirts‬\tok\n\u009b"
	got := terminalSafe(in)
	if strings.ContainsAny(got, "\x1b\x07‮‬\u009b") || !strings.Contains(got, `\u001b[2J`) || !strings.Contains(got, "\tok\n") {
		t.Fatalf("terminalSafe(%q) = %q", in, got)
	}
	var errb bytes.Buffer
	c := New(&bytes.Buffer{}, &errb)
	c.errorf("recite: %s", "x\x1b]0;title\x07")
	if strings.ContainsAny(errb.String(), "\x1b\x07") {
		t.Fatalf("errorf wrote a control: %q", errb.String())
	}
}
