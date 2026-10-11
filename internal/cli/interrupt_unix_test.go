//go:build unix

package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/realtime"
	rtfake "github.com/xiaolai/recite/internal/realtime/fake"
)

const (
	sigintHelperEnv    = "RECITE_TEST_SIGINT_HELPER"
	sigintFakeCodexEnv = "RECITE_TEST_SIGINT_FAKECODEX"
)

// cleanups collects the fake server's cleanup outside a test.
type cleanups struct{ fns []func() }

func (c *cleanups) Cleanup(f func()) { c.fns = append(c.fns, f) }

// sigintHelper runs `recite say` in this process against a fake server whose response stalls
// mid-stream. It prints "rendering" when the response is in flight and exits with recite's
// exit code; the parent sends SIGINT in between.
func sigintHelper(dir string) int {
	sc, err := rtfake.LoadScenario("speak")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 99
	}
	var once sync.Once
	stall := func(ctx context.Context, _ time.Duration) {
		once.Do(func() { fmt.Println("rendering") })
		<-ctx.Done() // the response never finishes on its own
	}
	cl := &cleanups{}
	srv := rtfake.NewServer(cl, sc, rtfake.WithDeltaDelay(time.Millisecond, stall))
	defer func() {
		for _, f := range cl.fns {
			f()
		}
	}()
	u, _ := url.Parse(srv.URL)
	tr := &http.Transport{}
	defer tr.CloseIdleConnections()
	hc, err := openai.NewClient(openai.WithTestOrigin(u.Host), openai.WithBaseTransport(plainHTTP{tr}))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 99
	}
	base := "https://" + u.Host
	var errb bytes.Buffer
	c := New(io.Discard, &errb)
	c.Getenv = func(string) string { return "" }
	c.Now = func() time.Time { return cliNow }
	c.DrainTimeout = 200 * time.Millisecond
	c.NewSynth = func(creds *auth.Credentials) (realtime.Synthesizer, error) {
		return &realtime.Synth{Minter: &realtime.Minter{Client: hc, BaseURL: base, Creds: creds}, Client: hc, BaseURL: base}, nil
	}
	code := c.Run([]string{"--codex-bin", os.Getenv(sigintFakeCodexEnv), "--codex-home", filepath.Join(dir, "home"),
		"--auth-source", "codex", "say", "Hello from recite.", "-o", filepath.Join(dir, "out", "h.wav")})
	fmt.Fprint(os.Stderr, errb.String())
	return code
}

// accept: RUN-03#2
// accept: CLI-03#1
func TestSIGINTExits130(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(filepath.Join(dir, "home"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pidfile := filepath.Join(dir, "codex.pids")
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(),
		sigintHelperEnv+"="+dir,
		sigintFakeCodexEnv+"="+fakeCodex,
		"FAKECODEX_MODE=chatgpt",
		"FAKECODEX_PIDFILE="+pidfile,
		"FAKECODEX_TOKEN_EXP="+strconv.FormatInt(cliNow.Add(time.Hour).Unix(), 10),
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "rendering\n" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("helper: %q %v\n%s", line, err, stderr.String())
	}
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 130 {
		t.Fatalf("exit: %v, want 130\n%s", err, stderr.String())
	}
	entries, err := os.ReadDir(outDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("output directory holds %v (%v), want nothing, not even a temp file", entries, err)
	}
	b, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatalf("the fake codex recorded no PID: %v", err)
	}
	pids := strings.Fields(string(b))
	if len(pids) == 0 {
		t.Fatal("no codex child was started")
	}
	for _, p := range pids {
		pid, err := strconv.Atoi(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Errorf("codex child %d outlived recite: %v", pid, err)
		}
	}
}
