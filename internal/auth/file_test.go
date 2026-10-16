package auth

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/logx"
)

const fileKeyCanary = "sk-canary-filekey-opaque"

func writeAuth(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func chatgptFile(exp int64) string {
	return fmt.Sprintf(`{"OPENAI_API_KEY":%q,"auth_mode":"chatgpt","tokens":{"access_token":%q,"refresh_token":"rt-canary"}}`,
		fileKeyCanary, jwt(exp))
}

// probeFile probes the file source in home.
func probeFile(t *testing.T, home string, logger *slog.Logger) *Status {
	t.Helper()
	st, _ := probeFake(t, probeCase{source: SourceFile, bin: "-", home: home, logger: logger})
	return st
}

// accept: AUTH-02#1
func TestFileSourceReadOnlyDirectory(t *testing.T) {
	dir := writeAuth(t, chatgptFile(1800000000))
	path := filepath.Join(dir, "auth.json")
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := probeFile(t, dir, nil)
	wantState(t, st, StateReady)
	tok, err := st.Creds.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok.Expires.Unix() != 1800000000 || tok.Source != "file" || st.SourceName() != "file" {
		t.Fatalf("token exp %v source %q", tok.Expires, tok.Source)
	}
	after, err := os.Stat(path)
	if err != nil || !after.ModTime().Equal(before.ModTime()) || after.Mode() != before.Mode() {
		t.Fatal("auth.json was modified")
	}
	// The file source cannot refresh: a refused token is expired.
	if _, err := st.Creds.RefreshAfter(context.Background(), tok); err == nil {
		t.Fatal("the file source refreshed")
	}
}

// accept: AUTH-02#2
func TestFileSourceOversizedOrMalformed(t *testing.T) {
	big := `{"auth_mode":"chatgpt","pad":"` + strings.Repeat("x", 64<<10) + `"}`
	cases := map[string]string{
		"oversized": big,
		"malformed": `{"auth_mode":"chatgpt",`,
		"no token":  `{"auth_mode":"chatgpt","tokens":{}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			st := probeFile(t, writeAuth(t, body), nil)
			wantState(t, st, StateCodexError)
			if name == "oversized" && !strings.Contains(st.Err.Error(), "64 KiB") {
				t.Fatalf("unclear message: %v", st.Err)
			}
		})
	}
	// One byte under the cap is accepted; 64 KiB itself is refused.
	body := chatgptFile(1800000000)
	pad := func(n int) string {
		return body[:len(body)-1] + `,"pad":"` + strings.Repeat("x", n-len(body)-9) + `"}`
	}
	if under := pad(64<<10 - 1); len(under) != 64<<10-1 {
		t.Fatalf("fixture is %d bytes", len(under))
	} else {
		wantState(t, probeFile(t, writeAuth(t, under), nil), StateReady)
	}
	wantState(t, probeFile(t, writeAuth(t, pad(64<<10)), nil), StateCodexError)
	// A token that is not a JWT has an unknown expiry; the API decides.
	st := probeFile(t, writeAuth(t, `{"auth_mode":"chatgpt","tokens":{"access_token":"not-a-jwt"}}`), nil)
	wantState(t, st, StateReady)
	if !st.Expires.IsZero() {
		t.Fatalf("expiry %v, want unknown", st.Expires)
	}
}

func TestFileSourceGuard(t *testing.T) {
	apikey := fmt.Sprintf(`{"OPENAI_API_KEY":%q,"auth_mode":"apikey"}`, fileKeyCanary)
	wantState(t, probeFile(t, writeAuth(t, apikey), nil), StateAPIKey)
	// Without auth_mode a non-null OPENAI_API_KEY wins over leftover tokens, as in Codex.
	leftover := fmt.Sprintf(`{"OPENAI_API_KEY":%q,"tokens":{"access_token":%q}}`, fileKeyCanary, jwt(1800000000))
	wantState(t, probeFile(t, writeAuth(t, leftover), nil), StateAPIKey)
	wantState(t, probeFile(t, t.TempDir(), nil), StateNoSignInFile)
}

// accept: AUTH-02#3
func TestFileSourceNeverReadsAPIKey(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(logx.NewHandler(slog.NewTextHandler(&logs, nil)))
	for _, body := range []string{chatgptFile(1800000000), fmt.Sprintf(`{"OPENAI_API_KEY":%q,"auth_mode":"apikey"}`, fileKeyCanary)} {
		st := probeFile(t, writeAuth(t, body), logger)
		var tok Token
		if st.Creds != nil {
			tok, _ = st.Creds.Token(context.Background())
		}
		logger.Error("token", "tok", tok, "status", st, "err", st.Err)
		dumps := fmt.Sprintf("%v %+v %#v %s | %v %+v %#v | %v", tok, tok, tok, tok, st, st, st, st.Err)
		for _, out := range []string{dumps, logs.String(), tok.Bearer()} {
			if strings.Contains(out, fileKeyCanary) {
				t.Fatalf("OPENAI_API_KEY canary surfaced: %q", out)
			}
		}
	}
	// The env variable is not consulted either.
	t.Setenv("OPENAI_API_KEY", fileKeyCanary)
	wantState(t, probeFile(t, t.TempDir(), nil), StateNoSignInFile)
}

// A relative --codex-home is resolved against the working directory, not refused by codexauth.
func TestFileSourceRelativeHome(t *testing.T) {
	dir := writeAuth(t, chatgptFile(1800000000))
	t.Chdir(filepath.Dir(dir))
	st := probeFile(t, "./"+filepath.Base(dir), nil)
	wantState(t, st, StateReady)
	if !filepath.IsAbs(st.Context.CodexHome) {
		t.Fatalf("CodexHome %q, want absolute", st.Context.CodexHome)
	}
}

// Under --auth-source codex the missing-file guide does not send the user back to the same probe.
func TestNoSignInFileGuideUnderCodexSource(t *testing.T) {
	g, _ := GuideFor(StateNoSignInFile, Context{CodexBin: "codex", Source: SourceCodex}, "", "")
	for _, s := range g.Steps {
		if strings.Contains(s.Command, "auth status") {
			t.Fatalf("guide repeats the probe: %+v", g.Steps)
		}
	}
	if len(g.Steps) == 0 || !strings.HasSuffix(g.Steps[0].Command, " login") {
		t.Fatalf("guide %+v, want a sign-in", g.Steps)
	}
	g, _ = GuideFor(StateNoSignInFile, Context{CodexBin: "codex", Source: SourceFile}, "", "")
	if !strings.Contains(g.Steps[0].Command, "auth status") {
		t.Fatalf("file-source guide %+v, want the Codex probe", g.Steps)
	}
}

// A Codex without getAuthStatus whose config/read names the file store has its auth.json read
// instead; when that file is missing, the guide asks for a sign-in, not for the same probe
// again, under the auto source as under codex (audit).
// accept: AUTH-07#15
func TestNoSignInFileAfterStoreFallbackAsksForSignIn(t *testing.T) {
	for _, src := range []string{SourceAuto, SourceCodex} {
		st, _ := probeFake(t, probeCase{mode: "unknown-method", source: src, env: map[string]string{"FAKECODEX_CONFIG_STORE": "file"}})
		wantState(t, st, StateNoSignInFile)
		if !st.Context.StoreFallback {
			t.Fatalf("%s: the store fallback is not recorded", src)
		}
		g, _ := st.Guide()
		for _, s := range g.Steps {
			if strings.Contains(s.Command, "auth status") {
				t.Fatalf("%s: guide repeats the probe: %+v", src, g.Steps)
			}
		}
		if len(g.Steps) == 0 || !strings.HasSuffix(g.Steps[0].Command, " login") {
			t.Fatalf("%s: guide %+v, want a sign-in", src, g.Steps)
		}
	}
	// The file source never asked Codex: its guide still offers to.
	st, _ := probeFake(t, probeCase{source: SourceFile})
	wantState(t, st, StateNoSignInFile)
	if st.Context.StoreFallback {
		t.Fatal("the file source recorded a store fallback")
	}
}
