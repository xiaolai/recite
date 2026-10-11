//go:build unix

package cli

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/xiaolai/recite/internal/exitcode"
)

// The fake reports not-signed-in until its login writes chatgpt into the state file. Only a
// new app-server child reads that file, so a ready status proves the state was re-read by a
// new child; the first child has exited.
// accept: AUTH-08#2
func TestAuthLoginReclassifiesWithNewChild(t *testing.T) {
	e := newLoginEnv(t, "logged-out", "chatgpt")
	r := e.run("auth", "login")
	if r.code != exitcode.OK || !strings.Contains(r.stdout, "state:    ready\n") {
		t.Fatalf("code %d stdout %q stderr %q", r.code, r.stdout, r.stderr)
	}
	b, err := os.ReadFile(e.pids)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(b))
	if len(lines) != 3 { // app-server, login, app-server
		t.Fatalf("codex ran %d times, want 3: %q", len(lines), lines)
	}
	for _, l := range lines {
		pid, err := strconv.Atoi(l)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("codex child %d still exists: %v", pid, err)
		}
	}
	// Without the restart the old child would still say not-signed-in: the fake reads the state
	// file only at start.
	e2 := newLoginEnv(t, "logged-out", "")
	if r := e2.run("auth", "login"); r.code != exitcode.Auth || !strings.Contains(r.stdout, "state:    not-signed-in\n") {
		t.Fatalf("login that changes nothing: %+v", r)
	}
}
