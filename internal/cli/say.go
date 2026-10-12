package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/cache"
	"github.com/xiaolai/recite/internal/fsx"
	"github.com/xiaolai/recite/internal/guard"
	"github.com/xiaolai/recite/internal/manifest"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/stt"
	"github.com/xiaolai/recite/internal/text"
	"github.com/xiaolai/recite/internal/usage"
	"github.com/xiaolai/recite/internal/verify"
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

// productionTranscriber is the verifier over the production client: HTTP transcription, with
// the realtime transcription session as the fallback once HTTP is route-denied (VER-02).
func productionTranscriber(c *CLI) func(*auth.Credentials) (stt.Transcriber, func() error, error) {
	return func(creds *auth.Credentials) (stt.Transcriber, func() error, error) {
		hc, err := openai.NewClient()
		if err != nil {
			return nil, nil, fmt.Errorf("http client: %w", err)
		}
		rt := &stt.Realtime{
			Minter: &realtime.Minter{Client: hc, BaseURL: openai.APIBase, Creds: creds},
			Client: hc, BaseURL: openai.APIBase, Logger: c.Logger,
		}
		chain := &stt.Chain{
			Primary:  &stt.HTTP{Client: hc, BaseURL: openai.APIBase, Creds: creds, Logger: c.Logger},
			Fallback: rt,
			Logger:   c.Logger,
		}
		return chain, rt.Close, nil
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

// runSay is `say` (CLI-02): every local check — input, guard, spoken form, plan, gaps and
// output target — runs before the login probe, and the probe before any request. The chunks
// are rendered and verified by the scheduler and assembled in order (design §10).
func (c *CLI) runSay(req SayRequest) int {
	c.written = nil
	job, code := c.prepareSay(req)
	if code != ExitOK {
		return code
	}
	c.Logger.Info("plan", "chunks", len(job.tasks), "jobs", req.Jobs)
	if req.DryRun || c.globals.JSON {
		ev := c.estimate(req, job, req.DryRun)
		switch {
		case req.DryRun && !c.globals.JSON:
			return c.writePlan(ev, req.Settings.Model)
		case req.DryRun:
			c.event(ev)
			return ExitOK
		}
		c.event(ev)
	}
	return c.withCredentials(func(ctx context.Context, creds *auth.Credentials) error {
		return c.renderAll(ctx, creds, req, job)
	})
}

// prepareSay runs every local stage of `say` before the network: output, input, plan, cache
// and ledger. A non-zero code has been reported.
func (c *CLI) prepareSay(req SayRequest) (sayJob, int) {
	target, enc, code := c.outputEncoding(req)
	if code != ExitOK {
		return sayJob{}, code
	}
	stream := target == stdoutTarget
	input, code := c.readInput(req.prefix(), textInput{preread: req.preread},
		text.Source{Args: req.Text, File: req.File, Stdin: c.Stdin, StdinIsTTY: c.StdinIsTTY()}, req.MaxInput)
	if code != ExitOK {
		return sayJob{}, code
	}
	plan, seg, doc, code := c.planText(req, input)
	if code != ExitOK {
		return sayJob{}, code
	}
	for _, w := range seg.Warnings {
		c.Logger.Warn("segmentation", "code", w.Code, "chunk", plan.Chunks[w.Chunk].ID, "detail", w.Msg)
	}
	x, code := c.planExtras(req, target, enc, plan, doc)
	if code != ExitOK {
		return sayJob{}, code
	}
	gaps, err := audio.GapSamples(req.Gaps, plan.Joins())
	if err != nil {
		c.errorf("%s: %v", req.prefix(), err)
		return sayJob{}, ExitUsage
	}
	spec, err := req.Settings.SessionSpec()
	if err != nil {
		c.errorf("%s: %v", req.prefix(), err)
		return sayJob{}, ExitUsage
	}
	tasks := tasksFor(plan, spec, req.Settings.Emotion, req.RetryEffort)
	if req.DryRun {
		// A dry run writes nothing and needs no output target; it reads the cache if one exists.
		job := sayJob{input: input, plan: plan, tasks: tasks, gaps: gaps, target: target, enc: enc, extras: x}
		if req.CacheDir != "" && !req.NoCache {
			if _, err := os.Stat(req.CacheDir); err == nil {
				if job.cache, err = c.takeCache(req); err != nil {
					c.Logger.Warn("take cache not read", "err", err)
				}
			}
		}
		return job, ExitOK
	}
	if !stream {
		pf := preflight
		if x.split {
			pf = func(string, bool) error { return nil } // a directory: extras.preflight checks it
		}
		if err := pf(target, req.Force); err != nil {
			return sayJob{}, c.reportFailure(err)
		}
		if err := x.preflight(target, req.Force); err != nil {
			return sayJob{}, c.reportFailure(err)
		}
	}
	mpath, mreplace, err := manifestTarget(req, target)
	if err != nil {
		return sayJob{}, c.reportFailure(err)
	}
	if !stream {
		if err := distinctOutputs(append(x.outputs(target), mpath)...); err != nil {
			return sayJob{}, c.reportFailure(err)
		}
	}
	job := sayJob{input: input, plan: plan, tasks: tasks, gaps: gaps, target: target, enc: enc, extras: x,
		manifest: mpath, replaceManifest: mreplace}
	if job.cache, err = c.takeCache(req); err == nil {
		if err = keepRejectedDir(req.KeepRejected); err == nil {
			job.ledger, err = c.openLedger()
		}
	}
	if err != nil {
		c.errorf("%s: %v", req.prefix(), err)
		if errors.Is(err, fsx.ErrUnsafePath) {
			return sayJob{}, ExitUsage
		}
		return sayJob{}, ExitInternal
	}
	return job, ExitOK
}

// stdoutTarget is the -o value that streams the audio to stdout (AUD-07).
const stdoutTarget = "-"

// outputEncoding resolves the output path, format, sample rate and encoder, before anything
// touches the network (AUD-04, AUD-05). Streaming takes no bytes back, so -o - excludes
// --json (stdout carries audio only) and --strict-output (AUD-07).
func (c *CLI) outputEncoding(req SayRequest) (string, audio.Encoding, int) {
	target, format := req.Output, req.Format
	if req.SplitSections {
		// -o names the directory of section files; the format is --format, else wav.
		if format == "" {
			format = audio.FormatWAV
		}
		switch target {
		case stdoutTarget:
		case "":
			target = defaultOutput(req.File, c.Now(), "")
		default:
			target = filepath.Clean(target)
		}
	}
	if target == stdoutTarget {
		for _, bad := range []struct {
			on   bool
			flag string
		}{{c.globals.JSON, "--json"}, {req.Policy.StrictOutput, "--strict-output"},
			{req.Loudness != "" && req.Loudness != audio.LoudnessNone, "--loudness " + string(req.Loudness)}} {
			if bad.on {
				c.errorf("%s: -o - and %s exclude each other: a stream cannot take bytes back, and stdout carries audio only", req.prefix(), bad.flag)
				return "", audio.Encoding{}, ExitUsage
			}
		}
	}
	if format == "" {
		switch target {
		case "", stdoutTarget:
			format = audio.FormatWAV
		default:
			f, err := audio.FormatFor(target)
			if err != nil {
				c.errorf("%s: %v", req.prefix(), err)
				return "", audio.Encoding{}, ExitUsage
			}
			format = f
		}
	}
	if target == "" {
		target = defaultOutput(req.File, c.Now(), format.Ext())
	}
	enc, err := audio.ChooseEncoding(format, req.SampleRate, target == stdoutTarget, runtime.GOOS, c.LookPath)
	if err == nil {
		enc, err = audio.WithLoudness(enc, c.loudness(req, target, enc), c.LookPath)
	}
	if err != nil {
		c.errorf("%s: %v", req.prefix(), err)
		return "", audio.Encoding{}, ExitUsage
	}
	return target, enc, ExitOK
}

// loudness is the preset of the run (AUD-06): --loudness when given, else none for a stream
// and the format's default otherwise. The m4a fallback to afconvert cannot normalise, so
// there the podcast default gives way to none, with a warning; an explicit preset still
// requires ffmpeg.
func (c *CLI) loudness(req SayRequest, target string, enc audio.Encoding) audio.Loudness {
	switch {
	case req.Loudness != "":
		return req.Loudness
	case target == stdoutTarget:
		return audio.LoudnessNone
	}
	l := audio.DefaultLoudness(enc.Format)
	if l != audio.LoudnessNone && enc.Tool == audio.ToolAfconvert {
		c.Logger.Warn("loudness not normalised: ffmpeg is not on PATH and afconvert cannot normalise; install ffmpeg, or pass --loudness none",
			"default", string(l))
		return audio.LoudnessNone
	}
	return l
}

// sayJob is a planned `say` run: everything decided before the first request.
type sayJob struct {
	input           string
	plan            pipeline.Plan
	tasks           []pipeline.Task
	cache           *pipeline.TakeCache
	ledger          *usage.Ledger
	gaps            []int
	target          string // stdoutTarget streams (AUD-07)
	enc             audio.Encoding
	manifest        string // "" under --no-manifest
	replaceManifest bool
	extras          extras // subtitles, chapters, split output
}

// takeCache opens the take cache (RUN-04): nil under --no-cache or without a cache directory.
func (c *CLI) takeCache(req SayRequest) (*pipeline.TakeCache, error) {
	if req.NoCache || req.CacheDir == "" {
		c.Logger.Debug("take cache off", "no_cache", req.NoCache)
		return nil, nil
	}
	st, err := cache.Open(req.CacheDir, c.Now)
	if err != nil {
		return nil, err
	}
	return &pipeline.TakeCache{Store: st, Refresh: req.Refresh, AllowUnverified: req.Policy.AllowUnverified}, nil
}

// renderAll renders, verifies and assembles every chunk, then publishes the output when the
// verdicts allow it (VER-09). With verification off (the default, D19/D23), eligible
// unverified chunks succeed; with it on, an unverified chunk makes exit 5 after the file is
// written, unless --allow-unverified or --strict-output says otherwise. The manifest is written
// whenever the scheduler ran, with partial state after an interrupt or a failure (design §10).
func (c *CLI) renderAll(ctx context.Context, creds *auth.Credentials, req SayRequest, job sayJob) error {
	synth, err := c.NewSynth(creds)
	if err != nil {
		return err
	}
	rec := &recorder{c: c, ledger: job.ledger, prices: usage.ListPrices, keepDir: req.KeepRejected}
	var tr stt.Transcriber
	if req.Verify.Mode != verify.ModeOff {
		t, closeTr, err := c.NewTranscriber(creds)
		if err != nil {
			return err
		}
		defer func() {
			if cerr := closeTr(); cerr != nil {
				c.Logger.Debug("close transcription session", "err", cerr)
			}
		}()
		tr = t
		rec.watch(tr)
	}
	var sink io.Writer
	var stream *audio.StreamWriter
	var spool *os.File
	if job.target == stdoutTarget {
		// Each ordered prefix goes to stdout as soon as it is assembled (AUD-07).
		if stream, err = audio.NewStreamWriter(ctx, job.enc, c.Stdout); err != nil {
			return err
		}
		defer func() { _ = stream.Close() }() // ends the stream on every path; Close is idempotent
		sink = stream
	} else {
		// The assembly goes to a temp file beside the output, not to memory: hours of PCM would
		// otherwise be held whole until publication. It is read back to publish, then removed.
		if spool, err = assemblySpool(job.target); err != nil {
			return err
		}
		defer func() {
			_ = spool.Close()
			_ = os.Remove(spool.Name()) // best effort: the temp name is ours alone
		}()
		sink = spool
	}
	asm := audio.NewAssembler(sink, job.gaps)
	var estTotal float64
	for _, t := range job.tasks {
		estTotal += t.Chunk.Estimate.Seconds()
	}
	prog := c.newProgress(len(job.tasks), estTotal)
	cfg := pipeline.Config{
		Jobs: req.Jobs, VerifyJobs: req.VerifyJobs, SessionChunks: req.SessionChunks,
		Verify: req.Verify, Synth: synth, Transcriber: tr, Cache: job.cache,
		Deliver: func(i int, r verify.ChunkResult) error {
			// Under --verify off every chunk is unverified by request: one summary line, not
			// a report per chunk.
			if req.Verify.Mode != verify.ModeOff || r.State != verify.StateUnverified {
				c.reportChunk(r)
			}
			if c.globals.JSON {
				for _, ev := range chunkEvents(r) {
					c.event(ev)
				}
			}
			prog.chunk(r, job.tasks[i].Chunk.Estimate.Seconds())
			return asm.Add(i, r.PCM)
		},
		DrainTimeout: c.DrainTimeout,
		DebugDir:     c.globals.DebugDir,
		Logger:       c.Logger,
		OnUsage:      rec.usage,
	}
	if req.KeepRejected != "" {
		cfg.OnRejected = rec.rejected
	}
	if c.intr != nil {
		cfg.Stop = c.intr.Stop()
	}
	res, err := c.RunPipeline(ctx, cfg, job.tasks)
	prog.finish()
	code, write := pipeline.ExitCode(res, err, req.Policy)
	status := manifest.StatusNoOutput
	switch {
	case c.interrupted() || res.Interrupted:
		status, write = manifest.StatusInterrupted, false // an interrupted run publishes no audio
	case err != nil:
		status = manifest.StatusFailed
	}
	var out *manifest.Output
	var sections []manifest.Section
	var subtitles []manifest.Subtitle
	if stream != nil {
		// A stream cannot take bytes back: whatever prefix was assembled has been sent, and
		// the exit code reports a failure after it (AUD-07).
		if cerr := stream.Close(); cerr != nil && err == nil {
			err = cerr
			write = false
		}
	}
	if write {
		var seconds float64
		var pub publication
		perr := asm.Close()
		switch {
		case perr != nil:
		case stream != nil:
			seconds = float64(asm.Written()) / audio.BytesPerSecond
			c.Logger.Info("streamed output", "seconds", seconds)
		default:
			seconds = audio.DurationOf(asm.Written()).Seconds()
			pub, perr = c.publishAudio(ctx, req, job, spool, asm.Written(), asm.Spans())
			sections, subtitles = pub.sections, pub.subtitles
			if perr == nil {
				c.Logger.Info("wrote output", "path", job.target, "seconds", seconds)
			}
		}
		output := func() *manifest.Output {
			o := &manifest.Output{Path: job.target, Format: string(job.enc.Format), DurationS: seconds,
				SampleRate: job.enc.Rate, Loudness: string(audio.LoudnessNone)}
			if job.enc.Loudness != "" {
				o.Loudness = string(job.enc.Loudness)
			}
			return o
		}
		if perr != nil {
			// Rendering is done but publication failed: the manifest still records the failed
			// run before the error is returned (design §10), with every file that was published
			// before the failure (a split file's section path, or the single audio file).
			status, err = manifest.StatusFailed, perr
			if pub.audio {
				out = output()
			}
		} else {
			status = manifest.StatusComplete
			out = output()
		}
	}
	if status == manifest.StatusComplete {
		c.written = out // a failed run's published audio is in its manifest, not played or reported done
	}
	switch {
	case req.Play && req.SplitSections:
		c.Logger.Warn("--play: --split-sections writes a directory of files, so there is no single file to play", "dir", job.target)
	case req.Play:
		defer c.play(ctx, job.target, c.written)
	}
	if sections == nil {
		sections = manifestSections(job.extras.sections)
	}
	if merr := c.writeRunManifest(req, job, rec, tr, res, asm.Spans(), status, out, sections, subtitles); merr != nil {
		if err == nil {
			return merr
		}
		c.Logger.Warn("manifest not written", "err", merr)
	}
	if err != nil {
		return err
	}
	if c.interrupted() {
		return nil // withCredentials reports 130; an interrupted run publishes nothing
	}
	if code != ExitOK {
		return verdictError(res, code, write)
	}
	if req.Verify.Mode == verify.ModeOff {
		c.errorf("recite: verification is off: %d chunk(s) written unverified; --verify normal checks them with STT", len(res.Chunks))
	}
	return nil
}

// writeRunManifest builds and publishes the run's manifest, unless --no-manifest.
func (c *CLI) writeRunManifest(req SayRequest, job sayJob, rec *recorder, tr stt.Transcriber, res pipeline.Result,
	spans []audio.Span, status manifest.Status, out *manifest.Output, sections []manifest.Section,
	subtitles []manifest.Subtitle) error {
	if job.manifest == "" {
		return nil
	}
	estimate, fb := rec.totals()
	m, err := manifest.Build(manifest.Run{
		CreatedAt: c.Now(), Status: status, Input: inputOf(req, job.input), Settings: settingsOf(req),
		Verifier: verifierOf(req.Verify.Mode, tr, fb), Output: out,
		Plan: job.plan, Tasks: job.tasks, Results: res.Chunks, Spans: spans, Gaps: job.gaps, Sections: sections,
		Subtitles: subtitles, EstimateUSD: estimate, PriceNote: usage.ListPrices.Label(),
	})
	if err != nil {
		return err
	}
	if err := writeManifest(job.manifest, job.replaceManifest, m); err != nil {
		return err
	}
	c.Logger.Info("wrote manifest", "path", job.manifest)
	return nil
}

// reportChunk logs a delivered chunk; a chunk that is not verified is always reported.
func (c *CLI) reportChunk(r verify.ChunkResult) {
	switch r.State {
	case verify.StateVerified:
		if r.Cached {
			c.Logger.Info("chunk reused from cache", "chunk", r.ChunkID, "score", r.Score)
		} else {
			c.Logger.Info("chunk verified", "chunk", r.ChunkID, "attempt", r.Final, "score", r.Score)
		}
	case verify.StateUnverified:
		c.errorf("recite: chunk %s unverified after %d attempt(s): kept attempt %d, score %.3f%s",
			r.ChunkID, len(r.Attempts), r.Final, r.Score, lastRejection(r))
	default:
		c.errorf("recite: chunk %s: no take passed the guards in %d attempt(s)%s", r.ChunkID, len(r.Attempts), lastRejection(r))
	}
}

func lastRejection(r verify.ChunkResult) string {
	for i := len(r.Attempts) - 1; i >= 0; i-- {
		if rej := r.Attempts[i].Rejection; rej != nil {
			return fmt.Sprintf(" (last rejection: %s: %s)", rej.Reason, rej.Detail)
		}
	}
	return ""
}

// exitError is a finished run whose verdict is a non-zero exit code.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

// ExitCode is the run's exit code.
func (e *exitError) ExitCode() int { return e.code }

func verdictError(res pipeline.Result, code int, written bool) error {
	var unverified, none int
	for _, r := range res.Chunks {
		switch r.State {
		case verify.StateUnverified:
			unverified++
		case verify.StateNoEligible:
			none++
		}
	}
	msg := fmt.Sprintf("%d of %d chunk(s) unverified, %d with no usable take", unverified, len(res.Chunks), none)
	if written {
		msg += "; the output was written (pass --allow-unverified to accept it)"
	} else {
		msg += "; no output was written"
	}
	return &exitError{code: code, msg: msg}
}

// planText runs the text stages of `say` and plans the chunks. A spoken script (TXT-15)
// skips the guard and the spoken-form rules: its text is the spoken form.
// The textDoc is the Markdown or plain-text document the plan came from (empty for a spoken
// script).
func (c *CLI) planText(req SayRequest, input string) (pipeline.Plan, text.Segmentation, textDoc, int) {
	if req.InputFormat == formatSpoken {
		plan, doc, err := spokenScriptPlan(input, req.Marks, req.Spoken, planSettings(req.Settings), req.Settings.Speed)
		if err != nil {
			c.errorf("%s: %v", req.prefix(), err)
			return plan, doc.Seg, textDoc{}, textErrorCode(err)
		}
		return plan, doc.Seg, textDoc{}, ExitOK
	}
	doc, err := speechDoc(input, textInput{format: req.InputFormat, markdown: req.Markdown, lang: req.Lang, voices: req.Voices, directives: req.Directives})
	c.skippedWarning(req.prefix(), doc.skipped)
	if err != nil {
		c.errorf("%s: %v", req.prefix(), err)
		return pipeline.Plan{}, text.Segmentation{}, doc, textErrorCode(err)
	}
	g, code := c.guardText(req, doc.text)
	if code != ExitOK {
		return pipeline.Plan{}, text.Segmentation{}, doc, code
	}
	if err := checkPauses(g); err != nil {
		c.errorf("%s: %v", req.prefix(), err)
		return pipeline.Plan{}, text.Segmentation{}, doc, ExitUsage
	}
	// The spoken form is what the model is asked to say; form.Display keeps
	// the cleaned text for the plan, the manifest and subtitles.
	form, code := c.spokenForm(req, g)
	if code != ExitOK {
		return pipeline.Plan{}, text.Segmentation{}, doc, code
	}
	plan, seg, err := planDoc(doc, g, form, req.Spoken.Lexicon, req.ChunkSeconds, req.Settings)
	if err != nil {
		c.errorf("%s: %v", req.prefix(), err)
		return plan, seg, doc, ExitInternal
	}
	return plan, seg, doc, ExitOK
}

// textErrorCode is the exit code of a text-stage error: 2 for the user's input.
func textErrorCode(err error) int {
	var se *text.ScriptError
	var de *text.DirectiveError
	var ue *usageError
	if errors.As(err, &se) || errors.As(err, &de) || errors.As(err, &ue) || text.IsInputError(err) {
		return ExitUsage
	}
	return ExitInternal
}

// guardText runs the text guard (TXT-09) before anything touches the network.
// Findings go to stderr, one per line, unless -q; a guard failure (a fail
// action, strict with warn-level findings, nothing speakable) is exit 2 and
// always prints its findings.
func (c *CLI) guardText(req SayRequest, input string) (guard.Result, int) {
	res, err := guard.Run(input, req.Guard)
	var ge *guard.Error
	failed := errors.As(err, &ge)
	if err != nil && !failed {
		c.errorf("%s: %v", req.prefix(), err)
		return res, ExitInternal
	}
	if failed || !c.globals.Quiet {
		for _, f := range res.Findings {
			c.errorf("%s", f)
		}
	}
	if failed {
		c.errorf("%s: %v", req.prefix(), err)
		return res, ExitUsage
	}
	return res, ExitOK
}

// spokenForm applies the spoken-form rules (TXT-10, TXT-11) for the text's
// language. N-AMBIGUOUS findings print like guard findings; under --guard
// strict they stop the run (exit 2), as design §7.7 says. Each rewrite is
// logged at debug level with the rule that made it.
func (c *CLI) spokenForm(req SayRequest, g guard.Result) (spoken.Result, int) {
	display := g.Text
	lang := text.Resolve(display, text.ResolveOptions{FrontMatter: req.Lang})
	so := markOptions(g, req.Spoken, markReader(lang.Code, req.Spoken.DateStyle))
	res, err := spoken.Normalize(display, lang.Code, so)
	if err != nil {
		c.errorf("%s: %v", req.prefix(), err)
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
		c.errorf("%s: --guard strict: %d ambiguous span(s) (%s); resolve them or use --guard fix", req.prefix(), len(res.Findings), spoken.AmbiguousCode)
		return res, ExitUsage
	}
	return res, ExitOK
}
