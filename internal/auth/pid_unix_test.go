//go:build unix

package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// Codex runs only for a fetch or a refresh: no child outlives the call that started it.
// accept: AUTH-01#1
func TestCodexChildReapedAfterEachCall(t *testing.T) {
	pids := filepath.Join(t.TempDir(), "pids")
	st, _, _ := run(t, 0, "chatgpt", "FAKECODEX_PIDFILE="+pids)
	wantState(t, st, StateReady)
	tok, err := st.Creds.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Creds.RefreshAfter(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(pids)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(b))
	if len(lines) != 2 { // one child for the fetch, one for the refresh
		t.Fatalf("%d children started, want 2", len(lines))
	}
	for _, l := range lines {
		pid, err := strconv.Atoi(l)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("child %d still exists: %v", pid, err)
		}
	}
}
