package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/exitcode"
)

// Two outputs of one run that name the same file are a usage error before any request:
// publishing one would silently replace the other.
func TestSayRejectsOutputCollisions(t *testing.T) {
	for name, args := range map[string][]string{
		"manifest is the audio":        {"-o", "out.wav", "--manifest", "out.wav"},
		"manifest by another spelling": {"-o", "out.wav", "--manifest", "sub/../out.wav"},
		"manifest is the subtitles":    {"-o", "out.wav", "--subtitles", "srt", "--manifest", "out.srt"},
		"manifest through a symlink":   {"-o", "out.wav", "--manifest", "link.json"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newSayEnv(t)
			if err := os.Mkdir(e.path("sub"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(e.path("out.wav"), []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("out.wav", e.path("link.json")); err != nil {
				t.Fatal(err)
			}
			r := e.run(append([]string{"Hello", "--force"}, args...)...)
			if r.code != exitcode.Usage || !strings.Contains(r.stderr, "same file") {
				t.Fatalf("code %d, stderr %q", r.code, r.stderr)
			}
			if n := len(e.srv.MintRequests()); n != 0 {
				t.Fatalf("%d mints before the collision was reported", n)
			}
			if b, _ := os.ReadFile(e.path("out.wav")); string(b) != "old" {
				t.Fatal("out.wav was replaced")
			}
		})
	}
}

// Outputs that do not exist yet still collide when a symlinked parent directory makes two
// spellings name one file: alias -> real, so real/out.wav and alias/out.wav are the same.
func TestSayRejectsCollisionsThroughAliasedParents(t *testing.T) {
	for name, args := range map[string][]string{
		"manifest through an aliased parent":  {"-o", "real/out.wav", "--manifest", "alias/out.wav"},
		"subtitles through an aliased parent": {"-o", "alias/out.wav", "--subtitles", "srt", "--manifest", "real/out.srt"},
		"dotted spelling through the alias":   {"-o", "real/out.wav", "--manifest", "alias/../real/out.wav"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newSayEnv(t)
			if err := os.Mkdir(e.path("real"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("real", e.path("alias")); err != nil {
				t.Fatal(err)
			}
			r := e.run(append([]string{"Hello", "--force"}, args...)...)
			if r.code != exitcode.Usage || !strings.Contains(r.stderr, "same file") {
				t.Fatalf("code %d, stderr %q", r.code, r.stderr)
			}
			if n := len(e.srv.MintRequests()); n != 0 {
				t.Fatalf("%d mints before the collision was reported", n)
			}
			if _, err := os.Lstat(e.path("real/out.wav")); err == nil {
				t.Fatal("real/out.wav was published")
			}
		})
	}
}

// canonicalPath resolves the longest existing ancestor and keeps the missing rest.
func TestCanonicalPathMissingTail(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "real"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	a, err := canonicalPath(filepath.Join(dir, "alias", "new", "deeper", "out.wav"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := canonicalPath(filepath.Join(dir, "real", "new", "deeper", "out.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if a != b || !strings.HasSuffix(a, filepath.Join("real", "new", "deeper", "out.wav")) {
		t.Fatalf("%s != %s", a, b)
	}
}
