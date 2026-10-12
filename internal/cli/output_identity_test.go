//go:build unix

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Existing outputs sharing size and modification time are told apart by device and inode in
// one map lookup each: distinct files pass and a hard link to an earlier output collides.
func TestDistinctOutputsByFileIdentity(t *testing.T) {
	dir := t.TempDir()
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	const n = 500
	paths := make([]string, n)
	for i := range paths {
		paths[i] = filepath.Join(dir, fmt.Sprintf("part-%03d.wav", i))
		if err := os.WriteFile(paths[i], []byte("same"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(paths[i], stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := distinctOutputs(paths...); err != nil {
		t.Fatalf("%d distinct files with one size and mtime: %v", n, err)
	}

	link := filepath.Join(dir, "link.srt")
	if err := os.Link(paths[n/2], link); err != nil {
		t.Fatal(err)
	}
	err := distinctOutputs(append(append([]string{}, paths...), link)...)
	if err == nil || !strings.Contains(err.Error(), "same file") {
		t.Fatalf("hard link to an output: %v, want a same-file error", err)
	}
}
