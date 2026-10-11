package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/guard"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/text"
)

// M1 renders one chunk: input longer than the segmentation budget waits for the
// segmenter (implementation plan §5).
const (
	m1ChunkBudget = 25 * time.Second
	m1ChunkID     = "c0001"
)

// productionSynth is the real synthesizer over the production client.
func productionSynth(c *CLI) func(*auth.Credentials) (realtime.Synthesizer, error) {
	return func(creds *auth.Credentials) (realtime.Synthesizer, error) {
		hc, err := openai.NewClient()
		if err != nil {
			return nil, fmt.Errorf("http client: %w", err)
		}
		return &realtime.Synth{
			Minter:  &realtime.Minter{Client: hc, BaseURL: openai.APIBase, Creds: creds},
			Client:  hc,
			BaseURL: openai.APIBase,
			Logger:  c.Logger,
			Dumps:   &realtime.Dumps{Dir: c.globals.DebugDir, StateDir: c.stateDir()},
		}, nil
	}
}

// stateDir is recite's state directory (design §11.3): $XDG_STATE_HOME/recite, else
// ~/Library/Application Support/recite on macOS and ~/.local/state/recite elsewhere. "" when no
// home directory is known; a protocol failure then writes no dump.
func (c *CLI) stateDir() string {
	if x := c.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "recite")
	}
	home := c.Getenv("HOME")
	if home == "" {
		return ""
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "recite")
	}
	return filepath.Join(home, ".local", "state", "recite")
}

// stdinIsTTY reports whether os.Stdin is a terminal (a character device).
func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// runSay is `say` (CLI-02, M1 slice): every local check runs before the login
// probe, and the probe before any request.
func (c *CLI) runSay(req SayRequest) int {
	target := req.Output
	if target == "" {
		target = defaultOutput(req.File, c.Now())
	}
	if target == "-" {
		c.errorf("recite say: -o - (audio on stdout) is not supported yet; give an output path")
		return ExitUsage
	}
	format, err := audio.FormatFor(target)
	if err != nil {
		c.errorf("recite say: %v", err)
		return ExitUsage
	}
	input, err := text.Read(text.Source{Args: req.Text, File: req.File, Stdin: c.Stdin, StdinIsTTY: c.StdinIsTTY()}, req.MaxInput)
	if err != nil {
		c.errorf("recite say: %v", err)
		if text.IsInputError(err) {
			return ExitUsage
		}
		return ExitInternal
	}
	input, code := c.guardText(req, input)
	if code != ExitOK {
		return code
	}
	// The spoken form is what the model is asked to say; form.Display keeps
	// the cleaned text for the manifest and subtitles.
	form, code := c.spokenForm(req, input)
	if code != ExitOK {
		return code
	}
	input = form.Spoken
	if est := realtime.EstimateDuration(input, req.Settings.Speed); est > m1ChunkBudget {
		c.errorf("recite say: the text is about %.0f s of speech, over one %v chunk; multi-chunk input arrives in M2", est.Seconds(), m1ChunkBudget)
		return ExitUsage
	}
	spec, err := req.Settings.SessionSpec()
	if err != nil {
		c.errorf("recite say: %v", err)
		return ExitUsage
	}
	if err := preflight(target, req.Force); err != nil {
		return c.reportFailure(err)
	}
	return c.withCredentials(func(ctx context.Context, creds *auth.Credentials) error {
		pcm, err := c.render(ctx, creds, spec, input)
		if err != nil {
			return err
		}
		if c.interrupted() {
			return nil // withCredentials reports 130; an interrupted run publishes nothing
		}
		if err := publishOutput(target, req.Force, func(w io.Writer) error { return audio.Write(w, format, pcm) }); err != nil {
			return err
		}
		c.Logger.Info("wrote output", "path", target, "seconds", pcm.Duration().Seconds())
		return nil
	})
}

// render speaks text as one chunk. The session is closed on every path.
func (c *CLI) render(ctx context.Context, creds *auth.Credentials, spec realtime.SessionSpec, input string) (audio.PCM, error) {
	synth, err := c.NewSynth(creds)
	if err != nil {
		return nil, err
	}
	sess, err := synth.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := sess.Close(); cerr != nil {
			c.Logger.Debug("close speech session", "err", cerr)
		}
	}()
	take, err := sess.Render(ctx, realtime.RenderRequest{ChunkID: m1ChunkID, Text: input})
	if err != nil {
		return nil, err
	}
	if take.Status != realtime.StatusCompleted {
		return nil, &openai.Error{Class: openai.ClassProtocol, Message: fmt.Sprintf("chunk %s ended %s", m1ChunkID, take.Status)}
	}
	pcm := audio.PCM(take.PCM)
	if err := pcm.Validate(); err != nil {
		return nil, &openai.Error{Class: openai.ClassProtocol, Message: "the speech audio holds a partial sample", Err: err}
	}
	return pcm, nil
}

// guardText runs the text guard (TXT-09) before anything touches the network.
// Findings go to stderr, one per line, unless -q; a guard failure (a fail
// action, strict with warn-level findings, nothing speakable) is exit 2 and
// always prints its findings.
func (c *CLI) guardText(req SayRequest, input string) (string, int) {
	res, err := guard.Run(input, req.Guard)
	var ge *guard.Error
	failed := errors.As(err, &ge)
	if err != nil && !failed {
		c.errorf("recite say: %v", err)
		return "", ExitInternal
	}
	if failed || !c.globals.Quiet {
		for _, f := range res.Findings {
			c.errorf("%s", f)
		}
	}
	if failed {
		c.errorf("recite say: %v", err)
		return "", ExitUsage
	}
	return res.Text, ExitOK
}

// spokenForm applies the spoken-form rules (TXT-10, TXT-11) for the text's
// language. N-AMBIGUOUS findings print like guard findings; under --guard
// strict they stop the run (exit 2), as design §7.7 says. Each rewrite is
// logged at debug level with the rule that made it.
func (c *CLI) spokenForm(req SayRequest, display string) (spoken.Result, int) {
	lang := text.Resolve(display, text.ResolveOptions{})
	res, err := spoken.Normalize(display, lang.Code, req.Spoken)
	if err != nil {
		c.errorf("recite say: %v", err)
		return res, ExitInternal
	}
	for _, rw := range res.Rewrites() {
		c.Logger.Debug("spoken form", "rewrite", rw.String())
	}
	strict := req.Guard.Policy == guard.PolicyStrict && len(res.Findings) > 0
	if strict || !c.globals.Quiet {
		for _, f := range res.Findings {
			c.errorf("%s", f)
		}
	}
	if strict {
		c.errorf("recite say: --guard strict: %d ambiguous span(s) (%s); resolve them or use --guard fix", len(res.Findings), spoken.AmbiguousCode)
		return res, ExitUsage
	}
	return res, ExitOK
}
