package cli

import (
	"strings"
	"testing"

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

func TestSayInstructionsVersionHidden(t *testing.T) {
	if r := run("say", "Hi", "--instructions-version", "1"); r.code != ExitOK || r.says[0].Settings.InstructionsVersion != 1 {
		t.Errorf("v1: %+v", r)
	}
	rejected(t, "valid: 1, 2", "--instructions-version", "3")
	if r := run("help", "say"); strings.Contains(r.stdout, "instructions-version") {
		t.Error("hidden flag shown in help")
	}
}

func TestSayMarkerPrecondition(t *testing.T) {
	for _, text := range []string{"a <<< b", "b >>> a"} {
		r := run("say", text)
		if r.code != ExitUsage || len(r.says) != 0 || !strings.Contains(r.stderr, "the text contains <<< or >>>; this is supported from M2") {
			t.Errorf("%q: %+v", text, r)
		}
	}
}
