package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/manifest"
)

// failingEncoder installs an ffmpeg that always fails, after rendering has succeeded.
func failingEncoder(t *testing.T) func(string) (string, error) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho encoder exploded >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return func(name string) (string, error) {
		if name == audio.ToolFFmpeg {
			return bin, nil
		}
		return "", exec.ErrNotFound
	}
}

// files lists dir's entries by name.
func files(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// An encoder failure after every chunk rendered still leaves the failed-run manifest, then
// fails the run with the encoder's error; no audio and no temp file is left behind.
func TestSayEncoderFailureWritesFailedManifest(t *testing.T) {
	m := newMultiEnv(t)
	m.look = failingEncoder(t)
	r := m.run(threeParagraphs, "-o", "out.mp3", "--loudness", "none")
	if r.code == exitcode.OK || !strings.Contains(r.stderr, "encoder exploded") {
		t.Fatalf("code %d, stderr %q", r.code, r.stderr)
	}
	man := readManifest(t, m.path("out.mp3.json"))
	if man.Status != manifest.StatusFailed || man.Output != nil {
		t.Fatalf("manifest status %q, output %+v", man.Status, man.Output)
	}
	if got := files(t, m.dir); !slices.Equal(got, []string{"out.mp3.json"}) {
		t.Fatalf("files left: %q", got)
	}
}

// A WAV run assembles through a temp file beside the output and removes it once published.
func TestSayLeavesNoAssemblyFile(t *testing.T) {
	m := newMultiEnv(t)
	r := m.run(threeParagraphs, "-o", "out.wav", "--gap-paragraph", "300ms")
	if r.code != exitcode.OK {
		t.Fatalf("%+v", r)
	}
	if got := files(t, m.dir); !slices.Equal(got, []string{"out.wav", "out.wav.json"}) {
		t.Fatalf("files left: %q", got)
	}
}
