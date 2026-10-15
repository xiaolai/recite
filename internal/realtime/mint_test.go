package realtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/auth"
	codexfake "github.com/xiaolai/recite/internal/codexapp/fake"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/realtime/fake"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	got = append(append([]byte(nil), got...), '\n')
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run go test -update): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("%s mismatch\n--- got\n%s--- want\n%s", path, got, want)
	}
}

func speechSpec(effort realtime.Effort, speed float64) realtime.SessionSpec {
	return realtime.SessionSpec{Model: "gpt-realtime-2.1", Voice: "cedar", Speed: speed, Instructions: "<INSTR>", Effort: effort}
}

// accept: NET-02#1
func TestMintBodiesGolden(t *testing.T) {
	for _, effort := range realtime.Efforts {
		for _, speed := range []float64{0.75, 1.0, 1.5} {
			name := fmt.Sprintf("mint-speech-%s-%g.json", effort, speed)
			body, err := realtime.SpeechSessionBody(speechSpec(effort, speed))
			if err != nil {
				t.Fatal(err)
			}
			golden(t, name, body)
			if !strings.Contains(string(body), `"tools":[]`) {
				t.Errorf("%s: tools is not an empty list", name)
			}
		}
	}
	body, err := realtime.TranscriptionSessionBody("gpt-live-transcribe")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "mint-transcription.json", body)

	for _, bad := range []realtime.SessionSpec{speechSpec("turbo", 1), speechSpec("low", 0), {Effort: "low", Speed: 1}} {
		if _, err := realtime.SpeechSessionBody(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func minter(t *testing.T, srv *fake.Server, creds realtime.Credentials) *realtime.Minter {
	c, base := client(t, srv.URL)
	return &realtime.Minter{Client: c, BaseURL: base, Creds: creds}
}

// accept: NET-02#1
func TestMintThroughFake(t *testing.T) {
	srv := fake.NewServer(t, loadScenario(t, "speak"))
	creds := newStatic()
	m := minter(t, srv, creds)
	sec, err := m.MintSpeech(context.Background(), speechSpec("minimal", 1))
	if err != nil {
		t.Fatal(err)
	}
	if sec.Bearer() != "ek_fixture" || sec.ExpiresAt.Unix() != 1800000000 {
		t.Fatalf("secret exp %v", sec.ExpiresAt)
	}
	if s := fmt.Sprintf("%v %+v %#v", sec, sec, sec); strings.Contains(s, "ek_fixture") {
		t.Fatalf("secret printable: %s", s)
	}
	sec2, err := m.MintTranscription(context.Background(), "gpt-live-transcribe")
	if err != nil || sec2.Bearer() != "ek_fixture" {
		t.Fatalf("transcription mint: %v", err)
	}
	reqs := srv.MintRequests()
	if len(reqs) != 2 {
		t.Fatalf("%d mint requests", len(reqs))
	}
	want, _ := realtime.SpeechSessionBody(speechSpec("minimal", 1))
	if string(reqs[0].Body) != string(want) || reqs[0].Header.Get("Authorization") != "Bearer "+creds.tok.Bearer() {
		t.Fatalf("mint request: %s", reqs[0].Body)
	}
	var sess struct {
		Session struct{ Type string } `json:"session"`
	}
	if err := json.Unmarshal(reqs[1].Body, &sess); err != nil || sess.Session.Type != "transcription" {
		t.Fatalf("transcription body %s", reqs[1].Body)
	}
}

// accept: NET-02#2
func TestMint400NamesParam(t *testing.T) {
	srv := fake.NewServer(t, loadScenario(t, "speak"), fake.WithMintErrorDetail(400, "invalid_value", "session.audio.output.voice"))
	_, err := minter(t, srv, newStatic()).MintSpeech(context.Background(), speechSpec("low", 1))
	wantClass(t, err, openai.ClassInvalidRequest)
	var e *openai.Error
	if errors.As(err, &e); e.Param != "session.audio.output.voice" || !strings.Contains(err.Error(), "session.audio.output.voice") {
		t.Fatalf("param not named: %v", err)
	}
}

// accept: NET-02#3
func TestMint401MissingScopeIsRouteDenied(t *testing.T) {
	srv := fake.NewServer(t, loadScenario(t, "speak"), fake.WithMintErrorDetail(401, "missing_scope", ""))
	_, err := minter(t, srv, newStatic()).MintSpeech(context.Background(), speechSpec("low", 1))
	wantClass(t, err, openai.ClassRouteDenied)
	if openai.ExitCode(err) != 4 {
		t.Fatalf("exit %d", openai.ExitCode(err))
	}
}

// The mint rows of design §5.3 through the fake.
// accept: NET-05#1
func TestMintStatusClasses(t *testing.T) {
	for status, class := range map[int]openai.Class{
		400: openai.ClassInvalidRequest, 401: openai.ClassAuthExpired, 403: openai.ClassRouteDenied,
		429: openai.ClassRateLimited, 500: openai.ClassServer,
	} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv := fake.NewServer(t, loadScenario(t, "speak"), fake.WithMintError(status))
			_, err := minter(t, srv, newStatic()).MintSpeech(context.Background(), speechSpec("low", 1))
			wantClass(t, err, class)
		})
	}
}

var codexBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "realtime-codex-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := 1
	codexBin, err = codexfake.BuildTo(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	} else {
		code = m.Run()
	}
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// codexCreds returns Credentials over the fake Codex in mode, and the request record path.
func codexCreds(t *testing.T, mode string) (*auth.Credentials, string) {
	t.Helper()
	rec := filepath.Join(t.TempDir(), "rec.txt")
	t.Setenv("FAKECODEX_MODE", mode)
	t.Setenv("FAKECODEX_RECORD", rec)
	t.Setenv("FAKECODEX_TOKEN_EXP", "1800000000")
	t.Setenv("FAKECODEX_REFRESHED_EXP", "1900000000")
	st := auth.Probe(context.Background(), auth.ProbeConfig{CodexBin: codexBin, CodexHome: t.TempDir(),
		Source: auth.SourceCodex, Now: func() time.Time { return time.Unix(1_700_000_000, 0) }})
	if st.State != auth.StateReady {
		t.Fatalf("probe: %s %v", st.State, st.Err)
	}
	return st.Creds, rec
}

// accept: AUTH-04#1
func TestMint401RefreshesOnceForEightWorkers(t *testing.T) {
	creds, rec := codexCreds(t, "refresh-ok")
	old, err := creds.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	srv := fake.NewServer(t, loadScenario(t, "speak"), fake.WithMintRejectBearer(old.Bearer()))
	// Every worker takes the old token before any of them mints, so all eight are refused.
	var took sync.WaitGroup
	took.Add(8)
	m := minter(t, srv, barrier{creds, &took})
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range 8 {
		wg.Go(func() { _, errs[i] = m.MintSpeech(context.Background(), speechSpec("low", 1)) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
	}
	b, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), `"refreshToken":true`); n != 1 {
		t.Fatalf("%d refreshes, want 1", n)
	}
	refused := 0
	for _, r := range srv.MintRequests() {
		if r.Header.Get("Authorization") == "Bearer "+old.Bearer() {
			refused++
		}
	}
	if refused != 8 || len(srv.MintRequests()) != 16 {
		t.Fatalf("%d refused of %d mints, want 8 of 16", refused, len(srv.MintRequests()))
	}
}

// barrier holds every Token caller until all workers have taken one.
type barrier struct {
	realtime.Credentials
	wg *sync.WaitGroup
}

func (b barrier) Token(ctx context.Context) (auth.Token, error) {
	tok, err := b.Credentials.Token(ctx)
	b.wg.Done()
	b.wg.Wait()
	return tok, err
}

// accept: AUTH-04#2
func TestMintRefreshFailsExit3(t *testing.T) {
	creds, _ := codexCreds(t, "refresh-fails")
	old, err := creds.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	srv := fake.NewServer(t, loadScenario(t, "speak"), fake.WithMintRejectBearer(old.Bearer()))
	_, err = minter(t, srv, creds).MintSpeech(context.Background(), speechSpec("low", 1))
	// The CLI follows this message with the login guide for the Codex in use
	// (cli.TestRuntimeSignInFailureShowsTheGuide).
	if openai.ExitCode(err) != 3 || !strings.Contains(err.Error(), realtime.MsgSignInRefused) {
		t.Fatalf("err = %v", err)
	}
}

// Two refusals in a row keep the provider's status and code on recite's error, for callers that
// read them with errors.As; only the message is recite's own.
func TestMintRefusedTwiceKeepsTheRefusal(t *testing.T) {
	srv := fake.NewServer(t, loadScenario(t, "speak"), fake.WithMintErrorDetail(401, "invalid_token", ""))
	_, err := minter(t, srv, renewingCreds{}).MintSpeech(context.Background(), speechSpec("low", 1))
	var e *openai.Error
	if !errors.As(err, &e) || e.Message != realtime.MsgSignInRefused || e.Status != 401 || e.Code != "invalid_token" {
		t.Fatalf("err = %#v", e)
	}
}

// renewingCreds renews to a second token, which the server refuses too.
type renewingCreds struct{}

func (renewingCreds) Token(context.Context) (auth.Token, error) {
	return auth.NewToken("eyJcanary.first.canary", time.Unix(1800000000, 0), "", "test"), nil
}

func (renewingCreds) RefreshAfter(context.Context, auth.Token) (auth.Token, error) {
	return auth.NewToken("eyJcanary.second.canary", time.Unix(1800000000, 0), "", "test"), nil
}
