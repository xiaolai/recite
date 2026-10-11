package cli

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// buildRecite builds cmd/recite with ldflags into a temp dir.
func buildRecite(t *testing.T, ldflags string) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go tool not on PATH: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "recite")
	cmd := exec.Command(goBin, "build", "-ldflags", ldflags, "-o", bin, "github.com/xiaolai/recite/cmd/recite")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func runBin(t *testing.T, bin string, args ...string) string {
	t.Helper()
	out, err := exec.Command(bin, args...).Output()
	if err != nil {
		t.Fatalf("%s %v: %v", bin, args, err)
	}
	return string(out)
}

// accept: FND-02#1
func TestVersionInjected(t *testing.T) {
	const pkg = "github.com/xiaolai/recite/internal/buildinfo"
	bin := buildRecite(t, "-X "+pkg+".version=v9.8.7 -X "+pkg+".commit=abc123 -X "+pkg+".date=2026-01-02T03:04:05Z")
	want := "recite v9.8.7\ncommit: abc123\nbuilt:  2026-01-02T03:04:05Z\ngo:     " + runtime.Version() + "\n"
	if got := runBin(t, bin, "version"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// The short forms print the same: the v alias and a leading --version. -v stays verbosity.
	for _, args := range [][]string{{"v"}, {"--version"}, {"-version"}} {
		if got := runBin(t, bin, args...); got != want {
			t.Errorf("%v: got %q, want %q", args, got, want)
		}
	}
	for _, args := range [][]string{{"version", "--json"}, {"--json", "version"}, {"v", "--json"}, {"--version", "--json"}} {
		var m map[string]string
		if err := json.Unmarshal([]byte(runBin(t, bin, args...)), &m); err != nil {
			t.Fatal(err)
		}
		if m["version"] != "v9.8.7" || m["commit"] != "abc123" || m["date"] != "2026-01-02T03:04:05Z" || m["go"] != runtime.Version() {
			t.Errorf("%v: %v", args, m)
		}
	}
}

// accept: FND-02#1
func TestVersionUninjected(t *testing.T) {
	bin := buildRecite(t, "")
	got := runBin(t, bin, "version")
	want := "recite dev\ncommit: unknown\nbuilt:  unknown\ngo:     " + runtime.Version() + "\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// In-process too: the test binary is not injected.
	r := run("version", "--json")
	var m map[string]string
	if err := json.Unmarshal([]byte(r.stdout), &m); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"version": "dev", "commit": "unknown", "date": "unknown"} {
		if m[k] != v {
			t.Errorf("%s = %q, want %q", k, m[k], v)
		}
	}
	if strings.TrimSpace(m["go"]) == "" {
		t.Error("empty go version")
	}
}
