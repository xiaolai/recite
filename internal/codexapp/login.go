package codexapp

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/xiaolai/recite/internal/testguard"
)

// Streams are the standard streams handed to an interactive Codex command. In production
// they are the process's own os.Stdin, os.Stdout and os.Stderr, which exec passes to the
// child as-is, so Codex talks to the user's terminal directly (AUTH-08).
type Streams struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
}

// Login runs `<bin> login`, or `<bin> login --device-auth`, with the resolved executable and
// CODEX_HOME of cfg and the caller's streams, and waits for it. recite never reads the flow.
func Login(cfg Config, deviceAuth bool, s Streams) error {
	if !filepath.IsAbs(cfg.Bin) {
		return fmt.Errorf("codexapp: executable path %q is not absolute", cfg.Bin)
	}
	if testguard.Active() && !testguard.TempPath(cfg.Bin) {
		return fmt.Errorf("codexapp: a non-live test tried to run %s; tests must use the fake codex", cfg.Bin)
	}
	args := []string{"login"}
	if deviceAuth {
		args = append(args, "--device-auth")
	}
	// No CommandContext: an interrupt at the terminal reaches Codex through the process group,
	// and Codex decides how its own flow ends.
	cmd := exec.Command(cfg.Bin, args...)
	cmd.Env = childEnv(os.Environ(), cfg.Home)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = s.Stdin, s.Stdout, s.Stderr
	if err := cmd.Run(); err != nil {
		return codexError("`codex login` did not complete", err)
	}
	return nil
}
