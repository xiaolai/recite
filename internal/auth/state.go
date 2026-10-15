package auth

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xiaolai/sayso/go/codexapp"
	"github.com/xiaolai/sayso/go/codexauth"
	"github.com/xiaolai/sayso/go/secret"

	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/testguard"
)

// State is the login state of design §5.1a (AUTH-07). Exactly one applies.
type State = codexauth.State

// The login states (github.com/xiaolai/sayso/go/codexauth).
const (
	StateReady                     = codexauth.StateReady
	StateNoCodex                   = codexauth.StateNoCodex
	StateCodexTooOld               = codexauth.StateCodexTooOld
	StateNotSignedIn               = codexauth.StateNotSignedIn
	StateNoSignInFile              = codexauth.StateNoSignInFile
	StateAPIKey                    = codexauth.StateAPIKey
	StateUnsupported               = codexauth.StateUnsupported
	StateExpired                   = codexauth.StateExpired
	StateCodexError                = codexauth.StateCodexError
	StateAppServerAuthUnavailable  = codexauth.StateAppServerAuthUnavailable
	StateProviderWithoutOpenAIAuth = codexauth.StateProviderWithoutOpenAIAuth
)

// MinCodexVersion is the oldest Codex recite is tested against.
const MinCodexVersion = codexauth.MinCodexVersion

// RefreshMargin is how much lifetime a token must have left; below it, recite asks Codex to
// refresh (AUTH-04).
const RefreshMargin = 10 * time.Minute

// DefaultTimeout bounds every app-server call (design §5.1a, AUTH-01#2).
const DefaultTimeout = 10 * time.Second

// Token is the ChatGPT bearer; ClientSecret a minted `ek_` secret. Every formatting path of
// both prints <redacted> (AUTH-05).
type (
	Token        = secret.Token
	ClientSecret = secret.ClientSecret
)

// NewToken returns a Token and registers its bearer with the scrubber.
func NewToken(bearer string, expires time.Time, plan, source string) Token {
	return secret.NewToken(bearer, expires, plan, source)
}

// NewClientSecret returns a ClientSecret and registers it with the scrubber.
func NewClientSecret(value string, expiresAt time.Time) ClientSecret {
	return secret.NewClientSecret(value, expiresAt)
}

// Credentials hands out the bearer and owns the refresh policy (AUTH-04): single-flight, a
// refresh succeeds when the token it returns is fresh, and a failed refresh backs off.
type Credentials = codexauth.Credentials

// Source values of --auth-source.
const (
	SourceAuto  = "auto"
	SourceCodex = "codex"
	SourceFile  = "file"
)

// Context is the invocation context the probe resolved (design §5.1a step 1).
type Context struct {
	CodexBin  string // absolute path; "" when no executable was found
	CodexHome string // the CODEX_HOME in force; "" means Codex's default
	Source    string // codex or file: where the token came from, or was looked for
	// MissingBin is the executable the user named (--codex-bin or RECITE_CODEX_BIN) when it
	// was not found, so the guide can point at the path rather than say Codex is missing.
	MissingBin string

	// child is how the probe started Codex. It is a pointer so that no formatting of a Context
	// or a Status (%v, %+v, %#v) prints the environment, which holds the user's credentials.
	child *childConfig
}

// childConfig is the environment and per-call timeout every Codex child of one probe uses.
type childConfig struct {
	env     []string
	timeout time.Duration
}

// CodexConfig is the configuration of a Codex child for this context, with recite's test guard:
// the probe's environment and per-call timeout, so a later child (plan, login) talks to the same
// Codex sign-in the probe classified.
func (c Context) CodexConfig(logger *slog.Logger) codexapp.Config {
	timeout, env := DefaultTimeout, []string(nil)
	if c.child != nil {
		timeout, env = c.child.timeout, c.child.env
	}
	return codexapp.Config{Bin: c.CodexBin, CodexHome: c.CodexHome, Env: env, Timeout: timeout, Logger: logger,
		ClientName: "recite", Guard: guard}
}

// ProbeConfig is the input of the login-state probe: the raw flags.
type ProbeConfig struct {
	CodexBin, CodexHome string // --codex-bin, --codex-home
	Source              string // --auth-source: auto, codex or file
	Getenv              func(string) string
	Now                 func() time.Time // nil means time.Now
	Timeout             time.Duration    // per app-server call; 0 means DefaultTimeout
	Logger              *slog.Logger
	// WithPlan also reads the plan type (account/read). Only `auth status` shows it, and the
	// call costs about 1 s over the network, so every other command leaves it off.
	WithPlan bool
}

// Status is the outcome of the probe. It never holds the token in a printable field; Creds
// hands it out only when the state is ready.
type Status struct {
	State        State
	Context      Context   // the resolved invocation context; every fix command uses it
	Mode         string    // the reported sign-in mode, scrubbed; "" when none
	CodexVersion string    // "" when unknown
	Plan         string    // "" when unknown
	Expires      time.Time // zero when no token was read, or its expiry is unknown
	Err          error     // the underlying failure of a non-ready state, for the debug log

	// Creds is non-nil only when State is ready.
	Creds  *Credentials
	logger *slog.Logger
}

// Probe runs design §5.1a through github.com/xiaolai/sayso/go/codexauth: Codex first, the file
// when no Codex runs or Codex says its store is a file, only a ChatGPT sign-in, and the refresh
// decision. It makes no network call of its own. The exit code of `codex login status` is never
// used.
func Probe(ctx context.Context, cfg ProbeConfig) *Status {
	if cfg.Getenv == nil {
		cfg.Getenv = func(string) string { return "" }
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	bin := cfg.CodexBin
	if bin == "" {
		bin = cfg.Getenv("RECITE_CODEX_BIN")
	}
	home := cfg.CodexHome
	if home == "" {
		home = cfg.Getenv("CODEX_HOME")
	}
	// codexauth reads auth.json only below an absolute home: "--codex-home ./profile" is resolved
	// here, against recite's working directory, as the user meant it.
	if home != "" && !filepath.IsAbs(home) {
		abs, err := filepath.Abs(home)
		if err != nil {
			return &Status{Context: Context{CodexHome: home, Source: cfg.Source}, logger: cfg.Logger, State: StateCodexError, Err: fmt.Errorf("auth: resolve CODEX_HOME %q: %w", home, err)}
		}
		home = abs
	}
	st := &Status{Context: Context{CodexHome: home, Source: cfg.Source}, logger: cfg.Logger}
	var source codexauth.Source
	switch cfg.Source {
	case SourceAuto, "":
		source = codexauth.SourceAuto
	case SourceCodex:
		source = codexauth.SourceCodex
	case SourceFile:
		source = codexauth.SourceFile
	default:
		st.State, st.Err = StateCodexError, fmt.Errorf("auth: unknown source %q", cfg.Source)
		return st
	}
	// The process environment, minus CODEX_HOME: recite decides CODEX_HOME from its flags and
	// Getenv, and codexauth sets it on the child when one is in force.
	env := []string{}
	path := ""
	for _, kv := range os.Environ() {
		if testguard.Active() && !testEnv(kv) {
			continue
		}
		if strings.HasPrefix(kv, "PATH=") {
			path = kv[len("PATH="):]
		}
		if !strings.HasPrefix(kv, "CODEX_HOME=") {
			env = append(env, kv)
		}
	}
	// Resolve the executable once, here: the refresh and every later child then run this same
	// absolute path even if PATH changes, and the file source still knows it for the guide's
	// fix commands. An executable that does not resolve is left to codexauth to classify.
	cands := candidates()
	if cands == nil {
		if h, err := os.UserHomeDir(); err == nil {
			cands = codexapp.Candidates(h)
		}
	}
	resolved, rerr := codexapp.Resolve(bin, path, cands)
	if rerr == nil {
		bin = resolved
	}
	cst, err := codexauth.Probe(ctx, codexauth.Config{
		Source: source, Bin: bin, CodexHome: home, Env: env, Candidates: cands,
		Margin: RefreshMargin, Timeout: cfg.Timeout, Now: cfg.Now, Logger: cfg.Logger,
		ClientName: "recite", Guard: guard,
	})
	if err != nil {
		st.State, st.Err = StateCodexError, err
		return st
	}
	st.State, st.Err = cst.State, cst.Err
	st.Mode, st.CodexVersion, st.Expires = cst.Mode, cst.Info.CodexVersion, cst.Expires
	st.Context = Context{CodexBin: cst.Info.Bin, CodexHome: cst.Info.CodexHome, Source: string(cst.Info.Source),
		MissingBin: cst.Info.MissingBin, child: &childConfig{env: env, timeout: cfg.Timeout}}
	if st.Context.CodexBin == "" && rerr == nil {
		st.Context.CodexBin = resolved // the file source never starts Codex, but its guide names it
	}
	if st.State != StateReady {
		return st
	}
	st.Creds = cst.Creds
	if cfg.WithPlan && st.Context.Source == SourceCodex {
		st.Plan = st.plan(ctx)
	}
	return st
}

// testEnv reports the variables a non-live test passes to a Codex child: what the fake reads
// and a process needs, and the authentication overrides, which tests set to check they are
// removed. The rest of the developer's environment, which may hold live credentials, stays out.
func testEnv(kv string) bool {
	name, _, _ := strings.Cut(kv, "=")
	switch {
	case strings.HasPrefix(name, "FAKECODEX_"), name == "PATH", name == "HOME", name == "TMPDIR", name == "GOCOVERDIR":
		return true
	}
	for _, o := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN", "CODEX_APP_SERVER_LOGIN_CLIENT_ID",
		"CODEX_REFRESH_TOKEN_URL_OVERRIDE", "CODEX_REVOKE_TOKEN_URL_OVERRIDE"} {
		if strings.EqualFold(name, o) {
			return true
		}
	}
	return false
}

// candidates are the folders searched after PATH: Codex's GUI install locations, except in a
// non-live test, which must never discover the machine's real Codex.
func candidates() []string {
	if testguard.Active() {
		return []string{}
	}
	return nil
}

// guard keeps a non-live test away from the real Codex and the real sign-in file.
func guard(path string) error {
	if !testguard.Active() || testguard.TempPath(path) {
		return nil
	}
	if filepath.Base(path) == "auth.json" {
		return fmt.Errorf("a non-live test tried to read %s; tests must use a temporary CODEX_HOME", path)
	}
	return fmt.Errorf("a non-live test tried to run %s; tests must use the fake codex", path)
}

// plan returns the plan type for display; a failure leaves it unknown ("").
func (s *Status) plan(ctx context.Context) string {
	c, err := codexapp.Start(ctx, s.Context.CodexConfig(s.logger))
	if err != nil {
		return ""
	}
	defer func() { _ = c.Close() }()
	p, _ := c.PlanType(ctx) // display only
	return p
}

// Close is kept for callers' lifecycles: the probe no longer keeps a Codex child.
func (s *Status) Close() error { return nil }

// SourceName is how auth status names the credential source.
func (s *Status) SourceName() string {
	switch s.Context.Source {
	case SourceCodex:
		return "codex app-server"
	case SourceFile:
		return "file"
	}
	return "none"
}

// ExitCode is 0 for ready and 3 (auth) for every other state (AUTH-06, AUTH-07).
func (s *Status) ExitCode() int {
	if s.State == StateReady {
		return exitcode.OK
	}
	return exitcode.Auth
}

// PlanStatus reads the Codex plan status (design §12.4 check 3, OBS-04) through a Codex child.
// Only a ready sign-in is asked (AUTH-07: nothing reaches Codex's account methods otherwise), and
// the file source has no app-server, so the call is skipped with a warning; ok is false in both.
func (s *Status) PlanStatus(ctx context.Context) (rl codexapp.RateLimits, ok bool, err error) {
	if s.State != StateReady {
		return codexapp.RateLimits{}, false, nil
	}
	if s.Context.Source != SourceCodex || s.Context.CodexBin == "" {
		if s.logger != nil {
			s.logger.Warn("Codex plan status skipped: it is read through the Codex app-server, and the auth source is file")
		}
		return codexapp.RateLimits{}, false, nil
	}
	c, err := codexapp.Start(ctx, s.Context.CodexConfig(s.logger))
	if err != nil {
		return codexapp.RateLimits{}, false, err
	}
	defer func() { _ = c.Close() }()
	rl, err = c.PlanStatus(ctx)
	if err != nil {
		return codexapp.RateLimits{}, false, err
	}
	return rl, true, nil
}
