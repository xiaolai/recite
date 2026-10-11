package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/codexapp/fake"
	"github.com/xiaolai/recite/internal/logx"
	"github.com/xiaolai/recite/internal/openai"
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

// run starts a CodexSource against the fake in mode with extra env, returning the
// source, the captured (scrubbed) logs and the request record path.
func run(t *testing.T, timeout time.Duration, mode string, env ...string) (*CodexSource, *bytes.Buffer, string) {
	t.Helper()
	rec := filepath.Join(t.TempDir(), "rec.txt")
	t.Setenv("FAKECODEX_MODE", mode)
	t.Setenv("FAKECODEX_RECORD", rec)
	t.Setenv("FAKECODEX_TOKEN_EXP", "1800000000")
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	var logs bytes.Buffer
	logger := slog.New(logx.NewHandler(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	s := NewCodexSource(fakeBin, t.TempDir(), timeout, logger)
	t.Cleanup(func() { _ = s.Close() })
	return s, &logs, rec
}

func record(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func wantClass(t *testing.T, err error, class openai.Class) *openai.Error {
	t.Helper()
	var e *openai.Error
	if !errors.As(err, &e) || e.Class != class || openai.ExitCode(err) != 3 {
		t.Fatalf("err = %v, want %s with exit 3", err, class)
	}
	return e
}

// accept: AUTH-01#1
func TestCodexSourceReturnsToken(t *testing.T) {
	s, _, rec := run(t, 0, "chatgpt")
	tok, err := s.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok.Bearer(), "eyJ") || tok.Expires.Unix() != 1800000000 || tok.Source != "codex" {
		t.Fatalf("token: exp %v source %q", tok.Expires, tok.Source)
	}
	again, err := s.Token(context.Background())
	if err != nil || again.Bearer() != tok.Bearer() {
		t.Fatal("token not cached")
	}
	// Metadata first, then the token; and only once thanks to the cache.
	reqs := record(t, rec)
	i, j := strings.Index(reqs, `"includeToken":false`), strings.Index(reqs, `"includeToken":true`)
	if i < 0 || j < i || strings.Count(reqs, "getAuthStatus") != 2 {
		t.Fatalf("request order wrong:\n%s", reqs)
	}
	if _, err := s.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh = %v", err)
	}
	if !strings.Contains(record(t, rec), `"refreshToken":true`) {
		t.Fatal("Refresh did not ask Codex with refreshToken: true")
	}
}

// accept: AUTH-03#1
// accept: AUTH-03#3
func TestCodexSourceRefusesAPIKey(t *testing.T) {
	s, _, rec := run(t, 0, "apikey")
	_, err := s.Token(context.Background())
	e := wantClass(t, err, openai.ClassAuthMode)
	if e.Message != "Codex is signed in with an API key; recite works only with a ChatGPT sign-in" {
		t.Fatalf("message = %q", e.Message)
	}
	reqs := record(t, rec)
	if strings.Contains(reqs, `"includeToken":true`) || !strings.Contains(reqs, `"includeToken":false`) {
		t.Fatalf("includeToken:true was sent in apikey mode:\n%s", reqs)
	}
}

// accept: AUTH-03#3
func TestLeakedKeyNeverSurfaces(t *testing.T) {
	const key = "sk-canary-leaked-opaque"
	s, logs, rec := run(t, 0, "apikey", "FAKECODEX_LEAK=token", "FAKECODEX_CANARY="+key)
	_, err := s.Token(context.Background())
	_ = wantClass(t, err, openai.ClassAuthMode)
	_ = s.Close()
	if logx.Scrub(key) == key {
		t.Fatal("leaked key not registered with the scrubber")
	}
	reqs := record(t, rec)
	_, reqs, _ = strings.Cut(reqs, "req=") // the argv/env part holds the canary as FAKECODEX_CANARY
	for _, out := range []string{err.Error(), fmt.Sprintf("%v %+v %#v", err, s, s), logs.String(), reqs} {
		if strings.Contains(out, key) {
			t.Fatalf("leaked key surfaced: %q", out)
		}
	}
}

// accept: AUTH-03#2
func TestOpenAIAPIKeyEnvHasNoEffect(t *testing.T) {
	const key = "sk-canary-env-key"
	s, logs, rec := run(t, 0, "chatgpt", "OPENAI_API_KEY="+key)
	tok, err := s.Token(context.Background())
	if err != nil || tok.Bearer() == key {
		t.Fatalf("token %v err %v", tok, err)
	}
	_ = s.Close()
	if r := record(t, rec); strings.Contains(r, key) || strings.Contains(r, "OPENAI_API_KEY") {
		t.Fatalf("the fake received OPENAI_API_KEY:\n%s", r)
	}
	if strings.Contains(logs.String(), key) {
		t.Fatal("key logged")
	}
}

func TestCodexSourceLoggedOutAndOtherModes(t *testing.T) {
	for mode, class := range map[string]openai.Class{
		"logged-out": openai.ClassAuthMode, "headers": openai.ClassAuthMode,
		"future-mode": openai.ClassAuthMode, "rpc-error": openai.ClassCodexError,
	} {
		t.Run(mode, func(t *testing.T) {
			s, _, rec := run(t, 0, mode)
			_, err := s.Token(context.Background())
			_ = wantClass(t, err, class)
			if strings.Contains(record(t, rec), `"includeToken":true`) {
				t.Fatal("token requested for a non-ChatGPT sign-in")
			}
		})
	}
}

// accept: AUTH-01#2
func TestCodexSourceTimeout(t *testing.T) {
	s, _, _ := run(t, 50*time.Millisecond, "slow", "FAKECODEX_DELAY_MS=5000")
	_, err := s.Token(context.Background())
	e := wantClass(t, err, openai.ClassCodexError)
	if !strings.Contains(e.Error(), "Codex") {
		t.Fatalf("error does not name Codex: %v", e)
	}
}

func TestCodexSourceCrash(t *testing.T) {
	s, _, _ := run(t, 0, "crash")
	_, err := s.Token(context.Background())
	_ = wantClass(t, err, openai.ClassCodexError)
}

// accept: AUTH-01#3
func TestChildArgvEnvCarryNoToken(t *testing.T) {
	s, _, rec := run(t, 0, "chatgpt", "OPENAI_API_KEY=sk-canary-argv")
	tok, err := s.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	argvEnv, _, _ := strings.Cut(record(t, rec), "req=")
	if strings.Contains(argvEnv, tok.Bearer()) || strings.Contains(argvEnv, "OPENAI_API_KEY") {
		t.Fatalf("argv/env carry a secret:\n%s", argvEnv)
	}
}

// accept: AUTH-05#2
func TestStderrCanaryNotLogged(t *testing.T) {
	s, logs, _ := run(t, 0, "stderr-canary", "FAKECODEX_CANARY=eyJcanary.canary.canary")
	if _, err := s.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if !strings.Contains(logs.String(), "fakecodex debug") {
		t.Fatalf("stderr not logged: %q", logs.String())
	}
	if strings.Contains(logs.String(), "canary") {
		t.Fatalf("canary logged: %q", logs.String())
	}
}

func TestResolveContext(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	t.Setenv("PATH", t.TempDir()) // no codex anywhere

	c, err := ResolveContext("", "", SourceAuto, getenv)
	if err != nil || c.Source != SourceFile || c.CodexBin != "" {
		t.Fatalf("auto without codex: %+v %v", c, err)
	}
	_, err = ResolveContext("", "", SourceCodex, getenv)
	_ = wantClass(t, err, openai.ClassCodexError)

	env["CODEX_HOME"] = "/env/home"
	c, err = ResolveContext(fakeBin, "", SourceAuto, getenv)
	if err != nil || c.Source != SourceCodex || c.CodexBin != fakeBin || c.CodexHome != "/env/home" {
		t.Fatalf("auto with codex: %+v %v", c, err)
	}
	c, err = ResolveContext(fakeBin, "/flag/home", SourceFile, getenv)
	if err != nil || c.Source != SourceFile || c.CodexHome != "/flag/home" {
		t.Fatalf("file: %+v %v", c, err)
	}
	src, err := NewSource(c, 0, nil)
	if fs, ok := src.(*FileSource); err != nil || !ok || fs.path != "/flag/home/auth.json" {
		t.Fatalf("NewSource(file) = %v %v", src, err)
	}
	_, err = ResolveContext(filepath.Join(t.TempDir(), "nope"), "", SourceAuto, getenv)
	_ = wantClass(t, err, openai.ClassCodexError)
}

// jwt builds a fake unsigned JWT with exp.
func jwt(exp int64) string {
	b := base64.RawURLEncoding
	return b.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." +
		b.EncodeToString(fmt.Appendf(nil, `{"exp":%d}`, exp)) + "." + b.EncodeToString([]byte("sig"))
}
