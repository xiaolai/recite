//go:build unix

package codexapp

import (
	"context"
	"errors"
	"syscall"
	"testing"
)

// accept: AUTH-01#1
func TestChildAliveUntilClose(t *testing.T) {
	c, _, err := start(t, 0, "FAKECODEX_MODE=chatgpt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetAuthStatus(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
	pid := c.PID()
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("child %d not running after the call: %v", pid, err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	// Reaped, not a zombie: the PID no longer exists at all.
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("child %d still exists after Close: %v", pid, err)
	}
}
