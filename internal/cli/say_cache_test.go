package cli

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/cache"
	"github.com/xiaolai/recite/internal/exitcode"
	rtfake "github.com/xiaolai/recite/internal/realtime/fake"
)

// entries lists the cached entry files under root.
func entries(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if strings.HasSuffix(p, ".json") {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// `say` reads and writes the take cache under cache_dir: an identical second run renders and
// transcribes nothing and writes a byte-identical file; --refresh renders anew; --no-cache
// touches no cache.
// accept: RUN-04#1
func TestSayCacheSecondRunNoSynthCalls(t *testing.T) {
	m := newMultiEnv(t)
	root := m.path("cache")
	m.env = map[string]string{"RECITE_CACHE_DIR": root}
	if r := m.run(threeParagraphs, "-o", "a.wav"); r.code != exitcode.OK {
		t.Fatalf("first: %+v", r)
	}
	renders, stt := len(m.synth.Calls()), m.tr.Calls()
	if renders != 3 || len(entries(t, root)) != 3 {
		t.Fatalf("first run: %d renders, %d entries", renders, len(entries(t, root)))
	}
	if r := m.run(threeParagraphs, "-o", "b.wav"); r.code != exitcode.OK {
		t.Fatalf("second: %+v", r)
	}
	if n := len(m.synth.Calls()) - renders; n != 0 || m.tr.Calls() != stt {
		t.Fatalf("second run: %d renders, %d transcriptions", n, m.tr.Calls()-stt)
	}
	if a, b := readWAV(t, m.path("a.wav")), readWAV(t, m.path("b.wav")); !bytes.Equal(a, b) {
		t.Fatal("the cached run's output differs")
	}
	for _, p := range entries(t, root) {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", p, fi.Mode(), err)
		}
	}
	if fi, err := os.Stat(filepath.Join(root, "takes")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("takes directory: %v %v", fi, err)
	}
	if r := m.run(threeParagraphs, "-o", "c.wav", "--refresh"); r.code != exitcode.OK || len(m.synth.Calls())-renders != 3 {
		t.Fatalf("--refresh: %+v, %d renders", r, len(m.synth.Calls())-renders)
	}
	other := m.path("other")
	m.env["RECITE_CACHE_DIR"] = other
	if r := m.run(threeParagraphs, "-o", "d.wav", "--no-cache"); r.code != exitcode.OK || len(m.synth.Calls())-renders != 6 {
		t.Fatalf("--no-cache: %+v", r)
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatalf("--no-cache created the cache: %v", err)
	}
}

// A SIGINT mid-run exits 130 with no output file, and the takes verified before it are in the
// cache.
// accept: RUN-03#1
func TestSaySIGINTKeepsVerifiedTakesInCache(t *testing.T) {
	m := newMultiEnv(t)
	root := m.path("cache")
	m.env = map[string]string{"RECITE_CACHE_DIR": root}
	m.sigs = make(chan os.Signal, 2)
	m.drain = 50 * time.Millisecond
	m.synth.Hook = func(ctx context.Context, c rtfake.RenderCall) error {
		if c.Request.ChunkID != "c0003" {
			return nil
		}
		// Interrupt once the first two chunks are verified and cached.
		for len(entries(t, root)) < 2 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Millisecond):
			}
		}
		m.sigs <- os.Interrupt
		<-ctx.Done() // still in flight when the drain ends
		return ctx.Err()
	}
	text := threeParagraphs + "\n\nA fourth paragraph.\n\nA fifth paragraph."
	r := m.run(text, "-o", "out.wav", "--verify", "normal", "--jobs", "1")
	if r.code != exitcode.Interrupted {
		t.Fatalf("exit %d, want 130: %s", r.code, r.stderr)
	}
	if _, err := os.Stat(m.path("out.wav")); !os.IsNotExist(err) {
		t.Fatalf("an output file exists: %v", err)
	}
	st, err := cache.Open(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, tk := range m.lastTasks() {
		e, _, ok, _ := st.Get(tk.CacheKey)
		if i < 2 && (!ok || e.Verification.Verdict != cache.VerdictAccepted) {
			t.Errorf("%s verified before the interrupt is not cached", tk.Chunk.ID)
		}
		if i >= 2 && ok {
			t.Errorf("%s was never verified but is cached", tk.Chunk.ID)
		}
	}
}
