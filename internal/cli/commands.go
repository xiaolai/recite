package cli

import (
	"flag"

	"github.com/xiaolai/recite/internal/exitcode"
)

// Exit codes used by the router; the values live in internal/exitcode (design §12.3).
const (
	ExitOK    = exitcode.OK
	ExitUsage = exitcode.Usage
)

// command is one entry of the registry. setup registers the command's own
// flags; run executes it with the positional arguments.
type command struct {
	name    string
	args    string // synopsis of the positional arguments
	summary string
	setup   func(fs *flag.FlagSet, c *CLI) func(args []string) int
}

// commands lists every 1.0 command of design §12.1 in help order. `mcp` is
// post-1.0 (M8) and deliberately absent.
func (c *CLI) commands() []command {
	return []command{
		{"say", "[TEXT...]", "Text to verified audio file (default command)", setupSay},
		{"render", "PROJECT.toml", "Render a batch or project into many outputs", stub("render")},
		{"check", "FILE", "Text guard, spoken form and chunk plan, no network", stub("check")},
		{"prepare", "FILE", "Write the editable spoken-form script", stub("prepare")},
		{"voices", "", "List voices", stub("voices")},
		{"doctor", "", "Check the environment and the route", stub("doctor")},
		{"auth", "status|login", "Login state, or sign in through codex", setupAuth},
		{"usage", "", "Local ledger summary and Codex plan status", stub("usage")},
		{"cache", "ls|prune|clear", "Manage the take cache", stub("cache")},
		{"config", "show|path", "Effective merged config and its sources", stub("config")},
		{"version", "", "Version, commit, build date and Go version", setupVersion},
		{"help", "[COMMAND]", "Show help for recite or a command", setupHelp},
	}
}

func (c *CLI) lookup(name string) (command, bool) {
	for _, cmd := range c.commands() {
		if cmd.name == name {
			return cmd, true
		}
	}
	return command{}, false
}

func stub(name string) func(*flag.FlagSet, *CLI) func([]string) int {
	return func(_ *flag.FlagSet, c *CLI) func([]string) int {
		return func([]string) int { return c.notImplemented(name) }
	}
}

func (c *CLI) notImplemented(name string) int {
	c.errorf("recite %s: not implemented yet", name)
	return ExitUsage
}

// SayRequest is what the router resolved for `say`.
type SayRequest struct {
	Globals Globals
	File    string
	Text    []string
}

func setupSay(fs *flag.FlagSet, c *CLI) func([]string) int {
	var file string
	fs.StringVar(&file, "f", "", "read text from `PATH`")
	fs.StringVar(&file, "file", "", "read text from `PATH`")
	return func(args []string) int {
		return c.Say(SayRequest{Globals: c.globals, File: file, Text: args})
	}
}

// defaultSay is the `say` runner until CLI-02 lands.
func (c *CLI) defaultSay(SayRequest) int { return c.notImplemented("say") }

func setupHelp(_ *flag.FlagSet, c *CLI) func([]string) int {
	return func(args []string) int {
		switch len(args) {
		case 0:
			c.writeHelp(c.Stdout)
			return ExitOK
		case 1:
			cmd, ok := c.lookup(args[0])
			if !ok {
				return c.unknown(args[0])
			}
			c.writeCommandHelp(c.Stdout, cmd)
			return ExitOK
		}
		c.errorf("recite help: takes at most one command")
		return ExitUsage
	}
}
