package cli

import (
	"errors"
	"flag"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/config"
	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/guard"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/text"
	"github.com/xiaolai/recite/internal/verify"
)

// Exit codes used by the router; the values live in internal/exitcode (design §12.3).
const (
	ExitOK       = exitcode.OK
	ExitInternal = exitcode.Internal
	ExitUsage    = exitcode.Usage

	ExitInterrupted = exitcode.Interrupted
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
		{"check", "FILE", "Text guard, spoken form and chunk plan, no network", setupCheck},
		{"prepare", "FILE", "Write the editable spoken-form script", setupPrepare},
		{"voices", "", "List voices; --preview DIR renders a sample of each", setupVoices},
		{"doctor", "", "Check the environment and the route", setupDoctor},
		{"auth", "status|login", "Login state, or sign in through codex", setupAuth},
		{"usage", "", "Local ledger summary and Codex plan status", setupUsage},
		{"cache", "ls|prune|clear", "Manage the take cache", setupCache},
		{"config", "show|path", "Effective merged config and its sources", setupConfig},
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
	Globals    Globals
	File       string
	Text       []string
	Output     string
	Format     audio.Format   // --format; "" means from the output's extension
	SampleRate int            // --sample-rate (AUD-05)
	Loudness   audio.Loudness // --loudness (AUD-06); "" means the format's default
	Play       bool           // --play (AUD-08)
	Force      bool
	MaxInput   int64
	Settings   realtime.Settings
	// RetryEffort is --retry-effort (SPK-11); nil means every attempt runs at Settings.Effort.
	RetryEffort []realtime.Effort
	Guard       guard.Options
	Spoken      spoken.Options

	InputFormat string               // --input-format: text, markdown (TXT-05) or spoken (TXT-15)
	Marks       bool                 // speech marks are read (TXT-13)
	Markdown    text.MarkdownOptions // --read-alt and --code (TXT-05)
	Lang        string               // the front-matter language (TXT-08); "" means detect
	preread     *prereadInput        // the input, when it was read before the settings; nil means read it

	ChunkSeconds  float64 // segmentation budget (TXT-03)
	Gaps          audio.Gaps
	Jobs          int // render workers (RUN-02)
	VerifyJobs    int // verify workers; 0 means 2 × Jobs
	SessionChunks int // renders per pooled session before rotation (SPK-06)
	Verify        verify.Options
	Policy        verify.OutputPolicy
	CacheDir      string // the take cache root (cache_dir); "" means no cache
	NoCache       bool   // --no-cache: no cache reads or writes
	Refresh       bool   // --refresh: no cache reads

	Manifest     string // --manifest PATH; "" means <output>.json
	NoManifest   bool   // --no-manifest
	KeepRejected string // --keep-rejected DIR; "" keeps none

	DryRun bool // --dry-run: plan and estimate only, no network (RUN-06)

	Command string // the command named in error messages; "" means say
}

// prefix starts an error message from the command that ran the request.
func (r SayRequest) prefix() string {
	if r.Command == "" {
		return "recite say"
	}
	return "recite " + r.Command
}

// hiddenFlags are registered but left out of help (design §12.2).
var hiddenFlags = map[string]bool{"instructions-version": true}

// sayFlags holds the raw speech flags; validation happens after parsing so
// every error lists the valid values and nothing touches the network first.
type sayFlags struct {
	model, voice, speed, effort string
	retryEffort                 string
	modelUnchecked              bool
	instructionsVersion         int
}

func (f *sayFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.model, "model", realtime.DefaultModel, "speech `MODEL`: "+strings.Join(realtime.Models, ", "))
	fs.BoolVar(&f.modelUnchecked, "model-unchecked", false, "accept any --model name")
	fs.StringVar(&f.voice, "voice", realtime.DefaultVoice, "`VOICE`: "+strings.Join(realtime.Voices, ", "))
	fs.StringVar(&f.speed, "speed", "1.0", "`SPEED` from 0.25 to 1.5 in steps of 0.01")
	fs.StringVar(&f.effort, "effort", string(realtime.DefaultEffort), "reasoning `EFFORT`: minimal, low, medium, high, xhigh")
	fs.StringVar(&f.retryEffort, "retry-effort", "", "`LIST` of efforts for attempts 1, 2, 3…, the last repeating, e.g. minimal,low,medium (default off)")
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

// textFlags are the text-stage flags `say` and `check` share: input limit,
// guard, spoken form and segmentation.
type textFlags struct {
	maxInput     int64
	policy       string
	normalize    string
	dateStyle    string
	disableSpecs []string
	chunkSeconds float64
	inputFormat  string
	marks        bool
	noMarks      bool
	readAlt      bool
	code         string
}

func (f *textFlags) register(fs *flag.FlagSet) {
	f.maxInput = text.DefaultMaxInput
	fs.Int64Var(&f.maxInput, "max-input", f.maxInput, "input size limit in `BYTES`")
	f.policy = string(guard.PolicyFix)
	fs.StringVar(&f.policy, "guard", f.policy, "text guard `POLICY`: fix, strict, warn or off")
	f.normalize, f.dateStyle = string(spoken.ModeAuto), string(spoken.DateUS)
	fs.StringVar(&f.normalize, "normalize", f.normalize, "spoken-form `MODE`: auto or off")
	fs.Var(listFlag{&f.disableSpecs}, "normalize-disable", "turn off spoken-form rules, `RULE,…` (repeatable)")
	fs.StringVar(&f.dateStyle, "date-style", f.dateStyle, "spoken date `STYLE`: us, uk or iso")
	f.chunkSeconds = text.DefaultChunkSeconds
	fs.Float64Var(&f.chunkSeconds, "chunk-seconds", f.chunkSeconds, "segmentation budget in `SECONDS` per chunk")
	fs.StringVar(&f.inputFormat, "input-format", "", "input `FORMAT`: text, markdown, or spoken (a prepare script: no guard, no spoken-form rules); default markdown for .md and .markdown files, else text")
	fs.BoolVar(&f.readAlt, "read-alt", false, "read the alt text of Markdown images")
	f.code = codeSkip
	fs.StringVar(&f.code, "code", f.code, "Markdown code blocks: `MODE` skip, or announce (say \"Code block omitted.\")")
	fs.BoolVar(&f.marks, "marks", false, "read inline speech marks such as {{spell|API}} (default on for spoken input)")
	fs.BoolVar(&f.noMarks, "no-marks", false, "read {{…}} as plain text")
}

// resolve validates the text flags; actions are the per-code guard actions of [guard].
func (f *textFlags) resolve(actions map[guard.Code]guard.Action) (guard.Options, spoken.Options, error) {
	if f.maxInput <= 0 {
		return guard.Options{}, spoken.Options{}, errors.New("--max-input must be a positive number of bytes")
	}
	if !(f.chunkSeconds > 0) || math.IsInf(f.chunkSeconds, 0) {
		return guard.Options{}, spoken.Options{}, fmt.Errorf("--chunk-seconds %v: want a positive number of seconds", f.chunkSeconds)
	}
	p, err := guard.ParsePolicy(f.policy)
	if err != nil {
		return guard.Options{}, spoken.Options{}, err
	}
	g := guard.Options{Policy: p, Actions: actions}
	so, err := spokenOptions(f.normalize, f.dateStyle, f.disableSpecs)
	if err != nil {
		return guard.Options{}, spoken.Options{}, err
	}
	return g, so, nil
}

// runFlags are the M2 rendering flags of `say`: scheduling, verification and gaps.
type runFlags struct {
	jobs, verifyJobs, sessionChunks   int
	verify                            string
	maxAttempts, requestExtraAttempts int
	noTrim                            bool
	allowUnverified, strictOutput     bool
	noCache, refresh                  bool
	gapSentence, gapList              string
	gapParagraph, gapSection          string
	gapSeed                           int64
	loudness                          string
	play                              bool
}

func (f *runFlags) register(fs *flag.FlagSet) {
	fs.IntVar(&f.jobs, "jobs", pipeline.DefaultJobs, fmt.Sprintf("render `N` chunks at once, 1 to %d", pipeline.MaxJobs))
	fs.IntVar(&f.verifyJobs, "verify-jobs", 0, "verify `N` takes at once (default 2 × --jobs)")
	fs.IntVar(&f.sessionChunks, "session-chunks", realtime.DefaultSessionChunks, "renders per speech session before it is replaced, `N`")
	fs.StringVar(&f.verify, "verify", string(verify.ModeNormal), "verification `MODE`: strict, normal or off")
	fs.IntVar(&f.maxAttempts, "max-attempts", verify.DefaultMaxAttempts, "renders per chunk, `N`")
	fs.IntVar(&f.requestExtraAttempts, "request-extra-attempts", verify.DefaultRequestExtraAttempts, "extra renders for a request-like chunk, `N`")
	fs.BoolVar(&f.noTrim, "no-trim", false, "keep the leading and trailing silence of each take")
	fs.BoolVar(&f.allowUnverified, "allow-unverified", false, "exit 0 when a chunk stays unverified")
	fs.BoolVar(&f.strictOutput, "strict-output", false, "write no output when a chunk stays unverified")
	fs.BoolVar(&f.noCache, "no-cache", false, "neither read nor write the take cache")
	fs.BoolVar(&f.refresh, "refresh", false, "render every chunk anew; the takes still go to the cache")
	d := audio.DefaultGaps()
	fs.StringVar(&f.gapSentence, "gap-sentence", d.Sentence.String(), "gap after a sentence, `DUR` or MIN..MAX")
	fs.StringVar(&f.gapList, "gap-list", d.List.String(), "gap after a list item, `DUR` or MIN..MAX")
	fs.StringVar(&f.gapParagraph, "gap-paragraph", d.Paragraph.String(), "gap after a paragraph, `DUR` or MIN..MAX")
	fs.StringVar(&f.gapSection, "gap-section", d.Section.String(), "gap after a section, `DUR` or MIN..MAX")
	fs.Int64Var(&f.gapSeed, "gap-seed", 0, "`SEED` of the random gaps; the same seed gives the same gaps")
	fs.StringVar(&f.loudness, "loudness", "", "loudness `PRESET`: podcast, audiobook or none (default podcast for encoded formats, none for wav, pcm and -o -)")
	fs.BoolVar(&f.play, "play", false, "play the output when done")
}

// apply validates the flags into req. Every error is a usage error (exit 2).
func (f *runFlags) apply(req *SayRequest) error {
	if err := pipeline.ValidateJobs(f.jobs, f.verifyJobs); err != nil {
		return err
	}
	if f.sessionChunks < 1 {
		return fmt.Errorf("--session-chunks %d: want at least 1", f.sessionChunks)
	}
	mode, err := verify.ParseMode(f.verify)
	if err != nil {
		return err
	}
	if f.maxAttempts < 1 {
		return fmt.Errorf("--max-attempts %d: want at least 1", f.maxAttempts)
	}
	if f.requestExtraAttempts < 0 {
		return fmt.Errorf("--request-extra-attempts %d: want 0 or more", f.requestExtraAttempts)
	}
	g := audio.Gaps{Seed: f.gapSeed}
	for _, x := range []struct {
		name, val string
		dst       *audio.GapRange
	}{
		{"--gap-sentence", f.gapSentence, &g.Sentence},
		{"--gap-list", f.gapList, &g.List},
		{"--gap-paragraph", f.gapParagraph, &g.Paragraph},
		{"--gap-section", f.gapSection, &g.Section},
	} {
		r, err := audio.ParseGapRange(x.val)
		if err != nil {
			return fmt.Errorf("%s: %w", x.name, err)
		}
		*x.dst = r
	}
	req.Jobs, req.VerifyJobs, req.SessionChunks = f.jobs, f.verifyJobs, f.sessionChunks
	req.Verify = verify.Options{Mode: mode, MaxAttempts: f.maxAttempts, RequestExtraAttempts: f.requestExtraAttempts, NoTrim: f.noTrim}
	req.Policy = verify.OutputPolicy{AllowUnverified: f.allowUnverified, StrictOutput: f.strictOutput, VerifyOff: mode == verify.ModeOff}
	req.Gaps = g
	req.NoCache, req.Refresh = f.noCache, f.refresh
	if f.loudness != "" {
		if req.Loudness, err = audio.ParseLoudness(f.loudness); err != nil {
			return err
		}
	}
	req.Play = f.play
	return nil
}

// settingFlags are the flags of every setting `say` takes; `config show` registers them too,
// so it reports exactly what `say` would use.
type settingFlags struct {
	sf sayFlags
	tf textFlags
	rf runFlags
}

func (s *settingFlags) register(fs *flag.FlagSet) {
	s.sf.register(fs)
	s.tf.register(fs)
	s.rf.register(fs)
}

// textSettings resolves every layer into fs (CFG-04) and validates the text-stage settings.
func (c *CLI) textSettings(fs *flag.FlagSet, tf *textFlags, fm *frontMatterSource) (guard.Options, spoken.Options, config.Settings, error) {
	st, err := c.resolveSettings(fs, fm)
	if err != nil {
		return guard.Options{}, spoken.Options{}, st, err
	}
	actions, err := guardActions(st)
	if err != nil {
		return guard.Options{}, spoken.Options{}, st, err
	}
	g, so, err := tf.resolve(actions)
	return g, so, st, err
}

func setupSay(fs *flag.FlagSet, c *CLI) func([]string) int {
	var file string
	var all settingFlags
	fs.StringVar(&file, "f", "", "read text from `PATH`")
	fs.StringVar(&file, "file", "", "read text from `PATH`")
	var output string
	var force bool
	fs.StringVar(&output, "o", "", "write audio to `PATH`, or - for stdout (default <file stem>.<format> or speech-<timestamp>.<format>)")
	fs.StringVar(&output, "output", "", "write audio to `PATH`, or - for stdout (default <file stem>.<format> or speech-<timestamp>.<format>)")
	fs.BoolVar(&force, "force", false, "overwrite the output")
	var format string
	var sampleRate string
	fs.StringVar(&format, "format", "", "output `FMT`: wav pcm mp3 m4a m4b opus ogg flac (default from the extension, else wav)")
	fs.StringVar(&sampleRate, "sample-rate", strconv.Itoa(audio.SampleRate), "output sample rate `HZ`: 16000, 22050, 24000, 44100, 48000")
	var manifestPath, keepRejected string
	var noManifest bool
	fs.StringVar(&manifestPath, "manifest", "", "write the manifest to `PATH` (default <output>.json)")
	fs.BoolVar(&noManifest, "no-manifest", false, "write no manifest")
	fs.StringVar(&keepRejected, "keep-rejected", "", "save every rejected take and its report in `DIR`")
	var dryRun bool
	fs.BoolVar(&dryRun, "dry-run", false, "print the chunk plan and estimates (cache hits, audio, wall time, list price); no network, no login")
	all.register(fs)
	run := func(args []string) int {
		pre, code := c.prereadMarkdown("recite say", &all.tf, text.Source{Args: args, File: file, Stdin: c.Stdin, StdinIsTTY: c.StdinIsTTY()}, file)
		if code != ExitOK {
			return code
		}
		req, err := c.sayRequest(fs, &all, pre.frontMatter(), file)
		if err != nil {
			c.errorf("recite say: %v", err)
			return ExitUsage
		}
		req.File, req.Text, req.Output, req.Force = file, args, output, force
		req.preread = pre
		if noManifest && manifestPath != "" {
			c.errorf("recite say: --manifest and --no-manifest exclude each other")
			return ExitUsage
		}
		req.Manifest, req.NoManifest, req.KeepRejected = manifestPath, noManifest, keepRejected
		req.DryRun = dryRun
		if format != "" {
			if req.Format, err = audio.ParseFormat(format); err != nil {
				c.errorf("recite say: --format: %v", err)
				return ExitUsage
			}
		}
		if req.SampleRate, err = audio.ParseSampleRate(sampleRate); err != nil {
			c.errorf("recite say: %v", err)
			return ExitUsage
		}
		return c.Say(req)
	}
	// Under --json the last line is done, whatever ended the run (CLI-06).
	return func(args []string) int { return c.sayDone(run(args)) }
}

// sayRequest resolves every setting of fs (flags, environment, config) into a request with no
// source or output; every error is a usage error. `say` and `voices --preview` share it.
func (c *CLI) sayRequest(fs *flag.FlagSet, all *settingFlags, fm *frontMatterSource, file string) (SayRequest, error) {
	g, so, st, err := c.textSettings(fs, &all.tf, fm)
	if err != nil {
		return SayRequest{}, err
	}
	settings, err := all.sf.settings()
	if err != nil {
		return SayRequest{}, err
	}
	ladder, err := realtime.ParseEffortLadder(all.sf.retryEffort)
	if err != nil {
		return SayRequest{}, err
	}
	inFormat, marks, err := all.tf.inputMode(file)
	if err != nil {
		return SayRequest{}, err
	}
	mdOpts, err := all.tf.markdownOptions()
	if err != nil {
		return SayRequest{}, err
	}
	g.Marks = marks
	g.Markdown = inFormat == formatMarkdown
	req := SayRequest{Globals: c.globals, MaxInput: all.tf.maxInput, Settings: settings, RetryEffort: ladder, Guard: g, Spoken: so,
		InputFormat: inFormat, Marks: marks, Markdown: mdOpts, Lang: st.Get(langKey).Raw,
		ChunkSeconds: all.tf.chunkSeconds, CacheDir: st.Get("cache_dir").Raw}
	if err := all.rf.apply(&req); err != nil {
		return SayRequest{}, err
	}
	return req, nil
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
