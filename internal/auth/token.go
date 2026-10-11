package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/xiaolai/recite/internal/logx"
)

// TokenSource yields the ChatGPT bearer used for minting and transcription (design §4).
type TokenSource interface {
	Token(ctx context.Context) (Token, error)   // cached in memory
	Refresh(ctx context.Context) (Token, error) // asks Codex to refresh; at most once per process
}

// Token is a bearer with its metadata. The bearer is unexported and reachable
// only through Bearer, so no formatting or encoding path can print it (AUTH-05).
type Token struct {
	bearer  string
	Expires time.Time
	Plan    string
	Source  string
}

// NewToken returns a Token and registers its bearer with the log scrubber.
func NewToken(bearer string, expires time.Time, plan, source string) Token {
	logx.RegisterSecret(bearer)
	return Token{bearer: bearer, Expires: expires, Plan: plan, Source: source}
}

// Bearer returns the secret. Call it only to build an Authorization header.
func (t Token) Bearer() string { return t.bearer }

func (Token) String() string               { return logx.Redacted }
func (Token) GoString() string             { return logx.Redacted }
func (Token) Format(f fmt.State, _ rune)   { _, _ = f.Write([]byte(logx.Redacted)) }
func (Token) MarshalJSON() ([]byte, error) { return []byte(`"` + logx.Redacted + `"`), nil }
func (Token) MarshalText() ([]byte, error) { return []byte(logx.Redacted), nil }

// ClientSecret is a minted ephemeral `ek_` secret (design §5.1).
type ClientSecret struct {
	value     string
	ExpiresAt time.Time
}

// NewClientSecret returns a ClientSecret and registers it with the log scrubber.
func NewClientSecret(value string, expiresAt time.Time) ClientSecret {
	logx.RegisterSecret(value)
	return ClientSecret{value: value, ExpiresAt: expiresAt}
}

// Bearer returns the secret. Call it only to build an Authorization header.
func (s ClientSecret) Bearer() string { return s.value }

func (ClientSecret) String() string               { return logx.Redacted }
func (ClientSecret) GoString() string             { return logx.Redacted }
func (ClientSecret) Format(f fmt.State, _ rune)   { _, _ = f.Write([]byte(logx.Redacted)) }
func (ClientSecret) MarshalJSON() ([]byte, error) { return []byte(`"` + logx.Redacted + `"`), nil }
func (ClientSecret) MarshalText() ([]byte, error) { return []byte(logx.Redacted), nil }
