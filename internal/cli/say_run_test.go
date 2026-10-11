package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/realtime"
	rtfake "github.com/xiaolai/recite/internal/realtime/fake"
)

// sayEnv is one end-to-end `say` run against the fake Codex and the fake
// realtime server, in a temporary working directory.
type sayEnv struct {
	t        *testing.T
	dir      string
	mode     string // FAKECODEX_MODE
	srv      *rtfake.Server
	stdin    io.Reader
	tty      bool
	newSynth func(*auth.Credentials) (realtime.Synthesizer, error) // nil = Synth over srv
}

func newSayEnv(t *testing.T, opts ...rtfake.Option) *sayEnv {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	return &sayEnv{t: t, dir: dir, mode: "chatgpt", tty: true, srv: rtfake.NewServer(t, speakScenario(t), opts...)}
}

// testClient is the production client wired to the plain-HTTP fake at rawURL.
func testClient(t *testing.T, rawURL string) (*http.Client, string) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	hc, err := openai.NewClient(openai.WithTestOrigin(u.Host), openai.WithBaseTransport(plainHTTP{tr}))
	if err != nil {
		t.Fatal(err)
	}
	return hc, "https://" + u.Host
}

// fakeSynth is the production Synth wired to the fake server.
func (e *sayEnv) fakeSynth(creds *auth.Credentials) (realtime.Synthesizer, error) {
	hc, base := testClient(e.t, e.srv.URL)
	return &realtime.Synth{Minter: &realtime.Minter{Client: hc, BaseURL: base, Creds: creds}, Client: hc, BaseURL: base}, nil
}

func (e *sayEnv) run(args ...string) result {
	e.t.Helper()
	e.t.Setenv("FAKECODEX_MODE", e.mode)
	e.t.Setenv("FAKECODEX_TOKEN_EXP", fmt.Sprint(cliNow.Add(time.Hour).Unix()))
	var out, errb bytes.Buffer
	c := New(&out, &errb)
	c.Getenv = func(string) string { return "" }
	c.Now = func() time.Time { return cliNow }
	c.Stdin = e.stdin
	c.StdinIsTTY = func() bool { return e.tty }
	c.NewSynth = e.fakeSynth
	if e.newSynth != nil {
		c.NewSynth = e.newSynth
	}
	full := append([]string{"--codex-bin", fakeCodex, "--codex-home", e.t.TempDir(), "--auth-source", "codex", "say"}, args...)
	code := c.Run(full)
	return result{code: code, stdout: out.String(), stderr: errb.String()}
}

func (e *sayEnv) path(name string) string { return filepath.Join(e.dir, name) }

// scenarioPCM is the audio the speak fixture streams.
func scenarioPCM(t *testing.T) []byte {
	t.Helper()
	var pcm []byte
	for _, st := range speakScenario(t).Steps {
		if st.Dir != "server" || st.Type != realtime.TypeAudioDelta {
			continue
		}
		var ev struct {
			Delta string `json:"delta"`
		}
		if err := json.Unmarshal(st.Event, &ev); err != nil {
			t.Fatal(err)
		}
		b, err := base64.StdEncoding.DecodeString(ev.Delta)
		if err != nil {
			t.Fatal(err)
		}
		pcm = append(pcm, b...)
	}
	return pcm
}

func readWAV(t *testing.T, path string) audio.PCM {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	p, err := audio.ReadWAV(f)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return p
}

// sentText is the text of the first response.create the fake received.
func sentText(t *testing.T, srv *rtfake.Server) string {
	t.Helper()
	for _, raw := range srv.ClientEvents() {
		if strings.Contains(string(raw), `"response.create"`) {
			return string(raw)
		}
	}
	t.Fatal("no response.create reached the fake")
	return ""
}

// accept: CLI-03#1
// accept: CLI-02#1
func TestSayExit0WritesWAV(t *testing.T) {
	e := newSayEnv(t)
	r := e.run("Hello from recite.", "-o", "h.wav")
	if r.code != exitcode.OK {
		t.Fatalf("code %d stderr %q", r.code, r.stderr)
	}
	if got := readWAV(t, e.path("h.wav")); !bytes.Equal(got, scenarioPCM(t)) {
		t.Fatalf("WAV holds %d bytes, want the fixture's %d", len(got), len(scenarioPCM(t)))
	}
	if !strings.Contains(sentText(t, e.srv), "Hello from recite.") || !strings.Contains(sentText(t, e.srv), `"c0001"`) {
		t.Fatalf("request: %s", sentText(t, e.srv))
	}
	entries, _ := os.ReadDir(e.dir)
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

// Each flag reaches the component that uses it.
// accept: CLI-02#1
func TestSayFlagsReachComponents(t *testing.T) {
	e := newSayEnv(t)
	if err := os.WriteFile(e.path("chapter.txt"), []byte("Read from\r\nthe file."), 0o600); err != nil {
		t.Fatal(err)
	}
	r := e.run("-f", "chapter.txt", "--voice", "marin", "--speed", "1.25", "--effort", "low", "--model", "gpt-realtime-2")
	if r.code != 0 {
		t.Fatalf("code %d stderr %q", r.code, r.stderr)
	}
	// -f: the default output takes the input stem; the normalised text was spoken.
	readWAV(t, e.path("chapter.wav"))
	if !strings.Contains(sentText(t, e.srv), `Read from\nthe file.`) {
		t.Fatalf("request: %s", sentText(t, e.srv))
	}
	mint := string(e.srv.MintRequests()[0].Body)
	for _, want := range []string{`"marin"`, `1.25`, `"low"`, `"gpt-realtime-2"`} {
		if !strings.Contains(mint, want) {
			t.Errorf("mint body lacks %s: %s", want, mint)
		}
	}

	// Stdin, implicit and with "-"; .pcm output; the timestamped default name.
	e = newSayEnv(t)
	e.stdin, e.tty = strings.NewReader("Piped text."), false
	if r := e.run("-o", "out.pcm"); r.code != 0 {
		t.Fatalf("stdin: %+v", r)
	}
	if b, err := os.ReadFile(e.path("out.pcm")); err != nil || !bytes.Equal(b, scenarioPCM(t)) {
		t.Fatalf("pcm: %v", err)
	}
	e = newSayEnv(t)
	e.stdin = strings.NewReader("Piped text.")
	if r := e.run("-"); r.code != 0 {
		t.Fatalf("dash: %+v", r)
	}
	readWAV(t, e.path("speech-"+cliNow.Format("20060102-150405")+".wav"))

	// --max-input bounds the read.
	e = newSayEnv(t)
	if r := e.run("Hello there", "--max-input", "5"); r.code != 2 || !strings.Contains(r.stderr, "--max-input") {
		t.Fatalf("max-input: %+v", r)
	}
	// Unsupported outputs fail before anything else.
	for _, o := range []string{"x.mp3", "-", "missing-dir/x.wav"} {
		if r := e.run("Hi", "-o", o); r.code != 2 {
			t.Errorf("-o %s: %+v", o, r)
		}
	}
	if n := len(e.srv.MintRequests()); n != 0 {
		t.Fatalf("%d mints for rejected runs", n)
	}
}

// accept: TXT-01#1
// accept: TXT-01#2
// accept: TXT-01#3
// accept: TXT-02#2
// accept: CLI-03#1
func TestSayInputErrorsExit2(t *testing.T) {
	e := newSayEnv(t)
	if err := os.WriteFile(e.path("bad.txt"), []byte("ok\xfe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.path("in.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"Hi", "-f", "in.txt"}, "exactly one input source"},
		{[]string{"-f", "bad.txt"}, "not valid UTF-8 at byte offset 2"},
		{[]string{"hello\x00world"}, "NUL byte at byte offset 5"},
		{[]string{" \n "}, "nothing to speak"},
		{[]string{"bell\x07"}, "control character U+0007"},
		{[]string{"a <<< b"}, "the text contains <<< or >>>"},
		{[]string{strings.Repeat("A long sentence of speech. ", 20)}, "multi-chunk input arrives in M2"},
		{nil, "no input"}, // stdin is a terminal
	} {
		r := e.run(tc.args...)
		if r.code != exitcode.Usage || !strings.Contains(r.stderr, tc.want) {
			t.Errorf("%q: code %d stderr %q, want %q", tc.args, r.code, r.stderr, tc.want)
		}
	}
	// The marker check covers file content too, not only arguments.
	if err := os.WriteFile(e.path("m.txt"), []byte("x >>> y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := e.run("-f", "m.txt"); r.code != 2 || !strings.Contains(r.stderr, "<<< or >>>") {
		t.Errorf("marker in file: %+v", r)
	}
	if n := len(e.srv.MintRequests()); n != 0 {
		t.Fatalf("%d mints for rejected input", n)
	}
}

// accept: CLI-03#1
func TestSayExitCodesFromFakes(t *testing.T) {
	t.Run("1 internal", func(t *testing.T) {
		e := newSayEnv(t)
		e.newSynth = func(*auth.Credentials) (realtime.Synthesizer, error) { return nil, errors.New("synthesizer bug") }
		if r := e.run("Hi"); r.code != exitcode.Internal || !strings.Contains(r.stderr, "synthesizer bug") {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("3 auth", func(t *testing.T) {
		e := newSayEnv(t)
		e.mode = "logged-out"
		r := e.run("Hi")
		if r.code != exitcode.Auth || !strings.Contains(r.stderr, auth.AuthURL) || len(e.srv.MintRequests()) != 0 {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("4 route denied", func(t *testing.T) {
		e := newSayEnv(t, rtfake.WithMintErrorDetail(403, "missing_scope", ""))
		if r := e.run("Hi"); r.code != exitcode.RouteDenied || !strings.Contains(r.stderr, "refused the speech-mint route") {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("7 network", func(t *testing.T) {
		e := newSayEnv(t, rtfake.WithMintError(500))
		r := e.run("Hi")
		if r.code != exitcode.Failure {
			t.Fatalf("%+v", r)
		}
		if _, err := os.Stat(e.path("speech-" + cliNow.Format("20060102-150405") + ".wav")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("output written after a failure: %v", err)
		}
	})
}

// accept: OUT-01#2
func TestExistingTargetFailsBeforeNetwork(t *testing.T) {
	e := newSayEnv(t)
	if err := os.WriteFile(e.path("h.wav"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := e.run("Hi", "-o", "h.wav")
	if r.code != 2 || !strings.Contains(r.stderr, "already exists; pass --force") || len(e.srv.MintRequests()) != 0 {
		t.Fatalf("%+v, %d mints", r, len(e.srv.MintRequests()))
	}
	if b, _ := os.ReadFile(e.path("h.wav")); string(b) != "keep" {
		t.Fatalf("target changed: %q", b)
	}
	if r := e.run("Hi", "-o", "h.wav", "--force"); r.code != 0 || len(readWAV(t, e.path("h.wav"))) == 0 {
		t.Fatalf("--force: %+v", r)
	}
}

// racingSynth creates the target while the run is rendering: after pre-flight,
// before publication.
type racingSynth struct {
	realtime.Synthesizer
	target string
}

func (s racingSynth) Open(ctx context.Context, spec realtime.SessionSpec) (realtime.SpeechSession, error) {
	if err := os.WriteFile(s.target, []byte("someone else's"), 0o600); err != nil {
		return nil, err
	}
	return s.Synthesizer.Open(ctx, spec)
}

// accept: OUT-01#3
func TestLinkPublicationNeverOverwrites(t *testing.T) {
	e := newSayEnv(t)
	target := e.path("h.wav")
	e.newSynth = func(c *auth.Credentials) (realtime.Synthesizer, error) {
		s, err := e.fakeSynth(c)
		return racingSynth{s, target}, err
	}
	r := e.run("Hi", "-o", "h.wav")
	if r.code != 2 || !strings.Contains(r.stderr, "created during the run") {
		t.Fatalf("%+v", r)
	}
	if b, _ := os.ReadFile(target); string(b) != "someone else's" {
		t.Fatalf("target overwritten: %q", b)
	}
	// The run's audio is kept at the temp path the message reports.
	i := strings.Index(r.stderr, "kept at ")
	if i < 0 {
		t.Fatalf("no temp path in %q", r.stderr)
	}
	kept := strings.TrimSpace(r.stderr[i+len("kept at "):])
	if got := readWAV(t, kept); !bytes.Equal(got, scenarioPCM(t)) {
		t.Fatalf("kept audio differs")
	}
}

// accept: OUT-01#4
func TestForceReplacesSymlinkWithoutFollowing(t *testing.T) {
	e := newSayEnv(t)
	dest := e.path("precious.txt")
	if err := os.WriteFile(dest, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dest, e.path("h.wav")); err != nil {
		t.Fatal(err)
	}
	if r := e.run("Hi", "-o", "h.wav", "--force"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	fi, err := os.Lstat(e.path("h.wav"))
	if err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("target is not a regular file: %v %v", fi, err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "precious" {
		t.Fatalf("link destination changed: %q", b)
	}
	readWAV(t, e.path("h.wav"))
}

const publishHelperEnv = "RECITE_TEST_PUBLISH_HELPER"

// publishHelper publishes to target with a writer that stops half way, says
// "ready" and blocks until the parent kills the process.
func publishHelper(target string) int {
	err := publishOutput(target, false, func(w io.Writer) error {
		if _, err := w.Write(make([]byte, 1<<20)); err != nil {
			return err
		}
		fmt.Println("ready")
		select {} // killed here, mid-write
	})
	fmt.Fprintln(os.Stderr, err)
	return 1
}

// accept: OUT-01#1
func TestPublicationSurvivesSIGKILL(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.wav")
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), publishHelperEnv+"="+target)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "ready\n" {
		_ = cmd.Process.Kill()
		t.Fatalf("helper: %q %v", line, err)
	}
	if err := cmd.Process.Kill(); err != nil { // SIGKILL on Unix
		t.Fatal(err)
	}
	_ = cmd.Wait() // the exit status is "killed" by construction
	if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("target exists after SIGKILL mid-write: %v", err)
	}
	// The partial write went to a temp name in the same directory.
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), ".out.wav.") {
		t.Fatalf("directory after kill: %v %v", entries, err)
	}
}

func TestDefaultOutputName(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local)
	for in, want := range map[string]string{
		"":                  "speech-20260102-030405.wav",
		"-":                 "speech-20260102-030405.wav",
		"docs/chapter-1.md": "chapter-1.wav",
		"notes":             "notes.wav",
		"/tmp/a.b.txt":      "a.b.wav",
		".":                 "speech-20260102-030405.wav",
	} {
		if got := defaultOutput(in, now); got != want {
			t.Errorf("defaultOutput(%q) = %q, want %q", in, got, want)
		}
	}
}
