package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xiaolai/sayso/go/secret"

	"github.com/xiaolai/recite/internal/codexapp/fake"
	"github.com/xiaolai/recite/internal/logx"
)

var fakeBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "auth-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fakeBin, err = fake.BuildTo(dir)
	code := 1
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	} else {
		code = m.Run()
	}
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// run probes the fake in mode with extra env (KEY=VALUE), returning the status, the captured
// (scrubbed) logs and the fake's record.
func run(t *testing.T, timeout time.Duration, mode string, env ...string) (*Status, *bytes.Buffer, string) {
	t.Helper()
	extra := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		extra[k] = v
	}
	var logs bytes.Buffer
	logger := slog.New(logx.NewHandler(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	st, rec := probeFake(t, probeCase{mode: mode, env: extra, timeout: timeout, logger: logger})
	return st, &logs, rec
}

func readRecord(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// accept: AUTH-01#1
func TestCodexSourceReturnsToken(t *testing.T) {
	st, _, _ := run(t, 0, "chatgpt")
	wantState(t, st, StateReady)
	tok, err := st.Creds.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok.Bearer(), "eyJ") || tok.Expires.Unix() != 1800000000 || tok.Source != "codex" {
		t.Fatalf("token: exp %v source %q", tok.Expires, tok.Source)
	}
	rec := os.Getenv("FAKECODEX_RECORD")
	reqs := readRecord(t, rec)
	// Metadata first, then the token; and only once: the token is cached, not the child.
	i, j := strings.Index(reqs, `"includeToken":false`), strings.Index(reqs, `"includeToken":true`)
	if i < 0 || j < i || strings.Count(reqs, "getAuthStatus") != 2 {
		t.Fatalf("request order wrong:\n%s", reqs)
	}
	again, err := st.Creds.Token(context.Background())
	if err != nil || again.Bearer() != tok.Bearer() || strings.Count(readRecord(t, rec), "getAuthStatus") != 2 {
		t.Fatal("token not cached")
	}
	if _, err := st.Creds.RefreshAfter(context.Background(), tok); err != nil {
		t.Fatalf("RefreshAfter = %v", err)
	}
	if !strings.Contains(readRecord(t, rec), `"refreshToken":true`) {
		t.Fatal("a refresh did not ask Codex with refreshToken: true")
	}
}

// accept: AUTH-03#1
// accept: AUTH-03#3
func TestCodexSourceRefusesAPIKey(t *testing.T) {
	st, _, rec := run(t, 0, "apikey")
	wantState(t, st, StateAPIKey)
	if g, _ := st.Guide(); !strings.Contains(g.Message, "signed in with an API key") {
		t.Fatalf("guide = %q", g.Message)
	}
	if strings.Contains(rec, `"includeToken":true`) || !strings.Contains(rec, `"includeToken":false`) {
		t.Fatalf("includeToken:true was sent in apikey mode:\n%s", rec)
	}
}

// accept: AUTH-03#3
func TestLeakedKeyNeverSurfaces(t *testing.T) {
	const key = "sk-canary-leaked-opaque"
	st, logs, rec := run(t, 0, "apikey", "FAKECODEX_LEAK=token", "FAKECODEX_CANARY="+key)
	wantState(t, st, StateAPIKey)
	if secret.Scrub(key) == key {
		t.Fatal("leaked key not registered with the scrubber")
	}
	_, reqs, _ := strings.Cut(rec, "req=") // the argv/env part holds the canary as FAKECODEX_CANARY
	for _, out := range []string{st.Err.Error(), fmt.Sprintf("%v %+v %#v", st, st, st), logs.String(), reqs} {
		if strings.Contains(out, key) {
			t.Fatalf("leaked key surfaced: %q", out)
		}
	}
}

// accept: AUTH-03#2
func TestOpenAIAPIKeyEnvHasNoEffect(t *testing.T) {
	const key = "sk-canary-env-key"
	st, logs, rec := run(t, 0, "chatgpt", "OPENAI_API_KEY="+key)
	wantState(t, st, StateReady)
	tok, err := st.Creds.Token(context.Background())
	if err != nil || tok.Bearer() == key {
		t.Fatalf("token %v err %v", tok, err)
	}
	if strings.Contains(rec, key) || strings.Contains(rec, "OPENAI_API_KEY") {
		t.Fatalf("the fake received OPENAI_API_KEY:\n%s", rec)
	}
	if strings.Contains(logs.String(), key) {
		t.Fatal("key logged")
	}
}

func TestCodexSourceLoggedOutAndOtherModes(t *testing.T) {
	for mode, state := range map[string]State{
		"logged-out": StateNotSignedIn, "headers": StateUnsupported,
		"future-mode": StateUnsupported, "rpc-error": StateCodexError,
	} {
		t.Run(mode, func(t *testing.T) {
			st, _, rec := run(t, 0, mode)
			wantState(t, st, state)
			if strings.Contains(rec, `"includeToken":true`) {
				t.Fatal("token requested for a non-ChatGPT sign-in")
			}
		})
	}
}

// accept: AUTH-01#2
func TestCodexSourceTimeout(t *testing.T) {
	st, _, _ := run(t, 50*time.Millisecond, "slow", "FAKECODEX_DELAY_MS=5000")
	wantState(t, st, StateCodexError)
	if !strings.Contains(st.Err.Error(), "Codex") {
		t.Fatalf("error does not name Codex: %v", st.Err)
	}
}

func TestCodexSourceCrash(t *testing.T) {
	st, _, _ := run(t, 0, "crash")
	wantState(t, st, StateCodexError)
}

// accept: AUTH-01#3
func TestChildArgvEnvCarryNoToken(t *testing.T) {
	st, _, rec := run(t, 0, "chatgpt", "OPENAI_API_KEY=sk-canary-argv")
	wantState(t, st, StateReady)
	tok, err := st.Creds.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	argvEnv, _, _ := strings.Cut(rec, "req=")
	if strings.Contains(argvEnv, tok.Bearer()) || strings.Contains(argvEnv, "OPENAI_API_KEY") {
		t.Fatalf("argv/env carry a secret:\n%s", argvEnv)
	}
	if !strings.Contains(argvEnv, "argv=-c\nargv=model_provider=\"openai\"\nargv=app-server\n") {
		t.Fatalf("the child does not force the OpenAI provider:\n%s", argvEnv)
	}
}

// The child's stderr is never logged: the scrubber cannot recognise every credential format.
// accept: AUTH-05#2
func TestStderrCanaryNotLogged(t *testing.T) {
	st, logs, _ := run(t, 0, "stderr-canary", "FAKECODEX_CANARY=eyJcanary.canary.canary")
	wantState(t, st, StateReady)
	if strings.Contains(logs.String(), "canary") || strings.Contains(logs.String(), "fakecodex debug") {
		t.Fatalf("stderr logged: %q", logs.String())
	}
	if !strings.Contains(logs.String(), "wrote to stderr") {
		t.Fatalf("the stderr byte count is not logged: %q", logs.String())
	}
}

// jwt builds a fake unsigned JWT with exp.
func jwt(exp int64) string {
	b := base64.RawURLEncoding
	return b.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." +
		b.EncodeToString(fmt.Appendf(nil, `{"exp":%d}`, exp)) + "." + b.EncodeToString([]byte("sig"))
}

// In a non-live test the Codex child gets only what the fake and a process need, and the
// authentication overrides tests set to check their removal; the developer's other variables,
// which may hold live credentials, never reach it or its record.
func TestTestChildEnvironmentIsHermetic(t *testing.T) {
	t.Setenv("RECITE_TEST_AMBIENT_SECRET", "canary-ambient")
	st, _, rec := run(t, 0, "chatgpt")
	wantState(t, st, StateReady)
	if strings.Contains(rec, "RECITE_TEST_AMBIENT_SECRET") {
		t.Fatal("an ambient variable reached the test child")
	}
	if !strings.Contains(rec, "env=FAKECODEX_MODE=chatgpt\n") {
		t.Fatal("the fake's own settings did not reach it")
	}
}
