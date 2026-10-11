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

	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/logx"
	"github.com/xiaolai/recite/internal/manifest"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/stt"
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
	written *manifest.Output    // the output `say` published, for the done event
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
	args = expandVerbose(args)

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

// expandVerbose rewrites stacked verbosity flags (-vv, -vvv) into repeated -v,
// which the flag package parses as a counter. Arguments after "--" are text.
func expandVerbose(args []string) []string {
	out := make([]string, 0, len(args))
	for i, a := range args {
		if a == "--" {
			return append(out, args[i:]...)
		}
		if len(a) > 2 && strings.Trim(a[1:], "v") == "" && a[0] == '-' {
			for range len(a) - 1 {
				out = append(out, "-v")
			}
			continue
		}
		out = append(out, a)
	}
	return out
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

// commandDone ends a `say` that failed before it ran with the done event under --json
// (CLI-06): its stdout is an event stream whatever happens.
func (c *CLI) commandDone(cmd command, code int) int {
	if cmd.name != "say" {
		return code
	}
	if c.Logger == nil {
		c.Logger = logx.New(io.Discard, logx.Level(true, 0), false) // event logs need a logger
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
	f, err := os.OpenFile(c.globals.LogFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
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
	_, _ = io.WriteString(c.Stderr, logx.Scrub(fmt.Sprintf(format, a...))+"\n")
}

func (c *CLI) writeResult(err error) int {
	if err != nil {
		c.errorf("recite: write output: %v", err)
		return 1
	}
	return ExitOK
}
