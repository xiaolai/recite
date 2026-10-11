package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/realtime"
)

// PreviewText is the sentence `voices --preview` speaks in every voice unless --text is given.
const PreviewText = "Hello. This is a short sample of my voice, read by recite."

// recommendedVoices are the voices OpenAI recommends for the speech model (CLI-04).
var recommendedVoices = []string{"marin", "cedar"}

// voiceEvent is the --json form of one voice.
type voiceEvent struct {
	Event       string `json:"event"`
	Name        string `json:"name"`
	Default     bool   `json:"default"`
	Recommended bool   `json:"recommended"`
}

func setupVoices(fs *flag.FlagSet, c *CLI) func([]string) int {
	var preview, sample, effort string
	fs.StringVar(&preview, "preview", "", "render the sample sentence in every voice into `DIR`")
	fs.StringVar(&sample, "text", PreviewText, "the sample `TEXT` of --preview")
	fs.StringVar(&effort, "effort", "", "reasoning `EFFORT` of --preview: minimal, low, medium, high, xhigh")
	return func(args []string) int {
		if len(args) > 0 {
			c.errorf("recite voices: takes no arguments")
			return ExitUsage
		}
		set := map[string]bool{}
		fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
		if preview == "" {
			if set["text"] || set["effort"] {
				c.errorf("recite voices: --text and --effort need --preview DIR")
				return ExitUsage
			}
			return c.listVoices()
		}
		if strings.TrimSpace(sample) == "" {
			c.errorf("recite voices: --text is empty")
			return ExitUsage
		}
		return c.previewVoices(preview, sample, effort)
	}
}

// listVoices prints the voices; it needs no network and no sign-in (CLI-04).
func (c *CLI) listVoices() int {
	for _, v := range realtime.Voices {
		ev := voiceEvent{Event: "voice", Name: v, Default: v == realtime.DefaultVoice, Recommended: slices.Contains(recommendedVoices, v)}
		if c.globals.JSON {
			if code := c.emit(ev, ExitOK); code != ExitOK {
				return code
			}
			continue
		}
		var marks []string
		if ev.Default {
			marks = append(marks, "default")
		}
		if ev.Recommended {
			marks = append(marks, "recommended")
		}
		line := v
		if len(marks) > 0 {
			line = fmt.Sprintf("%-8s (%s)", v, strings.Join(marks, ", "))
		}
		if _, err := io.WriteString(c.Stdout, line+"\n"); err != nil {
			return c.writeResult(err)
		}
	}
	return ExitOK
}

// previewVoices renders sample in every voice into dir/<voice>.wav, through the same cached,
// verified path as `say`: an unchanged second run renders nothing (CLI-04, RUN-04). The
// settings come from the config and environment like `say`'s, with the voice set per file.
func (c *CLI) previewVoices(dir, sample, effort string) int {
	settings := newFlagSet("recite voices")
	var all settingFlags
	all.register(settings)
	if effort != "" {
		if err := settings.Set("effort", effort); err != nil {
			c.errorf("recite voices: --effort: %v", err)
			return ExitUsage
		}
	}
	base, err := c.sayRequest(settings, &all)
	if err != nil {
		c.errorf("recite voices: %v", err)
		return ExitUsage
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		c.errorf("recite voices: --preview: %v", err)
		return ExitUsage
	}
	type voiceJob struct {
		req SayRequest
		job sayJob
	}
	jobs := make([]voiceJob, 0, len(realtime.Voices))
	for _, v := range realtime.Voices {
		req := base
		req.Command = "voices --preview"
		req.Settings.Voice = v
		req.Text = []string{sample}
		req.Output = filepath.Join(dir, v+".wav")
		req.Format, req.SampleRate = audio.FormatWAV, audio.SampleRate
		req.Force = true      // a preview folder is refreshed in place
		req.NoManifest = true // the folder holds the ten samples and nothing else
		job, code := c.prepareSay(req)
		if code != ExitOK {
			return code
		}
		jobs = append(jobs, voiceJob{req, job})
	}
	// One probe and one Codex child for all ten voices.
	return c.withCredentials(func(ctx context.Context, creds *auth.Credentials) error {
		for _, j := range jobs {
			if c.interrupted() {
				return nil
			}
			if err := c.renderAll(ctx, creds, j.req, j.job); err != nil {
				return fmt.Errorf("voice %s: %w", j.req.Settings.Voice, err)
			}
			c.Logger.Info("voice preview", "voice", j.req.Settings.Voice, "path", j.job.target)
		}
		return nil
	})
}
