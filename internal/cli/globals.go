package cli

import (
	"flag"
	"fmt"
	"strconv"
)

// Globals holds the global flags of design §12.2. They are registered on every
// command's FlagSet as well, so they may appear before or after the command name.
type Globals struct {
	Config     string
	Profile    string
	JSON       bool
	Quiet      bool
	Verbose    int
	LogFile    string
	DebugDir   string
	CodexBin   string
	CodexHome  string
	AuthSource string
	NoColor    bool
}

// register uses g's current values as defaults: flag.*Var assigns the default
// on registration, and a command's FlagSet must not reset globals parsed earlier.
func (g *Globals) register(fs *flag.FlagSet) {
	fs.StringVar(&g.Config, "config", g.Config, "read config from `PATH`")
	fs.StringVar(&g.Profile, "profile", g.Profile, "use config profile `NAME`")
	fs.BoolVar(&g.JSON, "json", g.JSON, "machine-readable output")
	fs.BoolVar(&g.Quiet, "q", g.Quiet, "quiet: errors only")
	fs.BoolVar(&g.Quiet, "quiet", g.Quiet, "quiet: errors only")
	fs.Var((*counter)(&g.Verbose), "v", "verbose (repeatable)")
	fs.Var((*counter)(&g.Verbose), "verbose", "verbose (repeatable)")
	fs.StringVar(&g.LogFile, "log-file", g.LogFile, "write the log to `PATH`")
	fs.StringVar(&g.DebugDir, "debug-dir", g.DebugDir, "write debug artifacts to `DIR`")
	fs.StringVar(&g.CodexBin, "codex-bin", g.CodexBin, "codex binary `PATH`")
	fs.StringVar(&g.CodexHome, "codex-home", g.CodexHome, "codex home `DIR`")
	fs.Var(&authSource{&g.AuthSource}, "auth-source", "auth `SOURCE`: auto, codex or file")
	fs.BoolVar(&g.NoColor, "no-color", g.NoColor, "disable colour")
}

func defaultGlobals() Globals { return Globals{AuthSource: "auto"} }

// counter is a repeatable boolean flag: each -v adds one.
type counter int

func (c *counter) String() string {
	if c == nil {
		return "0"
	}
	return strconv.Itoa(int(*c))
}
func (c *counter) IsBoolFlag() bool { return true }
func (c *counter) Set(s string) error {
	b, err := strconv.ParseBool(s)
	if err != nil {
		return fmt.Errorf("parse %q: %w", s, err)
	}
	if b {
		*c++
	}
	return nil
}

type authSource struct{ p *string }

func (a *authSource) String() string {
	if a == nil || a.p == nil {
		return ""
	}
	return *a.p
}
func (a *authSource) Set(s string) error {
	switch s {
	case "auto", "codex", "file":
		*a.p = s
		return nil
	}
	return fmt.Errorf("must be auto, codex or file, not %q", s)
}
