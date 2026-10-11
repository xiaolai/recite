package auth

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/xiaolai/recite/internal/codexapp"
	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/logx"
	"github.com/xiaolai/recite/internal/openai"
)

// State is the login state of design §5.1a (AUTH-07). Exactly one applies.
type State string

// The eight login states.
const (
	StateReady       State = "ready"
	StateNoCodex     State = "no-codex"
	StateCodexTooOld State = "codex-too-old"
	StateNotSignedIn State = "not-signed-in"
	StateAPIKey      State = "api-key"
	StateUnsupported State = "unsupported-auth"
	StateExpired     State = "expired"
	StateCodexError  State = "codex-error"
)

const (
	modeAPIKey = "apikey"
	maxModeLen = 64
)

// MinCodexVersion is the oldest Codex recite is tested against (E27 measured 0.162.1).
const MinCodexVersion = "0.162.0"

// ProbeConfig is the input of the login-state probe: the raw flags, resolved
// once into a Context.
type ProbeConfig struct {
	CodexBin, CodexHome string // --codex-bin, --codex-home
	Source              string // --auth-source: auto, codex or file
	Getenv              func(string) string
	Now                 func() time.Time // nil means time.Now
	Timeout             time.Duration    // per app-server call; 0 means 10 s
	Logger              *slog.Logger
	// WithPlan also reads the plan type (account/read). Only `auth status` shows it, and the
	// call costs about 1 s over the network, so every other command leaves it off.
	WithPlan bool
}

// Status is the outcome of the probe. It never holds the token in a printable
// field; Creds hands it out only when the state is ready.
type Status struct {
	State        State
	Context      Context   // the resolved invocation context; every fix command uses it
	Mode         string    // the reported sign-in mode, scrubbed; "" when none
	CodexVersion string    // "" when unknown
	Plan         string    // "" when unknown
	Expires      time.Time // zero when no token was read
	Err          error     // the underlying failure of a non-ready state, for the debug log

	// Creds is non-nil only when State is ready. Close releases it.
	Creds *Credentials
	src   Source
}

// Close releases the source (the Codex child). Safe on any Status.
func (s *Status) Close() error {
	if s.src == nil {
		return nil
	}
	err := s.src.Close()
	s.src = nil
	return err
}

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

// Probe runs design §5.1a: resolve the context once, choose the source, ask for
// metadata, request the token only for chatgpt, apply the refresh decision, and
// classify. It makes no network call of its own. The exit code of `codex login
// status` is never used.
func Probe(ctx context.Context, cfg ProbeConfig) *Status {
	if cfg.Getenv == nil {
		cfg.Getenv = func(string) string { return "" }
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	st := &Status{}
	rc, err := ResolveContext(cfg.CodexBin, cfg.CodexHome, cfg.Source, cfg.Getenv)
	if err != nil {
		st.Context = Context{CodexHome: cfg.CodexHome, Source: cfg.Source}
		if st.Context.CodexHome == "" {
			st.Context.CodexHome = cfg.Getenv("CODEX_HOME")
		}
		if errors.Is(err, codexapp.ErrNotFound) {
			st.Context.MissingBin = cfg.CodexBin
			if st.Context.MissingBin == "" {
				st.Context.MissingBin = cfg.Getenv("RECITE_CODEX_BIN")
			}
			return st.set(StateNoCodex, err)
		}
		return st.set(StateCodexError, err)
	}
	st.Context = rc
	if rc.Source == SourceCodex {
		probeCodex(ctx, st, cfg)
	} else {
		probeFile(ctx, st, cfg)
	}
	if st.State != StateReady {
		_ = st.Close()
	}
	return st
}

func (s *Status) set(state State, err error) *Status {
	s.State, s.Err = state, err
	return s
}

func probeCodex(ctx context.Context, st *Status, cfg ProbeConfig) {
	src := NewCodexSource(st.Context.CodexBin, st.Context.CodexHome, cfg.Timeout, cfg.Logger)
	st.src = src
	version, err := src.start(ctx)
	if err != nil {
		st.set(StateCodexError, err)
		return
	}
	st.CodexVersion = version
	if version != "" && codexapp.VersionLess(version, MinCodexVersion) {
		st.set(StateCodexTooOld, nil)
		return
	}
	method, err := src.method(ctx)
	if err != nil {
		var rpc *codexapp.RPCError
		if errors.As(err, &rpc) && rpc.Code == codexapp.CodeMethodNotFound {
			st.set(StateCodexTooOld, err)
			return
		}
		st.set(StateCodexError, err)
		return
	}
	if !classifyMode(st, method) {
		return
	}
	tok, err := src.fetch(ctx)
	if err != nil {
		st.set(StateCodexError, err)
		return
	}
	if cfg.WithPlan {
		st.Plan = src.plan(ctx)
	}
	decide(ctx, st, src, tok, cfg.Now)
}

func probeFile(ctx context.Context, st *Status, cfg ProbeConfig) {
	src, err := NewSource(st.Context, cfg.Timeout, cfg.Logger)
	if err != nil {
		st.set(StateCodexError, err)
		return
	}
	fs, ok := src.(*FileSource)
	if !ok {
		st.set(StateCodexError, errors.New("auth: file source expected"))
		return
	}
	probeFileSource(ctx, st, fs, cfg)
}

func probeFileSource(ctx context.Context, st *Status, fs *FileSource, cfg ProbeConfig) {
	st.src = fs
	// Under auto, the file source is the fallback for a missing executable: with no
	// usable auth.json either, the fix is to install Codex (design §5.1a step 2).
	fallback := cfg.Source == SourceAuto || cfg.Source == ""
	mode, bearer, err := fs.load()
	if err != nil {
		switch {
		case fallback:
			st.set(StateNoCodex, err)
		case errors.Is(err, errNoFile):
			st.set(StateNotSignedIn, err)
		default:
			st.set(StateCodexError, err)
		}
		return
	}
	if !classifyMode(st, mode) {
		return
	}
	tok, err := fs.bearer(bearer)
	if err != nil {
		st.set(StateCodexError, err)
		return
	}
	decide(ctx, st, fs, tok, cfg.Now)
}

// classifyMode sets the state for every mode but chatgpt and reports whether
// the probe may go on to request the token.
func classifyMode(st *Status, mode string) bool {
	st.Mode = cleanMode(mode)
	switch mode {
	case modeChatGPT:
		return true
	case "":
		st.set(StateNotSignedIn, nil)
	case modeAPIKey:
		st.set(StateAPIKey, nil)
	default:
		st.set(StateUnsupported, nil)
	}
	return false
}

// decide applies the refresh decision (AUTH-04) and classifies the outcome.
func decide(ctx context.Context, st *Status, src TokenSource, tok Token, now func() time.Time) {
	creds := NewCredentials(src, now)
	creds.seed(tok)
	st.Expires = tok.Expires
	fresh, err := creds.Token(ctx)
	if err != nil {
		if openai.Classify(err) == openai.ClassAuthExpired {
			st.set(StateExpired, err)
		} else {
			st.set(StateCodexError, err)
		}
		return
	}
	st.Expires = fresh.Expires
	st.Creds = creds
	st.set(StateReady, nil)
}

// cleanMode makes a Codex-reported mode safe to print: scrubbed, printable,
// and bounded, since a future Codex controls the string.
func cleanMode(m string) string {
	m = logx.Scrub(m)
	m = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, m)
	if len(m) > maxModeLen {
		m = m[:maxModeLen]
	}
	return m
}

// ExitCode is 0 for ready and 3 (auth) for every other state (AUTH-06, AUTH-07).
func (s *Status) ExitCode() int {
	if s.State == StateReady {
		return exitcode.OK
	}
	return exitcode.Auth
}
