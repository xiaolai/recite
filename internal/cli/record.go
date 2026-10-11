package cli

import (
	"encoding/json"
	"os"
	"sync"
	"unicode/utf8"

	"github.com/xiaolai/recite/internal/fsx"
	"github.com/xiaolai/recite/internal/manifest"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/stt"
	"github.com/xiaolai/recite/internal/usage"
	"github.com/xiaolai/recite/internal/verify"
)

// manifestTarget is where the manifest goes ("" under --no-manifest), and whether an existing
// file there may be replaced: with --force, or when it is the partial manifest of an earlier
// interrupted or failed run, so re-running the same command resumes (RUN-05). Audio streamed
// to stdout has a manifest only when --manifest names one (design §12.2).
func manifestTarget(req SayRequest, output string) (path string, replace bool, err error) {
	if req.NoManifest || (output == stdoutTarget && req.Manifest == "") {
		return "", false, nil
	}
	path = req.Manifest
	if path == "" {
		path = output + ".json"
	}
	if req.Force {
		return path, true, preflight(path, true)
	}
	if err := preflight(path, false); err == nil {
		return path, false, nil
	} else if !isPartialManifest(path) {
		return "", false, err
	}
	return path, true, nil
}

// isPartialManifest reports whether path is a regular file holding a recite manifest of a run
// that wrote no output.
func isPartialManifest(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var head struct {
		Schema string          `json:"schema"`
		Status manifest.Status `json:"status"`
	}
	return json.Unmarshal(b, &head) == nil && head.Schema == manifest.Schema &&
		head.Status != manifest.StatusComplete
}

// keepRejectedDir prepares --keep-rejected DIR as a private directory before any request.
func keepRejectedDir(dir string) error {
	if dir == "" {
		return nil
	}
	_, err := fsx.MkdirPrivate(dir)
	return err
}

// recorder collects what a run's manifest and usage ledger need while workers run: it is the
// pipeline's OnUsage and OnRejected, and the verifier chain's OnSwitch.
type recorder struct {
	c       *CLI
	ledger  *usage.Ledger // nil: no state directory
	prices  usage.PriceTable
	keepDir string

	mu          sync.Mutex
	estimate    float64
	fallback    *manifest.Fallback
	ledgerFails int
	keepFails   int
}

// openLedger opens the usage ledger in the state directory; none when no home is known.
func (c *CLI) openLedger() (*usage.Ledger, error) {
	dir := c.stateDir()
	if dir == "" {
		c.Logger.Debug("usage ledger off: no state directory")
		return nil, nil
	}
	return usage.Open(dir, c.Now)
}

// usage writes one ledger line and adds its estimate to the run's.
func (r *recorder) usage(ev pipeline.UsageEvent) {
	base := usage.Entry{Time: r.c.Now().UTC(), Command: "say", Model: ev.Model, Chunk: ev.ChunkID, Attempt: ev.Attempt}
	var e usage.Entry
	if ev.Kind == pipeline.UsageRender {
		u := ev.Tokens
		e = r.prices.RenderEntry(base, usage.Tokens{Input: u.InputTokens, Output: u.OutputTokens, InputText: u.InputTextTokens,
			OutputText: u.OutputTextTokens, OutputAudio: u.OutputAudioTokens, Detailed: u.Detailed})
	} else {
		e = r.prices.STTEntry(base, ev.STTSeconds)
	}
	r.mu.Lock()
	if e.EstimateUSD != nil {
		r.estimate += *e.EstimateUSD
	}
	r.mu.Unlock()
	if r.ledger == nil {
		return
	}
	if err := r.ledger.Append(e); err != nil {
		r.mu.Lock()
		r.ledgerFails++
		first := r.ledgerFails == 1
		r.mu.Unlock()
		if first {
			r.c.Logger.Warn("usage ledger write failed; the run continues", "err", err)
		}
	}
}

// rejected saves a rejected take under --keep-rejected (OUT-06).
func (r *recorder) rejected(c verify.Chunk, a verify.Attempt, take realtime.Take) {
	if err := manifest.WriteRejected(r.keepDir, c, a, take); err != nil {
		r.mu.Lock()
		r.keepFails++
		r.mu.Unlock()
		r.c.Logger.Warn("keep rejected take failed", "chunk", c.ID, "attempt", a.N, "err", err)
	}
}

// watch records the VER-02 switch of a verifier chain.
func (r *recorder) watch(tr stt.Transcriber) {
	ch, ok := tr.(*stt.Chain)
	if !ok {
		return
	}
	prev := ch.OnSwitch
	ch.OnSwitch = func(cause error) {
		r.mu.Lock()
		r.fallback = &manifest.Fallback{From: "http", To: "realtime", Cause: cause.Error()}
		r.mu.Unlock()
		if prev != nil {
			prev(cause)
		}
	}
}

func (r *recorder) totals() (float64, *manifest.Fallback) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.estimate, r.fallback
}

// verifierOf names the verifier route the run ended on.
func verifierOf(mode verify.Mode, tr stt.Transcriber, fb *manifest.Fallback) manifest.Verifier {
	if mode == verify.ModeOff || tr == nil {
		return manifest.Verifier{Route: "none"}
	}
	route, model := "http", stt.HTTPModel
	if rt, ok := tr.(pipeline.Router); ok {
		route, model = rt.Route()
	}
	return manifest.Verifier{Route: route, STTModel: &model, Fallback: fb}
}

// inputOf describes the text read for the manifest.
func inputOf(req SayRequest, input string) manifest.Input {
	src := req.File
	switch {
	case src != "":
	case len(req.Text) > 0:
		src = "args"
	default:
		src = "-"
	}
	format := req.InputFormat
	if format == "" {
		format = formatText
	}
	return manifest.Input{Source: src, Format: format, SHA256: manifest.SHA256(input), Chars: utf8.RuneCountInString(input)}
}

// settingsOf records the render and verification settings.
func settingsOf(req SayRequest) manifest.Settings {
	th := req.Verify.Mode.Thresholds()
	var style *string
	if req.Settings.Style != "" {
		s := req.Settings.Style
		style = &s
	}
	emotion := req.Settings.Emotion
	if emotion == "" {
		emotion = realtime.DefaultEmotion
	}
	return manifest.Settings{
		Model: req.Settings.Model, Voice: req.Settings.Voice, Speed: req.Settings.Speed,
		Effort: string(req.Settings.Effort), InstructionsVersion: req.Settings.InstructionsVersion,
		Emotion: manifest.Emotion{Preset: emotion, Version: realtime.EmotionVersion}, Style: style,
		Verify: manifest.VerifySettings{Mode: string(req.Verify.Mode), Accept: th.Accept, Gray: th.Reject,
			MaxAttempts: req.Verify.MaxAttempts, RequestExtraAttempts: req.Verify.RequestExtraAttempts},
	}
}

// writeManifest publishes m at path atomically (OUT-02). Like the audio, the manifest is an
// output and follows the umask (design §14.2).
func writeManifest(path string, replace bool, m manifest.Manifest) error {
	return publishOutput(path, replace, func(f *os.File) error { return m.Encode(f) })
}
