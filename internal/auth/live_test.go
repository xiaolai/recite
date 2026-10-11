//go:build live

package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
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

// accept: AUTH-07#14
func TestLiveProbeReady(t *testing.T) {
	if os.Getenv("RECITE_LIVE") == "" {
		t.Skip("set RECITE_LIVE=1 to run against the real Codex")
	}
	start := time.Now()
	st := Probe(context.Background(), ProbeConfig{Source: SourceAuto, Getenv: os.Getenv})
	defer func() { _ = st.Close() }()
	if d := time.Since(start); st.State != StateReady || d >= 2*time.Second {
		t.Fatalf("state %s (err %v) in %v; want ready in under 2 s", st.State, st.Err, d)
	}
}

// accept: AUTH-07#13
func TestLiveAuthURL(t *testing.T) {
	if os.Getenv("RECITE_LIVE") == "" {
		t.Skip("set RECITE_LIVE=1 to run against the real service")
	}
	// A plain client and no bearer: the page is public.
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Get(AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "codex login") {
		t.Fatalf("%s: HTTP %d, mentions codex login: %v", AuthURL, resp.StatusCode, strings.Contains(string(body), "codex login"))
	}
}

// Q6 settled: a refresh through an isolated CODEX_HOME leaves Codex working.
// RECITE_LIVE_CODEX_HOME must name a throwaway home with its own ChatGPT login;
// the real ~/.codex is never used.
// accept: AUTH-04#3
func TestLiveRefreshIsolatedHome(t *testing.T) {
	if os.Getenv("RECITE_LIVE") == "" {
		t.Skip("set RECITE_LIVE=1 to run against the real Codex")
	}
	home := os.Getenv("RECITE_LIVE_CODEX_HOME")
	if home == "" {
		t.Fatal("set RECITE_LIVE_CODEX_HOME to a throwaway CODEX_HOME with its own ChatGPT login")
	}
	c, err := ResolveContext("", home, SourceCodex, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	s := NewCodexSource(c.CodexBin, c.CodexHome, 0, nil)
	defer func() { _ = s.Close() }()
	creds := NewCredentials(s, nil)
	before, err := creds.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	after, err := creds.RefreshAfter(context.Background(), before)
	if err != nil {
		t.Fatal(err)
	}
	if after.Bearer() == before.Bearer() {
		t.Fatal("refresh returned the same token")
	}
	// Codex itself still works with the refreshed sign-in.
	cmd := exec.Command(c.CodexBin, "login", "status")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "ChatGPT") {
		t.Fatalf("codex login status after refresh: %v", err)
	}
}
