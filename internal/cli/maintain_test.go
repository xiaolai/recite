package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/cache"
)

// maintainRun runs recite with the fake Codex, env as the whole environment and the fixed clock.
func maintainRun(t *testing.T, env map[string]string, args ...string) result {
	t.Helper()
	t.Setenv("FAKECODEX_MODE", "chatgpt")
	t.Setenv("FAKECODEX_TOKEN_EXP", fmt.Sprint(cliNow.Add(2*time.Hour).Unix()))
	var out, errb bytes.Buffer
	c := New(&out, &errb)
	c.Getenv = func(k string) string { return env[k] }
	c.Now = func() time.Time { return cliNow }
	full := append([]string{"--codex-bin", fakeCodex, "--codex-home", t.TempDir(), "--auth-source", "codex"}, args...)
	code := c.Run(full)
	return result{code: code, stdout: out.String(), stderr: errb.String()}
}

func validateEvents(t *testing.T, stdout string) {
	t.Helper()
	sch := eventsSchema(t)
	for _, l := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var v any
		if err := json.Unmarshal([]byte(l), &v); err != nil {
			t.Fatalf("%q: %v", l, err)
		}
		if err := sch.Validate(v); err != nil {
			t.Fatalf("%s: %v", l, err)
		}
	}
}

func cacheEnv(t *testing.T) (map[string]string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "cache")
	return map[string]string{"RECITE_CACHE_DIR": root}, root
}

func putTake(t *testing.T, root string, n, size int, at time.Time) string {
	t.Helper()
	st, err := cache.Open(root, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	k := fmt.Sprintf("%064x", n)
	if err := st.Put(cache.Entry{Key: k, ChunkID: "c0001"}, make([]byte, size)); err != nil {
		t.Fatal(err)
	}
	return k
}

// accept: CLI-08#1
func TestCacheClearNeedsYes(t *testing.T) {
	env, root := cacheEnv(t)
	putTake(t, root, 1, 100, cliNow)
	r := maintainRun(t, env, "cache", "clear")
	if r.code != ExitUsage || !strings.Contains(r.stderr, "--yes") {
		t.Fatalf("clear without --yes: exit %d, %q", r.code, r.stderr)
	}
	if items, _ := cache.Scan(root); len(items) != 1 {
		t.Fatalf("clear without --yes removed takes: %d left", len(items))
	}
	r = maintainRun(t, env, "--json", "cache", "clear", "--yes")
	if r.code != ExitOK {
		t.Fatalf("clear --yes: exit %d %s", r.code, r.stderr)
	}
	validateEvents(t, r.stdout)
	if items, _ := cache.Scan(root); len(items) != 0 {
		t.Fatalf("%d takes left after clear", len(items))
	}
}

// accept: CLI-08#1
func TestCachePruneAndLs(t *testing.T) {
	env, root := cacheEnv(t)
	old := putTake(t, root, 1, 1000, cliNow.Add(-40*24*time.Hour))
	mid := putTake(t, root, 2, 1000, cliNow.Add(-2*time.Hour))
	fresh := putTake(t, root, 3, 1000, cliNow.Add(-time.Hour))
	r := maintainRun(t, env, "cache", "ls")
	if r.code != ExitOK || !strings.Contains(r.stdout, "takes   3\n") ||
		!strings.Contains(r.stdout, "oldest  2023-10-05 22:13 UTC") || !strings.Contains(r.stdout, "newest  2023-11-14 21:13 UTC") {
		t.Fatalf("ls: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	items, _ := cache.Scan(root)
	one := items[0].Size
	// 30d removes the old take; the size cap then removes the least recently used of the rest.
	r = maintainRun(t, env, "cache", "prune", "--older-than", "30d", "--max-size", fmt.Sprint(one))
	if r.code != ExitOK || !strings.HasPrefix(r.stdout, "removed 2 takes") {
		t.Fatalf("prune: exit %d %s%s", r.code, r.stdout, r.stderr)
	}
	left, _ := cache.Scan(root)
	if len(left) != 1 || left[0].Key != fresh {
		t.Fatalf("left %+v, want only %s (removed %s, %s)", left, fresh, old, mid)
	}
	for _, args := range [][]string{{"cache", "prune"}, {"cache", "prune", "--max-size", "lots"}, {"cache", "prune", "--older-than", "-1d"}, {"cache"}, {"cache", "nope"}} {
		if r := maintainRun(t, env, args...); r.code != ExitUsage {
			t.Errorf("%v: exit %d", args, r.code)
		}
	}
	r = maintainRun(t, env, "--json", "cache", "ls")
	validateEvents(t, r.stdout)
}

func TestCacheLsEmptyCreatesNothing(t *testing.T) {
	env, root := cacheEnv(t)
	r := maintainRun(t, env, "--json", "cache", "ls")
	if r.code != ExitOK {
		t.Fatalf("exit %d %s", r.code, r.stderr)
	}
	validateEvents(t, r.stdout)
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("ls created the cache: %v", err)
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"2GB": 2e9, "500mb": 5e8, "1GiB": 1 << 30, "1048576": 1 << 20, "0": 0, "1.5KB": 1500} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("%s: %d %v, want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "GB", "-1GB", "2XB", "1e30GB"} {
		if _, err := parseSize(in); err == nil {
			t.Errorf("%q: no error", in)
		}
	}
}
