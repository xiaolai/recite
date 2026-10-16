package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/fsx"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/stt"
	"github.com/xiaolai/recite/internal/verify"
	"github.com/xiaolai/sayso/go/secret"
)

// Check outcomes of `doctor` (design §12.4).
const (
	checkOK   = "ok"
	checkWarn = "warn"
	checkFail = "fail"
)

// doctorSentence is what --deep renders and transcribes (design §12.4 check 5).
const doctorSentence = "Doctor check."

// tokenMargin is the token lifetime below which doctor warns (design §12.4 check 2).
const tokenMargin = time.Hour

// doctorCheck is one line of `doctor`; Code is the exit code a failure contributes.
type doctorCheck struct {
	Event  string `json:"event"`
	Name   string `json:"check"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
	Code   int    `json:"exit"`
}

func okCheck(name, detail string) doctorCheck {
	return doctorCheck{Event: "doctor_check", Name: name, Status: checkOK, Detail: detail}
}

func warnCheck(name, detail, fix string) doctorCheck {
	return doctorCheck{Event: "doctor_check", Name: name, Status: checkWarn, Detail: detail, Fix: fix}
}

func failCheck(name, detail, fix string, code int) doctorCheck {
	return doctorCheck{Event: "doctor_check", Name: name, Status: checkFail, Detail: detail, Fix: fix, Code: code}
}

func setupDoctor(fs *flag.FlagSet, c *CLI) func([]string) int {
	var deep bool
	fs.BoolVar(&deep, "deep", false, "also render \""+doctorSentence+"\" and transcribe it (uses the service)")
	return func(args []string) int {
		if len(args) > 0 {
			c.errorf("recite doctor: takes no arguments")
			return ExitUsage
		}
		ctx, interrupted, done := c.interruptible()
		checks := c.doctor(ctx, deep)
		done()
		if interrupted() {
			c.errorf("recite: interrupted; the checks were stopped and nothing was printed")
			return ExitInterrupted
		}
		codes := make([]int, 0, len(checks))
		for _, ch := range checks {
			codes = append(codes, ch.Code)
			if c.globals.JSON {
				if code := c.emit(ch, ExitOK); code != ExitOK {
					return code
				}
				continue
			}
			line := fmt.Sprintf("%-4s  %-9s %s\n", ch.Status, ch.Name, ch.Detail)
			if ch.Fix != "" {
				line += fmt.Sprintf("%-4s  %-9s fix: %s\n", "", "", ch.Fix)
			}
			if _, err := io.WriteString(c.Stdout, terminalSafe(secret.Scrub(line))); err != nil {
				return c.writeResult(err)
			}
		}
		// The most severe failing check decides the exit code (CLI-05, design §12.3 order).
		return exitcode.Worst(codes...)
	}
}

// doctor runs the checks of design §12.4 in order. A check that needs an earlier one to pass
// is reported as a warning saying so, so every check still prints a line.
func (c *CLI) doctor(ctx context.Context, deep bool) []doctorCheck {
	st := c.probe(ctx, true) // the auth line shows the plan
	defer func() { _ = st.Close() }()
	c.authCtx = &st.Context // a sign-in failure in a later check gets the guide for this Codex
	defer func() { c.authCtx = nil }()
	checks := []doctorCheck{codexCheck(st)}
	authCheck := c.authCheck(st)
	checks = append(checks, authCheck)
	ready := st.State == auth.StateReady
	if ready {
		checks = append(checks, planCheck(ctx, st))
	} else {
		checks = append(checks, warnCheck("plan", "not checked: the sign-in is not ready", ""))
	}
	if ready {
		route, sess := c.routeCheck(ctx, st.Creds)
		checks = append(checks, route)
		if deep && sess != nil {
			checks = append(checks, c.deepCheck(ctx, st.Creds, sess))
		} else if deep {
			checks = append(checks, warnCheck("deep", "not checked: the route check failed", ""))
		}
		if sess != nil {
			_ = sess.Close()
		}
	} else {
		checks = append(checks, warnCheck("route", "not checked: the sign-in is not ready", ""))
		if deep {
			checks = append(checks, warnCheck("deep", "not checked: the sign-in is not ready", ""))
		}
	}
	checks = append(checks, c.encoderChecks()...)
	return append(checks, c.dirChecks()...)
}

// codexCheck is check 1: the executable and its version.
func codexCheck(st *auth.Status) doctorCheck {
	switch {
	case st.State == auth.StateNoCodex && st.Context.MissingBin != "":
		// Installing Codex cannot repair a stale override: say which one to fix.
		return failCheck("codex", "the executable named by --codex-bin or RECITE_CODEX_BIN was not found: "+st.Context.MissingBin,
			"correct or remove --codex-bin / RECITE_CODEX_BIN", st.ExitCode())
	case st.State == auth.StateNoCodex:
		g, _ := st.Guide()
		return failCheck("codex", "not found", firstFix(g), st.ExitCode())
	case st.State == auth.StateCodexTooOld:
		g, _ := st.Guide()
		return failCheck("codex", st.Context.CodexBin+" (version "+orUnknown(st.CodexVersion)+") is older than "+auth.MinCodexVersion, firstFix(g), st.ExitCode())
	case st.Context.CodexBin == "":
		return warnCheck("codex", "not found; using the auth file", "install Codex: npm install -g @openai/codex")
	}
	return okCheck("codex", st.Context.CodexBin+" (version "+orUnknown(st.CodexVersion)+")")
}

// authCheck is check 2: a ChatGPT sign-in, its plan, and more than an hour of token life.
func (c *CLI) authCheck(st *auth.Status) doctorCheck {
	switch st.State {
	case auth.StateReady:
	case auth.StateNoCodex, auth.StateCodexTooOld:
		return warnCheck("auth", "not checked: Codex is not usable (see the codex line)", "")
	default:
		g, _ := st.Guide()
		detail := string(st.State) + ": " + g.Message
		if st.Err != nil { // what Codex or the file actually did: a timeout, malformed data, an unusable file
			detail += " (" + secret.Scrub(st.Err.Error()) + ")"
		}
		return failCheck("auth", detail, guideFix(g), st.ExitCode())
	}
	if st.Expires.IsZero() { // the token carries no expiry; the API decides
		return okCheck("auth", "chatgpt, plan "+orUnknown(st.Plan)+", token expiry unknown")
	}
	left := st.Expires.Sub(c.Now())
	detail := "chatgpt, plan " + orUnknown(st.Plan) + ", token expires " + relative(left)
	if left < tokenMargin {
		return warnCheck("auth", detail, auth.CodexCommand(st.Context)+" login") // login status does not refresh
	}
	return okCheck("auth", detail)
}

// planCheck is check 3: the Codex plan status.
func planCheck(ctx context.Context, st *auth.Status) doctorCheck {
	rl, read, err := st.PlanStatus(ctx)
	switch {
	case err != nil:
		return warnCheck("plan", "Codex could not report the plan status: "+errMessage(err), auth.CodexCommand(st.Context)+" login status")
	case !read:
		return warnCheck("plan", "not checked: the plan status is read through the Codex app-server, and the auth source is file", "")
	case rl.OrdinaryUsageKnown && !rl.OrdinaryUsageAllowed: // an omitted field is not a denial
		return warnCheck("plan", "plan "+orUnknown(rl.PlanType)+": Codex reports that ordinary usage is not allowed now", "wait for the usage window to reset")
	}
	parts := []string{"plan " + orUnknown(rl.PlanType)}
	for _, w := range rl.Windows {
		parts = append(parts, fmt.Sprintf("%s window %.0f%% used", w.Name, w.UsedPercent))
	}
	return okCheck("plan", strings.Join(parts, ", "))
}

// routeCheck is check 4: mint a speech secret, open the WebSocket and receive
// session.created. The open session is returned for --deep; the caller closes it.
func (c *CLI) routeCheck(ctx context.Context, creds *auth.Credentials) (doctorCheck, realtime.SpeechSession) {
	spec, err := realtime.DefaultSettings().SessionSpec()
	if err != nil {
		return failCheck("route", err.Error(), "", exitcode.Internal), nil
	}
	synth, err := c.NewSynth(creds)
	if err != nil {
		return failCheck("route", err.Error(), "", exitcode.Internal), nil
	}
	start := time.Now() // timing only; never asserted
	sess, err := synth.Open(ctx, spec)
	if err != nil {
		return failCheck("route", routeFailure(err), c.routeFix(err), openai.ExitCode(err)), nil
	}
	if a, ok := sess.(realtime.CreatedAwaiter); ok {
		if err := a.AwaitCreated(ctx); err != nil {
			_ = sess.Close()
			return failCheck("route", routeFailure(err), c.routeFix(err), openai.ExitCode(err)), nil
		}
	}
	return okCheck("route", fmt.Sprintf("speech secret minted, session opened (%s, %s)", spec.Model, time.Since(start).Round(time.Millisecond))), sess
}

func routeFailure(err error) string {
	var e *openai.Error
	if errors.As(err, &e) && e.Class == openai.ClassRouteDenied {
		return fmt.Sprintf("OpenAI refused the %s route (HTTP %d): %s", e.Route, e.Status, e.Message)
	}
	return "the speech session could not be opened: " + err.Error() // the cause names the refusal; output is scrubbed
}

// routeFix never suggests signing in for a route denial: the sign-in works (AUTH-07). A sign-in
// that failed after the probe passed gets the login guide for the probed Codex.
func (c *CLI) routeFix(err error) string {
	if g, ok := c.runtimeGuide(err); ok {
		return guideFix(g)
	}
	switch openai.Classify(err) {
	case openai.ClassRouteDenied:
		return "the route may have closed; check for a newer recite"
	case openai.ClassRateLimited:
		return "wait and run doctor again"
	}
	return "check the network connection to api.openai.com, then run doctor again"
}

// deepCheck is check 5: render the doctor sentence and transcribe it over HTTP.
func (c *CLI) deepCheck(ctx context.Context, creds *auth.Credentials, sess realtime.SpeechSession) doctorCheck {
	start := time.Now()
	estimate := realtime.EstimateDuration(doctorSentence, realtime.DefaultSpeed)
	take, err := sess.Render(ctx, realtime.RenderRequest{ChunkID: "doctor", Text: doctorSentence,
		Effort: realtime.DefaultEffort, Attempt: 1, Estimate: estimate})
	if err != nil {
		return failCheck("deep", "render failed: "+errMessage(err), c.routeFix(err), openai.ExitCode(err))
	}
	rendered := time.Since(start)
	// The audio guards (status, empty, duration) need no STT: a take they reject is a speech
	// failure in every verification mode. The said guard is not judged here (Spoken is the
	// model's own transcript), as the doctor checks the audio, not the wording.
	if _, rej := verify.PreSTT(verify.Chunk{ID: "doctor", Spoken: take.Said, Estimate: estimate}, take, false); rej != nil {
		return failCheck("deep", fmt.Sprintf("the rendered take was rejected (%s): %s", rej.Reason, rej.Detail),
			"run doctor --deep again; a repeat failure is worth reporting", exitcode.VerifyFailed)
	}
	// With verification off (the default, D23) say never transcribes, so an STT problem is
	// reported as a warning that does not fail doctor.
	sttOptional := c.verifyMode() == verify.ModeOff
	sttProblem := func(detail, fix string, code int) doctorCheck {
		if sttOptional {
			return warnCheck("deep", detail+" (STT is used only by --verify normal or strict)", fix)
		}
		return failCheck("deep", detail, fix, code)
	}
	tr, closeTr, err := c.NewTranscriber(creds)
	if err != nil {
		return sttProblem(err.Error(), "", exitcode.Internal)
	}
	defer func() { _ = closeTr() }()
	t, err := tr.Transcribe(ctx, take.PCM, stt.Hint{Languages: []string{"en"}})
	if err != nil {
		return sttProblem("transcription failed: "+errMessage(err), c.routeFix(err), openai.ExitCode(err))
	}
	res := verify.Score(doctorSentence, t.Text, verify.ScriptMetric(doctorSentence))
	detail := fmt.Sprintf("score %.3f, render %s, transcription %s", res.Score,
		rendered.Round(time.Millisecond), (time.Since(start) - rendered).Round(time.Millisecond))
	if res.Errors > 0 {
		return sttProblem(detail+": heard "+fmt.Sprintf("%q", t.Text), "run doctor --deep again; a repeat failure is worth reporting", exitcode.VerifyFailed)
	}
	return okCheck("deep", detail)
}

// verifyMode is the verification mode say would use from the config file, profile and
// environment; an unreadable config counts as the default (dirChecks reports that error).
func (c *CLI) verifyMode() verify.Mode {
	st, err := c.resolveSettings(newFlagSet("doctor-settings"), nil)
	if err != nil {
		return verify.DefaultOptions().Mode
	}
	m, err := verify.ParseMode(st.Get("verify.mode").Raw)
	if err != nil {
		return verify.DefaultOptions().Mode
	}
	return m
}

// encoderChecks is check 6: the external encoders, warn only.
func (c *CLI) encoderChecks() []doctorCheck {
	names := []string{"ffmpeg"}
	if runtime.GOOS == "darwin" {
		names = append(names, "afconvert")
	}
	var out []doctorCheck
	for _, n := range names {
		if p, err := c.LookPath(n); err == nil {
			out = append(out, okCheck(n, p))
			continue
		}
		fix := "install ffmpeg (brew install ffmpeg, or your package manager)"
		if n == "afconvert" {
			fix = "afconvert ships with macOS; check that /usr/bin is on PATH"
		}
		out = append(out, warnCheck(n, "not found; formats that need it are unavailable", fix))
	}
	return out
}

// dirChecks is check 7: the cache and state directories are writable.
func (c *CLI) dirChecks() []doctorCheck {
	cacheDir := ""
	if st, err := c.resolveSettings(newFlagSet("doctor-settings"), nil); err == nil {
		cacheDir = st.Get("cache_dir").Raw
	} else {
		// The state directory does not depend on the config: still check it.
		return []doctorCheck{failCheck("cache", "config: "+err.Error(), "fix the config file (recite config path)", exitcode.Usage),
			dirCheck("state", c.stateDir())}
	}
	return []doctorCheck{dirCheck("cache", cacheDir), dirCheck("state", c.stateDir())}
}

func dirCheck(name, dir string) doctorCheck {
	if dir == "" {
		return warnCheck(name, "no directory: HOME is not set", "set HOME or XDG_CACHE_HOME / XDG_STATE_HOME")
	}
	if err := writable(dir); err != nil {
		return failCheck(name, dir+" is not writable: "+err.Error(), "fix the permissions of "+dir, exitcode.Usage)
	}
	return okCheck(name, dir+" writable")
}

// writable checks dir the way the cache and the usage ledger open it (fsx.OpenPrivate: created
// 0700 when missing, never a symlink, owned by the user), then writes, syncs and removes a probe
// file with data in it through the directory held open, so a filesystem that creates files but
// refuses their data fails here.
func writable(dir string) error {
	d, err := fsx.OpenPrivate(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }() // a read-only directory descriptor
	f, name, err := d.CreateTemp(".doctor-")
	if err != nil {
		return err
	}
	_, err = f.Write([]byte("recite doctor write probe\n"))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if rerr := d.Root().Remove(name); err == nil {
		err = rerr
	}
	return err
}

// guideFix is the whole guide on one line: every step's explanation and command, in order, so a
// sequence (logout, then login) keeps its second half and a warning or prerequisite stays with
// the commands it qualifies.
func guideFix(g auth.Guide) string {
	var parts []string
	for _, s := range g.Steps {
		parts = append(parts, strings.TrimSpace(s.Text+" "+s.Command))
	}
	if len(parts) == 0 {
		return auth.AuthURL
	}
	return strings.Join(parts, "; ")
}

func firstFix(g auth.Guide) string {
	if cmds := g.FixCommands(); len(cmds) > 0 {
		return cmds[0]
	}
	return auth.AuthURL
}

// errMessage is the user-facing message of err: openai.Error.Message without its prefix.
func errMessage(err error) string {
	var e *openai.Error
	if errors.As(err, &e) && e.Message != "" {
		return e.Message
	}
	return err.Error()
}
