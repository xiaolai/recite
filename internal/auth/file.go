package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/xiaolai/recite/internal/openai"
)

// maxAuthFile caps how much of auth.json is read (AUTH-02).
const maxAuthFile = 64 << 10

// FileSource reads $CODEX_HOME/auth.json read-only (AUTH-02). It cannot refresh.
type FileSource struct {
	path string

	mu     sync.Mutex
	cached *Token
}

// NewFileSource returns a source reading home/auth.json.
func NewFileSource(home string) *FileSource {
	return &FileSource{path: filepath.Join(home, "auth.json")}
}

// authFile names only the two fields recite uses. OPENAI_API_KEY is deliberately
// absent, so the decoder skips it and it never reaches a Go value (AUTH-02#3).
type authFile struct {
	AuthMode *string `json:"auth_mode"`
	Tokens   *struct {
		AccessToken string `json:"access_token"`
	} `json:"tokens"`
}

// Token returns the cached token, reading the file on first use.
func (s *FileSource) Token(_ context.Context) (Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil {
		return *s.cached, nil
	}
	tok, err := s.read()
	if err != nil {
		return Token{}, err
	}
	s.cached = &tok
	return tok, nil
}

func (s *FileSource) read() (Token, error) {
	f, err := os.OpenFile(s.path, os.O_RDONLY, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return Token{}, authModeError(fmt.Sprintf("Codex is not signed in: %s does not exist; run `codex login`", s.path))
	}
	if err != nil {
		return Token{}, &openai.Error{Class: openai.ClassCodexError, Message: "cannot open " + s.path, Err: err}
	}
	defer func() { _ = f.Close() }()
	buf, err := io.ReadAll(io.LimitReader(f, maxAuthFile+1))
	// The raw bytes hold every field, including OPENAI_API_KEY; wipe them once decoded.
	defer clear(buf)
	if err != nil {
		return Token{}, &openai.Error{Class: openai.ClassCodexError, Message: "cannot read " + s.path, Err: err}
	}
	if len(buf) > maxAuthFile {
		return Token{}, codexError(fmt.Sprintf("%s is larger than 64 KiB; it is not a Codex sign-in file", s.path))
	}
	var af authFile
	if err := json.Unmarshal(buf, &af); err != nil {
		return Token{}, codexError(fmt.Sprintf("%s is not valid JSON; run `codex login` to rewrite it", s.path))
	}
	mode := ""
	if af.AuthMode != nil {
		mode = *af.AuthMode
	}
	if err := guard(mode); err != nil {
		return Token{}, err
	}
	if af.Tokens == nil || af.Tokens.AccessToken == "" {
		return Token{}, codexError(fmt.Sprintf("%s has a ChatGPT sign-in without an access token; run `codex login`", s.path))
	}
	return newBearer(af.Tokens.AccessToken, "file")
}

// Refresh is impossible for the file source: only Codex may rotate tokens (design D2).
func (s *FileSource) Refresh(_ context.Context) (Token, error) {
	return Token{}, &openai.Error{Class: openai.ClassAuthExpired, Message: "the auth.json source cannot refresh; run `codex login`"}
}

// Close is a no-op, so both sources share one lifecycle.
func (s *FileSource) Close() error { return nil }

func (s *FileSource) String() string { return "auth.FileSource{" + s.path + "}" }

// GoString redacts like String.
func (s *FileSource) GoString() string { return s.String() }

// Format prints only the path, never the cached token.
func (s *FileSource) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(s.String())) }
