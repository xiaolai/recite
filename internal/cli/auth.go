package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/logx"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/pipeline"
)

func setupAuth(_ *flag.FlagSet, c *CLI) func([]string) int {
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
			return c.authStatus()
		case "login":
			return c.notImplemented("auth login")
		}
		c.errorf("recite auth: unknown subcommand %q; use status or login", args[0])
		return ExitUsage
	}
}

// probe runs the AUTH-07 login-state probe with the global flags.
func (c *CLI) probe(ctx context.Context) *auth.Status {
	st := auth.Probe(ctx, auth.ProbeConfig{
		WithPlan:  true,
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

// authStatus is `recite auth status` (AUTH-06). It prints the state, never the
// token, email or account ID; exit 0 only when ready.
func (c *CLI) authStatus() int {
	st := c.probe(context.Background())
	defer func() { _ = st.Close() }()
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
		row("expires", relative(st.Expires.Sub(c.Now())))
	}
	_, _ = io.WriteString(c.Stdout, logx.Scrub(b.String())) // stdout has no error channel worth more than the exit code
	if g, ok := st.Guide(); ok {
		_, _ = io.WriteString(c.Stderr, logx.Scrub(g.Text()))
	}
	return st.ExitCode()
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
	ExpiresInSecs int64  `json:"token_expires_in_s"`
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
		ExpiresInSecs: int64(st.Expires.Sub(now) / time.Second),
	}
}

// emit writes one NDJSON event to stdout and returns code.
func (c *CLI) emit(v any, code int) int {
	b, err := json.Marshal(v)
	if err != nil {
		c.errorf("recite: encode event: %v", err)
		return exitcode.Internal
	}
	if _, err := io.WriteString(c.Stdout, logx.Scrub(string(b))+"\n"); err != nil {
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
	st := c.probe(ctx)
	defer func() { _ = st.Close() }()
	if g, ok := st.Guide(); ok {
		if c.globals.JSON {
			return c.emit(g.Event(), st.ExitCode())
		}
		_, _ = io.WriteString(c.Stderr, logx.Scrub(g.Text()))
		return st.ExitCode()
	}
	err := fn(ctx, st.Creds)
	if intr.Interrupted() {
		if err != nil && openai.Classify(err) != openai.ClassCancelled {
			c.Logger.Debug("failure during the interrupt", "err", err)
		}
		c.errorf("recite: interrupted; no output was written")
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

// reportFailure prints err for the user and returns its exit code. Messages
// come from openai.Error.Message, not Error(), which carries a prefix. Route
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
	var e *openai.Error
	if !errors.As(err, &e) {
		c.errorf("recite: %v", err)
		return openai.ExitCode(err)
	}
	switch e.Class {
	case openai.ClassRouteDenied:
		c.errorf("recite: your ChatGPT sign-in works, but OpenAI refused the %s route (HTTP %d): %s. The route may have closed.", e.Route, e.Status, e.Message)
	default:
		c.errorf("recite: %s: %s", e.Class, e.Message)
	}
	return openai.ExitCode(err)
}
