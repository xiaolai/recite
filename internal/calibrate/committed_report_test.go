package calibrate_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// verdictLine is the report's held-out verdict as WriteReport states it: PASS, or FAIL with
// the counts behind it.
var verdictLine = regexp.MustCompile(`(?m)^\*\*Held-out verdict( \(machine-labelled\))?: (PASS|FAIL: .*\d+ (false accepts|false rejects) of \d+ clips.*)\*\*$`)

// The committed calibration reports exist and each states its held-out verdict as measured,
// with the method, the per-type results and Wilson intervals. Under D23 the verdict is
// reported, not gated: a FAIL verdict is valid evidence about the opt-in STT mode.
// accept: VER-07#2
// accept: VER-07#3
// accept: VER-11#1
func TestCommittedReportsStateVerdict(t *testing.T) {
	docs := filepath.Join("..", "..", "dev-docs")
	for _, pattern := range []string{"calibration-[0-9]*[0-9].md", "calibration-*-latin.md", "calibration-*-han.md"} {
		paths, err := filepath.Glob(filepath.Join(docs, pattern))
		if err != nil {
			t.Fatal(err)
		}
		if len(paths) == 0 {
			t.Fatalf("no committed report matches dev-docs/%s", pattern)
		}
		for _, p := range paths {
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			s := string(data)
			if !verdictLine.MatchString(s) {
				t.Errorf("%s: no held-out verdict line", p)
			}
			for _, want := range []string{"## Method", "## Held-out criteria", "## Per-type results (held out)", "Wilson 95%"} {
				if !strings.Contains(s, want) {
					t.Errorf("%s: lacks %q", p, want)
				}
			}
		}
	}
}
