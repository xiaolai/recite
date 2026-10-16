package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/cache"
	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/realtime"
	rtfake "github.com/xiaolai/recite/internal/realtime/fake"
	"github.com/xiaolai/recite/internal/stt"
)

// doctorEnv runs `recite doctor` against the fake Codex and the fake realtime server.
type doctorEnv struct {
	t         *testing.T
	mode      string
	srv       *rtfake.Server
	wsRefused bool // the WebSocket dial is refused, deterministically, by an injected dialer
	noCodex   bool
	missing   map[string]bool // encoders LookPath does not find
	env       map[string]string
	tr        stt.Transcriber      // when set, the --deep check transcribes with it
	synth     realtime.Synthesizer // when set, replaces the fake realtime server
	stdout    io.Writer            // when set, doctor writes its report here
}

func newDoctorEnv(t *testing.T, opts ...rtfake.Option) *doctorEnv {
	dir := t.TempDir()
	return &doctorEnv{t: t, mode: "chatgpt", srv: rtfake.NewServer(t, speakScenario(t), opts...), missing: map[string]bool{},
		env: map[string]string{"RECITE_CACHE_DIR": filepath.Join(dir, "cache"), "XDG_STATE_HOME": filepath.Join(dir, "state")}}
}

func (e *doctorEnv) run(args ...string) result {
	e.t.Helper()
	e.t.Setenv("FAKECODEX_MODE", e.mode)
	e.t.Setenv("FAKECODEX_TOKEN_EXP", fmt.Sprint(cliNow.Add(2*time.Hour).Unix()))
	var out, errb bytes.Buffer
	c := New(&out, &errb)
	if e.stdout != nil {
		c.Stdout = e.stdout
	}
	c.Getenv = func(k string) string { return e.env[k] }
	c.Now = func() time.Time { return cliNow }
	c.LookPath = func(name string) (string, error) {
		if e.missing[name] {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + name, nil
	}
	c.NewSynth = func(creds *auth.Credentials) (realtime.Synthesizer, error) {
		if e.synth != nil {
			return e.synth, nil
		}
		hc, base := testClient(e.t, e.srv.URL)
		s := &realtime.Synth{Minter: &realtime.Minter{Client: hc, BaseURL: base, Creds: creds}, Client: hc, BaseURL: base}
		if e.wsRefused {
			s.Client = refusingClient(e.t, e.srv.URL)
		}
		return s, nil
	}
	if e.tr != nil {
		c.NewTranscriber = func(*auth.Credentials) (stt.Transcriber, func() error, error) {
			return e.tr, func() error { return nil }, nil
		}
	}
	full := []string{"--codex-bin", fakeCodex, "--codex-home", e.t.TempDir(), "--auth-source", "codex"}
	if e.noCodex {
		e.t.Setenv("PATH", e.t.TempDir())
		full = []string{"--auth-source", "codex"}
	}
	code := c.Run(append(append(full, "doctor"), args...))
	return result{code: code, stdout: out.String(), stderr: errb.String()}
}

// line returns doctor's line for check, failing when there is none.
func line(t *testing.T, stdout, check string) string {
	t.Helper()
	for _, l := range strings.Split(stdout, "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 && f[1] == check && (f[0] == checkOK || f[0] == checkWarn || f[0] == checkFail) {
			return l
		}
	}
	t.Fatalf("no %s line in:\n%s", check, stdout)
	return ""
}

// refusingClient is the production client whose every dial fails with ECONNREFUSED. The
// refusal is injected rather than produced by a released port, which another listener could
// claim before the dial.
func refusingClient(t *testing.T, rawURL string) *http.Client {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	tr := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	}}
	hc, err := openai.NewClient(openai.WithTestOrigin(u.Host), openai.WithBaseTransport(plainHTTP{tr}))
	if err != nil {
		t.Fatal(err)
	}
	return hc
}

func TestDoctorAllOK(t *testing.T) {
	e := newDoctorEnv(t)
	r := e.run()
	if r.code != exitcode.OK {
		t.Fatalf("code %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	for _, check := range []string{"codex", "auth", "plan", "route", "ffmpeg", "cache", "state"} {
		if l := line(t, r.stdout, check); !strings.HasPrefix(l, "ok") {
			t.Errorf("%s: %q", check, l)
		}
	}
	if !strings.Contains(line(t, r.stdout, "codex"), fakeCodex+" (version 0.162.1)") ||
		!strings.Contains(line(t, r.stdout, "plan"), "plan pro") {
		t.Fatalf("stdout:\n%s", r.stdout)
	}
	if n := len(e.srv.MintRequests()); n != 1 {
		t.Fatalf("%d mints, want 1", n)
	}
	if strings.Contains(r.stdout, "deep") {
		t.Fatal("rendered without --deep")
	}
	for _, s := range secrets {
		if strings.Contains(r.stdout+r.stderr, s) {
			t.Fatalf("printed %q", s)
		}
	}
}

// accept: CLI-05#1
func TestDoctorFailureModes(t *testing.T) {
	for _, tc := range []struct {
		name, check, status, want string
		code                      int
		setup                     func(*doctorEnv)
	}{
		{"no codex", "codex", checkFail, "npm install -g @openai/codex", exitcode.Auth, func(e *doctorEnv) { e.noCodex = true }},
		{"api-key auth", "auth", checkFail, "api-key", exitcode.Auth, func(e *doctorEnv) { e.mode = "apikey" }},
		{"mint 403", "route", checkFail, "HTTP 403", exitcode.RouteDenied, func(e *doctorEnv) {
			e.srv = rtfake.NewServer(t, speakScenario(t), rtfake.WithMintError(403))
		}},
		{"WebSocket refused", "route", checkFail, "connection refused", exitcode.Failure, func(e *doctorEnv) { e.wsRefused = true }},
		// The upgrade alone is not a working session: doctor waits for session.created.
		{"error before session.created", "route", checkFail, "could not be opened", exitcode.Failure, func(e *doctorEnv) {
			e.srv = rtfake.NewServer(t, speakScenario(t), rtfake.WithErrorEvent(0, "server_error"))
		}},
		{"closed before session.created", "route", checkFail, "could not be opened", exitcode.Failure, func(e *doctorEnv) {
			e.srv = rtfake.NewServer(t, speakScenario(t), rtfake.WithCloseAfter(0))
		}},
		// Design §12.4 check 6: the encoders warn only.
		{"no ffmpeg", "ffmpeg", checkWarn, "not found", exitcode.OK, func(e *doctorEnv) { e.missing["ffmpeg"] = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newDoctorEnv(t)
			tc.setup(e)
			r := e.run()
			l := line(t, r.stdout, tc.check)
			if !strings.HasPrefix(l, tc.status) || !strings.Contains(r.stdout, tc.want) {
				t.Fatalf("%s line %q, want %s with %q:\n%s", tc.check, l, tc.status, tc.want, r.stdout)
			}
			if tc.status != checkOK && !strings.Contains(r.stdout, "fix: ") {
				t.Fatalf("no fix line:\n%s", r.stdout)
			}
			if r.code != tc.code {
				t.Fatalf("code %d, want %d:\n%s", r.code, tc.code, r.stdout)
			}
		})
	}
}

// The route line is recite's wording: the provider's message (an error event's, a mint error
// body's) is diagnostic only and never printed (audit).
// accept: NET-05#2
func TestDoctorRouteLineOmitsProviderMessage(t *testing.T) {
	for _, opt := range []rtfake.Option{rtfake.WithErrorEvent(0, "server_error"), rtfake.WithMintError(403)} {
		e := newDoctorEnv(t)
		e.srv = rtfake.NewServer(t, speakScenario(t), opt)
		r := e.run()
		l := line(t, r.stdout, "route")
		if !strings.HasPrefix(l, checkFail) || strings.Contains(r.stdout, "injected by fake") || strings.Contains(r.stdout, "fake mint error") {
			t.Fatalf("route line %q:\n%s", l, r.stdout)
		}
	}
}

// accept: CLI-05#2
func TestDoctorExitIsMostSevereFailure(t *testing.T) {
	// The route is denied (4) and the state directory is not writable (2): 4 wins.
	e := newDoctorEnv(t, rtfake.WithMintError(403))
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	e.env["XDG_STATE_HOME"] = filepath.Join(blocker, "under-a-file")
	e.missing["ffmpeg"] = true
	r := e.run()
	if !strings.HasPrefix(line(t, r.stdout, "state"), checkFail) || !strings.HasPrefix(line(t, r.stdout, "route"), checkFail) {
		t.Fatalf("stdout:\n%s", r.stdout)
	}
	if r.code != exitcode.RouteDenied {
		t.Fatalf("code %d, want %d", r.code, exitcode.RouteDenied)
	}
	// Only the unwritable directory fails: 2.
	e = newDoctorEnv(t)
	e.env["XDG_STATE_HOME"] = filepath.Join(blocker, "under-a-file")
	if r := e.run(); r.code != exitcode.Usage {
		t.Fatalf("code %d, want 2:\n%s", r.code, r.stdout)
	}
}

func TestDoctorJSONValidates(t *testing.T) {
	sch := eventsSchema(t)
	e := newDoctorEnv(t, rtfake.WithMintError(403))
	r := e.run("--json")
	if r.code != exitcode.RouteDenied || r.stderr != "" {
		t.Fatalf("code %d stderr %q", r.code, r.stderr)
	}
	lines := strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n")
	if len(lines) < 7 {
		t.Fatalf("%d lines:\n%s", len(lines), r.stdout)
	}
	for _, l := range lines {
		validate(t, sch, l)
	}
}

func TestDoctorSkipsRouteWhenNotSignedIn(t *testing.T) {
	e := newDoctorEnv(t)
	e.mode = "logged-out"
	r := e.run("--deep")
	if r.code != exitcode.Auth || len(e.srv.MintRequests()) != 0 {
		t.Fatalf("code %d, %d mints:\n%s", r.code, len(e.srv.MintRequests()), r.stdout)
	}
	for _, check := range []string{"plan", "route", "deep"} {
		if l := line(t, r.stdout, check); !strings.HasPrefix(l, checkWarn) || !strings.Contains(l, "not checked") {
			t.Fatalf("%s: %q", check, l)
		}
	}
}

// downSTT is a transcriber whose service is unreachable.
type downSTT struct{}

func (downSTT) Transcribe(context.Context, []byte, stt.Hint) (stt.Transcript, error) {
	return stt.Transcript{}, errors.New("stt unreachable")
}

// With verification off (the default, D23) say never transcribes, so an unreachable STT in
// doctor --deep warns and exits 0; with verify.mode normal it still fails.
// accept: CLI-05#3
func TestDoctorDeepSTTDownWarnsWhenVerifyOff(t *testing.T) {
	e := newDoctorEnv(t)
	e.tr = downSTT{}
	e.synth = rtfake.NewScriptedSynthesizer().On("doctor", rtfake.Pass(time.Second))
	r := e.run("--deep")
	if l := line(t, r.stdout, "deep"); !strings.HasPrefix(l, checkWarn) || !strings.Contains(l, "transcription failed") {
		t.Fatalf("deep line %q:\n%s", l, r.stdout)
	}
	if r.code != exitcode.OK {
		t.Fatalf("code %d, want 0:\n%s", r.code, r.stdout)
	}
	e = newDoctorEnv(t)
	e.tr = downSTT{}
	e.synth = rtfake.NewScriptedSynthesizer().On("doctor", rtfake.Pass(time.Second))
	r = e.run("--deep", "--config", writeConfig(t, "[verify]\nmode = \"normal\"\n"))
	if l := line(t, r.stdout, "deep"); !strings.HasPrefix(l, checkFail) || r.code == exitcode.OK {
		t.Fatalf("verify normal: code %d, deep line %q", r.code, l)
	}
}

// A take that is not usable audio fails doctor --deep in every verification mode: the status,
// empty and duration guards run before anything counts as an optional STT problem.
func TestDoctorDeepRejectsBadTake(t *testing.T) {
	for name, o := range map[string]rtfake.Outcome{
		"incomplete": {Echo: true, Duration: time.Second, Status: realtime.StatusIncomplete},
		"failed":     {Echo: true, Duration: time.Second, Status: realtime.StatusFailed},
		"empty":      {Echo: true, Duration: 0},
		"too long":   {Echo: true, Duration: 10 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			e := newDoctorEnv(t)
			e.tr = downSTT{} // an STT failure must not be what decides the line
			e.synth = rtfake.NewScriptedSynthesizer().On("doctor", o)
			r := e.run("--deep")
			l := line(t, r.stdout, "deep")
			if !strings.HasPrefix(l, checkFail) || r.code == exitcode.OK || strings.Contains(l, "transcription") {
				t.Fatalf("code %d, deep line %q", r.code, l)
			}
		})
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

// A report doctor could not write is a failure, not a clean exit.
func TestDoctorReportsWriteFailure(t *testing.T) {
	e := newDoctorEnv(t)
	e.stdout = failingWriter{}
	r := e.run()
	if r.code == exitcode.OK || !strings.Contains(r.stderr, "disk full") {
		t.Fatalf("code %d, stderr %q", r.code, r.stderr)
	}
}

// The auth fix is the whole guide: logout, then login, and the warning that it changes how Codex
// itself signs in, never the logout alone.
func TestDoctorAuthFixKeepsTheWholeGuide(t *testing.T) {
	e := newDoctorEnv(t)
	e.mode = "apikey"
	r := e.run()
	for _, want := range []string{" logout; ", " login; Warning: this changes how Codex itself signs in."} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("doctor output lacks %q:\n%s", want, r.stdout)
		}
	}
}

// A symlinked cache directory fails doctor, as it fails the cache itself (fsx.MkdirPrivate).
func TestDoctorRejectsSymlinkedCacheDir(t *testing.T) {
	e := newDoctorEnv(t)
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "cache-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	e.env["RECITE_CACHE_DIR"] = link
	r := e.run()
	if l := line(t, r.stdout, "cache"); !strings.HasPrefix(l, checkFail) || !strings.Contains(l, "symlink") {
		t.Fatalf("cache line %q, want a failure naming the symlink", l)
	}
	if _, err := cache.Open(link, nil); err == nil {
		t.Fatal("the cache accepted the symlinked directory too; the test no longer pins the shared rule")
	}
}

// The auth line names what Codex actually did, not only the state.
func TestDoctorAuthLineNamesTheCause(t *testing.T) {
	e := newDoctorEnv(t)
	e.mode = "rpc-error"
	r := e.run()
	l := line(t, r.stdout, "auth")
	if !strings.HasPrefix(l, checkFail) || !strings.Contains(l, "-32000") {
		t.Fatalf("auth line %q, want the JSON-RPC failure", l)
	}
}

// A sign-in refused at the mint after the probe passed gets the login guide, not a network hint.
func TestDoctorRouteSignInFailureGetsTheGuide(t *testing.T) {
	e := newDoctorEnv(t)
	e.srv = rtfake.NewServer(t, speakScenario(t), rtfake.WithMintError(401))
	r := e.run()
	l := line(t, r.stdout, "route")
	if !strings.HasPrefix(l, checkFail) || strings.Contains(r.stdout, "check the network connection") ||
		!strings.Contains(r.stdout, auth.ShellQuote(fakeCodex)+" login") {
		t.Fatalf("route line %q:\n%s", l, r.stdout)
	}
}

// A config error fails the cache line and still checks the state directory.
func TestDoctorConfigErrorStillChecksState(t *testing.T) {
	e := newDoctorEnv(t)
	e.env["RECITE_JOBS"] = "0"
	r := e.run()
	if l := line(t, r.stdout, "cache"); !strings.HasPrefix(l, checkFail) {
		t.Fatalf("cache line %q", l)
	}
	if l := line(t, r.stdout, "state"); !strings.HasPrefix(l, checkOK) {
		t.Fatalf("state line %q", l)
	}
}

// An omitted ordinaryUsageAllowed is not a denial: doctor reports the plan as ok, and only an
// explicit false is the "not allowed" warning.
func TestDoctorPlanOmittedPermissionIsNotADenial(t *testing.T) {
	for _, tc := range []struct{ usage, status string }{{"omit", checkOK}, {"false", checkWarn}} {
		t.Setenv("FAKECODEX_ORDINARY_USAGE", tc.usage)
		r := newDoctorEnv(t).run()
		if l := line(t, r.stdout, "plan"); !strings.HasPrefix(l, tc.status) {
			t.Fatalf("ordinaryUsageAllowed %s: plan line %q", tc.usage, l)
		}
	}
}

// A stale --codex-bin is named, with the override to fix, not only "not found" and an install
// command that cannot repair it.
func TestDoctorNamesTheMissingOverride(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "no-such", "codex")
	var out, errb bytes.Buffer
	c := New(&out, &errb)
	env := map[string]string{"RECITE_CACHE_DIR": filepath.Join(dir, "cache"), "XDG_STATE_HOME": filepath.Join(dir, "state")}
	c.Getenv = func(k string) string { return env[k] }
	c.Now = func() time.Time { return cliNow }
	c.Run([]string{"--codex-bin", missing, "--codex-home", t.TempDir(), "--auth-source", "codex", "doctor"})
	l := line(t, out.String(), "codex")
	if !strings.Contains(l, missing) || !strings.Contains(out.String(), "--codex-bin") {
		t.Fatalf("codex check:\n%s", out.String())
	}
}
