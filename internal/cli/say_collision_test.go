package cli

import (
	"os"
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
