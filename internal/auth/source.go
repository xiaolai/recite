package auth

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/xiaolai/recite/internal/codexapp"
	"github.com/xiaolai/recite/internal/openai"
)

// Source values of --auth-source.
const (
	SourceAuto  = "auto"
	SourceCodex = "codex"
	SourceFile  = "file"
)

// Context is the invocation context resolved once per run (design §5.1a step 1).
type Context struct {
	CodexBin  string // absolute path; "" when no executable was found
	CodexHome string // the CODEX_HOME in force; "" means Codex's default
	Source    string // codex or file, after auto is resolved
	// MissingBin is the executable the user named (--codex-bin or RECITE_CODEX_BIN) when it
	// was not found, so the guide can point at the path rather than say Codex is missing.
	MissingBin string
}

// ResolveContext resolves the executable, CODEX_HOME and the source (design §5.1a
// steps 1-2): auto uses Codex when its executable exists, otherwise the file source.
func ResolveContext(flagBin, flagHome, source string, getenv func(string) string) (Context, error) {
	ctx := Context{CodexHome: flagHome}
	if ctx.CodexHome == "" {
		ctx.CodexHome = getenv("CODEX_HOME")
	}
	explicit := flagBin != "" || getenv("RECITE_CODEX_BIN") != ""
	bin, err := codexapp.Resolve(flagBin, getenv)
	notFound := errors.Is(err, codexapp.ErrNotFound)
	switch {
	case err != nil && !notFound:
		return Context{}, err
	case notFound && source != SourceFile && (explicit || source == SourceCodex):
		return Context{}, &openai.Error{Class: openai.ClassCodexError, Message: "Codex CLI not found; install it, then run `codex login`", Err: err}
	}
	ctx.CodexBin = bin
	switch source {
	case SourceCodex, SourceFile:
		ctx.Source = source
	case SourceAuto, "":
		ctx.Source = SourceCodex
		if bin == "" {
			ctx.Source = SourceFile
		}
	default:
		return Context{}, fmt.Errorf("auth: unknown source %q", source)
	}
	return ctx, nil
}

// Source is a TokenSource that owns resources released by Close.
type Source interface {
	TokenSource
	io.Closer
}

// NewSource returns the TokenSource for c. timeout 0 means the 10 s default.
func NewSource(c Context, timeout time.Duration, logger *slog.Logger) (Source, error) {
	if c.Source == SourceCodex {
		return NewCodexSource(c.CodexBin, c.CodexHome, timeout, logger), nil
	}
	home := c.CodexHome
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("auth: locate home directory: %w", err)
		}
		home = filepath.Join(h, ".codex")
	}
	return NewFileSource(home), nil
}
