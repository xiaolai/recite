package auth

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/logx"
	"github.com/xiaolai/recite/internal/openai"
)

const fileKeyCanary = "sk-canary-filekey-opaque"

func writeAuth(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func chatgptFile(exp int64) string {
	return fmt.Sprintf(`{"OPENAI_API_KEY":%q,"auth_mode":"chatgpt","tokens":{"access_token":%q,"refresh_token":"rt-canary"}}`,
		fileKeyCanary, jwt(exp))
}

// accept: AUTH-02#1
func TestFileSourceReadOnlyDirectory(t *testing.T) {
	dir := writeAuth(t, chatgptFile(1800000000))
	path := filepath.Join(dir, "auth.json")
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewFileSource(dir)
	tok, err := s.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok.Expires.Unix() != 1800000000 || tok.Source != "file" {
		t.Fatalf("token exp %v source %q", tok.Expires, tok.Source)
	}
	after, err := os.Stat(path)
	if err != nil || !after.ModTime().Equal(before.ModTime()) || after.Mode() != before.Mode() {
		t.Fatal("auth.json was modified")
	}
	if _, err := s.Refresh(context.Background()); openai.ExitCode(err) != 3 {
		t.Fatalf("Refresh = %v", err)
	}
}

// accept: AUTH-02#2
func TestFileSourceOversizedOrMalformed(t *testing.T) {
	big := `{"auth_mode":"chatgpt","pad":"` + strings.Repeat("x", 64<<10) + `"}`
	cases := map[string]string{
		"oversized": big,
		"malformed": `{"auth_mode":"chatgpt",`,
		"no token":  `{"auth_mode":"chatgpt","tokens":{}}`,
		"bad jwt":   `{"auth_mode":"chatgpt","tokens":{"access_token":"not-a-jwt"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewFileSource(writeAuth(t, body)).Token(context.Background())
			if openai.ExitCode(err) != 3 {
				t.Fatalf("err = %v, want exit 3", err)
			}
			if name == "oversized" && !strings.Contains(err.Error(), "larger than 64 KiB") {
				t.Fatalf("unclear message: %v", err)
			}
			if name == "malformed" && !strings.Contains(err.Error(), "not valid JSON") {
				t.Fatalf("unclear message: %v", err)
			}
		})
	}
	// Exactly at the cap is accepted.
	body := chatgptFile(1800000000)
	body = body[:len(body)-1] + `,"pad":"` + strings.Repeat("x", 64<<10-len(body)-9) + `"}`
	if len(body) != 64<<10 {
		t.Fatalf("fixture is %d bytes", len(body))
	}
	if _, err := NewFileSource(writeAuth(t, body)).Token(context.Background()); err != nil {
		t.Fatalf("64 KiB file refused: %v", err)
	}
}

func TestFileSourceGuard(t *testing.T) {
	apikey := fmt.Sprintf(`{"OPENAI_API_KEY":%q,"auth_mode":"apikey"}`, fileKeyCanary)
	_, err := NewFileSource(writeAuth(t, apikey)).Token(context.Background())
	if e := wantClass(t, err, openai.ClassAuthMode); e.Message != MsgAPIKey {
		t.Fatalf("message = %q", e.Message)
	}
	_, err = NewFileSource(t.TempDir()).Token(context.Background())
	_ = wantClass(t, err, openai.ClassAuthMode)
}

// accept: AUTH-02#3
func TestFileSourceNeverReadsAPIKey(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(logx.NewHandler(slog.NewTextHandler(&logs, nil)))
	for _, body := range []string{chatgptFile(1800000000), fmt.Sprintf(`{"OPENAI_API_KEY":%q,"auth_mode":"apikey"}`, fileKeyCanary)} {
		s := NewFileSource(writeAuth(t, body))
		tok, err := s.Token(context.Background())
		logger.Error("token", "tok", tok, "src", s, "err", err)
		dumps := fmt.Sprintf("%v %+v %#v %s | %v %+v %#v | %v", tok, tok, tok, tok, s, s, s, err)
		for _, out := range []string{dumps, logs.String(), tok.Bearer()} {
			if strings.Contains(out, fileKeyCanary) {
				t.Fatalf("OPENAI_API_KEY canary surfaced: %q", out)
			}
		}
	}
	// The env variable is not consulted either.
	t.Setenv("OPENAI_API_KEY", fileKeyCanary)
	if _, err := NewFileSource(t.TempDir()).Token(context.Background()); openai.ExitCode(err) != 3 {
		t.Fatalf("missing auth.json with OPENAI_API_KEY set: %v", err)
	}
}
