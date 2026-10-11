package codexapp

import (
	"bytes"
	"context"
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

const canary = "eyJcanary.canary.canary"

var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "codexapp-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	bin, err = fake.BuildTo(dir)
	code := 1
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	} else {
		code = m.Run()
	}
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// start runs the fake with env (KEY=VALUE pairs) and a logger capturing scrubbed output.
func start(t *testing.T, timeout time.Duration, env ...string) (*Client, *bytes.Buffer, error) {
	t.Helper()
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	var logs bytes.Buffer
	logger := slog.New(logx.NewHandler(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	c, err := Start(context.Background(), Config{Bin: bin, Timeout: timeout, Logger: logger})
	if c != nil {
		t.Cleanup(func() { _ = c.Close() })
	}
	return c, &logs, err
}

// wantCodexError asserts err is codex-error with exit 3 naming Codex, and returns it.
func wantCodexError(t *testing.T, err error) (e *openai.Error) {
	t.Helper()
	if !errors.As(err, &e) || e.Class != openai.ClassCodexError || openai.ExitCode(err) != 3 {
		t.Fatalf("err = %v, want codex-error with exit 3", err)
	}
	if !strings.Contains(err.Error(), "Codex") {
		t.Fatalf("error does not name Codex: %v", err)
	}
	return e
}

func TestHandshakeAndAuthStatus(t *testing.T) {
	c, _, err := start(t, 0, "FAKECODEX_MODE=chatgpt", "FAKECODEX_TOKEN_EXP=1800000000")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := c.GetAuthStatus(context.Background(), false, false)
	if err != nil || meta.Method != "chatgpt" || meta.Token() != "" {
		t.Fatalf("metadata = %v, %v", meta, err)
	}
	full, err := c.GetAuthStatus(context.Background(), true, false)
	if err != nil || !strings.HasPrefix(full.Token(), "eyJ") {
		t.Fatalf("full = %v, %v", full, err)
	}
	if s := fmt.Sprintf("%v %+v %#v %s", full, full, full, full); strings.Contains(s, full.Token()) {
		t.Fatalf("AuthStatus formatting leaked the token: %s", s)
	}
	if logx.Scrub(full.Token()) == full.Token() {
		t.Fatal("token not registered with the scrubber")
	}
	for _, f := range []func(context.Context) ([]byte, error){
		func(ctx context.Context) ([]byte, error) { r, err := c.ReadAccount(ctx); return r, err },
		func(ctx context.Context) ([]byte, error) { r, err := c.ReadRateLimits(ctx); return r, err },
	} {
		if r, err := f(context.Background()); err != nil || len(r) == 0 {
			t.Fatalf("account call: %v", err)
		}
	}
}

// accept: AUTH-01#2
func TestTimeoutIsCodexError(t *testing.T) {
	if DefaultTimeout != 10*time.Second {
		t.Fatalf("DefaultTimeout = %v, want 10s", DefaultTimeout)
	}
	_, _, err := start(t, 50*time.Millisecond, "FAKECODEX_MODE=slow", "FAKECODEX_DELAY_MS=5000")
	e := wantCodexError(t, err)
	if !strings.Contains(e.Message, "did not answer initialize within 50ms") {
		t.Fatalf("message = %q", e.Message)
	}
}

func TestCrashIsCodexError(t *testing.T) {
	_, _, err := start(t, 0, "FAKECODEX_MODE=crash")
	_ = wantCodexError(t, err)
}

func TestRPCErrorKeepsCode(t *testing.T) {
	c, _, err := start(t, 0, "FAKECODEX_MODE=unknown-method")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.GetAuthStatus(context.Background(), false, false)
	_ = wantCodexError(t, err)
	var re *RPCError
	if !errors.As(err, &re) || re.Code != CodeMethodNotFound {
		t.Fatalf("err = %v, want RPCError -32601", err)
	}
}

// accept: AUTH-05#2
func TestCanaryNeverSurfaces(t *testing.T) {
	cases := []struct{ name, mode, leak string }{
		{"stderr", "stderr-canary", ""},
		{"rpc-error", "chatgpt", "rpc-error"},
		{"malformed", "chatgpt", "malformed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, logs, err := start(t, 0, "FAKECODEX_MODE="+tc.mode, "FAKECODEX_LEAK="+tc.leak, "FAKECODEX_CANARY="+canary)
			if err != nil {
				t.Fatal(err)
			}
			_, callErr := c.GetAuthStatus(context.Background(), false, false)
			if tc.leak != "" {
				e := wantCodexError(t, callErr)
				var re *RPCError
				if errors.As(callErr, &re) && strings.Contains(re.Message, "canary") {
					t.Fatalf("RPCError.Message kept the canary: %q", re.Message)
				}
				if strings.Contains(fmt.Sprintf("%v %+v", e, e), "canary") {
					t.Fatalf("formatted error leaked: %+v", e)
				}
			}
			_ = c.Close()
			out := logs.String()
			if tc.name == "stderr" && !strings.Contains(out, "fakecodex debug") {
				t.Fatalf("child stderr was not logged at all: %q", out)
			}
			for _, s := range []string{out, fmt.Sprint(callErr)} {
				if strings.Contains(s, "canary") {
					t.Fatalf("canary surfaced: %q", s)
				}
			}
		})
	}
}

// accept: AUTH-01#3
func TestChildArgvAndEnv(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "rec.txt")
	home := t.TempDir()
	t.Setenv("OPENAI_API_KEY", "sk-canary-env")
	t.Setenv("FAKECODEX_RECORD", rec)
	t.Setenv("FAKECODEX_MODE", "chatgpt")
	c, err := Start(context.Background(), Config{Bin: bin, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	full, err := c.GetAuthStatus(context.Background(), true, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	data, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	var argvEnv []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(l, "argv=") || strings.HasPrefix(l, "env=") {
			argvEnv = append(argvEnv, l)
		}
	}
	joined := strings.Join(argvEnv, "\n")
	if strings.Contains(joined, full.Token()) || strings.Contains(joined, "OPENAI_API_KEY") || strings.Contains(string(data), "sk-canary-env") {
		t.Fatal("child argv/env or requests carry the token or OPENAI_API_KEY")
	}
	if !strings.Contains(joined, "env=CODEX_HOME="+home+"\n") {
		t.Fatal("CODEX_HOME not passed through")
	}
	if !strings.Contains(string(data), `"name":"recite"`) {
		t.Fatal("initialize did not name recite")
	}
}

func TestStartRejectsRelativePath(t *testing.T) {
	if _, err := Start(context.Background(), Config{Bin: "codex"}); err == nil {
		t.Fatal("relative path accepted")
	}
}

func TestResolveOrder(t *testing.T) {
	dir := filepath.Dir(bin)
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	t.Setenv("PATH", dir)
	if got, err := Resolve("", getenv); err != nil || got != bin {
		t.Fatalf("PATH: %q %v", got, err)
	}
	other := filepath.Join(t.TempDir(), "codex-other")
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, data, 0o700); err != nil {
		t.Fatal(err)
	}
	env["RECITE_CODEX_BIN"] = other
	if got, err := Resolve("", getenv); err != nil || got != other {
		t.Fatalf("env: %q %v", got, err)
	}
	if got, err := Resolve(bin, getenv); err != nil || got != bin {
		t.Fatalf("flag: %q %v", got, err)
	}
	env["RECITE_CODEX_BIN"] = filepath.Join(dir, "missing")
	if _, err := Resolve("", getenv); !errors.Is(err, ErrNotFound) {
		t.Fatalf("explicit missing: %v", err)
	}
	delete(env, "RECITE_CODEX_BIN")
	t.Setenv("PATH", t.TempDir())
	if _, err := Resolve("", getenv); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no codex on PATH: %v", err)
	}
}

func TestBoundedStderr(t *testing.T) {
	var b boundedBuffer
	n, err := b.Write(bytes.Repeat([]byte("x"), stderrCap+100))
	_, _ = b.Write([]byte("more"))
	if err != nil || n != stderrCap+100 || len(b.String()) != stderrCap {
		t.Fatalf("n=%d len=%d err=%v", n, len(b.String()), err)
	}
}
