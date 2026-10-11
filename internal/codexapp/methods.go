package codexapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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

// ReadAccount calls account/read and returns the raw result.
func (c *Client) ReadAccount(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	return out, c.Call(ctx, "account/read", nil, &out)
}

// PlanType returns account/read's account.planType, "" when absent. Only this
// field is decoded: the email and account IDs never reach a Go value (AUTH-06).
func (c *Client) PlanType(ctx context.Context) (string, error) {
	var out struct {
		Account *struct {
			PlanType string `json:"planType"`
		} `json:"account"`
	}
	if err := c.Call(ctx, "account/read", nil, &out); err != nil {
		return "", err
	}
	if out.Account == nil {
		return "", nil
	}
	return logx.Scrub(out.Account.PlanType), nil
}

// Version extracts the Codex version from an initialize userAgent such as
// "codex_cli_rs/0.162.1 (Mac OS 15.0; arm64)"; "" when there is none.
func Version(userAgent string) string {
	_, rest, ok := strings.Cut(userAgent, "/")
	if !ok {
		return ""
	}
	end := strings.IndexFunc(rest, func(r rune) bool { return r != '.' && (r < '0' || r > '9') })
	if end >= 0 {
		rest = rest[:end]
	}
	if rest == "" || rest[0] < '0' || rest[0] > '9' {
		return ""
	}
	return rest
}

// VersionLess reports whether dotted numeric version a is below b. Missing
// components count as zero.
func VersionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(as), len(bs)) {
		x, y := 0, 0
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// ReadRateLimits calls account/rateLimits/read and returns the raw result.
func (c *Client) ReadRateLimits(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	return out, c.Call(ctx, "account/rateLimits/read", nil, &out)
}

// RateLimits is the Codex plan status from account/rateLimits/read (OBS-04). Only these fields
// are decoded; nothing that identifies the account reaches a Go value.
type RateLimits struct {
	PlanType             string
	OrdinaryUsageAllowed bool
	Windows              []RateWindow // the primary window, then the secondary one, when present
}

// RateWindow is one usage window of the plan.
type RateWindow struct {
	Name        string // "primary" or "secondary"
	UsedPercent float64
	WindowMins  int
	ResetsAt    time.Time // zero when not reported
}

// PlanStatus calls account/rateLimits/read and decodes the plan status.
func (c *Client) PlanStatus(ctx context.Context) (RateLimits, error) {
	type window struct {
		UsedPercent        float64 `json:"usedPercent"`
		WindowDurationMins int     `json:"windowDurationMins"`
		ResetsAt           *int64  `json:"resetsAt"`
	}
	var raw struct {
		OrdinaryUsageAllowed bool `json:"ordinaryUsageAllowed"`
		RateLimits           *struct {
			PlanType  string  `json:"planType"`
			Primary   *window `json:"primary"`
			Secondary *window `json:"secondary"`
		} `json:"rateLimits"`
	}
	if err := c.Call(ctx, "account/rateLimits/read", nil, &raw); err != nil {
		return RateLimits{}, err
	}
	out := RateLimits{OrdinaryUsageAllowed: raw.OrdinaryUsageAllowed}
	if rl := raw.RateLimits; rl != nil {
		out.PlanType = logx.Scrub(rl.PlanType)
		for _, w := range []struct {
			name string
			w    *window
		}{{"primary", rl.Primary}, {"secondary", rl.Secondary}} {
			if w.w == nil {
				continue
			}
			rw := RateWindow{Name: w.name, UsedPercent: w.w.UsedPercent, WindowMins: w.w.WindowDurationMins}
			if w.w.ResetsAt != nil {
				rw.ResetsAt = time.Unix(*w.w.ResetsAt, 0).UTC()
			}
			out.Windows = append(out.Windows, rw)
		}
	}
	return out, nil
}
