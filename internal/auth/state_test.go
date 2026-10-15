package auth

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/codexapp/fake"
)

var update = flag.Bool("update", false, "rewrite golden files")

// probeNow is the fixed clock of the probe tests.
var probeNow = time.Unix(1_700_000_000, 0)

func fixedNow() time.Time { return probeNow }

type probeCase struct {
	mode    string            // FAKECODEX_MODE
	env     map[string]string // extra process env for the fake
	source  string            // --auth-source; "" = codex
	bin     string            // --codex-bin; "" = the fake, "-" = none
	home    string            // --codex-home; "" = a temp dir, "-" = none
	getenv  map[string]string // what recite sees as its environment
	timeout time.Duration
	logger  *slog.Logger
}

// probeFake runs the probe against the fake and returns the status and the fake's record.
func probeFake(t *testing.T, pc probeCase) (*Status, string) {
	t.Helper()
	rec := filepath.Join(t.TempDir(), "rec.txt")
	t.Setenv("FAKECODEX_MODE", pc.mode)
	t.Setenv("FAKECODEX_RECORD", rec)
	t.Setenv("FAKECODEX_TOKEN_EXP", "1800000000")
	for k, v := range pc.env {
		t.Setenv(k, v)
	}
	if pc.source == "" {
		pc.source = SourceCodex
	}
	switch pc.bin {
	case "":
		pc.bin = fakeBin
	case "-":
		pc.bin = ""
	}
	switch pc.home {
	case "":
		pc.home = t.TempDir()
	case "-":
		pc.home = ""
	}
	getenv := func(k string) string { return pc.getenv[k] }
	st := Probe(context.Background(), ProbeConfig{
		CodexBin: pc.bin, CodexHome: pc.home, Source: pc.source,
		Getenv: getenv, Now: fixedNow, Timeout: pc.timeout, Logger: pc.logger,
	})
	t.Cleanup(func() { _ = st.Close() })
	b, _ := os.ReadFile(rec) // absent when the fake never ran
	return st, string(b)
}

func soon() string { return fmt.Sprint(probeNow.Add(5 * time.Minute).Unix()) }

func wantState(t *testing.T, st *Status, want State) {
	t.Helper()
	if st.State != want {
		t.Fatalf("state %q, want %q (err %v)", st.State, want, st.Err)
	}
	g, hasGuide := st.Guide()
	if want == StateReady {
		if hasGuide || st.ExitCode() != 0 || st.Creds == nil {
			t.Fatalf("ready: guide %v exit %d creds %v", hasGuide, st.ExitCode(), st.Creds)
		}
		return
	}
	if !hasGuide || st.ExitCode() != 3 || st.Creds != nil {
		t.Fatalf("%s: guide %v exit %d", want, hasGuide, st.ExitCode())
	}
	if g.State != want || !strings.HasSuffix(g.Text(), "More help: "+AuthURL+"\n") {
		t.Fatalf("guide %q", g.Text())
	}
}

// accept: AUTH-07#1
func TestProbeEveryStateWithGolden(t *testing.T) {
	cases := []struct {
		state State
		pc    probeCase
	}{
		{StateReady, probeCase{mode: "chatgpt"}},
		{StateNoCodex, probeCase{mode: "chatgpt", bin: filepath.Join(t.TempDir(), "missing", "codex")}},
		{StateCodexTooOld, probeCase{mode: "chatgpt", env: map[string]string{"FAKECODEX_VERSION": "0.150.3"}}},
		{StateAppServerAuthUnavailable, probeCase{mode: "unknown-method"}},
		{StateNoSignInFile, probeCase{source: SourceFile, bin: "-"}},
		{StateNotSignedIn, probeCase{mode: "logged-out"}},
		{StateAPIKey, probeCase{mode: "apikey"}},
		{StateUnsupported, probeCase{mode: "headers"}},
		{StateExpired, probeCase{mode: "refresh-fails", env: map[string]string{"FAKECODEX_TOKEN_EXP": soon()}}},
		{StateCodexError, probeCase{mode: "rpc-error"}},
	}
	for _, c := range cases {
		t.Run(string(c.state), func(t *testing.T) {
			st, _ := probeFake(t, c.pc)
			wantState(t, st, c.state)
		})
	}
	// Goldens are rendered from a fixed context, so they do not depend on temp paths.
	fixed := Context{CodexBin: "/opt/codex tools/bin/codex", CodexHome: "/srv/codex-home", Source: SourceCodex}
	for _, s := range []State{StateNoCodex, StateCodexTooOld, StateNotSignedIn, StateAPIKey, StateUnsupported, StateExpired, StateCodexError,
		StateNoSignInFile, StateAppServerAuthUnavailable, StateProviderWithoutOpenAIAuth} {
		t.Run("golden-"+string(s), func(t *testing.T) {
			g, ok := GuideFor(s, fixed, "headers", "0.150.0")
			if !ok {
				t.Fatal("no guide")
			}
			golden(t, "guide-"+string(s), g.Text())
		})
	}
	if _, ok := GuideFor(StateReady, fixed, "", ""); ok {
		t.Fatal("ready has a guide")
	}
}

// accept: AUTH-07#2
func TestProbeEveryAuthMode(t *testing.T) {
	modes := append(slices.Clone(fake.AuthModes), "future-mode")
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			st, rec := probeFake(t, probeCase{mode: mode})
			asked := strings.Contains(rec, `"includeToken":true`)
			switch mode {
			case "chatgpt":
				wantState(t, st, StateReady)
				if !asked {
					t.Fatal("token not requested for chatgpt")
				}
				return
			case "apikey":
				wantState(t, st, StateAPIKey)
			default:
				wantState(t, st, StateUnsupported)
				want := mode
				if mode == "future-mode" {
					want = "futureMode"
				}
				if st.Mode != want {
					t.Fatalf("mode %q, want %q", st.Mode, want)
				}
			}
			if asked {
				t.Fatalf("token requested for %s:\n%s", mode, rec)
			}
		})
	}
}

func writeHome(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func homeWith(t *testing.T, exp int64) string {
	return writeHome(t, fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"access_token":%q}}`, jwt(exp)))
}

// accept: AUTH-07#3
func TestProbeTokenOutcomes(t *testing.T) {
	t.Run("file, 5 minutes left", func(t *testing.T) {
		home := homeWith(t, probeNow.Add(5*time.Minute).Unix())
		st, _ := probeFake(t, probeCase{source: SourceFile, home: home})
		wantState(t, st, StateExpired)
	})
	t.Run("codex, 5 minutes left, refresh ok", func(t *testing.T) {
		st, rec := probeFake(t, probeCase{mode: "refresh-ok", env: map[string]string{"FAKECODEX_TOKEN_EXP": soon()}})
		wantState(t, st, StateReady)
		if strings.Count(rec, `"refreshToken":true`) != 1 || st.Expires.Sub(probeNow) < time.Hour {
			t.Fatalf("expected one refresh to a later token, exp %v:\n%s", st.Expires, rec)
		}
	})
	t.Run("codex, refresh fails", func(t *testing.T) {
		st, _ := probeFake(t, probeCase{mode: "refresh-fails", env: map[string]string{"FAKECODEX_TOKEN_EXP": soon()}})
		wantState(t, st, StateExpired)
	})
	t.Run("codex, refreshed token still too short", func(t *testing.T) {
		st, _ := probeFake(t, probeCase{mode: "refresh-ok", env: map[string]string{"FAKECODEX_TOKEN_EXP": soon(), "FAKECODEX_REFRESHED_EXP": soon()}})
		wantState(t, st, StateExpired)
	})
	t.Run("null-token", func(t *testing.T) {
		st, _ := probeFake(t, probeCase{mode: "null-token"})
		wantState(t, st, StateCodexError)
	})
	t.Run("bad-jwt", func(t *testing.T) {
		// A token that is not a JWT has an unknown expiry: it is used, and the API decides.
		st, _ := probeFake(t, probeCase{mode: "bad-jwt"})
		wantState(t, st, StateReady)
		if !st.Expires.IsZero() {
			t.Fatalf("expiry %v, want unknown", st.Expires)
		}
	})
}

// accept: AUTH-07#4
func TestProbeSourceResolution(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no executable anywhere
	home := homeWith(t, 1800000000)
	for _, src := range []string{SourceAuto, SourceFile} {
		st, _ := probeFake(t, probeCase{source: src, bin: "-", home: home})
		wantState(t, st, StateReady)
		if st.SourceName() != "file" {
			t.Fatalf("%s: source %q", src, st.SourceName())
		}
	}
	st, _ := probeFake(t, probeCase{source: SourceCodex, bin: "-", home: home})
	wantState(t, st, StateNoCodex)
	st, _ = probeFake(t, probeCase{source: SourceAuto, bin: "-", home: t.TempDir()})
	wantState(t, st, StateNoCodex)
	st, _ = probeFake(t, probeCase{source: SourceAuto, bin: "-", home: writeHome(t, "{not json")})
	wantState(t, st, StateNoCodex)
	// Under file, a missing auth.json may mean a sign-in kept in the keychain.
	st, _ = probeFake(t, probeCase{source: SourceFile, bin: "-", home: t.TempDir()})
	wantState(t, st, StateNoSignInFile)
}

// accept: AUTH-07#5
func TestProbeContextBinding(t *testing.T) {
	// B is first on PATH and records any run; A is selected by --codex-bin.
	dirB := t.TempDir()
	marker := filepath.Join(t.TempDir(), "b-ran")
	script := "#!/bin/sh\ntouch " + ShellQuote(marker) + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dirB, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dirB)
	home := filepath.Join(t.TempDir(), "my home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	st, rec := probeFake(t, probeCase{mode: "logged-out", home: home})
	wantState(t, st, StateNotSignedIn)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("executable B ran")
	}
	if !strings.Contains(rec, "argv="+fakeBin+"\n") || !strings.Contains(rec, "env=CODEX_HOME="+home+"\n") {
		t.Fatalf("probe did not use A and the home:\n%s", rec)
	}
	g, _ := st.Guide()
	want := "CODEX_HOME=" + ShellQuote(home) + " " + ShellQuote(fakeBin)
	for _, cmd := range g.FixCommands() {
		if !strings.HasPrefix(cmd, want+" ") {
			t.Fatalf("fix command %q does not start with %q", cmd, want)
		}
	}
}

// accept: AUTH-07#6
func TestProbeFixCommandsUseAbsolutePathFromPATH(t *testing.T) {
	t.Setenv("PATH", filepath.Dir(fakeBin))
	st, _ := probeFake(t, probeCase{mode: "logged-out", bin: "-", home: "-"})
	wantState(t, st, StateNotSignedIn)
	if st.Context.CodexBin != fakeBin || st.Context.CodexHome != "" {
		t.Fatalf("context %+v", st.Context)
	}
	g, _ := st.Guide()
	for _, cmd := range g.FixCommands() {
		if !strings.HasPrefix(cmd, ShellQuote(fakeBin)+" ") || strings.HasPrefix(cmd, "codex ") || strings.Contains(cmd, "CODEX_HOME") {
			t.Fatalf("fix command %q", cmd)
		}
	}
}

// accept: AUTH-07#7
func TestGuideContents(t *testing.T) {
	c := Context{CodexBin: "/usr/local/bin/codex"}
	g, _ := GuideFor(StateNotSignedIn, c, "", "")
	text := g.Text()
	for _, want := range []string{"/usr/local/bin/codex login\n", "/usr/local/bin/codex login --device-auth\n", "Device-code login must first be enabled"} {
		if !strings.Contains(text, want) {
			t.Errorf("not-signed-in guide lacks %q:\n%s", want, text)
		}
	}
	g, _ = GuideFor(StateAPIKey, c, "apikey", "")
	text = g.Text()
	for _, want := range []string{"/usr/local/bin/codex logout\n", "/usr/local/bin/codex login\n", "Warning: this changes how Codex itself signs in."} {
		if !strings.Contains(text, want) {
			t.Errorf("api-key guide lacks %q:\n%s", want, text)
		}
	}
	g, _ = GuideFor(StateUnsupported, c, "bedrockApiKey", "")
	text = g.Text()
	if !strings.Contains(text, "signed in with bedrockApiKey") || strings.Contains(text, "logout") {
		t.Errorf("unsupported-auth guide:\n%s", text)
	}
	g, _ = GuideFor(StateNoCodex, Context{}, "", "")
	if cmds := g.FixCommands(); cmds[len(cmds)-1] != "codex login" {
		t.Errorf("no-codex ends with %q", cmds[len(cmds)-1])
	}
}

// accept: AUTH-07#8
func TestProbeSlowChildIsCodexError(t *testing.T) {
	st, _ := probeFake(t, probeCase{mode: "slow", env: map[string]string{"FAKECODEX_DELAY_MS": "5000"}, timeout: 100 * time.Millisecond})
	wantState(t, st, StateCodexError)
	g, _ := st.Guide()
	if !strings.HasPrefix(g.Text(), "Codex could not report a usable sign-in.") {
		t.Fatalf("guide %q", g.Text())
	}
}

// accept: AUTH-07#9
func TestProbeRPCErrors(t *testing.T) {
	st, _ := probeFake(t, probeCase{mode: "rpc-error"})
	wantState(t, st, StateCodexError)
	// Without getAuthStatus, and without config/read to confirm the store is the file, the file
	// is not Codex's authority (D-3).
	st, _ = probeFake(t, probeCase{mode: "unknown-method"})
	wantState(t, st, StateAppServerAuthUnavailable)
	st, _ = probeFake(t, probeCase{mode: "chatgpt", env: map[string]string{"FAKECODEX_VERSION": "0.150.3"}})
	wantState(t, st, StateCodexTooOld)
	if g, _ := st.Guide(); !strings.HasPrefix(g.Text(), "Codex 0.150.3 is too old for recite (needs ≥ 0.162.0).") {
		t.Fatalf("guide %q", g.Text())
	}
	st, _ = probeFake(t, probeCase{mode: "crash"})
	wantState(t, st, StateCodexError)
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/bin/codex": "/usr/bin/codex", "/a b/codex": "'/a b/codex'", "it's": `'it'\''s'`, "": "''", "$(x)": "'$(x)'",
	} {
		if got := ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run go test -update): %v", err)
	}
	if got != string(want) {
		t.Errorf("%s mismatch\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}

// accept: AUTH-07#1
func TestProbeSkipsPlanLookupByDefault(t *testing.T) {
	// account/read costs about 1 s on the real service (measured 2026-10-11), and only
	// `auth status` shows the plan, so the default probe must not call it.
	st, rec := probeFake(t, probeCase{mode: "chatgpt"})
	wantState(t, st, StateReady)
	if strings.Contains(rec, `"account/read"`) {
		t.Fatal("default probe called account/read")
	}
	if st.Plan != "" {
		t.Fatalf("default probe set plan %q", st.Plan)
	}
}

// accept: AUTH-07#1
func TestNoCodexGuideNamesMistypedPath(t *testing.T) {
	// A mistyped --codex-bin must not be reported as "Codex is not installed".
	st, _ := probeFake(t, probeCase{bin: "/nonexistent/codex-typo"})
	wantState(t, st, StateNoCodex)
	g, ok := st.Guide()
	if !ok || !strings.Contains(g.Message, "/nonexistent/codex-typo") || strings.Contains(g.Message, "not installed") {
		t.Fatalf("guide message %q", g.Message)
	}
}

// A later Codex child (plan, login) uses the probe's environment and timeout, so it talks to the
// sign-in the probe classified: an ambient CODEX_HOME that recite's environment does not report
// stays out of it.
func TestContextKeepsTheProbesChildConfig(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	st, _ := probeFake(t, probeCase{mode: "chatgpt", home: "-", timeout: 7 * time.Second})
	wantState(t, st, StateReady)
	cfg := st.Context.CodexConfig(nil)
	if cfg.Timeout != 7*time.Second {
		t.Fatalf("timeout %v, want 7s", cfg.Timeout)
	}
	if cfg.Env == nil {
		t.Fatal("no environment: the child would inherit the process's CODEX_HOME")
	}
	for _, kv := range cfg.Env {
		if strings.HasPrefix(kv, "CODEX_HOME=") {
			t.Fatal("the child environment carries the ambient CODEX_HOME") // the value is not printed
		}
	}
	if s := fmt.Sprintf("%v %+v %#v", st.Context, st.Context, st.Context); strings.Contains(s, "PATH=") {
		t.Fatal("formatting a Context prints the environment")
	}
}

// The file source never starts Codex, but its guide still names the executable the user chose.
func TestFileSourceKeepsTheExecutableForTheGuide(t *testing.T) {
	st, rec := probeFake(t, probeCase{source: SourceFile})
	wantState(t, st, StateNoSignInFile)
	if rec != "" {
		t.Fatal("the file source started Codex")
	}
	if st.Context.CodexBin != fakeBin {
		t.Fatalf("CodexBin %q, want the --codex-bin executable", st.Context.CodexBin)
	}
	g, _ := st.Guide()
	if !strings.Contains(g.Text(), ShellQuote(fakeBin)+" login") {
		t.Fatalf("guide does not name the chosen executable:\n%s", g.Text())
	}
}
