package recite_test

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// The dependency allowlist of implementation plan §1.3, split by where a module may appear:
// runtime modules may be linked into the binary; test-only modules may appear in go.mod only.
var (
	runtimeModules = []string{
		"github.com/coder/websocket",
		"github.com/BurntSushi/toml",
		"github.com/yuin/goldmark",
		"golang.org/x/text",
		// The shared Codex sign-in, Realtime and said-guard module, split out of recite itself.
		"github.com/xiaolai/sayso/go",
		// Through sayso only: it opens auth.json with openat(2) and O_NOFOLLOW, one directory at a
		// time, which the syscall package lacks on macOS.
		"golang.org/x/sys",
	}
	testOnlyModules = []string{
		"go.uber.org/goleak",
		"github.com/santhosh-tekuri/jsonschema/v6",
	}
	allowedModules = slices.Concat(runtimeModules, testOnlyModules)
)

// parseRequires returns the module paths named by require directives, both
// single-line and block form. Comments are stripped first.
func parseRequires(gomod string) ([]string, error) {
	var mods []string
	inBlock := false
	for i, raw := range strings.Split(gomod, "\n") {
		line := raw
		if j := strings.Index(line, "//"); j >= 0 {
			line = line[:j]
		}
		f := strings.Fields(line)
		switch {
		case len(f) == 0:
		case inBlock && f[0] == ")":
			inBlock = false
		case inBlock:
			mods = append(mods, f[0])
		case f[0] == "require" && len(f) == 2 && f[1] == "(":
			inBlock = true
		case f[0] == "require" && len(f) >= 3:
			mods = append(mods, f[1])
		case f[0] == "require":
			return nil, fmt.Errorf("go.mod line %d: malformed require: %q", i+1, raw)
		}
	}
	if inBlock {
		return nil, fmt.Errorf("go.mod: unterminated require block")
	}
	return mods, nil
}

func disallowed(mods []string) []string {
	var bad []string
	for _, m := range mods {
		if !slices.Contains(allowedModules, m) {
			bad = append(bad, m)
		}
	}
	return bad
}

// accept: FND-01#3
func TestGoModDependenciesAllowlisted(t *testing.T) {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	mods, err := parseRequires(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if bad := disallowed(mods); len(bad) > 0 {
		t.Errorf("go.mod requires modules outside the plan §1.3 allowlist: %v", bad)
	}
}

// accept: FND-01#3
func TestParseRequiresFlagsDisallowed(t *testing.T) {
	fixture := `module example.com/x

go 1.27

require github.com/yuin/goldmark v1.7.0 // markdown

require (
	golang.org/x/text v0.20.0
	github.com/evil/dep v1.0.0 // indirect
)
`
	mods, err := parseRequires(fixture)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"github.com/yuin/goldmark", "golang.org/x/text", "github.com/evil/dep"}
	if !slices.Equal(mods, want) {
		t.Fatalf("parseRequires = %v, want %v", mods, want)
	}
	if bad := disallowed(mods); !slices.Equal(bad, []string{"github.com/evil/dep"}) {
		t.Fatalf("disallowed = %v, want [github.com/evil/dep]", bad)
	}
	if _, err := parseRequires("require (\n\tfoo v1\n"); err == nil {
		t.Fatal("unterminated block: want error")
	}
}

// accept: FND-01#2
func TestBinaryDepsAllowlisted(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{with .Module}}{{.Path}}{{end}}", "./cmd/recite").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, m := range strings.Fields(string(out)) {
		if m != "github.com/xiaolai/recite" && !slices.Contains(runtimeModules, m) {
			t.Errorf("cmd/recite depends on non-approved module %s", m)
		}
	}
}

// accept: FND-01#1
func TestBuildEmptyImplementation(t *testing.T) {
	if out, err := exec.Command("go", "build", "./...").CombinedOutput(); err != nil {
		t.Fatalf("go build ./...: %v\n%s", err, out)
	}
}
