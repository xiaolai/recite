package cache

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Item is one take on disk, as `cache ls` and `cache prune` see it: every file named after
// its key (audio, entry, access index), whether or not the entry is complete.
type Item struct {
	Key      string
	Size     int64     // bytes of every file of the take
	Accessed time.Time // the recorded access, else the newest file modification time
}

// Stats summarises the store for `cache ls`.
type Stats struct {
	Count          int
	Size           int64
	Oldest, Newest time.Time // by last access; zero when the store is empty
}

// TakesDir is the takes directory under root, the same path Open uses.
func TakesDir(root string) string { return filepath.Join(root, "takes") }

// Scan lists the takes under root without creating anything: a missing store is empty.
func Scan(root string) ([]Item, error) {
	s, ok, err := OpenExisting(root)
	if err != nil || !ok {
		return nil, err
	}
	defer func() { _ = s.Close() }()
	return s.Scan()
}

// Scan lists the store's takes. Stray temporary files of an interrupted write are counted with
// their take, so pruning and clearing reclaim them too. Items are sorted by access time, oldest
// first, then by key. The walk is relative to the store's directory, which it never leaves.
func (s *Store) Scan() ([]Item, error) {
	byKey := map[string]*Item{}
	err := fs.WalkDir(s.dir.Root().FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		k := fileKey(d.Name())
		if k == "" {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		it := byKey[k]
		if it == nil {
			it = &Item{Key: k}
			byKey[k] = it
		}
		it.Size += fi.Size()
		if fi.ModTime().After(it.Accessed) {
			it.Accessed = fi.ModTime()
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("cache: scan %s: %w", s.Dir(), err)
	}
	items := make([]Item, 0, len(byKey))
	for _, it := range byKey {
		if at, ok := s.Accessed(it.Key); ok {
			it.Accessed = at
		}
		items = append(items, *it)
	}
	slices.SortFunc(items, func(a, b Item) int {
		return cmp.Or(a.Accessed.Compare(b.Accessed), strings.Compare(a.Key, b.Key))
	})
	return items, nil
}

// fileKey is the take key a file in the store belongs to, or "" for a foreign file.
func fileKey(name string) string {
	name = strings.TrimPrefix(name, ".")
	if len(name) < 64 || !validKey(name[:64]) {
		return ""
	}
	return name[:64]
}

// Summarize totals items for `cache ls`.
func Summarize(items []Item) Stats {
	s := Stats{Count: len(items)}
	for i, it := range items {
		s.Size += it.Size
		if i == 0 || it.Accessed.Before(s.Oldest) {
			s.Oldest = it.Accessed
		}
		if it.Accessed.After(s.Newest) {
			s.Newest = it.Accessed
		}
	}
	return s
}

// PrunePlan selects the takes to remove: every take last accessed before cutoff (a zero
// cutoff selects none), then the least recently accessed until the rest fit in maxSize
// (maxSize < 0 means no cap). items must be in Scan order.
func PrunePlan(items []Item, cutoff time.Time, maxSize int64) []Item {
	var total int64
	for _, it := range items {
		total += it.Size
	}
	var out []Item
	for _, it := range items {
		stale := !cutoff.IsZero() && it.Accessed.Before(cutoff)
		over := maxSize >= 0 && total > maxSize
		if !stale && !over {
			continue
		}
		out = append(out, it)
		total -= it.Size
	}
	return out
}

// Remove deletes every file of the take key under root. A take already gone is not an error.
func Remove(root, key string) error { return RemoveKeys(root, []string{key}) }

// RemoveKeys deletes every file of each take in keys under root (Store.RemoveKeys); a missing
// store has nothing to remove.
func RemoveKeys(root string, keys []string) error {
	s, ok, err := OpenExisting(root)
	if err != nil || !ok {
		return err
	}
	defer func() { _ = s.Close() }()
	return s.RemoveKeys(keys)
}

// RemoveKeys deletes every file of each take in keys, reading each shard directory once
// however many of its takes go (one read per take made clearing a full shard quadratic).
// Within a shard every entry goes before any audio, so a removal interrupted half way leaves
// misses, never an entry whose audio is gone. A take already gone is not an error. Every
// removal is relative to the store's directory.
func (s *Store) RemoveKeys(keys []string) error {
	byShard := map[string]map[string]bool{}
	for _, key := range keys {
		if !validKey(key) {
			return fmt.Errorf("cache: invalid key %q", key)
		}
		if byShard[key[:2]] == nil {
			byShard[key[:2]] = map[string]bool{}
		}
		byShard[key[:2]][key] = true
	}
	shards := slices.Sorted(maps.Keys(byShard))
	for _, shard := range shards {
		want := byShard[shard]
		names, err := fs.ReadDir(s.dir.Root().FS(), shard)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("cache: %w", err)
		}
		slices.SortStableFunc(names, func(a, b fs.DirEntry) int {
			return cmp.Compare(rank(a.Name()), rank(b.Name()))
		})
		for _, n := range names {
			key := fileKey(n.Name())
			if !want[key] || n.IsDir() {
				continue
			}
			if err := s.dir.Root().Remove(filepath.Join(shard, n.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("cache: remove %s: %w", key, err)
			}
		}
		_ = s.dir.Root().Remove(shard) // best effort: only succeeds when the shard directory is empty
	}
	return nil
}

func rank(name string) int {
	if strings.HasSuffix(name, ".json") {
		return 0
	}
	return 1
}
