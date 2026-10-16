package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"

	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/config"
	"github.com/xiaolai/recite/internal/fsx"
	"github.com/xiaolai/recite/internal/logx"
	"github.com/xiaolai/recite/internal/manifest"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/stt"
	"github.com/xiaolai/sayso/go/secret"
)

// CLI is one invocation of recite. Say is injectable so tests can observe how
// arguments resolve; New sets it to the real runner.
type CLI struct {
	Stdout io.Writer
	Stderr io.Writer
	Say    func(SayRequest) int
	// Logger is the redacting logger, set up from the global flags before a command runs.
	Logger *slog.Logger
	// Getenv and Now are the environment and clock; tests replace them.
	Getenv func(string) string
	Now    func() time.Time
	// Stdin and StdinIsTTY are the input stream and whether it is a terminal.
	Stdin      io.Reader
	StdinIsTTY func() bool
	// StderrIsTTY reports whether Stderr is a terminal, for the progress line (CLI-07).
	StderrIsTTY func() bool
	// NewSynth builds the speech synthesizer over the probed credentials.
	NewSynth func(*auth.Credentials) (realtime.Synthesizer, error)
	// NewTranscriber builds the verifier over the probed credentials; close ends any session
	// it opened.
	NewTranscriber func(*auth.Credentials) (tr stt.Transcriber, close func() error, err error)
	// RunPipeline is the scheduler (pipeline.Run); tests wrap it to observe its configuration.
	RunPipeline func(context.Context, pipeline.Config, []pipeline.Task) (pipeline.Result, error)
	// Signals subscribes to SIGINT and SIGTERM and returns the unsubscribe function.
	Signals func() (<-chan os.Signal, func())
	// DrainTimeout bounds the graceful drain after the first interrupt; 0 means
	// pipeline.DrainTimeout (RUN-03).
	DrainTimeout time.Duration
	// LookPath finds an external encoder (ffmpeg, afconvert) on PATH; tests replace it.
	LookPath func(string) (string, error)

	globals Globals
	intr    *pipeline.Interrupt // set while a command with credentials runs
	authCtx *auth.Context       // the probed Codex context, set while a command with credentials runs
	written *manifest.Output    // the output `say` published, for the done event
	project *config.File        // the project layer of the output `render` is running (RUN-07)
}

// New returns a CLI writing to stdout and stderr.
func New(stdout, stderr io.Writer) *CLI {
	c := &CLI{Stdout: stdout, Stderr: stderr, Getenv: os.Getenv, Now: time.Now, Stdin: os.Stdin, StdinIsTTY: stdinIsTTY, Signals: notifySignals, LookPath: exec.LookPath}
	c.Say = c.runSay
	c.StderrIsTTY = func() bool { return stderrIsTTY(c.Stderr) }
	c.NewSynth = productionSynth(c)
	c.NewTranscriber = productionTranscriber(c)
	c.RunPipeline = pipeline.Run
	return c
}

// Run executes recite with args (without the program name) and returns the exit code.
func (c *CLI) Run(args []string) int {
	c.globals = defaultGlobals()
	// --version leads like the version command; -v stays the verbosity flag.
	if len(args) > 0 && (args[0] == "--version" || args[0] == "-version") {
		return c.runCommand(c.mustLookup("version"), args[1:])
	}

	// Global flags come first. An unknown flag here is a `say` flag
	// (`recite -f x.md`), so the whole line goes to `say`.
	gfs := newFlagSet("recite")
	c.globals.register(gfs)
	c.globals.registerStacked(gfs)
	if err := gfs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			c.writeHelp(c.Stdout)
			return ExitOK
		}
		if !strings.Contains(err.Error(), "flag provided but not defined") {
			// A say or render run still ends its --json stream with its final event (CLI-06), even when --json
			// comes after the flag that failed, before or after the command name: Parse stopped at the failure, so
			// the boolean flags of the whole line are applied here.
			cmd := c.commandOf(gfs, args)
			c.applyBoolFlags(cmd, args)
			return c.commandDone(cmd, c.usageErr("recite", err))
		}
		c.globals = defaultGlobals()
		return c.runCommand(c.mustLookup("say"), args)
	}
	rest := gfs.Args()
	if endedByTerminator(gfs, args) {
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
	c.globals.registerStacked(fs)
	run := cmd.setup(fs, c)
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			c.writeCommandHelp(c.Stdout, cmd)
			return ExitOK
		}
		return c.commandDone(cmd, c.usageErr("recite "+cmd.name, err))
	}
	closeLog, err := c.setupLogging()
	if err != nil {
		c.errorf("recite: %v", err)
		return c.commandDone(cmd, ExitUsage)
	}
	defer closeLog()
	return run(positional)
}

// commandOf is the command a line whose global flags failed to parse would have run, found the
// way Run finds it: past the global flags (fs), skipping each flag's value, the first argument
// names the command when it is one; anything else (text, an unknown flag, "--") is say's, the
// default. A flag's value is never taken for a command (`--profile render say Hi` is say).
func (c *CLI) commandOf(fs *flag.FlagSet, args []string) command {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" || len(a) < 2 || a[0] != '-' {
			if cmd, ok := c.lookup(a); ok && a != "--" {
				return cmd
			}
			break
		}
		name := strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-")
		if strings.Contains(name, "=") {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			break // an unknown flag sends the whole line to say
		}
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			continue
		}
		i++ // the value, whatever it looks like
	}
	return c.mustLookup("say")
}

// applyBoolFlags sets the boolean flags written anywhere on the line, read with cmd's flag set (its own flags and
// the globals, as runCommand parses them, interspersed): it runs after Parse failed partway, so a boolean flag past
// the failure (`--auth-source x --json say`, `--auth-source x say --json Hi`) still counts. Walking stops at "--". A
// flag whose value does not parse, or that the set does not know, is skipped: a failure is already being reported.
func (c *CLI) applyBoolFlags(cmd command, args []string) {
	fs := newFlagSet("recite " + cmd.name)
	c.globals.register(fs)
	c.globals.registerStacked(fs)
	cmd.setup(fs, c)
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return
		}
		if len(a) < 2 || a[0] != '-' {
			continue // the command name or a positional
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-"), "=")
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			if !hasValue {
				value = "true"
			}
			_ = fs.Set(name, value)
			continue
		}
		if !hasValue {
			i++ // the value, whatever it looks like
		}
	}
}

// endedByTerminator reports whether fs.Parse(args) stopped at a "--" terminator, as opposed to a
// "--" consumed as a flag's value (`--style -- Hello`). It walks args the way the flag package
// does: a flag either carries "=value", is boolean, or takes the next argument.
func endedByTerminator(fs *flag.FlagSet, args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return true
		}
		if len(a) < 2 || a[0] != '-' {
			return false
		}
		name := strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-")
		if strings.Contains(name, "=") {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			return false
		}
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			continue
		}
		i++ // the value, whatever it looks like
	}
	return false
}

// parseInterspersed parses flags anywhere among the positional arguments.
// "--" ends flag parsing; everything after it is positional.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		terminated := endedByTerminator(fs, args)
		if err := fs.Parse(args); err != nil {
			return nil, fmt.Errorf("parse flags: %w", err)
		}
		rest := fs.Args()
		if terminated {
			return append(positional, rest...), nil
		}
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// commandDone ends a `say` or `render` that failed before it ran with its last event under --json
// (CLI-06): its stdout is an event stream whatever happens.
func (c *CLI) commandDone(cmd command, code int) int {
	if cmd.name != "say" && cmd.name != "render" {
		return code
	}
	if c.Logger == nil {
		c.Logger = logx.New(io.Discard, logx.Level(true, 0), false) // event logs need a logger
	}
	if cmd.name == "render" {
		return c.renderDone(renderEvent{Exit: code})
	}
	return c.sayDone(code)
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

// setupLogging builds c.Logger from the global flags (OBS-01): text on stderr,
// or JSON appended to --log-file. Every handler is wrapped by the redactor.
func (c *CLI) setupLogging() (closeFn func(), err error) {
	level := logx.Level(c.globals.Quiet, c.globals.Verbose)
	if c.globals.LogFile == "" {
		c.Logger = logx.New(c.Stderr, level, false)
		return func() {}, nil
	}
	// The debug log holds input-derived text: an existing world-readable file is tightened, and a
	// symlink, a foreign file or a hard link is refused (fsx.AppendPrivate).
	f, err := fsx.AppendPrivate(c.globals.LogFile)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	c.Logger = logx.New(f, level, true)
	return func() {
		if err := f.Close(); err != nil {
			c.errorf("recite: close log file: %v", err)
		}
	}, nil
}

// errorf writes a user-facing error. It passes through the scrubber, so a
// secret embedded in a wrapped error never reaches the terminal (AUTH-05).
func (c *CLI) errorf(format string, a ...any) {
	// A failed write to stderr has nowhere left to be reported.
	_, _ = io.WriteString(c.Stderr, terminalSafe(secret.Scrub(fmt.Sprintf(format, a...)))+"\n")
}

// terminalSafe escapes the characters a terminal would act on rather than show: C0 and C1
// controls other than newline and tab, and the bidirectional overrides. Provider text (an error
// message, a code) reaches the terminal through errors; a decoded "\u001b[2J" in it must
// print as text, not clear the screen.
func terminalSafe(s string) string {
	unsafe := func(r rune) bool {
		return (unicode.IsControl(r) && r != '\n' && r != '\t') || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
	}
	if !strings.ContainsFunc(s, unsafe) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if unsafe(r) {
			fmt.Fprintf(&b, "\\u%04x", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (c *CLI) writeResult(err error) int {
	if err != nil {
		c.errorf("recite: write output: %v", err)
		return 1
	}
	return ExitOK
}
