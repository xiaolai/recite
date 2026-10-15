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

// usageEnv is a state directory holding the usage package's fixture ledger.
func usageEnv(t *testing.T) map[string]string {
	t.Helper()
	state := t.TempDir()
	dir := filepath.Join(state, "recite")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join("..", "usage", "testdata", "ledger")
	names, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(src, n.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, n.Name()), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return map[string]string{"XDG_STATE_HOME": state}
}

// accept: CLI-09#1
func TestUsageGoldenWithPlanStatus(t *testing.T) {
	r := maintainRun(t, usageEnv(t), "usage")
	if r.code != ExitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	golden(t, "usage", r.stdout)
	if !strings.Contains(r.stderr, "1 ledger lines could not be read") {
		t.Fatalf("the unreadable line is not reported: %q", r.stderr)
	}
	// The plan status comes from the fake app-server; its account email and IDs never print.
	if !strings.Contains(r.stdout, "Codex plan: pro") || !strings.Contains(r.stdout, "41% used") {
		t.Fatalf("no plan status:\n%s", r.stdout)
	}
	for _, leak := range []string{"canary", "@", "account"} {
		if strings.Contains(r.stdout+r.stderr, leak) {
			t.Fatalf("output contains %q:\n%s%s", leak, r.stdout, r.stderr)
		}
	}
}

func TestUsageSinceAndJSON(t *testing.T) {
	env := usageEnv(t)
	r := maintainRun(t, env, "--json", "usage", "--since", "7d")
	if r.code != ExitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	validateEvents(t, r.stdout)
	var ev usageEvent
	if err := json.Unmarshal([]byte(r.stdout), &ev); err != nil {
		t.Fatal(err)
	}
	// cliNow is 2023-11-14; every fixture line is later, so 7d keeps them all.
	if len(ev.Rows) != 5 || ev.Plan == nil || ev.Plan.PlanType != "pro" || len(ev.Plan.Windows) != 1 || ev.PlanError != "" {
		t.Fatalf("event %+v", ev)
	}
	if r := maintainRun(t, env, "usage", "--since", "7x"); r.code != ExitUsage {
		t.Fatalf("bad --since: exit %d", r.code)
	}
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

// --since keeps the usage at or after the cutoff and drops what came before it.
func TestUsageSinceCutoff(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "recite")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cutoff := cliNow.Add(-24 * time.Hour).UTC()
	var ledger strings.Builder
	for _, at := range []time.Time{cutoff.Add(-time.Second), cutoff, cutoff.Add(time.Hour)} {
		fmt.Fprintf(&ledger, `{"time":%q,"command":"say","kind":"render","model":"gpt-realtime-2.1","chunk":"c0001","attempt":1,"output_tokens":100,"output_audio_tokens":100,"estimate_usd":0.0064,"price_note":"list price, not billed"}`+"\n",
			at.Format(time.RFC3339))
	}
	if err := os.WriteFile(filepath.Join(dir, "usage.jsonl"), []byte(ledger.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	renders := func(args ...string) int {
		r := maintainRun(t, map[string]string{"XDG_STATE_HOME": state}, append([]string{"--json", "usage"}, args...)...)
		var ev usageEvent
		if r.code != ExitOK || json.Unmarshal([]byte(r.stdout), &ev) != nil {
			t.Fatalf("exit %d: %s %s", r.code, r.stdout, r.stderr)
		}
		n := 0
		for _, row := range ev.Rows {
			n += row.Renders
		}
		return n
	}
	if n := renders(); n != 3 {
		t.Fatalf("all usage: %d renders, want 3", n)
	}
	if n := renders("--since", "1d"); n != 2 {
		t.Fatalf("--since 1d: %d renders, want 2 (the one before the cutoff dropped, the one at it kept)", n)
	}
}

// clear takes no prune limits: it would remove every take whatever they said.
func TestCacheClearRefusesPruneFlags(t *testing.T) {
	env, root := cacheEnv(t)
	putTake(t, root, 1, 100, cliNow)
	for _, args := range [][]string{{"cache", "clear", "--yes", "--older-than", "30d"}, {"cache", "clear", "--yes", "--max-size", "1GB"}} {
		if r := maintainRun(t, env, args...); r.code != ExitUsage {
			t.Errorf("%v: exit %d", args, r.code)
		}
	}
	if items, _ := cache.Scan(root); len(items) != 1 {
		t.Fatalf("%d takes left, want 1", len(items))
	}
}

// Maintenance refuses a relative root from any variable, and a symlinked cache, as the cache does.
func TestCacheMaintenanceRefusesUnsafeRoots(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)
	if r := maintainRun(t, map[string]string{"XDG_CACHE_HOME": "rel"}, "cache", "clear", "--yes"); r.code != ExitUsage {
		t.Fatalf("relative XDG_CACHE_HOME: exit %d", r.code)
	}
	target := t.TempDir()
	putTake(t, target, 1, 100, cliNow)
	link := filepath.Join(t.TempDir(), "cache-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	// "link/" and "link/." name the link too: the kernel would resolve it before any check.
	for _, root := range []string{link, link + "/", link + "/."} {
		if r := maintainRun(t, map[string]string{"RECITE_CACHE_DIR": root}, "cache", "clear", "--yes"); r.code != ExitUsage {
			t.Fatalf("symlinked root %q: exit %d", root, r.code)
		}
	}
	if items, _ := cache.Scan(target); len(items) != 1 {
		t.Fatalf("the symlink's target lost takes: %d left", len(items))
	}
}

// An omitted ordinaryUsageAllowed is reported as unknown, as JSON null and as "not reported",
// never as a denial; an explicit false stays false.
func TestUsagePlanPermissionUnknown(t *testing.T) {
	for _, tc := range []struct{ usage, json, text string }{
		{"omit", `"ordinary_usage_allowed":null`, "ordinary usage allowed: not reported"},
		{"false", `"ordinary_usage_allowed":false`, "ordinary usage allowed: no"},
	} {
		t.Setenv("FAKECODEX_ORDINARY_USAGE", tc.usage)
		if r := maintainRun(t, usageEnv(t), "--json", "usage"); r.code != 0 || !strings.Contains(r.stdout, tc.json) {
			t.Fatalf("%s: code %d stdout %s", tc.usage, r.code, r.stdout)
		}
		if r := maintainRun(t, usageEnv(t), "usage"); r.code != 0 || !strings.Contains(r.stdout, tc.text) {
			t.Fatalf("%s: code %d stdout %s", tc.usage, r.code, r.stdout)
		}
	}
}
