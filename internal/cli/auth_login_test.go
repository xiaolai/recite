package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/exitcode"
)

// loginEnv runs `recite auth login` with the fake Codex as executable A (--codex-bin) and a
// marker script B first on PATH, so a fresh PATH lookup would run B.
type loginEnv struct {
	t                  *testing.T
	home, record, pids string
	stateFile, markerB string
	tty                bool
}

func newLoginEnv(t *testing.T, mode, loginSets string) *loginEnv {
	t.Helper()
	dir := t.TempDir()
	e := &loginEnv{t: t, home: filepath.Join(dir, "codex-home"), record: filepath.Join(dir, "login.record"),
		pids: filepath.Join(dir, "pids"), stateFile: filepath.Join(dir, "state"), markerB: filepath.Join(dir, "b-ran"), tty: true}
	binB := filepath.Join(dir, "pathdir")
	if err := os.MkdirAll(binB, 0o700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho ran > '" + e.markerB + "'\n"
	if err := os.WriteFile(filepath.Join(binB, "codex"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binB+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKECODEX_MODE", mode)
	t.Setenv("FAKECODEX_TOKEN_EXP", fmt.Sprint(cliNow.Add(time.Hour).Unix()))
	t.Setenv("FAKECODEX_STATE_FILE", e.stateFile)
	t.Setenv("FAKECODEX_LOGIN_SETS", loginSets)
	t.Setenv("FAKECODEX_LOGIN_RECORD", e.record)
	t.Setenv("FAKECODEX_PIDFILE", e.pids)
	return e
}

func (e *loginEnv) run(args ...string) result {
	e.t.Helper()
	var out, errb bytes.Buffer
	c := New(&out, &errb)
	c.Getenv = func(string) string { return "" }
	c.Now = func() time.Time { return cliNow }
	c.Stdin = strings.NewReader("typed-at-the-terminal\n")
	c.StdinIsTTY = func() bool { return e.tty }
	full := append([]string{"--codex-bin", fakeCodex, "--codex-home", e.home, "--auth-source", "codex"}, args...)
	code := c.Run(full)
	return result{code: code, stdout: out.String(), stderr: errb.String()}
}

// loginRecord is what the fake's login and logout wrote; "" when neither ran.
func (e *loginEnv) loginRecord() string {
	b, err := os.ReadFile(e.record)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

func (e *loginEnv) bRan() bool {
	_, err := os.Stat(e.markerB)
	return err == nil
}

// accept: AUTH-08#1
func TestAuthLoginRunsResolvedCodex(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"auth", "login"}, "login"},
		{[]string{"auth", "login", "--device-auth"}, "login --device-auth"},
		{[]string{"auth", "--device-auth", "login"}, "login --device-auth"},
	} {
		e := newLoginEnv(t, "logged-out", "chatgpt")
		r := e.run(tc.args...)
		if r.code != exitcode.OK {
			t.Fatalf("%v: code %d stderr %q", tc.args, r.code, r.stderr)
		}
		want := "argv0=" + fakeCodex + "\nargs=" + tc.want + "\nhome=" + e.home + "\nstdin=typed-at-the-terminal\n"
		if got := e.loginRecord(); got != want {
			t.Fatalf("%v: login record\n%q\nwant\n%q", tc.args, got, want)
		}
		// Inherited streams: the user's stdin reached Codex, and Codex's output reached the
		// user's stdout and stderr unchanged.
		if !strings.Contains(r.stdout, "fakecodex login stdout: typed-at-the-terminal\n") ||
			!strings.Contains(r.stderr, "fakecodex login stderr\n") {
			t.Fatalf("%v: streams not passed through: stdout %q stderr %q", tc.args, r.stdout, r.stderr)
		}
		if e.bRan() {
			t.Fatalf("%v: the codex on PATH ran", tc.args)
		}
	}
}

// accept: AUTH-08#3
func TestAuthLoginRefusesWithoutTTYOrWithJSON(t *testing.T) {
	e := newLoginEnv(t, "logged-out", "chatgpt")
	e.tty = false
	if r := e.run("auth", "login"); r.code != exitcode.Usage || !strings.Contains(r.stderr, "not a terminal") {
		t.Fatalf("non-TTY: %+v", r)
	}
	e.tty = true
	if r := e.run("auth", "login", "--json"); r.code != exitcode.Usage || r.stdout != "" || !strings.Contains(r.stderr, "--json") {
		t.Fatalf("--json: %+v", r)
	}
	if rec := e.loginRecord(); rec != "" || e.bRan() {
		t.Fatalf("codex ran: %q", rec)
	}
	if _, err := os.Stat(e.pids); !os.IsNotExist(err) {
		t.Fatalf("a codex process started: %v", err)
	}
}

// accept: AUTH-08#4
func TestAuthLoginLeavesAPIKeyAndUnsupportedAlone(t *testing.T) {
	for _, mode := range []string{"apikey", "headers", "bedrockApiKey"} {
		e := newLoginEnv(t, mode, "chatgpt")
		r := e.run("auth", "login")
		if r.code != exitcode.Auth {
			t.Fatalf("%s: code %d stderr %q", mode, r.code, r.stderr)
		}
		if rec := e.loginRecord(); rec != "" {
			t.Fatalf("%s: codex login or logout ran: %q", mode, rec)
		}
		if !strings.Contains(r.stderr, "More help: ") {
			t.Fatalf("%s: no guide: %q", mode, r.stderr)
		}
	}
}

// The status printed after the login never shows a token, email or account ID (AUTH-06).
func TestAuthLoginPrintsNoSecret(t *testing.T) {
	e := newLoginEnv(t, "logged-out", "chatgpt")
	r := e.run("auth", "login", "-vvv")
	if r.code != exitcode.OK {
		t.Fatalf("code %d stderr %q", r.code, r.stderr)
	}
	for _, s := range secrets {
		if strings.Contains(r.stdout+r.stderr, s) {
			t.Fatalf("printed %q:\n%s\n%s", s, r.stdout, r.stderr)
		}
	}
}

// auth login takes no positional arguments, and --device-auth belongs to login only.
func TestAuthLoginRejectsArguments(t *testing.T) {
	e := newLoginEnv(t, "logged-out", "chatgpt")
	if r := e.run("auth", "login", "extra"); r.code != exitcode.Usage {
		t.Fatalf("extra argument: %+v", r)
	}
	if r := e.run("auth", "status", "--device-auth"); r.code != exitcode.Usage {
		t.Fatalf("status --device-auth: %+v", r)
	}
}
