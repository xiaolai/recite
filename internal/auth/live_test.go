//go:build live

package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// accept: AUTH-01#4
func TestLiveAuth(t *testing.T) {
	if os.Getenv("RECITE_LIVE") == "" {
		t.Skip("set RECITE_LIVE=1 to run against the real Codex")
	}
	c, err := ResolveContext("", "", SourceCodex, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	s := NewCodexSource(c.CodexBin, c.CodexHome, 0, nil)
	defer func() { _ = s.Close() }()
	start := time.Now()
	tok, err := s.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d >= 2*time.Second {
		t.Fatalf("token took %v, want < 2s", d)
	}
	parts := strings.Split(tok.Bearer(), ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal("payload not base64url")
	}
	var claims struct {
		Aud json.RawMessage `json:"aud"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal("payload not JSON")
	}
	var auds []string
	var one string
	if json.Unmarshal(claims.Aud, &one) == nil {
		auds = []string{one}
	} else if err := json.Unmarshal(claims.Aud, &auds); err != nil {
		t.Fatal("aud is neither a string nor a list")
	}
	if !slices.Contains(auds, "https://api.openai.com/v1") {
		t.Fatalf("aud = %v", auds)
	}
}
