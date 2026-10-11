package auth

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/xiaolai/recite/internal/openai"
)

// RefreshMargin is how much lifetime a token must have left; below it, recite
// asks Codex to refresh (AUTH-04).
const RefreshMargin = 10 * time.Minute

// Credentials hands out the bearer and owns the AUTH-04 refresh policy: at most
// one refresh per process, single-flight under concurrency. One Credentials
// exists per process; it is safe for concurrent use.
type Credentials struct {
	src TokenSource
	now func() time.Time

	// mu is held across a refresh, so concurrent callers wait for its outcome
	// instead of starting their own (single-flight).
	mu        sync.Mutex
	cur       *Token
	refreshed bool
}

// NewCredentials wraps src. now is the clock; nil means time.Now.
func NewCredentials(src TokenSource, now func() time.Time) *Credentials {
	if now == nil {
		now = time.Now
	}
	return &Credentials{src: src, now: now}
}

// seed installs a token the caller already fetched (the probe), so it is not
// asked for twice.
func (c *Credentials) seed(t Token) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cur = &t
}

// Token returns the bearer, refreshing first when fewer than RefreshMargin
// remain. It fails with auth-expired (exit 3) when the token is still too
// close to expiry after the one allowed refresh.
func (c *Credentials) Token(ctx context.Context) (Token, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur == nil {
		t, err := c.src.Token(ctx)
		if err != nil {
			return Token{}, err
		}
		c.cur = &t
	}
	if c.fresh(*c.cur) {
		return *c.cur, nil
	}
	if c.refreshed {
		return Token{}, expiredError(nil)
	}
	if err := c.refreshLocked(ctx); err != nil {
		return Token{}, err
	}
	if !c.fresh(*c.cur) {
		return Token{}, expiredError(nil)
	}
	return *c.cur, nil
}

// RefreshAfter is called when a request made with stale was refused as
// auth-expired. When another caller already replaced stale, the new token is
// returned without a second refresh; otherwise the one allowed refresh runs.
func (c *Credentials) RefreshAfter(ctx context.Context, stale Token) (Token, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur != nil && c.cur.Bearer() != stale.Bearer() {
		return *c.cur, nil
	}
	if c.refreshed {
		return Token{}, expiredError(nil)
	}
	if err := c.refreshLocked(ctx); err != nil {
		return Token{}, err
	}
	return *c.cur, nil
}

func (c *Credentials) fresh(t Token) bool { return t.Expires.Sub(c.now()) > RefreshMargin }

// refreshLocked spends the one refresh of the process. The caller holds c.mu.
func (c *Credentials) refreshLocked(ctx context.Context) error {
	c.refreshed = true
	t, err := c.src.Refresh(ctx)
	if err != nil {
		var e *openai.Error
		if errors.As(err, &e) && (e.Class == openai.ClassAuthExpired || e.Class == openai.ClassCodexError) {
			return err // already carries the user's next step
		}
		return expiredError(err)
	}
	c.cur = &t
	return nil
}

func expiredError(cause error) error {
	return &openai.Error{Class: openai.ClassAuthExpired, Message: "the ChatGPT sign-in has expired and the one refresh did not renew it; run `codex login`", Err: cause}
}
