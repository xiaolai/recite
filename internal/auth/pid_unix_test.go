//go:build unix

package auth

import (
	"context"
	"errors"
	"syscall"
	"testing"
)

// accept: AUTH-01#1
func TestCodexChildReapedOnClose(t *testing.T) {
	s, _, _ := run(t, 0, "chatgpt")
	if _, err := s.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	pid := s.pid()
	if pid == 0 || syscall.Kill(pid, 0) != nil {
		t.Fatalf("child %d not running after the call", pid)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("child %d still exists: %v", pid, err)
	}
}
