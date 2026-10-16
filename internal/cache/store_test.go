package cache_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/cache"
)

var now = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

func open(t *testing.T) (*cache.Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "recite")
	st, err := cache.Open(root, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return st, root
}

func key(n int) string { return fmt.Sprintf("%064x", n+1) }

func entry(k string) cache.Entry {
	return cache.Entry{Key: k, ChunkID: "c0001", Said: "hello", Status: "completed", Score: 1,
		Verification: cache.Verification{Verdict: cache.VerdictAccepted, Score: 1, Guards: map[string]cache.GuardVerdict{
			"said": {Verdict: cache.GuardPass, ContextHash: "h"}}}}
}

func TestStoreRoundTripPrivate(t *testing.T) {
	st, root := open(t)
	k := key(1)
	if err := st.Put(entry(k), []byte("pcm-bytes")); err != nil {
		t.Fatal(err)
	}
	e, pcm, ok, err := st.Get(k)
	if !ok || err != nil || string(pcm) != "pcm-bytes" || e.Said != "hello" || e.Verification.Guards["said"].ContextHash != "h" || !e.CreatedAt.Equal(now) {
		t.Fatalf("get: %+v %q %v %v", e, pcm, ok, err)
	}
	if at, ok := st.Accessed(k); !ok || !at.Equal(now) {
		t.Fatalf("access time %v %v", at, ok)
	}
	err = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		want := os.FileMode(0o600)
		if fi.IsDir() {
			want = 0o700
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s: mode %v, want %v", p, fi.Mode().Perm(), want)
		}
		if strings.Contains(fi.Name(), ".tmp-") {
			t.Errorf("temporary file left: %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStoreCorruptOrPartialIsMiss(t *testing.T) {
	st, _ := open(t)
	if _, _, ok, err := st.Get(key(9)); ok || err != nil {
		t.Fatalf("absent entry: ok %v err %v", ok, err)
	}
	if _, _, ok, err := st.Get("../../etc/passwd"); ok || err == nil {
		t.Fatal("an invalid key was looked up")
	}
	cases := map[string]func(base string){
		"truncated json": func(b string) { must(t, os.WriteFile(b+".json", []byte(`{"schema":`), 0o600)) },
		"tampered audio": func(b string) { must(t, os.WriteFile(b+".pcm", []byte("pcm-bytez"), 0o600)) },
		"short audio":    func(b string) { must(t, os.WriteFile(b+".pcm", []byte("pcm"), 0o600)) },
		"audio missing":  func(b string) { must(t, os.Remove(b+".pcm")) },
		"entry missing":  func(b string) { must(t, os.Remove(b+".json")) },
		"another key":    func(b string) { rewrite(t, b, func(s string) string { return strings.Replace(s, key(2), key(3), 1) }) },
		"another schema": func(b string) {
			rewrite(t, b, func(s string) string { return strings.Replace(s, cache.EntrySchema, "recite/take@0", 1) })
		},
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			if err := st.Put(entry(key(2)), []byte("pcm-bytes")); err != nil {
				t.Fatal(err)
			}
			corrupt(filepath.Join(st.Dir(), key(2)[:2], key(2)))
			if _, _, ok, _ := st.Get(key(2)); ok {
				t.Fatal("a corrupt entry was trusted")
			}
		})
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func rewrite(t *testing.T, base string, f func(string) string) {
	t.Helper()
	b, err := os.ReadFile(base + ".json")
	must(t, err)
	must(t, os.WriteFile(base+".json", []byte(f(string(b))), 0o600))
}

// Concurrent runs writing and reading the same key never observe a mixed entry: every hit
// holds audio matching its own entry.
func TestStoreConcurrentPutGet(t *testing.T) {
	st, _ := open(t)
	k := key(4)
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Go(func() {
			for i := range 20 {
				pcm := bytes.Repeat([]byte{byte(w)}, 100+w)
				e := entry(k)
				e.Said = fmt.Sprintf("writer %d", w)
				if err := st.Put(e, pcm); err != nil {
					t.Errorf("put: %v", err)
					return
				}
				if e, pcm, ok, _ := st.Get(k); ok && (len(pcm) != e.PCMBytes || e.Said != fmt.Sprintf("writer %d", int(pcm[0]))) {
					t.Errorf("iteration %d: entry of %q with audio of writer %d", i, e.Said, pcm[0])
				}
			}
		})
	}
	wg.Wait()
}

func TestOpenRejectsRelativeRoot(t *testing.T) {
	if _, err := cache.Open("relative/dir", nil); err == nil {
		t.Fatal("a relative cache root was accepted")
	}
}

func TestDecide(t *testing.T) {
	v := cache.Verification{Verdict: cache.VerdictAccepted, Score: 0.96, Route: "http", STTModel: "m", CanonVersion: 2, GuardVersion: 1,
		Guards: map[string]cache.GuardVerdict{"said": {Verdict: cache.GuardPass, ContextHash: "a"}, "language": {Verdict: cache.GuardSkipped, ContextHash: "l"}}}
	cur := cache.Current{Accept: 0.95, Route: "http", STTModel: "m", CanonVersion: 2, GuardVersion: 1, Required: map[string]string{"said": "a"}}
	if d := v.Decide(cur); d != cache.ReuseVerified {
		t.Fatalf("matching context: %v", d)
	}
	for name, mut := range map[string]func(*cache.Current){
		"strict threshold":  func(c *cache.Current) { c.Accept = 0.98 },
		"canon version":     func(c *cache.Current) { c.CanonVersion = 3 },
		"guard version":     func(c *cache.Current) { c.GuardVersion = 2 },
		"route":             func(c *cache.Current) { c.Route = "realtime" },
		"stt model":         func(c *cache.Current) { c.STTModel = "n" },
		"context hash":      func(c *cache.Current) { c.Required = map[string]string{"said": "b"} },
		"required, skipped": func(c *cache.Current) { c.Required = map[string]string{"said": "a", "language": "l"} },
		"required, missing": func(c *cache.Current) { c.Required = map[string]string{"said": "a", "content-word": "c"} },
	} {
		c := cur
		mut(&c)
		if d := v.Decide(c); d != cache.Reverify {
			t.Errorf("%s: %v, want re-verify", name, d)
		}
	}
	v.Verdict = cache.VerdictUnverified
	if d := v.Decide(cur); d != cache.Reverify {
		t.Errorf("unverified: %v", d)
	}
	cur.AllowUnverified = true
	if d := v.Decide(cur); d != cache.ReuseUnverified {
		t.Errorf("unverified, allowed: %v", d)
	}
}

// Peek validates like Get but records no access, so counting hits never changes pruning.
func TestPeekRecordsNoAccess(t *testing.T) {
	clock := now
	st, err := cache.Open(filepath.Join(t.TempDir(), "recite"), func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	k := key(7)
	if err := st.Put(entry(k), []byte("pcm-bytes")); err != nil {
		t.Fatal(err)
	}
	put, _ := st.Accessed(k)
	clock = now.Add(time.Hour)
	if ok, err := st.Peek(k); !ok || err != nil {
		t.Fatalf("peek hit: %v %v", ok, err)
	}
	if at, _ := st.Accessed(k); !at.Equal(put) {
		t.Fatalf("peek moved the access from %v to %v", put, at)
	}
	if _, _, ok, _ := st.Get(k); !ok {
		t.Fatal("get missed")
	}
	if at, _ := st.Accessed(k); !at.Equal(clock) {
		t.Fatalf("get recorded %v, want %v", at, clock)
	}
	if ok, err := st.Peek(key(8)); ok || err != nil {
		t.Fatalf("peek miss: %v %v", ok, err)
	}
	if err := os.WriteFile(filepath.Join(st.Dir(), k[:2], k+".pcm"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.Peek(k); ok || err == nil {
		t.Fatalf("peek accepted tampered audio: %v %v", ok, err)
	}
}

// The store writes, reads and removes through the takes directory it checked at Open: once the
// cache root is replaced by a symlink, nothing reaches the link's destination (audit: every
// operation reopened the full path).
// accept: AUTH-05#8
func TestStoreStaysInTheCheckedDirectory(t *testing.T) {
	st, root := open(t)
	defer func() { _ = st.Close() }()
	base := filepath.Dir(root)
	evil := filepath.Join(base, "evil")
	if err := os.MkdirAll(filepath.Join(evil, "takes"), 0o700); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(base, "moved")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(evil, root); err != nil {
		t.Fatal(err)
	}
	k := key(7)
	if err := st.Put(entry(k), []byte("pcm")); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := st.Get(k); !ok || err != nil {
		t.Fatalf("Get after the swap: ok %v, err %v", ok, err)
	}
	if _, err := os.Stat(filepath.Join(moved, "takes", k[:2], k+".json")); err != nil {
		t.Fatalf("the entry is not in the checked directory: %v", err)
	}
	if ents, _ := os.ReadDir(filepath.Join(evil, "takes")); len(ents) != 0 {
		t.Fatalf("the link's destination was written: %v", ents)
	}
	items, err := st.Scan()
	if err != nil || len(items) != 1 || items[0].Key != k {
		t.Fatalf("Scan = %v, %v", items, err)
	}
	if err := st.RemoveKeys([]string{k}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(moved, "takes", k[:2], k+".json")); !os.IsNotExist(err) {
		t.Fatalf("the entry survived its removal: %v", err)
	}
}
