package codexapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/xiaolai/recite/internal/logx"
)

// ErrNotFound reports that no Codex executable could be resolved.
var ErrNotFound = errors.New("codex executable not found")

// Resolve returns the absolute path of the Codex executable: flagBin, then
// $RECITE_CODEX_BIN, then `codex` on PATH (design §5.1a step 1). An explicit
// choice that does not resolve is an error, never a silent fall-through.
func Resolve(flagBin string, getenv func(string) string) (string, error) {
	name := flagBin
	if name == "" {
		name = getenv("RECITE_CODEX_BIN")
	}
	explicit := name != ""
	if !explicit {
		name = "codex"
	}
	p, err := exec.LookPath(name)
	if err != nil {
		if explicit {
			return "", fmt.Errorf("%w: %q: %w", ErrNotFound, name, err)
		}
		return "", ErrNotFound
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("codexapp: resolve %q: %w", p, err)
	}
	if fi, err := os.Stat(abs); err != nil || fi.IsDir() {
		return "", fmt.Errorf("%w: %q is not a file", ErrNotFound, abs)
	}
	return abs, nil
}

// AuthStatus is the getAuthStatus result. The token is unexported and every
// formatting path redacts it (AUTH-05).
type AuthStatus struct {
	// Method is authMethod; empty when Codex reports null (not signed in).
	Method string
	token  string
}

// Token returns the credential Codex sent, or "". It is already registered with the scrubber.
func (s AuthStatus) Token() string { return s.token }

func (s AuthStatus) String() string {
	return fmt.Sprintf("AuthStatus{Method:%q}", logx.Scrub(s.Method))
}

// GoString redacts like String.
func (s AuthStatus) GoString() string { return s.String() }

// Format redacts like String for every verb.
func (s AuthStatus) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(s.String())) }

// GetAuthStatus calls getAuthStatus. Any token in the reply is registered with the
// scrubber before it is returned, whatever was asked for (AUTH-03).
func (c *Client) GetAuthStatus(ctx context.Context, includeToken, refreshToken bool) (AuthStatus, error) {
	var raw struct {
		AuthMethod *string `json:"authMethod"`
		AuthToken  *string `json:"authToken"`
	}
	params := map[string]any{"includeToken": includeToken, "refreshToken": refreshToken}
	if err := c.Call(ctx, "getAuthStatus", params, &raw); err != nil {
		return AuthStatus{}, err
	}
	var s AuthStatus
	if raw.AuthToken != nil {
		logx.RegisterSecret(*raw.AuthToken)
		s.token = *raw.AuthToken
	}
	if raw.AuthMethod != nil {
		s.Method = *raw.AuthMethod
	}
	return s, nil
}

// ReadAccount calls account/read and returns the raw result (decoded by WP-1.4).
func (c *Client) ReadAccount(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	return out, c.Call(ctx, "account/read", nil, &out)
}

// ReadRateLimits calls account/rateLimits/read and returns the raw result.
func (c *Client) ReadRateLimits(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	return out, c.Call(ctx, "account/rateLimits/read", nil, &out)
}
