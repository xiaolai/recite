package cache_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/cache"
)

// putAt stores a take of n audio bytes whose last access is at.
func putAt(t *testing.T, root string, k string, n int, at time.Time) {
	t.Helper()
	st, err := cache.Open(root, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Put(entry(k), make([]byte, n)); err != nil {
		t.Fatal(err)
	}
}

func keys(items []cache.Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Key)
	}
	return out
}

func TestScanMissingStoreIsEmpty(t *testing.T) {
	root := filepath.Join(t.TempDir(), "none")
	items, err := cache.Scan(root)
	if err != nil || len(items) != 0 {
		t.Fatalf("scan: %v %v", items, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("scan created the store: %v", err)
	}
}

// accept: CLI-08#1
func TestPruneByAccessTimeOrder(t *testing.T) {
	_, root := open(t)
	// Created in key order, but accessed in the order 3, 1, 2 (oldest first).
	putAt(t, root, key(1), 1000, now.Add(-2*time.Hour))
	putAt(t, root, key(2), 1000, now.Add(-1*time.Hour))
	putAt(t, root, key(3), 1000, now.Add(-3*time.Hour))
	// A read refreshes the access time: key 3 becomes the most recent.
	st, err := cache.Open(root, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := st.Get(key(3)); !ok || err != nil {
		t.Fatalf("get: %v %v", ok, err)
	}
	items, err := cache.Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := keys(items), []string{key(1), key(2), key(3)}; !slices.Equal(got, want) {
		t.Fatalf("scan order %v, want %v", got, want)
	}
	one := items[0].Size
	// Room for two takes: the least recently used one goes.
	if got := keys(cache.PrunePlan(items, time.Time{}, 2*one)); !slices.Equal(got, []string{key(1)}) {
		t.Fatalf("max-size plan %v", got)
	}
	// Room for one: the two least recently used go, the fresh hit stays.
	if got := keys(cache.PrunePlan(items, time.Time{}, one)); !slices.Equal(got, []string{key(1), key(2)}) {
		t.Fatalf("max-size plan %v", got)
	}
	// Age alone: only what was last used before the cutoff.
	if got := keys(cache.PrunePlan(items, now.Add(-90*time.Minute), -1)); !slices.Equal(got, []string{key(1)}) {
		t.Fatalf("older-than plan %v", got)
	}
	for _, it := range cache.PrunePlan(items, time.Time{}, one) {
		if err := cache.Remove(root, it.Key); err != nil {
			t.Fatal(err)
		}
	}
	left, err := cache.Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := keys(left); !slices.Equal(got, []string{key(3)}) {
		t.Fatalf("left %v", got)
	}
	if _, _, ok, _ := st.Get(key(1)); ok {
		t.Fatal("a pruned take is still a hit")
	}
	if s := cache.Summarize(left); s.Count != 1 || s.Size != one || !s.Oldest.Equal(now) || !s.Newest.Equal(now) {
		t.Fatalf("summary %+v", s)
	}
}

func TestScanCountsPartialTakes(t *testing.T) {
	st, root := open(t)
	k := key(7)
	sub := filepath.Join(st.Dir(), k[:2])
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	// Audio without its entry (an interrupted Put) and a stray temp file are reclaimable.
	for _, n := range []string{k + ".pcm", "." + k + ".json.tmp-1", "README"} {
		if err := os.WriteFile(filepath.Join(sub, n), []byte("xx"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	items, err := cache.Scan(root)
	if err != nil || len(items) != 1 || items[0].Size != 4 {
		t.Fatalf("scan %+v %v", items, err)
	}
	if err := cache.Remove(root, k); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(sub, "README")); err != nil {
		t.Fatalf("a foreign file was removed: %v", err)
	}
	if items, _ := cache.Scan(root); len(items) != 0 {
		t.Fatalf("left %+v", items)
	}
}
