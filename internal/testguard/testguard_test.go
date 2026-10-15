package testguard_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/testguard"
	"github.com/xiaolai/sayso/go/codexapp"
)

// These tests prove that an ordinary test cannot reach the real Codex sign-in or the real
// API, whatever it does by mistake (WP-1.7 incident: a stale test ran the real `say`).

func TestGuardActiveInUnitTests(t *testing.T) {
	t.Setenv("RECITE_LIVE", "")
	if !testguard.Active() {
		t.Fatal("guard inactive in a non-live test")
	}
	t.Setenv("RECITE_LIVE", "1")
	if testguard.Active() {
		t.Fatal("guard active with RECITE_LIVE=1")
	}
}

func TestRealCodexExecutableRefused(t *testing.T) {
	t.Setenv("RECITE_LIVE", "")
	// A path outside the temp dir stands for a real install; it is refused before any exec.
	st := auth.Probe(context.Background(), auth.ProbeConfig{CodexBin: "/usr/bin/true", CodexHome: t.TempDir(), Source: auth.SourceCodex})
	if st.State != auth.StateCodexError || st.Err == nil || !strings.Contains(st.Err.Error(), "fake codex") {
		t.Fatalf("Probe(/usr/bin/true) = %s %v; want the test guard refusal", st.State, st.Err)
	}
	cfg := auth.Context{CodexBin: "/usr/bin/true"}.CodexConfig(nil)
	if _, err := codexapp.Start(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "fake codex") {
		t.Fatalf("Start(/usr/bin/true) = %v; want the test guard refusal", err)
	}
}

func TestRealAuthFileRefused(t *testing.T) {
	t.Setenv("RECITE_LIVE", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	codexHome := filepath.Join(home, ".codex")
	if _, err := os.Stat(codexHome); err != nil {
		t.Skip("no ~/.codex to be refused")
	}
	st := auth.Probe(context.Background(), auth.ProbeConfig{CodexHome: codexHome, Source: auth.SourceFile})
	if st.State != auth.StateCodexError || st.Err == nil || !strings.Contains(st.Err.Error(), "temporary CODEX_HOME") {
		t.Fatalf("Probe(file, ~/.codex) = %s %v; want the test guard refusal", st.State, st.Err)
	}
}

func TestRealNetworkToOpenAIRefused(t *testing.T) {
	t.Setenv("RECITE_LIVE", "")
	c, err := openai.NewClient() // the production transport
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/realtime/client_secrets", nil)
	req.Header.Set("Authorization", "Bearer eyJcanary.canary.canary")
	_, err = c.Do(req)
	if err == nil || !strings.Contains(err.Error(), "non-live test run") {
		t.Fatalf("real request = %v; want the test guard refusal", err)
	}
}
