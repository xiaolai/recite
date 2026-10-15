package fake

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakecodex-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	bin, err = BuildTo(dir)
	code := 1
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	} else {
		code = m.Run()
	}
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// hermeticEnv is the environment the fake runs with in these tests: what a process needs, and
// none of the developer's variables, which may hold live credentials.
func hermeticEnv() []string {
	env := []string{}
	for _, name := range []string{"PATH", "HOME", "TMPDIR"} {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	return env
}

// client is a minimal JSON-RPC peer for the tests; the real one is internal/codexapp (WP-1.3).
type client struct {
	t      *testing.T
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Scanner
	stderr *bytes.Buffer
	next   int
}

func start(t *testing.T, env ...string) *client {
	t.Helper()
	cmd := exec.Command(bin, "app-server", "--listen", "stdio://")
	cmd.Env = append(hermeticEnv(), env...)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &client{t: t, cmd: cmd, in: in, out: bufio.NewScanner(out), stderr: &stderr}
	t.Cleanup(func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return c
}

type reply struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *client) notify(method string) {
	c.t.Helper()
	if _, err := fmt.Fprintf(c.in, `{"method":%q}`+"\n", method); err != nil {
		c.t.Fatal(err)
	}
}

// call returns the reply, or an error when the child ends without one.
func (c *client) call(method string, params any) (reply, error) {
	c.t.Helper()
	c.next++
	b, _ := json.Marshal(map[string]any{"id": c.next, "method": method, "params": params})
	if _, err := c.in.Write(append(b, '\n')); err != nil {
		return reply{}, err
	}
	if !c.out.Scan() {
		return reply{}, errors.New("no reply: child output ended")
	}
	var r reply
	if err := json.Unmarshal(c.out.Bytes(), &r); err != nil {
		c.t.Fatalf("malformed reply %q: %v", c.out.Text(), err)
	}
	if r.ID != c.next {
		c.t.Fatalf("reply id %d, want %d", r.ID, c.next)
	}
	return r, nil
}

func (c *client) mustCall(method string, params any) reply {
	c.t.Helper()
	r, err := c.call(method, params)
	if err != nil {
		c.t.Fatal(err)
	}
	return r
}

type authStatus struct {
	AuthMethod *string `json:"authMethod"`
	AuthToken  *string `json:"authToken"`
}

func (c *client) handshake() {
	c.t.Helper()
	if r := c.mustCall("initialize", map[string]any{"clientInfo": map[string]any{"name": "recite"}}); r.Error != nil {
		c.t.Fatalf("initialize: %+v", r.Error)
	}
	c.notify("initialized")
}

func (c *client) status(include, refresh bool) (authStatus, reply) {
	c.t.Helper()
	r := c.mustCall("getAuthStatus", map[string]any{"includeToken": include, "refreshToken": refresh})
	var s authStatus
	if r.Error == nil {
		if err := json.Unmarshal(r.Result, &s); err != nil {
			c.t.Fatal(err)
		}
	}
	return s, r
}

func jwtExp(t *testing.T, tok string) int64 {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 || !strings.HasPrefix(tok, "eyJ") {
		t.Fatalf("token is not a JWT shape: %d parts", len(parts))
	}
	p, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(p, &claims); err != nil {
		t.Fatal(err)
	}
	return claims.Exp
}

// accept: FND-05#1
func TestModesAnswerGetAuthStatus(t *testing.T) {
	for _, mode := range Modes {
		t.Run(mode, func(t *testing.T) {
			env := []string{"FAKECODEX_MODE=" + mode, "FAKECODEX_TOKEN_EXP=1800000000", "FAKECODEX_DELAY_MS=1"}
			c := start(t, env...)
			if mode == "crash" {
				if _, err := c.call("initialize", map[string]any{}); err == nil {
					t.Fatal("crash mode answered initialize")
				}
				err := c.cmd.Wait()
				var ee *exec.ExitError
				if !errors.As(err, &ee) || ee.ExitCode() != 101 {
					t.Fatalf("crash exit = %v, want 101", err)
				}
				return
			}
			c.handshake()
			meta, r := c.status(false, false)
			switch mode {
			case "rpc-error":
				if r.Error == nil || r.Error.Code != -32000 || r.Error.Message != "credential store unavailable" {
					t.Fatalf("rpc-error reply = %+v", r.Error)
				}
				return
			case "unknown-method":
				if r.Error == nil || r.Error.Code != -32601 {
					t.Fatalf("unknown-method reply = %+v", r.Error)
				}
				return
			}
			if r.Error != nil {
				t.Fatalf("getAuthStatus error %+v", r.Error)
			}
			if meta.AuthToken != nil {
				t.Fatal("token returned with includeToken false")
			}
			want := map[string]string{"logged-out": "", "future-mode": "futureMode", "slow": "chatgpt",
				"refresh-ok": "chatgpt", "refresh-fails": "chatgpt", "stderr-canary": "chatgpt",
				"null-token": "chatgpt", "bad-jwt": "chatgpt"}
			wm, ok := want[mode]
			if !ok {
				wm = mode // the eight AuthMode values
			}
			got := ""
			if meta.AuthMethod != nil {
				got = *meta.AuthMethod
			}
			if got != wm {
				t.Fatalf("authMethod = %q, want %q", got, wm)
			}
			full, _ := c.status(true, false)
			switch mode {
			case "null-token":
				if full.AuthToken != nil {
					t.Fatal("null-token returned a token")
				}
			case "bad-jwt":
				if full.AuthToken == nil || strings.HasPrefix(*full.AuthToken, "eyJ") {
					t.Fatal("bad-jwt returned no token or a JWT")
				}
			}
			switch wm {
			case "":
				if full.AuthToken != nil {
					t.Fatal("logged-out returned a token")
				}
			case "chatgpt":
				if mode == "null-token" || mode == "bad-jwt" {
					break
				}
				if full.AuthToken == nil || jwtExp(t, *full.AuthToken) != 1800000000 {
					t.Fatal("chatgpt token missing or exp not honoured")
				}
			default:
				if full.AuthToken == nil || *full.AuthToken == "" {
					t.Fatal("non-chatgpt mode returned no credential with includeToken")
				}
			}
			for _, m := range []string{"account/read", "account/rateLimits/read"} {
				if r := c.mustCall(m, map[string]any{}); r.Error != nil || len(r.Result) == 0 {
					t.Fatalf("%s: %+v", m, r.Error)
				}
			}
			// account/read shapes as measured on Codex 0.162.1.
			var acct struct {
				Account *struct {
					Type     string `json:"type"`
					Email    string `json:"email"`
					PlanType string `json:"planType"`
				} `json:"account"`
			}
			if err := json.Unmarshal(c.mustCall("account/read", map[string]any{}).Result, &acct); err != nil {
				t.Fatal(err)
			}
			switch wm {
			case "":
				if acct.Account != nil {
					t.Fatal("logged-out account/read returned an account")
				}
			case "chatgpt":
				if acct.Account == nil || acct.Account.Type != "chatgpt" || acct.Account.Email == "" || acct.Account.PlanType == "" {
					t.Fatalf("chatgpt account/read = %+v", acct.Account)
				}
			case "apikey":
				if acct.Account == nil || acct.Account.Type != "apiKey" {
					t.Fatalf("apikey account/read = %+v; want type apiKey", acct.Account)
				}
			}
		})
	}
}

// accept: FND-05#1
func TestRefreshModes(t *testing.T) {
	c := start(t, "FAKECODEX_MODE=refresh-ok", "FAKECODEX_TOKEN_EXP=1000", "FAKECODEX_REFRESHED_EXP=1900000000")
	c.handshake()
	before, _ := c.status(true, false)
	after, r := c.status(true, true)
	if r.Error != nil || jwtExp(t, *before.AuthToken) != 1000 || jwtExp(t, *after.AuthToken) != 1900000000 {
		t.Fatalf("refresh-ok: error %+v", r.Error)
	}
	if *before.AuthToken == *after.AuthToken {
		t.Fatal("refresh-ok returned the same token")
	}

	c = start(t, "FAKECODEX_MODE=refresh-fails", "FAKECODEX_TOKEN_EXP=1000")
	c.handshake()
	if s, r := c.status(true, false); r.Error != nil || jwtExp(t, *s.AuthToken) != 1000 {
		t.Fatal("refresh-fails: plain read must still work")
	}
	if _, r := c.status(true, true); r.Error == nil {
		t.Fatal("refresh-fails: refresh succeeded")
	}
}

// accept: FND-05#1
func TestStderrCanaryAndRecorder(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "rec.txt")
	c := start(t, "FAKECODEX_MODE=stderr-canary", "FAKECODEX_CANARY=eyJcanary.canary.canary", "FAKECODEX_RECORD="+rec)
	c.handshake()
	_ = c.in.Close()
	if err := c.cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.stderr.String(), "eyJcanary.canary.canary") {
		t.Fatal("canary not on stderr")
	}
	data, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"argv=app-server\n", "argv=--listen\n", "env=FAKECODEX_MODE=stderr-canary\n"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("record lacks %q", want)
		}
	}
}

// A record keeps the value of the fake's own settings only; any other variable is recorded by
// name, so a printed record holds no inherited credential but still shows a variable that
// should have been removed.
func TestRecordKeepsOtherValuesOut(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "rec.txt")
	c := start(t, "FAKECODEX_RECORD="+rec, "OPENAI_API_KEY=sk-canary-record", "SOME_TOKEN=canary-other")
	c.handshake()
	_ = c.in.Close()
	if err := c.cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "canary") {
		t.Error("the record holds a value it should name only")
	}
	for _, want := range []string{"env=OPENAI_API_KEY\n", "env=SOME_TOKEN\n", "env=FAKECODEX_RECORD=" + rec + "\n"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("record lacks %q", want)
		}
	}
}

// A state file that exists but cannot be read is a broken fixture, never the default sign-in.
func TestUnreadableStateFileFails(t *testing.T) {
	cmd := exec.Command(bin, "login", "status")
	cmd.Env = append(hermeticEnv(), "FAKECODEX_STATE_FILE="+t.TempDir()) // a folder: reading it fails
	var ee *exec.ExitError
	if err := cmd.Run(); !errors.As(err, &ee) || ee.ExitCode() != 70 {
		t.Fatalf("exit %v, want 70", err)
	}
}

// An oversized request ends the app-server with an error, not as a clean end of input.
func TestOversizedRequestFails(t *testing.T) {
	c := start(t)
	// The fake stops reading at the limit, so the rest of the write may meet a closed pipe.
	_, _ = c.in.Write(append(bytes.Repeat([]byte("x"), 5<<20), '\n'))
	_ = c.in.Close()
	var ee *exec.ExitError
	if err := c.cmd.Wait(); !errors.As(err, &ee) || ee.ExitCode() != 74 {
		t.Fatalf("exit %v, want 74", err)
	}
	if strings.Contains(c.stderr.String(), "xxxx") {
		t.Error("the request was quoted")
	}
}

// accept: FND-05#1
func TestLoginStatus(t *testing.T) {
	cases := []struct {
		mode string
		code int
		out  string
	}{
		{"chatgpt", 0, "Logged in using ChatGPT"},
		{"logged-out", 1, "Not logged in"},
		{"apikey", 0, "Logged in using an API key"},
		{"headers", 0, "Logged in using headers"},
	}
	for _, tc := range cases {
		cmd := exec.Command(bin, "login", "status")
		cmd.Env = append(hermeticEnv(), "FAKECODEX_MODE="+tc.mode)
		out, err := cmd.Output()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if code != tc.code || strings.TrimSpace(string(out)) != tc.out {
			t.Errorf("%s: exit %d %q, want %d %q", tc.mode, code, out, tc.code, tc.out)
		}
	}
	cmd := exec.Command(bin, "app-server")
	cmd.Env = append(hermeticEnv(), "FAKECODEX_MODE=no-such-mode")
	if err := cmd.Run(); err == nil {
		t.Fatal("unknown mode accepted")
	}
}

func TestBuildHelper(t *testing.T) {
	if p := Build(t); p == "" {
		t.Fatal("empty path")
	}
}

// A relative directory gives an absolute path the caller can run.
func TestBuildToRelativeDir(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(wd, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, err := BuildTo(rel)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("path %q is not absolute", got)
	}
	if fi, err := os.Stat(got); err != nil || fi.Mode()&0o111 == 0 {
		t.Fatalf("no executable at %s: %v", got, err)
	}
}

// Like Codex, the fake answers nothing but initialize until the handshake is complete, so a
// client that skips or reorders it fails here too.
func TestHandshakeRequired(t *testing.T) {
	c := start(t)
	if r := c.mustCall("getAuthStatus", map[string]any{"includeToken": false}); r.Error == nil || r.Error.Code != -32002 {
		t.Fatalf("getAuthStatus before initialize: %+v", r.Error)
	}
	if r := c.mustCall("initialize", map[string]any{"clientInfo": map[string]any{"name": "recite"}}); r.Error != nil {
		t.Fatalf("initialize: %+v", r.Error)
	}
	if r := c.mustCall("getAuthStatus", map[string]any{"includeToken": false}); r.Error == nil || r.Error.Code != -32002 {
		t.Fatalf("getAuthStatus before initialized: %+v", r.Error)
	}
	c.notify("initialized")
	if _, r := c.status(false, false); r.Error != nil {
		t.Fatalf("getAuthStatus after the handshake: %+v", r.Error)
	}
}

// Params of the wrong type are refused, never read as the zero value.
func TestMalformedParamsRefused(t *testing.T) {
	c := start(t)
	c.handshake()
	r := c.mustCall("getAuthStatus", map[string]any{"includeToken": true, "refreshToken": "true"})
	if r.Error == nil || r.Error.Code != -32602 {
		t.Fatalf("a string refreshToken: error %+v", r.Error)
	}
}

// FAKECODEX_REQUIRES_OPENAI_AUTH=false reports a provider that needs no OpenAI sign-in.
func TestRequiresOpenAIAuthFalse(t *testing.T) {
	c := start(t, "FAKECODEX_REQUIRES_OPENAI_AUTH=false")
	c.handshake()
	r := c.mustCall("getAuthStatus", map[string]any{"includeToken": false})
	var res struct {
		RequiresOpenaiAuth *bool `json:"requiresOpenaiAuth"`
	}
	if r.Error != nil || json.Unmarshal(r.Result, &res) != nil || res.RequiresOpenaiAuth == nil || *res.RequiresOpenaiAuth {
		t.Fatalf("reply %+v", r)
	}
}
