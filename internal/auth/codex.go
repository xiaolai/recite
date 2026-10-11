package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/xiaolai/recite/internal/codexapp"
	"github.com/xiaolai/recite/internal/openai"
)

// MsgRefreshFailed is the AUTH-04 message when Codex cannot refresh the sign-in.
const MsgRefreshFailed = "Codex could not refresh the ChatGPT sign-in; run `codex login`"

// CodexSource asks a `codex app-server` child for the token (AUTH-01). One child
// is started on first use and kept until Close.
type CodexSource struct {
	cfg codexapp.Config

	mu     sync.Mutex
	client *codexapp.Client
	cached *Token
}

// NewCodexSource returns a source for the resolved executable bin and CODEX_HOME home.
// timeout 0 means codexapp.DefaultTimeout.
func NewCodexSource(bin, home string, timeout time.Duration, logger *slog.Logger) *CodexSource {
	return &CodexSource{cfg: codexapp.Config{Bin: bin, Home: home, Timeout: timeout, Logger: logger}}
}

// Token returns the cached token, asking Codex on first use: metadata first, and the
// token only for a ChatGPT sign-in, so an API key is never requested (AUTH-03).
func (s *CodexSource) Token(ctx context.Context) (Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil {
		return *s.cached, nil
	}
	method, err := s.methodLocked(ctx)
	if err != nil {
		return Token{}, err
	}
	if err := guard(method); err != nil {
		return Token{}, err
	}
	return s.fetchLocked(ctx, false)
}

// startLocked starts the child on first use. The caller holds s.mu.
func (s *CodexSource) startLocked(ctx context.Context) (*codexapp.Client, error) {
	if s.client == nil {
		c, err := codexapp.Start(ctx, s.cfg)
		if err != nil {
			return nil, err
		}
		s.client = c
	}
	return s.client, nil
}

// methodLocked is the metadata call, getAuthStatus {includeToken: false}.
func (s *CodexSource) methodLocked(ctx context.Context) (string, error) {
	c, err := s.startLocked(ctx)
	if err != nil {
		return "", err
	}
	meta, err := c.GetAuthStatus(ctx, false, false)
	if err != nil {
		return "", err
	}
	return meta.Method, nil
}

// fetchLocked asks for the token, refreshing through Codex when refresh is set.
// Call it only after the metadata call reported chatgpt.
func (s *CodexSource) fetchLocked(ctx context.Context, refresh bool) (Token, error) {
	c, err := s.startLocked(ctx)
	if err != nil {
		return Token{}, err
	}
	full, err := c.GetAuthStatus(ctx, true, refresh)
	if err != nil {
		return Token{}, err
	}
	if full.Method != modeChatGPT { // the sign-in changed between the two calls
		return Token{}, guard(full.Method)
	}
	tok, err := newBearer(full.Token(), "codex")
	if err != nil {
		return Token{}, err
	}
	s.cached = &tok
	return tok, nil
}

// Refresh asks Codex to refresh the sign-in with refreshToken: true. A plain
// re-read is not enough: a running Codex serves its in-memory token until it
// refreshes (spike S0.1). A refusal by Codex is auth-expired (exit 3); a crash or
// timeout stays codex-error.
func (s *CodexSource) Refresh(ctx context.Context) (Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, err := s.fetchLocked(ctx, true)
	var rpc *codexapp.RPCError
	if errors.As(err, &rpc) {
		return Token{}, &openai.Error{Class: openai.ClassAuthExpired, Message: MsgRefreshFailed, Err: err}
	}
	return tok, err
}

// The probe (state.go) drives the same steps one at a time, so it can classify
// each failure; these wrappers take the lock.

// start starts the child and returns the Codex version from the initialize
// userAgent, "" when unknown.
func (s *CodexSource) start(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.startLocked(ctx)
	if err != nil {
		return "", err
	}
	return codexapp.Version(c.UserAgent()), nil
}

func (s *CodexSource) method(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.methodLocked(ctx)
}

func (s *CodexSource) fetch(ctx context.Context) (Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fetchLocked(ctx, false)
}

// plan returns the plan type for display; a failure leaves it unknown ("").
func (s *CodexSource) plan(ctx context.Context) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.startLocked(ctx)
	if err != nil {
		return ""
	}
	p, _ := c.PlanType(ctx) // display only
	return p
}

// Close reaps the child, if one was started.
func (s *CodexSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil {
		return nil
	}
	err := s.client.Close()
	s.client = nil
	return err
}

// pid returns the child's PID, or 0; for tests.
func (s *CodexSource) pid() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil {
		return 0
	}
	return s.client.PID()
}

func (s *CodexSource) String() string { return "auth.CodexSource{" + s.cfg.Bin + "}" }

// GoString redacts like String.
func (s *CodexSource) GoString() string { return s.String() }

// Format prints only the executable, never the cached token.
func (s *CodexSource) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(s.String())) }
