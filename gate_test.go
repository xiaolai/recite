package recite_test

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// makeRecipe returns the recipe lines of a Makefile target. Recipe lines are
// tab-indented and follow the "target:" line until the next non-recipe line.
func makeRecipe(t *testing.T, makefile, target string) []string {
	t.Helper()
	var recipe []string
	in := false
	for _, line := range strings.Split(makefile, "\n") {
		switch {
		case strings.HasPrefix(line, target+":"):
			in = true
		case in && strings.HasPrefix(line, "\t"):
			recipe = append(recipe, strings.TrimSpace(line))
		case in:
			return recipe
		}
	}
	if !in {
		t.Fatalf("Makefile has no %q target", target)
	}
	return recipe
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Each step is its own recipe line, so make stops at the first non-zero exit.
// accept: FND-03#1
func TestMakeCheckRunsEveryStepInOrder(t *testing.T) {
	recipe := makeRecipe(t, readFile(t, "Makefile"), "check")
	steps := []string{
		"gofmt -l",
		"go vet ./...",
		"$(GOLANGCI_LINT) run",
		"go test -race ./...",
		"$(GOVULNCHECK)",
		"go run ./tools/acceptcheck",
	}
	if len(recipe) != len(steps) {
		t.Fatalf("check has %d recipe lines, want %d: %q", len(recipe), len(steps), recipe)
	}
	for i, step := range steps {
		if !strings.Contains(recipe[i], step) {
			t.Errorf("check step %d = %q, want it to contain %q", i+1, recipe[i], step)
		}
	}
	if !strings.Contains(recipe[0], "exit 1") {
		t.Errorf("gofmt step must fail when gofmt -l prints files: %q", recipe[0])
	}
	for _, line := range recipe {
		if strings.HasPrefix(line, "-") || strings.Contains(line, "|| true") {
			t.Errorf("check step ignores failure: %q", line)
		}
	}
}

func TestLiveSuitesAreNotInCheck(t *testing.T) {
	mk := readFile(t, "Makefile")
	for _, line := range makeRecipe(t, mk, "check") {
		for _, tag := range []string{"live", "qualify", "calibrate", "RECITE_LIVE"} {
			if strings.Contains(line, tag) {
				t.Errorf("check runs %q: %q", tag, line)
			}
		}
	}
	got := makeRecipe(t, mk, "live qualify calibrate")
	if len(got) != 1 || got[0] != "RECITE_LIVE=1 go test -tags $@ -run . -count=1 ./..." {
		t.Errorf("live suite recipe = %q", got)
	}
}

// accept: FND-03#1
func TestWorkflowRunsCheckOnMacOSAndLinux(t *testing.T) {
	wf := readFile(t, ".github/workflows/check.yml")
	for _, want := range []string{
		"os: [macos-latest, ubuntu-latest]",
		"runs-on: ${{ matrix.os }}",
		"- run: make check",
		"permissions:\n  contents: read",
		"go-version-file: go.mod",
	} {
		if !strings.Contains(wf, want) {
			t.Errorf("workflow missing %q", want)
		}
	}
	for _, line := range strings.Split(wf, "\n") {
		if _, ref, ok := strings.Cut(line, "uses: "); ok {
			at := strings.Index(ref, "@")
			sha, _, _ := strings.Cut(ref[at+1:], " ")
			if at < 0 || len(sha) != 40 {
				t.Errorf("action not pinned to a full commit SHA: %q", strings.TrimSpace(line))
			}
		}
	}
}

// FND-03#3 (make check under 5 minutes on CI runners) is a benchmark-kind
// criterion reviewed at milestone exits from the CI run times, never a timed
// assertion here. This test holds the bound the runner enforces: the check
// job is killed after timeout-minutes, which must stay at 10 or less.
// accept: FND-03#3
func TestWorkflowCheckJobTimeout(t *testing.T) {
	wf := readFile(t, ".github/workflows/check.yml")
	_, job, ok := strings.Cut(wf, "\n  check:\n")
	if !ok {
		t.Fatal("workflow has no check job")
	}
	if next := strings.Index(job, "\n  fuzz-bench:"); next >= 0 {
		job = job[:next]
	}
	_, rest, ok := strings.Cut(job, "timeout-minutes: ")
	if !ok {
		t.Fatal("check job has no timeout-minutes")
	}
	v, _, _ := strings.Cut(rest, "\n")
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 || n > 10 {
		t.Errorf("check job timeout-minutes = %q, want 1..10", v)
	}
}
