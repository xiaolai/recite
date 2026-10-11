package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/xiaolai/recite/internal/manifest"
)

// player is an audio player's executable and its arguments before the file path.
type player struct {
	name string
	args []string
}

// players are the players --play tries on goos, in order (AUD-08).
func players(goos string) []player {
	switch goos {
	case "darwin":
		return []player{{"afplay", nil}}
	case "linux":
		return []player{{"ffplay", []string{"-nodisp", "-autoexit"}}, {"paplay", nil}}
	}
	return nil
}

var errNoPlayer = errors.New("no audio player found on PATH (afplay on macOS; ffplay or paplay on Linux)")

// play plays the published output under --play. Playback never changes the exit code: a
// missing or failing player is a warning (AUD-08).
func (c *CLI) play(ctx context.Context, path string, out *manifest.Output) {
	if out == nil {
		return // nothing was published; the run already reported why
	}
	if path == stdoutTarget {
		c.Logger.Warn("--play: the audio went to stdout, so there is no file to play")
		return
	}
	if err := c.runPlayer(ctx, path); err != nil {
		c.Logger.Warn("--play: playback failed", "path", path, "err", err)
	}
}

func (c *CLI) runPlayer(ctx context.Context, path string) error {
	// Absolute, so a name starting with "-" cannot read as a player option.
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for _, p := range players(runtime.GOOS) {
		bin, err := c.LookPath(p.name)
		if err != nil {
			continue
		}
		// The path is one argument to exec, never a shell word; the player's own output is
		// dropped so stdout stays clean.
		cmd := exec.CommandContext(ctx, bin, append(slices.Clone(p.args), abs)...)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", p.name, err)
		}
		c.Logger.Debug("played output", "player", p.name, "path", path)
		return nil
	}
	return errNoPlayer
}
