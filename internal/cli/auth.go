package cli

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/sayso/go/codexapp"
	"github.com/xiaolai/sayso/go/codexauth"
	"github.com/xiaolai/sayso/go/secret"
)

func setupAuth(fs *flag.FlagSet, c *CLI) func([]string) int {
	var deviceAuth bool
	fs.BoolVar(&deviceAuth, "device-auth", false, "auth login: sign in with a device code (no browser on this machine)")
	return func(args []string) int {
		if len(args) == 0 {
			cmd, _ := c.lookup("auth")
			c.writeCommandHelp(c.Stdout, cmd)
			return ExitOK
		}
		switch args[0] {
		case "status":
			if len(args) > 1 {
				c.errorf("recite auth status: takes no arguments")
				return ExitUsage
			}
			if deviceAuth {
				c.errorf("recite auth status: --device-auth belongs to auth login")
				return ExitUsage
			}
			return c.authStatus()
		case "login":
			if len(args) > 1 {
				c.errorf("recite auth login: takes no arguments")
				return ExitUsage
			}
			return c.authLogin(deviceAuth)
		}
		c.errorf("recite auth: unknown subcommand %q; use status or login", args[0])
		return ExitUsage
	}
}

// probe runs the AUTH-07 login-state probe with the global flags. withPlan also reads the plan
// type, which costs another Codex child and a network lookup, so only the commands that show it
// (auth status, doctor) ask for it.
func (c *CLI) probe(ctx context.Context, withPlan bool) *auth.Status {
	st := auth.Probe(ctx, auth.ProbeConfig{
		WithPlan:  withPlan,
		CodexBin:  c.globals.CodexBin,
		CodexHome: c.globals.CodexHome,
		Source:    c.globals.AuthSource,
		Getenv:    c.Getenv,
		Now:       c.Now,
		Logger:    c.Logger,
	})
	if st.Err != nil {
		c.Logger.Debug("login-state probe", "state", string(st.State), "err", st.Err)
	}
	return st
}

// probeWatched is probe with SIGINT and SIGTERM handled as in withCredentials (RUN-03), so an
// interrupt cancels the probe and its Codex children are reaped before recite exits. ok is
// false when an interrupt arrived.
// interruptible returns a context the first SIGINT or SIGTERM cancels, for a command with no
// output worth draining (doctor, usage): cancelling ends any Codex child started under it.
// interrupted reports whether a signal arrived; call done when the command is over.
func (c *CLI) interruptible() (ctx context.Context, interrupted func() bool, done func()) {
	sigs, unsubscribe := c.Signals()
	intr, wctx, release := pipeline.WatchSignals(context.Background(), sigs, c.DrainTimeout, nil)
	ctx, cancel := context.WithCancel(wctx)
	go func() {
		select {
		case <-intr.Stop():
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, intr.Interrupted, func() {
		cancel()
		release()
		unsubscribe()
	}
}

func (c *CLI) probeWatched(withPlan bool) (st *auth.Status, ok bool) {
	sigs, unsubscribe := c.Signals()
	defer unsubscribe()
	intr, ctx, release := pipeline.WatchSignals(context.Background(), sigs, c.DrainTimeout, nil)
	defer release()
	st = c.probe(ctx, withPlan)
	return st, !intr.Interrupted()
}

// authStatus is `recite auth status` (AUTH-06). It prints the state, never the
// token, email or account ID; exit 0 only when ready.
func (c *CLI) authStatus() int {
	st, ok := c.probeWatched(true)
	defer func() { _ = st.Close() }()
	if !ok {
		c.errorf("recite: interrupted; no output was written")
		return ExitInterrupted
	}
	if c.globals.JSON {
		if g, ok := st.Guide(); ok {
			return c.emit(g.Event(), st.ExitCode())
		}
		return c.emit(statusEvent(st, c.Now()), st.ExitCode())
	}
	var b strings.Builder
	row := func(k, v string) { fmt.Fprintf(&b, "%-9s %s\n", k+":", v) }
	row("state", string(st.State))
	row("source", st.SourceName())
	row("codex", codexLine(st))
	if st.Mode != "" {
		row("method", st.Mode)
	}
	if st.State == auth.StateReady {
		row("plan", orUnknown(st.Plan))
		if st.Expires.IsZero() {
			row("expires", "unknown") // the token carries no expiry; the API decides
		} else {
			row("expires", relative(st.Expires.Sub(c.Now())))
		}
	}
	if _, err := io.WriteString(c.Stdout, secret.Scrub(b.String())); err != nil {
		return c.writeResult(err)
	}
	if g, ok := st.Guide(); ok {
		_, _ = io.WriteString(c.Stderr, secret.Scrub(g.Text()))
	}
	return st.ExitCode()
}

// authLogin is `recite auth login` (AUTH-08): it runs `<codex> login` with the resolved
// executable and CODEX_HOME, attached to the user's terminal, then classifies again with a
// new app-server child, because a running child keeps reporting the old sign-in (A#5).
func (c *CLI) authLogin(deviceAuth bool) int {
	switch {
	case c.globals.JSON:
		c.errorf("recite auth login: --json is not supported: Codex's sign-in output would corrupt the event stream")
		return ExitUsage
	case !c.StdinIsTTY():
		c.errorf("recite auth login: stdin is not a terminal; the Codex sign-in is interactive. Run it in a terminal")
		return ExitUsage
	}
	// The signal handling ends with the probe: the interactive login that follows is Codex's own,
	// attached to the terminal, which delivers the user's Ctrl-C to it.
	st, ok := c.probeWatched(false) // the status printed after the login reads the plan
	if !ok {
		_ = st.Close()
		c.errorf("recite: interrupted; Codex was not started")
		return ExitInterrupted
	}
	switch st.State {
	case auth.StateAPIKey, auth.StateUnsupported, auth.StateNoCodex, auth.StateCodexTooOld,
		auth.StateProviderWithoutOpenAIAuth, auth.StateAppServerAuthUnavailable:
		// Changing how Codex itself signs in is the user's decision (api-key, unsupported-auth),
		// without a usable Codex there is nothing to run, and a login cannot lift a managed
		// provider or a Codex that does not report its sign-in: print the guide instead.
		defer func() { _ = st.Close() }()
		g, _ := st.Guide()
		_, _ = io.WriteString(c.Stderr, secret.Scrub(g.Text()))
		return st.ExitCode()
	}
	rc := st.Context
	// The old child must be gone before the state is read again; Close reaps it.
	if err := st.Close(); err != nil {
		c.Logger.Debug("close codex app-server", "err", err)
	}
	// Only an explicit --auth-source file refuses: under auto or codex, a Codex whose store is a
	// file hands the sign-in to auth.json (config/read), yet still signs in.
	switch {
	case c.globals.AuthSource == auth.SourceFile:
		c.errorf("recite auth login: needs the Codex executable; --auth-source file cannot sign in")
		return ExitUsage
	case rc.CodexBin == "":
		c.errorf("recite auth login: needs the Codex executable, and none was found; the sign-in was read from auth.json")
		return ExitUsage
	}
	err := codexapp.Login(rc.CodexConfig(c.Logger), deviceAuth,
		codexapp.Streams{Stdin: c.Stdin, Stdout: c.Stdout, Stderr: c.Stderr})
	if err != nil {
		c.errorf("recite auth login: %v", err)
		return exitcode.Auth
	}
	return c.authStatus()
}

// statusJSON is the --json form of a ready auth status.
type statusJSON struct {
	Event         string `json:"event"`
	State         string `json:"state"`
	Source        string `json:"source"`
	CodexPath     string `json:"codex_path"`
	CodexVersion  string `json:"codex_version"`
	AuthMethod    string `json:"auth_method"`
	PlanType      string `json:"plan_type"`
	ExpiresInSecs *int64 `json:"token_expires_in_s"` // nil when the token carries no expiry
}

func statusEvent(st *auth.Status, now time.Time) statusJSON {
	return statusJSON{
		Event:         "auth_status",
		State:         string(st.State),
		Source:        st.SourceName(),
		CodexPath:     st.Context.CodexBin,
		CodexVersion:  orUnknown(st.CodexVersion),
		AuthMethod:    st.Mode,
		PlanType:      orUnknown(st.Plan),
		ExpiresInSecs: expiresIn(st.Expires, now),
	}
}

func expiresIn(exp, now time.Time) *int64 {
	if exp.IsZero() {
		return nil
	}
	s := int64(exp.Sub(now) / time.Second)
	return &s
}

// emit writes one NDJSON event to stdout and returns code.
func (c *CLI) emit(v any, code int) int {
	b, err := json.Marshal(v)
	if err != nil {
		c.errorf("recite: encode event: %v", err)
		return exitcode.Internal
	}
	if _, err := io.WriteString(c.Stdout, secret.Scrub(string(b))+"\n"); err != nil {
		return c.writeResult(err)
	}
	return code
}

func codexLine(st *auth.Status) string {
	if st.Context.CodexBin == "" {
		return "not found"
	}
	return st.Context.CodexBin + " (version " + orUnknown(st.CodexVersion) + ")"
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// relative renders a token lifetime such as "in 1h 5m" or "2m ago".
func relative(d time.Duration) string {
	past := d < 0
	if past {
		d = -d
	}
	d = d.Round(time.Minute)
	h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
	s := fmt.Sprintf("%dm", m)
	if h > 0 {
		s = fmt.Sprintf("%dh %dm", h, m)
	}
	if past {
		return s + " ago"
	}
	return "in " + s
}

// withCredentials is the gate every command that needs credentials goes
// through (AUTH-07): the probe runs first, so no request reaches the API
// before the login state is known. A non-ready state prints its guide and
// exits 3; fn's failure is reported by class.
//
// SIGINT and SIGTERM are handled as RUN-03 says: the first stops new work and lets in-flight
// work finish for the drain timeout, the second aborts; either way the run exits 130 and the
// Codex child is reaped by the deferred Close.
func (c *CLI) withCredentials(fn func(ctx context.Context, creds *auth.Credentials) error) int {
	sigs, unsubscribe := c.Signals()
	defer unsubscribe()
	intr, ctx, release := pipeline.WatchSignals(context.Background(), sigs, c.DrainTimeout, nil)
	defer release()
	c.intr = intr
	defer func() { c.intr = nil }()
	st := c.probe(ctx, false)
	defer func() { _ = st.Close() }()
	c.authCtx = &st.Context
	defer func() { c.authCtx = nil }()
	if intr.Interrupted() {
		// A probe the interrupt cut short is not a sign-in problem: exit 130, not a login guide.
		c.errorf("recite: interrupted; no output was written")
		return ExitInterrupted
	}
	if g, ok := st.Guide(); ok {
		if c.globals.JSON {
			return c.emit(g.Event(), st.ExitCode())
		}
		_, _ = io.WriteString(c.Stderr, secret.Scrub(g.Text()))
		return st.ExitCode()
	}
	err := fn(ctx, st.Creds)
	if intr.Interrupted() {
		if err != nil && openai.Classify(err) != openai.ClassCancelled {
			c.Logger.Debug("failure during the interrupt", "err", err)
		}
		c.errorf("recite: interrupted; no audio file was published, and audio already streamed to stdout may be incomplete")
		return ExitInterrupted
	}
	return c.reportFailure(err)
}

// interrupted reports whether the first interrupt has arrived: no new work may start, and no
// output may be published (RUN-03).
func (c *CLI) interrupted() bool { return c.intr != nil && c.intr.Interrupted() }

// notifySignals subscribes to SIGINT and SIGTERM.
func notifySignals() (<-chan os.Signal, func()) {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	return ch, func() { signal.Stop(ch) }
}

// runtimeGuide is the login guide for a sign-in failure that comes after the probe passed: a
// refresh that did not renew the token, or a Codex that failed mid-run. It is rendered from the
// probed context, as the probe's own guide is (AUTH-07), so its commands name the Codex in use.
func (c *CLI) runtimeGuide(err error) (auth.Guide, bool) {
	if c.authCtx == nil {
		return auth.Guide{}, false
	}
	var ce *codexauth.Error
	if errors.As(err, &ce) && ce.State != auth.StateReady {
		return auth.GuideFor(ce.State, *c.authCtx, ce.Mode, ce.CodexVersion)
	}
	switch openai.Classify(err) {
	case openai.ClassAuthExpired:
		return auth.GuideFor(auth.StateExpired, *c.authCtx, "", "")
	case openai.ClassCodexError:
		return auth.GuideFor(auth.StateCodexError, *c.authCtx, "", "")
	}
	return auth.Guide{}, false
}

// reportGuide prints a runtime login guide, or emits it as an auth_error event with --json, and
// returns the auth exit code.
func (c *CLI) reportGuide(g auth.Guide) int {
	if c.globals.JSON {
		return c.emit(g.Event(), exitcode.Auth)
	}
	_, _ = io.WriteString(c.Stderr, secret.Scrub(g.Text()))
	return exitcode.Auth
}

// reportFailure prints err for the user and returns its exit code. A classified failure is
// worded by recite from its class, status and code (failureText): the provider's free-text
// message is for diagnostics only and goes to the debug log, never to the terminal. Route
// denial is not a login problem, so it never suggests signing in (AUTH-07).
func (c *CLI) reportFailure(err error) int {
	if err == nil {
		return ExitOK
	}
	var ue *usageError
	if errors.As(err, &ue) {
		c.errorf("recite: %v", err)
		return ExitUsage
	}
	// A run's failures are each reported; the exit code is the most severe (CLI-03).
	var re *pipeline.RunError
	if errors.As(err, &re) {
		for _, e := range re.Errs {
			c.reportFailure(e)
		}
		return re.ExitCode()
	}
	// recite's own errors carry their exit code. A subprocess's *exec.ExitError has an ExitCode
	// method too, but its status is ffmpeg's or a player's, not one of recite's codes: it is
	// classified below like any other failure.
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		if _, sub := coded.(*exec.ExitError); !sub {
			c.errorf("recite: %v", err)
			return coded.ExitCode()
		}
	}
	// The message below is the user's wording; the whole chain (chunk, cause, status, provider
	// code, request id) goes to the debug log, scrubbed, so --verbose can still diagnose it.
	c.Logger.Debug("failure", "err", secret.Scrub(err.Error()))
	var e *openai.Error
	if !errors.As(err, &e) {
		c.errorf("recite: %v", err)
		if g, ok := c.runtimeGuide(err); ok {
			return c.reportGuide(g)
		}
		return openai.ExitCode(err)
	}
	c.errorf("recite: %s", failureText(e))
	if g, ok := c.runtimeGuide(err); ok {
		return c.reportGuide(g)
	}
	return openai.ExitCode(err)
}

// networkCause names a transport failure from the error's type, in recite's words: the
// cause's own text may carry a peer's close reason, which is provider text.
func networkCause(e *openai.Error) string {
	var dns *net.DNSError
	var tlsErr *tls.RecordHeaderError
	var alert tls.AlertError
	var cert *tls.CertificateVerificationError
	switch err := e.Err; {
	case err == nil:
		return ""
	case errors.As(err, &dns):
		return "the name did not resolve"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE):
		return "the connection was reset"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return "timed out"
	case errors.As(err, &tlsErr), errors.As(err, &alert), errors.As(err, &cert):
		return "the TLS handshake failed"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "the connection closed early"
	}
	return ""
}

// failureText is recite's wording of a classified failure: what happened, from the class, then
// the HTTP status and the provider's code when there are any. The provider's free-text message
// (openai.Error.Message) is diagnostic only (spec/http-and-mint.md) and is never part of it;
// the debug log (-vv) has the whole error.
func failureText(e *openai.Error) string {
	var what, after string
	switch e.Class {
	case openai.ClassAuthExpired:
		what = "OpenAI did not accept the ChatGPT sign-in"
	case openai.ClassAuthMode:
		what = "the sign-in is not a ChatGPT sign-in"
	case openai.ClassCodexError:
		what = "Codex could not provide the sign-in"
	case openai.ClassRouteDenied:
		route := string(e.Route)
		if route == "" {
			route = "requested"
		}
		what, after = "your ChatGPT sign-in works, but OpenAI refused the "+route+" route", ". The route may have closed."
	case openai.ClassInvalidRequest:
		what = "OpenAI rejected the request"
	case openai.ClassRateLimited:
		what = "OpenAI's rate or usage limit was reached"
	case openai.ClassServer:
		what = "OpenAI's service failed"
	case openai.ClassNetwork:
		what = "the connection to OpenAI failed"
		if cause := networkCause(e); cause != "" {
			what += ": " + cause
		}
	case openai.ClassProtocol:
		what = "OpenAI's reply was not what recite expected"
	case openai.ClassCancelled:
		what = "interrupted"
	default:
		what = "internal error"
	}
	var detail []string
	if e.Status != 0 {
		detail = append(detail, fmt.Sprintf("HTTP %d", e.Status))
	}
	if e.Code != "" {
		detail = append(detail, "code "+e.Code)
	}
	if len(detail) > 0 {
		what += " (" + strings.Join(detail, ", ") + ")"
	}
	return what + after
}
