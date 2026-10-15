package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/xiaolai/recite/internal/auth"
	codexfake "github.com/xiaolai/recite/internal/codexapp/fake"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/realtime"
	rtfake "github.com/xiaolai/recite/internal/realtime/fake"
)

var fakeCodex string

// realEnv is the environment before isolateConfig: only live tests, which check the user's real
// setup, hand it to a child.
var realEnv []string

func TestMain(m *testing.M) {
	if target := os.Getenv(publishHelperEnv); target != "" {
		os.Exit(publishHelper(target)) // a subprocess of TestPublicationSurvivesSIGKILL
	}
	if dir := os.Getenv(sigintHelperEnv); dir != "" {
		os.Exit(sigintHelper(dir)) // a subprocess of TestSIGINTExits130
	}
	dir, err := os.MkdirTemp("", "cli-codex-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	realEnv = os.Environ()
	isolateConfig(dir)
	code := 1
	fakeCodex, err = codexfake.BuildTo(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	} else {
		code = m.Run()
	}
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// isolateConfig points every config and cache location at dir and clears the RECITE_*
// settings, so no test reads the real home or the developer's environment. RECITE_LIVE is
// kept: it gates the live suites.
func isolateConfig(dir string) {
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		if err := os.Setenv(k, filepath.Join(dir, "home", k)); err != nil {
			panic(err)
		}
	}
	for _, k := range []string{"RECITE_VOICE", "RECITE_MODEL", "RECITE_EFFORT", "RECITE_SPEED", "RECITE_JOBS",
		"RECITE_PROFILE", "RECITE_CODEX_BIN", "RECITE_CACHE_DIR"} {
		if err := os.Unsetenv(k); err != nil {
			panic(err)
		}
	}
}

var cliNow = time.Unix(1_700_000_000, 0)

// authRun runs recite with the fake Codex in mode, a temporary home, and the fixed clock.
func authRun(t *testing.T, mode string, args ...string) result {
	t.Helper()
	t.Setenv("FAKECODEX_MODE", mode)
	t.Setenv("FAKECODEX_TOKEN_EXP", fmt.Sprint(cliNow.Add(time.Hour).Unix()))
	var out, errb bytes.Buffer
	c := New(&out, &errb)
	c.Getenv = func(string) string { return "" }
	c.Now = func() time.Time { return cliNow }
	full := append([]string{"--codex-bin", fakeCodex, "--codex-home", t.TempDir(), "--auth-source", "codex"}, args...)
	code := c.Run(full)
	return result{code: code, stdout: out.String(), stderr: errb.String()}
}

func eventsSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	// Located from this file, not the working directory: say tests change directory.
	_, here, _, _ := runtime.Caller(0)
	f, err := os.Open(filepath.Join(filepath.Dir(here), "..", "..", "docs", "schema", "events.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource("events.schema.json", doc); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile("events.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return sch
}

func validate(t *testing.T, sch *jsonschema.Schema, line string) {
	t.Helper()
	v, err := jsonschema.UnmarshalJSON(strings.NewReader(line))
	if err != nil {
		t.Fatalf("not JSON: %q", line)
	}
	if err := sch.Validate(v); err != nil {
		t.Fatalf("%s does not validate: %v", line, err)
	}
}

// The values the fake holds that must never be printed.
var secrets = []string{"eyJ", "canary@example.invalid", "acct-canary-0000", "fake-subject"}

// accept: AUTH-06#1
func TestAuthStatusPrintsNoSecret(t *testing.T) {
	for _, args := range [][]string{{"auth", "status"}, {"auth", "status", "--json"}, {"auth", "status", "-vvv"}} {
		r := authRun(t, "chatgpt", args...)
		if r.code != 0 {
			t.Fatalf("%v: code %d stderr %q", args, r.code, r.stderr)
		}
		for _, s := range secrets {
			if strings.Contains(r.stdout+r.stderr, s) {
				t.Fatalf("%v printed %q:\n%s\n%s", args, s, r.stdout, r.stderr)
			}
		}
	}
	r := authRun(t, "chatgpt", "auth", "status")
	for _, want := range []string{"state:    ready\n", "source:   codex app-server\n", "codex:    " + fakeCodex + " (version 0.162.1)\n",
		"method:   chatgpt\n", "plan:     pro\n", "expires:  in 1h 0m\n"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, r.stdout)
		}
	}
	if r.stderr != "" {
		t.Errorf("ready printed to stderr: %q", r.stderr)
	}
}

// accept: AUTH-06#2
func TestAuthStatusExitCodes(t *testing.T) {
	for mode, state := range map[string]auth.State{
		"chatgpt": auth.StateReady, "logged-out": auth.StateNotSignedIn, "apikey": auth.StateAPIKey,
		"headers": auth.StateUnsupported, "rpc-error": auth.StateCodexError, "unknown-method": auth.StateAppServerAuthUnavailable,
		"null-token": auth.StateCodexError,
	} {
		t.Run(mode, func(t *testing.T) {
			r := authRun(t, mode, "auth", "status")
			if !strings.Contains(r.stdout, "state:    "+string(state)+"\n") {
				t.Fatalf("stdout %q", r.stdout)
			}
			if state == auth.StateReady {
				if r.code != 0 || r.stderr != "" {
					t.Fatalf("ready: code %d stderr %q", r.code, r.stderr)
				}
				return
			}
			if r.code != 3 || !strings.HasSuffix(r.stderr, "More help: "+auth.AuthURL+"\n") {
				t.Fatalf("code %d stderr %q", r.code, r.stderr)
			}
		})
	}
	// No codex at all.
	var out, errb bytes.Buffer
	c := New(&out, &errb)
	c.Getenv = func(string) string { return "" }
	t.Setenv("PATH", t.TempDir())
	if code := c.Run([]string{"auth", "status", "--auth-source", "codex"}); code != 3 || !strings.Contains(errb.String(), "npm install -g @openai/codex") {
		t.Fatalf("no-codex: %d %q", code, errb.String())
	}
}

func TestAuthSubcommands(t *testing.T) {
	r := run("auth")
	if r.code != ExitOK || !strings.HasPrefix(r.stdout, "Usage: recite auth") {
		t.Fatalf("auth: %+v", r)
	}
	if r := run("auth", "nope"); r.code != ExitUsage {
		t.Fatalf("auth nope: %+v", r)
	}
	if r := run("auth", "status", "extra"); r.code != ExitUsage {
		t.Fatalf("auth status extra: %+v", r)
	}
}

// accept: AUTH-07#10
func TestAuthErrorEventValidates(t *testing.T) {
	sch := eventsSchema(t)
	for _, mode := range []string{"logged-out", "apikey", "headers", "rpc-error", "unknown-method"} {
		r := authRun(t, mode, "auth", "status", "--json")
		lines := strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n")
		if r.code != 3 || len(lines) != 1 || r.stderr != "" {
			t.Fatalf("%s: code %d, %d lines, stderr %q", mode, r.code, len(lines), r.stderr)
		}
		validate(t, sch, lines[0])
		var ev auth.Event
		if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil || ev.Event != "auth_error" || ev.Link != auth.AuthURL || len(ev.FixCommands) == 0 {
			t.Fatalf("%s: event %+v", mode, ev)
		}
		// Every line of the guide travels with the commands: the api-key warning, the device-code
		// prerequisite.
		var texts []string
		for _, s := range ev.Steps {
			texts = append(texts, s.Text)
		}
		want := map[string]string{"apikey": "Warning: this changes how Codex itself signs in.", "logged-out": "Device-code login must first be enabled"}[mode]
		if want != "" && !strings.Contains(strings.Join(texts, "\n"), want) {
			t.Fatalf("%s: steps %+v lack %q", mode, ev.Steps, want)
		}
	}
	r := authRun(t, "chatgpt", "auth", "status", "--json")
	validate(t, sch, strings.TrimSuffix(r.stdout, "\n"))
	// The schema rejects a malformed auth_error.
	v, _ := jsonschema.UnmarshalJSON(strings.NewReader(`{"event":"auth_error","state":"ready","message":"x","fix_commands":[],"steps":[],"link":"https://x"}`))
	if sch.Validate(v) == nil {
		t.Fatal("schema accepted an auth_error in state ready")
	}
}

// gateCLI is a CLI over the fake Codex in mode, set up as runCommand would.
func gateCLI(t *testing.T, mode string) (*CLI, *bytes.Buffer) {
	t.Helper()
	t.Setenv("FAKECODEX_MODE", mode)
	t.Setenv("FAKECODEX_TOKEN_EXP", fmt.Sprint(cliNow.Add(time.Hour).Unix()))
	var errb bytes.Buffer
	c := New(&bytes.Buffer{}, &errb)
	c.Getenv = func(string) string { return "" }
	c.Now = func() time.Time { return cliNow }
	c.Logger = slog.New(slog.DiscardHandler)
	c.globals = defaultGlobals()
	c.globals.CodexBin, c.globals.CodexHome, c.globals.AuthSource = fakeCodex, t.TempDir(), "codex"
	return c, &errb
}

type plainHTTP struct{ rt http.RoundTripper }

func (p plainHTTP) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.URL.Scheme = "http"
	return p.rt.RoundTrip(r)
}

// mintWith returns a gate body that mints a speech secret against srv.
func mintWith(t *testing.T, srv *rtfake.Server) func(context.Context, *auth.Credentials) error {
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	hc, err := openai.NewClient(openai.WithTestOrigin(u.Host), openai.WithBaseTransport(plainHTTP{tr}))
	if err != nil {
		t.Fatal(err)
	}
	return func(ctx context.Context, creds *auth.Credentials) error {
		m := &realtime.Minter{Client: hc, BaseURL: "https://" + u.Host, Creds: creds}
		_, err := m.MintSpeech(ctx, realtime.SessionSpec{Model: "gpt-realtime-2.1", Voice: "cedar", Speed: 1, Effort: "minimal"})
		return err
	}
}

func speakScenario(t *testing.T) rtfake.Scenario {
	sc, err := rtfake.LoadScenario("speak")
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

// accept: AUTH-07#11
func TestRouteDeniedPrintsNoLoginGuidance(t *testing.T) {
	srv := rtfake.NewServer(t, speakScenario(t), rtfake.WithMintErrorDetail(401, "missing_scope", ""))
	c, errb := gateCLI(t, "chatgpt")
	if code := c.withCredentials(mintWith(t, srv)); code != 4 {
		t.Fatalf("code %d, stderr %q", code, errb.String())
	}
	low := strings.ToLower(errb.String())
	if strings.Contains(low, "login") || strings.Contains(low, "sign in") || strings.Contains(low, auth.AuthURL) {
		t.Fatalf("route denial suggests logging in: %q", errb.String())
	}
	if !strings.Contains(errb.String(), "refused the speech-mint route") {
		t.Fatalf("stderr %q", errb.String())
	}
}

// accept: AUTH-07#12
func TestClassificationRunsBeforeAnyRequest(t *testing.T) {
	for _, mode := range []string{"logged-out", "apikey", "rpc-error"} {
		srv := rtfake.NewServer(t, speakScenario(t))
		c, errb := gateCLI(t, mode)
		if code := c.withCredentials(mintWith(t, srv)); code != 3 || !strings.Contains(errb.String(), auth.AuthURL) {
			t.Fatalf("%s: code %d stderr %q", mode, code, errb.String())
		}
		if n := len(srv.MintRequests()); n != 0 {
			t.Fatalf("%s: %d requests reached the API", mode, n)
		}
	}
	srv := rtfake.NewServer(t, speakScenario(t))
	c, errb := gateCLI(t, "chatgpt")
	if code := c.withCredentials(mintWith(t, srv)); code != 0 || len(srv.MintRequests()) != 1 {
		t.Fatalf("ready: code %d, %d requests, stderr %q", code, len(srv.MintRequests()), errb.String())
	}
}

// An interrupt that cuts the login-state probe short exits 130, not 3 with a login guide.
func TestInterruptDuringProbeExits130(t *testing.T) {
	c, errb := gateCLI(t, "slow")
	t.Setenv("FAKECODEX_DELAY_MS", "3000")
	sigs := make(chan os.Signal, 2)
	c.Signals = func() (<-chan os.Signal, func()) { return sigs, func() {} }
	go func() {
		time.Sleep(300 * time.Millisecond) // the probe is waiting on the slow fake
		sigs <- os.Interrupt
		sigs <- os.Interrupt // the second aborts the probe
	}()
	ran := false
	code := c.withCredentials(func(context.Context, *auth.Credentials) error { ran = true; return nil })
	if code != ExitInterrupted || ran || strings.Contains(errb.String(), "More help:") {
		t.Fatalf("code %d, ran %v, stderr %q", code, ran, errb.String())
	}
}
