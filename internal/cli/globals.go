package cli

import (
	"flag"
	"fmt"
	"strconv"
	"strings"
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

// registerStacked adds -vv through -vvvvvvvvv as verbosity flags of their own. Registering them,
// instead of rewriting the argument list first, keeps a value that happens to look like one
// (`-f -vv` reads the file "-vv") a value. Help lists only -v.
func (g *Globals) registerStacked(fs *flag.FlagSet) {
	for n := 2; n <= 9; n++ {
		fs.Var(&stacked{&g.Verbose, n}, strings.Repeat("v", n), "")
	}
}

func defaultGlobals() Globals { return Globals{AuthSource: "auto"} }

// stacked is -vv, -vvv, …: one flag adding its number of v's to the verbosity.
type stacked struct {
	p *int
	n int
}

func (s *stacked) String() string   { return "" }
func (s *stacked) IsBoolFlag() bool { return true }
func (s *stacked) Set(v string) error {
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fmt.Errorf("parse %q: %w", v, err)
	}
	if b {
		*s.p += s.n
	}
	return nil
}

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
