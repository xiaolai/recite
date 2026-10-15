package cache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// RemoveKeys removes exactly the named takes, many of them in one shard, and leaves the rest.
func TestRemoveKeysRemovesOnlyTheNamedTakes(t *testing.T) {
	root := t.TempDir()
	key := func(i int) string { return "ab" + strings.Repeat("0", 61) + string(rune('0'+i)) }
	shard := filepath.Join(TakesDir(root), "ab")
	if err := os.MkdirAll(shard, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		for _, ext := range []string{".json", ".wav"} {
			if err := os.WriteFile(filepath.Join(shard, key(i)+ext), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := RemoveKeys(root, []string{key(0), key(2), key(4), key(9)}); err != nil { // key(9) is already gone
		t.Fatal(err)
	}
	for i := range 6 {
		_, err := os.Stat(filepath.Join(shard, key(i)+".wav"))
		if gone := os.IsNotExist(err); gone != (i%2 == 0) {
			t.Errorf("take %d: removed %v", i, gone)
		}
	}
	if err := RemoveKeys(root, []string{"not-a-key"}); err == nil {
		t.Error("an invalid key was accepted")
	}
}
