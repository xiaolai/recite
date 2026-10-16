package recite_test

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/cli"
	"github.com/xiaolai/recite/internal/exitcode"
)

// The README exit-code table lists exactly the constants, with their meanings.
// accept: CLI-03#2
func TestREADMEExitCodeTable(t *testing.T) {
	t.Parallel()
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

// helpOf runs `recite <args>` in process and returns its stdout.
func helpOf(t *testing.T, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := cli.New(&stdout, &stderr).Run(args); code != exitcode.OK {
		t.Fatalf("recite %s: exit %d\n%s", strings.Join(args, " "), code, stderr.String())
	}
	return stdout.String()
}

// helpFlag matches a flag line of Go's flag.PrintDefaults output: "  -name" then a
// space, a tab or the end of the line.
var helpFlag = regexp.MustCompile(`(?m)^  -([A-Za-z0-9][A-Za-z0-9-]*)(?:[ \t]|$)`)

// Every flag that `--help` and `help <command>` print is documented in the README, as
// -name or --name.
// accept: REL-04#1
func TestREADMEDocumentsEveryHelpFlag(t *testing.T) {
	t.Parallel()
	readme := readFile(t, "README.md")
	top := helpOf(t, "--help")
	_, cmds, ok := strings.Cut(top, "\nCommands:\n")
	if !ok {
		t.Fatal("--help has no Commands section")
	}
	cmds, _, _ = strings.Cut(cmds, "\n\n")
	var names []string
	for _, line := range strings.Split(cmds, "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			names = append(names, f[0])
		}
	}
	if len(names) < 10 {
		t.Fatalf("parsed only %d commands from --help: %q", len(names), names)
	}
	where := []string{"--help"}
	helps := []string{top}
	for _, name := range names {
		where = append(where, "help "+name)
		helps = append(helps, helpOf(t, "help", name))
	}
	seen := 0
	for i, help := range helps {
		for _, m := range helpFlag.FindAllStringSubmatch(help, -1) {
			seen++
			mention := regexp.MustCompile(`(^|[^A-Za-z0-9-])--?` + regexp.QuoteMeta(m[1]) + `($|[^A-Za-z0-9-])`)
			if !mention.MatchString(readme) {
				t.Errorf("README does not mention -%s (from recite %s)", m[1], where[i])
			}
		}
	}
	// A parser that matched nothing would pass vacuously.
	if seen < 50 {
		t.Fatalf("parsed only %d flags from the help output", seen)
	}
}

// The disclosure sits on the first screen of the README: its first 25 lines.
// accept: REL-04#1
func TestREADMENoticeOnFirstScreen(t *testing.T) {
	t.Parallel()
	lines := strings.Split(readFile(t, "README.md"), "\n")
	if len(lines) > 25 {
		lines = lines[:25]
	}
	first := strings.ToLower(strings.Join(lines, " "))
	first = strings.Join(strings.Fields(strings.NewReplacer(">", " ", "*", "").Replace(first)), " ")
	for _, want := range []string{
		"undocumented use of the chatgpt sign-in",
		"stop working",
		"your usage is your responsibility under openai's terms",
	} {
		if !strings.Contains(first, want) {
			t.Errorf("README first screen lacks %q", want)
		}
	}
}
