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

// ErrRefreshNotImplemented is returned by CodexSource.Refresh until AUTH-04 (WP-1.4).
var ErrRefreshNotImplemented = errors.New("auth: refresh through Codex is not implemented yet")

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
	if s.client == nil {
		c, err := codexapp.Start(ctx, s.cfg)
		if err != nil {
			return Token{}, err
		}
		s.client = c
	}
	meta, err := s.client.GetAuthStatus(ctx, false, false)
	if err != nil {
		return Token{}, err
	}
	if err := guard(meta.Method); err != nil {
		return Token{}, err
	}
	full, err := s.client.GetAuthStatus(ctx, true, false)
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

// Refresh is AUTH-04, built in WP-1.4.
func (s *CodexSource) Refresh(_ context.Context) (Token, error) {
	return Token{}, &openai.Error{Class: openai.ClassAuthExpired, Message: "token refresh is not available yet", Err: ErrRefreshNotImplemented}
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
