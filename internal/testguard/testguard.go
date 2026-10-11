// Package testguard stops ordinary tests from reaching real credentials or the real API.
//
// Inside a test binary, unless RECITE_LIVE=1 (set only by `make live`, `make qualify` and
// `make calibrate`), recite must not start a real codex executable, read the real
// ~/.codex, or send a request to OpenAI over the real network. Unit tests use the fakes.
package testguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Active reports whether the guard applies: a test binary that is not a live suite.
func Active() bool {
	return testing.Testing() && os.Getenv("RECITE_LIVE") != "1"
}

// TempPath reports whether path lies under the system temporary directory, where tests
// build their fake executables and homes.
func TempPath(path string) bool {
	tmp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		tmp = os.TempDir()
	}
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		p = path
	}
	for _, root := range []string{tmp, filepath.Clean(os.TempDir())} {
		if p == root || strings.HasPrefix(p, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
