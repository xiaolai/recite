// Package harden applies process-wide start-up protections (AUTH-05): no core
// dumps, so a crash cannot write the bearer token to disk.
package harden

import (
	"fmt"
	"syscall"
)

// Process sets RLIMIT_CORE to 0 and, on Linux, clears the dumpable flag,
// because a piped core_pattern collector ignores the size limit (N6).
// It covers this process only; the Codex child is Codex's responsibility.
func Process() error {
	if err := syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{Cur: 0, Max: 0}); err != nil {
		return fmt.Errorf("harden: set RLIMIT_CORE: %w", err)
	}
	return platform()
}
