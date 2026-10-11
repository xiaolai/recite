package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/realtime"
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
		}, nil
	}
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
	// M1 precondition: marker escaping (TXT-09) arrives in M2 (WP-2.2 removes this).
	if strings.Contains(input, "<<<") || strings.Contains(input, ">>>") {
		c.errorf("recite say: the text contains <<< or >>>; this is supported from M2")
		return ExitUsage
	}
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
