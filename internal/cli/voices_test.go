package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/realtime"
)

// The list needs no Codex and no network: with no codex anywhere it still lists the ten voices.
// accept: CLI-04#1
func TestVoicesListNeedsNoNetwork(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	r := run("voices")
	if r.code != exitcode.OK || r.stderr != "" {
		t.Fatalf("%+v", r)
	}
	lines := strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n")
	if len(lines) != len(realtime.Voices) || len(lines) != 10 {
		t.Fatalf("%d lines:\n%s", len(lines), r.stdout)
	}
	for i, v := range realtime.Voices {
		if f := strings.Fields(lines[i]); f[0] != v {
			t.Fatalf("line %d %q, want %s", i, lines[i], v)
		}
	}
	if !slices.Contains(lines, "marin    (recommended)") || !slices.Contains(lines, "cedar    (default, recommended)") {
		t.Fatalf("marks missing:\n%s", r.stdout)
	}
	sch := eventsSchema(t)
	r = run("voices", "--json")
	js := strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n")
	if r.code != exitcode.OK || len(js) != 10 {
		t.Fatalf("--json: %+v", r)
	}
	for _, l := range js {
		validate(t, sch, l)
	}
	if r := run("voices", "--text", "x"); r.code != exitcode.Usage {
		t.Fatalf("--text without --preview: %+v", r)
	}
}

// --preview renders the sample in every voice through the cached, verified path: ten files,
// and an unchanged second run renders nothing.
// accept: CLI-04#1
func TestVoicesPreviewWritesTenFilesThenCaches(t *testing.T) {
	m := newMultiEnv(t)
	m.env = map[string]string{"RECITE_CACHE_DIR": m.path("cache")}
	out := m.path("previews")
	if r := m.runCmd("voices", "--preview", out); r.code != exitcode.OK {
		t.Fatalf("first: %+v", r)
	}
	calls := m.synth.Calls()
	if len(calls) != 10 {
		t.Fatalf("%d renders, want 10", len(calls))
	}
	var voices []string
	for _, c := range calls {
		voices = append(voices, c.Spec.Voice)
		if c.Request.Text != PreviewText && !strings.Contains(c.Request.Text, "sample of my voice") {
			t.Fatalf("rendered %q", c.Request.Text)
		}
	}
	slices.Sort(voices)
	want := slices.Clone(realtime.Voices)
	slices.Sort(want)
	if !slices.Equal(voices, want) {
		t.Fatalf("voices rendered %v", voices)
	}
	ents, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 10 {
		t.Fatalf("%d files in the preview folder, want 10", len(ents))
	}
	for _, v := range realtime.Voices {
		readWAV(t, filepath.Join(out, v+".wav"))
	}
	if r := m.runCmd("voices", "--preview", out); r.code != exitcode.OK {
		t.Fatalf("second: %+v", r)
	}
	if n := len(m.synth.Calls()) - 10; n != 0 {
		t.Fatalf("second run made %d renders", n)
	}
}

// --text and --effort reach every render.
func TestVoicesPreviewOverrides(t *testing.T) {
	m := newMultiEnv(t)
	out := m.path("previews")
	if r := m.runCmd("voices", "--preview", out, "--text", "Testing one two.", "--effort", "low"); r.code != exitcode.OK {
		t.Fatalf("%+v", r)
	}
	calls := m.synth.Calls()
	if len(calls) != 10 {
		t.Fatalf("%d renders", len(calls))
	}
	for _, c := range calls {
		if c.Request.Text != "Testing one two." || c.Spec.Effort != "low" {
			t.Fatalf("render %+v", c)
		}
	}
	if r := m.runCmd("voices", "--preview", out, "--effort", "huge"); r.code != exitcode.Usage {
		t.Fatalf("bad effort: %+v", r)
	}
}

// A preview target that cannot be written fails before any render, and the shared say path
// never reports itself as `recite say`.
func TestVoicesPreviewBadTargetFailsBeforeRendering(t *testing.T) {
	m := newMultiEnv(t)
	out := m.path("previews")
	if err := os.MkdirAll(filepath.Join(out, realtime.Voices[0]+".wav"), 0o700); err != nil {
		t.Fatal(err)
	}
	r := m.runCmd("voices", "--preview", out)
	if r.code != exitcode.Usage || !strings.Contains(r.stderr, "is a directory") || strings.Contains(r.stderr, "recite say") {
		t.Fatalf("%+v", r)
	}
	if n := len(m.synth.Calls()); n != 0 {
		t.Fatalf("%d renders", n)
	}
}

// Messages from the shared say path name the command that ran it.
func TestSayRequestPrefixNamesCommand(t *testing.T) {
	if got := (SayRequest{}).prefix(); got != "recite say" {
		t.Errorf("say: %q", got)
	}
	if got := (SayRequest{Command: "voices --preview"}).prefix(); got != "recite voices --preview" {
		t.Errorf("voices: %q", got)
	}
}
