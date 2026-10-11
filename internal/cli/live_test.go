//go:build live

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/openai"
)

// The M1 smoke test: the built binary speaks "Hello" into a WAV, and the test
// itself transcribes the file over HTTP (the STT client is WP-2.5).
// accept: CLI-02#1
func TestLiveSayHello(t *testing.T) {
	if os.Getenv("RECITE_LIVE") == "" {
		t.Skip("set RECITE_LIVE=1 to run against the real service")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "recite")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/recite").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	wav := filepath.Join(dir, "h.wav")
	cmd := exec.Command(bin, "say", "Hello", "-o", wav)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("recite say: %v\n%s", err, stderr.String())
	}
	readWAV(t, wav)

	st := auth.Probe(context.Background(), auth.ProbeConfig{Source: auth.SourceAuto, Getenv: os.Getenv})
	t.Cleanup(func() { _ = st.Close() })
	if st.Creds == nil {
		t.Fatalf("auth not ready: %s", st.State)
	}
	said := transcribe(t, st.Creds, wav)
	if fold(said) != "hello" {
		t.Fatalf("transcript %q, want Hello", said)
	}
}

// transcribe posts path to /v1/audio/transcriptions with gpt-transcribe.
func transcribe(t *testing.T, creds *auth.Credentials, path string) string {
	t.Helper()
	ctx := context.Background()
	tok, err := creds.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("model", "gpt-transcribe"); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	hc, err := openai.NewClient()
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openai.APIBase+"/v1/audio/transcriptions", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.Bearer())
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := openai.CheckResponse(resp, openai.RouteHTTPSTT); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Text
}

var nonWord = regexp.MustCompile(`[\W_]+`)

// fold lower-cases and drops punctuation and spaces.
func fold(s string) string { return nonWord.ReplaceAllString(strings.ToLower(s), "") }
