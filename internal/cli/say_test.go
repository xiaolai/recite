package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/guard"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
)

// rejected runs say with args and asserts exit 2, no runner call (so no
// network or auth), and a message containing want.
func rejected(t *testing.T, want string, args ...string) {
	t.Helper()
	r := run(append([]string{"say", "Hello"}, args...)...)
	if r.code != ExitUsage || len(r.says) != 0 || !strings.Contains(r.stderr, want) {
		t.Errorf("%v: code %d, says %d, stderr %q (want %q)", args, r.code, len(r.says), r.stderr, want)
	}
}

// accept: SPK-02#1
func TestSayModelFlag(t *testing.T) {
	rejected(t, "gpt-realtime-2.1, gpt-realtime-2, gpt-realtime-2.1-mini", "--model", "gpt-realtime-9")
	for _, m := range realtime.Models {
		if r := run("say", "Hi", "--model", m); r.code != ExitOK || r.says[0].Settings.Model != m {
			t.Errorf("%s: %+v", m, r)
		}
	}
	if r := run("say", "Hi", "--model", "gpt-realtime-9", "--model-unchecked"); r.code != ExitOK || r.says[0].Settings.Model != "gpt-realtime-9" {
		t.Errorf("unchecked: %+v", r)
	}
}

// accept: SPK-03#1
func TestSayVoiceFlag(t *testing.T) {
	rejected(t, strings.Join(realtime.Voices, ", "), "--voice", "cove")
	if r := run("say", "Hi", "--voice", "marin"); r.code != ExitOK || r.says[0].Settings.Voice != "marin" {
		t.Errorf("marin: %+v", r)
	}
}

// accept: SPK-04#1
func TestSaySpeedFlag(t *testing.T) {
	for _, bad := range []string{"0.24", "1.51", "1.234", "fast"} {
		rejected(t, "0.25 to 1.50 in steps of 0.01", "--speed", bad)
	}
	if r := run("say", "Hi", "--speed", "1.25"); r.code != ExitOK || r.says[0].Settings.Speed != 1.25 {
		t.Errorf("1.25: %+v", r)
	}
}

// accept: SPK-10#1
func TestSayEffortFlag(t *testing.T) {
	for _, bad := range []string{"none", "max"} {
		rejected(t, "minimal, low, medium, high, xhigh", "--effort", bad)
	}
	if r := run("say", "Hi", "--effort", "xhigh"); r.code != ExitOK || r.says[0].Settings.Effort != "xhigh" {
		t.Errorf("xhigh: %+v", r)
	}
}

// accept: SPK-11#5
func TestSayRetryEffortFlag(t *testing.T) {
	for _, bad := range []string{"minimal,max", "low,,medium", ",", "none"} {
		rejected(t, "minimal, low, medium, high, xhigh", "--retry-effort", bad)
	}
	r := run("say", "Hi", "--effort", "high", "--retry-effort", "minimal, low")
	if r.code != ExitOK || !slices.Equal(r.says[0].RetryEffort, []realtime.Effort{"minimal", "low"}) || r.says[0].Settings.Effort != "high" {
		t.Errorf("ladder: %+v", r)
	}
	if r := run("say", "Hi"); r.code != ExitOK || r.says[0].RetryEffort != nil {
		t.Errorf("default is off: %+v", r)
	}
}

func TestTasksForCarriesLadder(t *testing.T) {
	p := pipeline.Plan{Chunks: []pipeline.Chunk{{ID: "c0001", Spoken: "Hi."}, {ID: "c0002", Spoken: "Bye."}}}
	ladder := []realtime.Effort{"minimal", "low"}
	for _, task := range tasksFor(p, realtime.SessionSpec{Effort: "high"}, ladder) {
		if !slices.Equal(task.Ladder, ladder) || task.Spec.Effort != "high" {
			t.Errorf("%s: ladder %v effort %s", task.Chunk.ID, task.Ladder, task.Spec.Effort)
		}
	}
}

func TestSayInstructionsVersionHidden(t *testing.T) {
	if r := run("say", "Hi", "--instructions-version", "1"); r.code != ExitOK || r.says[0].Settings.InstructionsVersion != 1 {
		t.Errorf("v1: %+v", r)
	}
	rejected(t, "valid: 1, 2", "--instructions-version", "3")
	if r := run("help", "say"); strings.Contains(r.stdout, "instructions-version") {
		t.Error("hidden flag shown in help")
	}
}

// The M1 marker precondition is gone: markers reach the guard, which escapes them.
func TestSayGuardFlags(t *testing.T) {
	cfg := writeConfig(t, "[guard]\nurl = \"full\"\nemoji = \"keep\"\n")
	r := run("say", "a <<< b", "--guard", "strict", "--config", cfg)
	if r.code != ExitOK || len(r.says) != 1 {
		t.Fatalf("%+v", r)
	}
	g := r.says[0].Guard
	if g.Policy != guard.PolicyStrict || g.Actions[guard.URL] != guard.ActFull || g.Actions[guard.Emoji] != guard.ActKeep {
		t.Fatalf("guard options %+v", g)
	}
	rejected(t, "--guard must be fix, strict, warn or off", "--guard", "loud")
	rejected(t, `:2: guard.url: G-URL takes host, full, drop, fail, not "keep"`, "--config", writeConfig(t, "[guard]\nurl = \"keep\"\n"))
	// The [guard] table replaced the M2 flag form (TXT-09).
	rejected(t, "guard-action", "--guard-action", "G-URL=full")
}
