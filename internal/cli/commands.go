package cli

import (
	"flag"
	"strings"

	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/guard"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/text"
)

// Exit codes used by the router; the values live in internal/exitcode (design §12.3).
const (
	ExitOK       = exitcode.OK
	ExitInternal = exitcode.Internal
	ExitUsage    = exitcode.Usage
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
	Globals  Globals
	File     string
	Text     []string
	Output   string
	Force    bool
	MaxInput int64
	Settings realtime.Settings
	Guard    guard.Options
	Spoken   spoken.Options
}

// hiddenFlags are registered but left out of help (design §12.2).
var hiddenFlags = map[string]bool{"instructions-version": true}

// sayFlags holds the raw speech flags; validation happens after parsing so
// every error lists the valid values and nothing touches the network first.
type sayFlags struct {
	model, voice, speed, effort string
	modelUnchecked              bool
	instructionsVersion         int
}

func (f *sayFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.model, "model", realtime.DefaultModel, "speech `MODEL`: "+strings.Join(realtime.Models, ", "))
	fs.BoolVar(&f.modelUnchecked, "model-unchecked", false, "accept any --model name")
	fs.StringVar(&f.voice, "voice", realtime.DefaultVoice, "`VOICE`: "+strings.Join(realtime.Voices, ", "))
	fs.StringVar(&f.speed, "speed", "1.0", "`SPEED` from 0.25 to 1.5 in steps of 0.01")
	fs.StringVar(&f.effort, "effort", string(realtime.DefaultEffort), "reasoning `EFFORT`: minimal, low, medium, high, xhigh")
	fs.IntVar(&f.instructionsVersion, "instructions-version", realtime.InstructionsVersion, "instruction `VERSION` (regression comparison only)")
}

func (f *sayFlags) settings() (realtime.Settings, error) {
	var s realtime.Settings
	var err error
	if s.Model, err = realtime.ParseModel(f.model, f.modelUnchecked); err != nil {
		return s, err
	}
	if s.Voice, err = realtime.ParseVoice(f.voice); err != nil {
		return s, err
	}
	if s.Speed, err = realtime.ParseSpeed(f.speed); err != nil {
		return s, err
	}
	if s.Effort, err = realtime.ParseEffort(f.effort); err != nil {
		return s, err
	}
	if _, err = realtime.Instructions(f.instructionsVersion); err != nil {
		return s, err
	}
	s.InstructionsVersion = f.instructionsVersion
	return s, nil
}

func setupSay(fs *flag.FlagSet, c *CLI) func([]string) int {
	var file string
	var sf sayFlags
	fs.StringVar(&file, "f", "", "read text from `PATH`")
	fs.StringVar(&file, "file", "", "read text from `PATH`")
	var output string
	var force bool
	var maxInput int64
	fs.StringVar(&output, "o", "", "write audio to `PATH` (.wav or .pcm; default <file stem>.wav or speech-<timestamp>.wav)")
	fs.StringVar(&output, "output", "", "write audio to `PATH` (.wav or .pcm; default <file stem>.wav or speech-<timestamp>.wav)")
	fs.BoolVar(&force, "force", false, "overwrite the output")
	fs.Int64Var(&maxInput, "max-input", text.DefaultMaxInput, "input size limit in `BYTES`")
	sf.register(fs)
	policy := string(guard.PolicyFix)
	var actionSpecs []string
	fs.StringVar(&policy, "guard", policy, "text guard `POLICY`: fix, strict, warn or off")
	fs.Func("guard-action", "set a guard rule's action, `CODE=ACTION` (repeatable)", func(s string) error {
		actionSpecs = append(actionSpecs, s)
		return nil
	})
	normalize, dateStyle := string(spoken.ModeAuto), string(spoken.DateUS)
	var disableSpecs []string
	fs.StringVar(&normalize, "normalize", normalize, "spoken-form `MODE`: auto or off")
	fs.Func("normalize-disable", "turn off spoken-form rules, `RULE,…` (repeatable)", func(s string) error {
		disableSpecs = append(disableSpecs, s)
		return nil
	})
	fs.StringVar(&dateStyle, "date-style", dateStyle, "spoken date `STYLE`: us, uk or iso")
	return func(args []string) int {
		if maxInput <= 0 {
			c.errorf("recite say: --max-input must be a positive number of bytes")
			return ExitUsage
		}
		settings, err := sf.settings()
		if err != nil {
			c.errorf("recite say: %v", err)
			return ExitUsage
		}
		g, err := guardOptions(policy, actionSpecs)
		if err != nil {
			c.errorf("recite say: %v", err)
			return ExitUsage
		}
		so, err := spokenOptions(normalize, dateStyle, disableSpecs)
		if err != nil {
			c.errorf("recite say: %v", err)
			return ExitUsage
		}
		return c.Say(SayRequest{Globals: c.globals, File: file, Text: args, Output: output, Force: force, MaxInput: maxInput, Settings: settings, Guard: g, Spoken: so})
	}
}

// guardOptions resolves --guard and the --guard-action values (TXT-09).
func guardOptions(policy string, specs []string) (guard.Options, error) {
	p, err := guard.ParsePolicy(policy)
	if err != nil {
		return guard.Options{}, err
	}
	o := guard.Options{Policy: p}
	for _, spec := range specs {
		code, act, err := guard.ParseActionSpec(spec)
		if err != nil {
			return guard.Options{}, err
		}
		if o.Actions == nil {
			o.Actions = map[guard.Code]guard.Action{}
		}
		o.Actions[code] = act
	}
	return o, nil
}

// spokenOptions resolves --normalize, --date-style and --normalize-disable (TXT-10).
func spokenOptions(mode, dateStyle string, disable []string) (spoken.Options, error) {
	var o spoken.Options
	var err error
	if o.Mode, err = spoken.ParseMode(mode); err != nil {
		return o, err
	}
	if o.DateStyle, err = spoken.ParseDateStyle(dateStyle); err != nil {
		return o, err
	}
	for _, spec := range disable {
		ids, err := spoken.ParseDisabled(spec)
		if err != nil {
			return o, err
		}
		o.Disabled = append(o.Disabled, ids...)
	}
	return o, nil
}

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
