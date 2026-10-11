package auth

import (
	"context"
	"time"
)

// TokenSource yields the ChatGPT bearer used for minting and transcription (design §4).
type TokenSource interface {
	Token(ctx context.Context) (Token, error)   // cached in memory
	Refresh(ctx context.Context) (Token, error) // asks Codex to refresh; at most once per process
}

// Token is a bearer with its metadata. Redaction of Bearer arrives with AUTH-05 (WP-1.1).
type Token struct {
	Bearer  string
	Expires time.Time
	Plan    string
	Source  string
}
