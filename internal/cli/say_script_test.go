package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/pipeline"
	rtfake "github.com/xiaolai/recite/internal/realtime/fake"
)

// threeSpeakers is a 3-speaker dialogue: each speaker has a different voice, and lines of
// the same speaker are not adjacent, so script order and voice order differ.
const threeSpeakers = `+++
title = "Three voices"
[voices]
HOST = "cedar"
GUEST = { voice = "marin", style = "warm, curious" }
NARRATOR = "sage"
+++

[section Opening]
NARRATOR: It was late when the call came in.
HOST: Welcome back to the show.
GUEST: (amused) Thanks for having me.
HOST: Let us begin with the night itself.
NARRATOR: The lights flickered once,
and then held steady.
GUEST: I remember that part very well.
`

var speakerVoice3 = map[string]string{"HOST": "cedar", "GUEST": "marin", "NARRATOR": "sage"}

func writeScript(t *testing.T, m *multiEnv, name, body string) string {
	t.Helper()
	p := m.path(name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A 3-speaker script renders with every chunk on a session of its speaker's voice, and the
// output holds the takes in script order.
// accept: SPK-09#1
func TestSayScriptThreeSpeakersVoicesAndOrder(t *testing.T) {
	m := newMultiEnv(t)
	in := writeScript(t, m, "episode.tts", threeSpeakers)
	r := m.run("-f", in, "-o", "out.wav", "--jobs", "3", "--gap-paragraph", "400ms", "--gap-section", "400ms")
	if r.code != exitcode.OK {
		t.Fatalf("code %d stderr %q", r.code, r.stderr)
	}
	tasks := m.lastTasks()
	wantLines := []struct{ speaker, text string }{
		{"NARRATOR", "It was late when the call came in."},
		{"HOST", "Welcome back to the show."},
		{"GUEST", "Thanks for having me."},
		{"HOST", "Let us begin with the night itself."},
		{"NARRATOR", "The lights flickered once, and then held steady."},
		{"GUEST", "I remember that part very well."},
	}
	if len(tasks) != len(wantLines) {
		t.Fatalf("%d chunks, want %d: %+v", len(tasks), len(wantLines), tasks)
	}
	voiceOf := map[string]string{}
	for i, w := range wantLines {
		tk := tasks[i]
		if tk.Chunk.Spoken != w.text || tk.Spec.Voice != speakerVoice3[w.speaker] {
			t.Errorf("chunk %d: %q on %s, want %q on %s", i+1, tk.Chunk.Spoken, tk.Spec.Voice, w.text, speakerVoice3[w.speaker])
		}
		voiceOf[tk.Chunk.ID] = speakerVoice3[w.speaker]
	}
	// Every render ran on a session minted with its chunk's speaker voice.
	calls := m.synth.Calls()
	if len(calls) != len(wantLines) {
		t.Fatalf("%d renders", len(calls))
	}
	for _, c := range calls {
		if c.Spec.Voice != voiceOf[c.Request.ChunkID] {
			t.Errorf("%s rendered on %s, want %s", c.Request.ChunkID, c.Spec.Voice, voiceOf[c.Request.ChunkID])
		}
	}
	// The output is the takes in script order with the fixed gaps between them.
	gap := int(400 * time.Millisecond * audio.SampleRate / time.Second)
	if got, want := readWAV(t, m.path("out.wav")), expectedOutput(tasks, 1, gap); !bytes.Equal(got, want) {
		t.Fatalf("output %d bytes, want %d in script order", len(got), len(want))
	}
	// The manifest records each chunk's speaker and voice, validates, and has the section.
	mf := validManifest(t, manifestSchema(t), m.path("out.wav.json"))
	for i, c := range mf.Chunks {
		if c.Speaker == nil || *c.Speaker != wantLines[i].speaker || c.Voice != speakerVoice3[wantLines[i].speaker] {
			t.Errorf("manifest chunk %s: speaker %v voice %s", c.ID, c.Speaker, c.Voice)
		}
	}
	if len(mf.Chapters) != 1 || mf.Chapters[0].Title != "Opening" || mf.Chapters[0].FirstChunk != "c0001" {
		t.Errorf("chapters %+v", mf.Chapters)
	}
	if mf.Input.Format != formatScript {
		t.Errorf("input format %q", mf.Input.Format)
	}
}

// The same words spoken by two speakers are two takes: the voice is part of the cache key,
// and the style of a speaker and a direction is too.
// accept: SPK-09#1
func TestScriptVoiceInCacheKey(t *testing.T) {
	m := newMultiEnv(t)
	in := writeScript(t, m, "same.tts", "+++\n[voices]\nA = \"cedar\"\nB = \"marin\"\n+++\nA: Same words.\nB: Same words.\nA: (sad) Same words.\n")
	if r := m.run("-f", in, "-o", "out.wav"); r.code != exitcode.OK {
		t.Fatalf("%+v", r)
	}
	tasks := m.lastTasks()
	if len(tasks) != 3 || tasks[0].CacheKey == tasks[1].CacheKey || tasks[0].CacheKey == tasks[2].CacheKey {
		t.Fatalf("cache keys not distinct: %+v", tasks)
	}
	if !strings.Contains(tasks[2].Style, "Delivery: sad") || tasks[0].Style != "" {
		t.Fatalf("styles %q %q", tasks[0].Style, tasks[2].Style)
	}
}

// No speaker label, bracketed directive or (direction) is sent to the model as text, or
// expected in the transcript: the direction rides in the response instructions as delivery.
// accept: TXT-07#2
func TestSayScriptDirectivesNeverReachTheModel(t *testing.T) {
	m := newMultiEnv(t)
	body := strings.Replace(threeSpeakers, "HOST: Let us", "HOST: (serious) Let us [pause 700ms] wait, let us", 1) + "[pause 1s]\n[section Close]\nHOST: Good night.\n"
	in := writeScript(t, m, "episode.tts", body)
	r := m.run("-f", in, "-o", "out.wav", "--style", "Unhurried.")
	if r.code != exitcode.OK {
		t.Fatalf("code %d stderr %q", r.code, r.stderr)
	}
	for _, c := range m.synth.Calls() {
		for _, bad := range []string{"HOST", "GUEST", "NARRATOR", ":", "[", "]", "(", ")", "pause", "section", "Opening", "Close", "amused", "serious"} {
			if strings.Contains(c.Request.Text, bad) {
				t.Errorf("%s: %q reached the model: %q", c.Request.ChunkID, bad, c.Request.Text)
			}
		}
	}
	for _, tk := range m.lastTasks() {
		if strings.ContainsAny(tk.Chunk.Spoken, "[]():") {
			t.Errorf("%s expected transcript %q", tk.Chunk.ID, tk.Chunk.Spoken)
		}
	}
	styles := map[string]string{}
	for _, tk := range m.lastTasks() {
		styles[tk.Chunk.Spoken] = tk.Style
	}
	for spoken, want := range map[string]string{
		"Thanks for having me.": "Delivery: Unhurried.; warm, curious; amused",
		"Let us":                "Delivery: Unhurried.; serious",
		"wait, let us begin with the night itself.": "Delivery: Unhurried.; serious",
		"It was late when the call came in.":        "Delivery: Unhurried.",
	} {
		if styles[spoken] != want {
			t.Errorf("%q: style %q, want %q", spoken, styles[spoken], want)
		}
	}
}

// Pauses are exact silence in the assembled PCM, to the sample, whatever the gap settings:
// between script lines, inside a line, and in plain text under --directives.
// accept: TXT-07#1
func TestSayDirectivePausesAreExactSilence(t *testing.T) {
	m := newMultiEnv(t)
	in := writeScript(t, m, "p.tts", "+++\n[voices]\nA = \"cedar\"\nB = \"marin\"\n+++\nA: First line here.\n[pause 1.2s]\n[pause 300ms]\nB: Second [pause 650ms] and third.\n")
	r := m.run("-f", in, "-o", "out.wav", "--gap-paragraph", "300ms..700ms", "--gap-sentence", "250ms", "--gap-seed", "3")
	if r.code != exitcode.OK {
		t.Fatalf("code %d stderr %q", r.code, r.stderr)
	}
	tasks := m.lastTasks()
	if len(tasks) != 3 || tasks[0].Chunk.Spoken != "First line here." || tasks[1].Chunk.Spoken != "Second" || tasks[2].Chunk.Spoken != "and third." {
		t.Fatalf("tasks %+v", tasks)
	}
	samples := func(d time.Duration) int { return int(d * audio.SampleRate / time.Second) }
	want := exactJoin(tasks, samples(1500*time.Millisecond), samples(650*time.Millisecond))
	if got := readWAV(t, m.path("out.wav")); !bytes.Equal(got, want) {
		t.Fatalf("output %d bytes, want %d with exactly 1.5 s and 0.65 s of silence", len(got), len(want))
	}

	m = newMultiEnv(t)
	r = m.run("One sentence here. [pause 800ms] Another one there.", "--directives", "-o", "t.wav", "--gap-sentence", "200ms..300ms")
	if r.code != exitcode.OK {
		t.Fatalf("code %d stderr %q", r.code, r.stderr)
	}
	tasks = m.lastTasks()
	if len(tasks) != 2 || tasks[1].Chunk.Spoken != "Another one there." {
		t.Fatalf("tasks %+v", tasks)
	}
	if got, want := readWAV(t, m.path("t.wav")), exactJoin(tasks, samples(800*time.Millisecond)); !bytes.Equal(got, want) {
		t.Fatalf("--directives output %d bytes, want %d", len(got), len(want))
	}
	// Without --directives the brackets are text.
	m = newMultiEnv(t)
	if r := m.run("One. [pause 800ms] Two.", "-o", "u.wav", "--guard", "off"); r.code != exitcode.OK || !strings.Contains(m.lastTasks()[0].Chunk.Spoken, "[pause ") {
		t.Fatalf("%+v %+v", r, m.lastTasks())
	}
}

// exactJoin is the expected output of first-attempt takes joined by the given silences.
func exactJoin(tasks []pipeline.Task, gaps ...int) []byte {
	var out []byte
	for i, tk := range tasks {
		out = append(out, audio.Fade(rtfake.TaggedPCM(tk.Chunk.ID, 1, tk.Chunk.Estimate))...)
		if i < len(gaps) {
			out = append(out, make([]byte, gaps[i]*audio.BytesPerSample)...)
		}
	}
	return out
}

// An unknown speaker or an empty line body stops the run before any request, exit 2 with the
// line number of the input (front matter counted); check reports the same.
// accept: TXT-06#1
func TestSayScriptErrorsExit2WithLine(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"+++\n[voices]\nHOST = \"cedar\"\n+++\nHOST: Hi.\nGEST: Hello.\n", `line 6: unknown speaker "GEST"; the front-matter [voices] map names HOST`},
		{"+++\n[voices]\nHOST = \"cedar\"\n+++\n\nHOST:\n", "line 6: HOST: the line has no text to speak"},
		{"HOST: Hi.\n", `line 1: unknown speaker "HOST"; the front-matter [voices] map names no speakers`},
	} {
		m := newMultiEnv(t)
		in := writeScript(t, m, "bad.tts", tc.body)
		r := m.run("-f", in, "-o", "out.wav")
		if r.code != exitcode.Usage || !strings.Contains(r.stderr, tc.want) {
			t.Errorf("%q: %+v", tc.body, r)
		}
		m.untouched()
		if r := run("check", in); r.code != exitcode.Usage || !strings.Contains(r.stderr, tc.want) {
			t.Errorf("check %q: %+v", tc.body, r)
		}
	}
}

// --input-format script reads a script from any file name; check lists each chunk's speaker
// and voice; config show --input reports the speaker map from a script's front matter;
// prepare refuses a script rather than drop its speakers.
func TestScriptInputSurfaces(t *testing.T) {
	m := newMultiEnv(t)
	in := writeScript(t, m, "episode.txt", threeSpeakers)
	if r := m.run("-f", in, "--input-format", "script", "-o", "x.wav"); r.code != exitcode.OK || m.lastTasks()[1].Spec.Voice != "cedar" {
		t.Fatalf("%+v", r)
	}
	r := run("check", "--input-format", "script", "--json", in)
	if r.code != exitcode.OK {
		t.Fatalf("%+v", r)
	}
	var rep CheckReport
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil {
		t.Fatal(err)
	}
	if it := rep.Plan.Items[2]; it.Speaker != "GUEST" || it.Voice != "marin" || it.Display != "Thanks for having me." {
		t.Fatalf("check item %+v", it)
	}
	tts := filepath.Join(m.dir, "episode.tts")
	if err := os.WriteFile(tts, []byte(threeSpeakers), 0o600); err != nil {
		t.Fatal(err)
	}
	r = run("config", "show", "--input", tts)
	if r.code != exitcode.OK || !strings.Contains(r.stdout, `{"GUEST":{"voice":"marin","style":"warm, curious"},"HOST":{"voice":"cedar"},"NARRATOR":{"voice":"sage"}}`) ||
		!strings.Contains(r.stdout, "front matter ("+tts+":3)") {
		t.Fatalf("config show:\n%s\n%s", r.stdout, r.stderr)
	}
	if r := run("prepare", "-o", "-", tts); r.code != exitcode.Usage || !strings.Contains(r.stderr, "cannot be prepared") {
		t.Fatalf("prepare: %+v", r)
	}
}

// --directives in Markdown: the block breaks move with the removed directives, a [section]
// adds a chapter without being spoken, and a leading (direction) styles its own chunk.
func TestSayMarkdownDirectives(t *testing.T) {
	m := newMultiEnv(t)
	in := writeScript(t, m, "doc.md", "# Title\n\nPara one. [pause 500ms] Para two.\n\n[section Extra]\n\n(whispering) Quiet bit.\n\nLoud bit.\n")
	r := m.run("-f", in, "--directives", "-o", "out.wav")
	if r.code != exitcode.OK {
		t.Fatalf("code %d stderr %q", r.code, r.stderr)
	}
	var got []string
	for _, tk := range m.lastTasks() {
		got = append(got, tk.Chunk.Spoken+"|"+tk.Style)
	}
	want := []string{"Title|", "Para one.|", "Para two.|", "Quiet bit.|Delivery: whispering", "Loud bit.|"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("chunks\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	mf := readManifest(t, m.path("out.wav.json"))
	if len(mf.Chapters) != 2 || mf.Chapters[0].Title != "Title" || mf.Chapters[1].Title != "Extra" || mf.Chapters[1].FirstChunk != "c0004" {
		t.Fatalf("chapters %+v", mf.Chapters)
	}
	if mf.Chunks[0].Speaker != nil || mf.Chunks[0].Voice != "cedar" {
		t.Fatalf("chunk %+v", mf.Chunks[0])
	}
}
