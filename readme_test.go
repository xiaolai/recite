package recite_test

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/exitcode"
)

// The README exit-code table lists exactly the constants, with their meanings.
// accept: CLI-03#2
func TestREADMEExitCodeTable(t *testing.T) {
	readme := readFile(t, "README.md")
	_, section, ok := strings.Cut(readme, "## Exit codes\n")
	if !ok {
		t.Fatal("README has no \"## Exit codes\" section")
	}
	row := regexp.MustCompile(`^\| (\d+) \| (.+) \|$`)
	var got []string
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "## ") {
			break
		}
		if m := row.FindStringSubmatch(line); m != nil {
			got = append(got, m[1]+" "+m[2])
		}
	}
	var want []string
	for _, m := range exitcode.Meanings {
		want = append(want, strconv.Itoa(m.Code)+" "+m.Meaning)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("README table:\n%s\nconstants:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	consts := []int{exitcode.OK, exitcode.Internal, exitcode.Usage, exitcode.Auth, exitcode.RouteDenied,
		exitcode.VerifyFailed, exitcode.RateLimited, exitcode.Failure, exitcode.Interrupted}
	if len(consts) != len(exitcode.Meanings) {
		t.Fatalf("%d constants, %d meanings", len(consts), len(exitcode.Meanings))
	}
	for i, c := range consts {
		if exitcode.Meanings[i].Code != c {
			t.Errorf("meaning %d is for code %d, want %d", i, exitcode.Meanings[i].Code, c)
		}
	}
	if !strings.Contains(section, "130 > 3 > 4 > 1 > 7 > 6 > 5 > 2") {
		t.Error("README lacks the severity order")
	}
}
