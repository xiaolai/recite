package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

// CLI is one invocation of recite. Say is injectable so tests can observe how
// arguments resolve; New sets it to the real runner.
type CLI struct {
	Stdout io.Writer
	Stderr io.Writer
	Say    func(SayRequest) int

	globals Globals
}

// New returns a CLI writing to stdout and stderr.
func New(stdout, stderr io.Writer) *CLI {
	c := &CLI{Stdout: stdout, Stderr: stderr}
	c.Say = c.defaultSay
	return c
}

// Run executes recite with args (without the program name) and returns the exit code.
func (c *CLI) Run(args []string) int {
	c.globals = defaultGlobals()

	// Global flags come first. An unknown flag here is a `say` flag
	// (`recite -f x.md`), so the whole line goes to `say`.
	gfs := newFlagSet("recite")
	c.globals.register(gfs)
	if err := gfs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			c.writeHelp(c.Stdout)
			return ExitOK
		}
		if !strings.Contains(err.Error(), "flag provided but not defined") {
			return c.usageErr("recite", err)
		}
		c.globals = defaultGlobals()
		return c.runCommand(c.mustLookup("say"), args)
	}
	rest := gfs.Args()
	if consumed := len(args) - len(rest); consumed > 0 && args[consumed-1] == "--" {
		return c.runCommand(c.mustLookup("say"), append([]string{"--"}, rest...))
	}
	if len(rest) == 0 {
		return c.runCommand(c.mustLookup("say"), rest)
	}
	if cmd, ok := c.lookup(rest[0]); ok {
		return c.runCommand(cmd, rest[1:])
	}
	if _, typo := c.looksLikeTypo(rest[0]); typo {
		return c.unknown(rest[0])
	}
	return c.runCommand(c.mustLookup("say"), rest)
}

func (c *CLI) mustLookup(name string) command {
	cmd, ok := c.lookup(name)
	if !ok {
		panic("cli: command not registered: " + name)
	}
	return cmd
}

func (c *CLI) runCommand(cmd command, args []string) int {
	fs := newFlagSet("recite " + cmd.name)
	c.globals.register(fs)
	run := cmd.setup(fs, c)
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			c.writeCommandHelp(c.Stdout, cmd)
			return ExitOK
		}
		return c.usageErr("recite "+cmd.name, err)
	}
	return run(positional)
}

// parseInterspersed parses flags anywhere among the positional arguments.
// "--" ends flag parsing; everything after it is positional.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, fmt.Errorf("parse flags: %w", err)
		}
		rest := fs.Args()
		consumed := len(args) - len(rest)
		if consumed > 0 && args[consumed-1] == "--" {
			return append(positional, rest...), nil
		}
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // errors and help are rendered by the CLI
	fs.Usage = func() {}
	return fs
}

func (c *CLI) unknown(name string) int {
	near, _ := c.nearest(name)
	// The word may be meant as text, since say is the default command; say how to speak it.
	c.errorf("recite: unknown command %q; did you mean %s? To speak it, run: recite say %s", name, near, name)
	return ExitUsage
}

func (c *CLI) usageErr(prefix string, err error) int {
	c.errorf("%s: %v (see \"recite help\")", prefix, err)
	return ExitUsage
}

func (c *CLI) errorf(format string, a ...any) {
	// A failed write to stderr has nowhere left to be reported.
	_, _ = fmt.Fprintf(c.Stderr, format+"\n", a...)
}

func (c *CLI) writeResult(err error) int {
	if err != nil {
		c.errorf("recite: write output: %v", err)
		return 1
	}
	return ExitOK
}
