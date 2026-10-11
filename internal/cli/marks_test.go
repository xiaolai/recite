package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/guard"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/stt"
	"github.com/xiaolai/recite/internal/text"
)

const everyMark = "Read {{spell|API}} as {{say:sequel|SQL}} in {{digits|2026}}, take {{fraction|1/2}} on {{date|10/11}} " +
	"for {{num|$3.5M}}. Keep {{verbatim|R2-D2}}, {{emph|stop}}, say {{lang:fr|bonjour}} to {{hint:oh-KAH-for|Okafor}}." +
	"{{pause:600ms}} Done."

// No mark reaches the model as literal braces: each becomes its spoken form, and the
// instruction marks ride in the response instructions (asserted at the fake synthesizer).
// accept: TXT-13#2
func TestSayMarksNeverReachTheModelAsBraces(t *testing.T) {
	m := newMultiEnv(t)
	r := m.run(everyMark+" A {{spel|bad}} mark.", "--marks", "-o", "out.wav", "--jobs", "1")
	if r.code != exitcode.OK {
		t.Fatalf("code %d stderr %q", r.code, r.stderr)
	}
	if !strings.Contains(r.stderr, `G-MARK strip: "{{spel|bad}}" → "bad"`) {
		t.Errorf("no G-MARK finding: %q", r.stderr)
	}
	calls := m.synth.Calls()
	if len(calls) != 2 {
		t.Fatalf("%d renders, want 2 (split at the pause)", len(calls))
	}
	for _, c := range calls {
		if strings.ContainsAny(c.Request.Text, "{}") {
			t.Errorf("%s: braces reached the model: %q", c.Request.ChunkID, c.Request.Text)
		}
	}
	want := "Read A P I as sequel in two zero two six, take one half on October eleventh for three point five million dollars. " +
		"Keep R2-D2, stop, say bonjour to Okafor."
	if got := calls[0].Request.Text; got != want {
		t.Errorf("chunk 1 sent %q\nwant %q", got, want)
	}
	if got := calls[1].Request.Text; got != "Done. A bad mark." {
		t.Errorf("chunk 2 sent %q", got)
	}
	style := calls[0].Request.Style
	for _, s := range []string{`Stress the word(s) "stop".`, "Say 'bonjour' in French.", "Pronounce Okafor as oh-KAH-for."} {
		if !strings.Contains(style, s) {
			t.Errorf("instructions %q lack %q", style, s)
		}
	}
	if calls[1].Request.Style != "" {
		t.Errorf("chunk 2 instructions %q", calls[1].Request.Style)
	}
	// {{say}}, {{spell}} and {{digits}} spans are protected for verification.
	tk := m.lastTasks()[0]
	var prot []string
	for _, p := range tk.Chunk.Protected {
		prot = append(prot, tk.Chunk.Spoken[p.Start:p.End])
	}
	if strings.Join(prot, "|") != "A P I|sequel|two zero two six" {
		t.Errorf("protected %q", prot)
	}
	// The {{lang}} span reaches verification, which scores and hints it in its own language.
	if ls := tk.Chunk.LangSpans; len(ls) != 1 || ls[0].Lang != "fr" || tk.Chunk.Spoken[ls[0].Spoken.Start:ls[0].Spoken.End] != "bonjour" {
		t.Errorf("lang spans %+v", ls)
	}
}

// On the wire: the user message inside the markers holds the spoken words, and the
// instructions carry the emphasis.
// accept: TXT-13#2
func TestSayMarksOnTheWire(t *testing.T) {
	e := newSayEnv(t)
	r := e.run("Read {{spell|API}} {{emph|now}}.", "--marks", "-o", "a.wav")
	if r.code != exitcode.OK {
		t.Fatalf("%+v", r)
	}
	if got := sentScript(t, e); got != "<<<\nRead A P I now.\n>>>" {
		t.Fatalf("sent %q", got)
	}
	raw := sentText(t, e.srv)
	if !strings.Contains(raw, `Stress the word(s) \"now\".`) || strings.Contains(raw, "{{") {
		t.Fatalf("request %s", raw)
	}
	// Marks are off by default for text input: the braces are text, and the guard does not
	// touch them either.
	e = newSayEnv(t)
	if r := e.run("Say {{spell|X}} now.", "-o", "b.wav"); r.code != exitcode.OK {
		t.Fatalf("%+v", r)
	}
	if got := sentScript(t, e); got != "<<<\nSay {{spell|X}} now.\n>>>" {
		t.Fatalf("marks off sent %q", got)
	}
}

// A malformed mark under --guard strict stops the run before any network call (exit 2),
// in say and in check.
// accept: TXT-13#1
func TestMalformedMarkStrictExits2(t *testing.T) {
	m := newMultiEnv(t)
	r := m.run("Read {{spell|API} now.", "--marks", "--guard", "strict", "-o", "out.wav")
	if r.code != exitcode.Usage || !strings.Contains(r.stderr, "G-MARK") {
		t.Fatalf("%+v", r)
	}
	m.untouched()
	run := offline(t)
	in := writeInput(t, "Read {{say:x|{{spell|A}}}} now.\n")
	if r := run("--marks", "--guard", "strict", in); r.code != exitcode.Usage || !strings.Contains(r.stdout, "G-MARK") {
		t.Fatalf("check: %+v", r)
	}
	if r := run("--marks", in); r.code != exitcode.OK {
		t.Fatalf("check fix: %+v", r)
	}
}

// {{pause}} is a chunk boundary and an exact silence: the output holds the two takes with
// exactly the pause between them, whatever the gap settings.
// accept: TXT-13#3
func TestSayPauseExactSilenceAndSplit(t *testing.T) {
	m := newMultiEnv(t)
	r := m.run("First part of the sentence{{pause:800ms}} and the rest of it.", "--marks", "-o", "out.wav",
		"--gap-sentence", "300ms", "--gap-paragraph", "700ms", "--gap-seed", "7")
	if r.code != exitcode.OK {
		t.Fatalf("code %d stderr %q", r.code, r.stderr)
	}
	tasks := m.lastTasks()
	if len(tasks) != 2 || tasks[0].Chunk.Spoken != "First part of the sentence" || tasks[1].Chunk.Spoken != "and the rest of it." {
		t.Fatalf("tasks %+v", tasks)
	}
	gap := int(800 * time.Millisecond * audio.SampleRate / time.Second)
	if got, want := readWAV(t, m.path("out.wav")), expectedOutput(tasks, 1, gap); !bytes.Equal(got, want) {
		t.Fatalf("output %d bytes, want %d with exactly %d samples of silence", len(got), len(want), gap)
	}
	// Two pauses in a row add up; a pause with no text on one side is refused.
	m = newMultiEnv(t)
	if r := m.run("One.{{pause:300ms}}{{pause:200ms}} Two.", "--marks", "-o", "a.wav"); r.code != exitcode.OK {
		t.Fatalf("%+v", r)
	}
	st, err := runTextStages("One.{{pause:300ms}}{{pause:200ms}} Two.", guard.Options{Marks: true}, spoken.Options{}, 25, realtime.DefaultSettings())
	if err != nil || len(st.plan.Chunks) != 2 || st.plan.Chunks[0].Pause != 500*time.Millisecond {
		t.Fatalf("plan %+v %v", st.plan.Chunks, err)
	}
	m = newMultiEnv(t)
	if r := m.run("{{pause:1s}} Hello.", "--marks", "-o", "b.wav"); r.code != exitcode.Usage || !strings.Contains(r.stderr, "between two pieces of text") {
		t.Fatalf("leading pause: %+v", r)
	}
	m.untouched()
}

func TestMaxPauseIsTheLongestGap(t *testing.T) {
	if text.MaxPause != audio.MaxGap {
		t.Fatalf("text.MaxPause %v, audio.MaxGap %v", text.MaxPause, audio.MaxGap)
	}
}

func TestInputModeFlags(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--marks", "--no-marks"}, "exclude each other"},
		{[]string{"--input-format", "spoken", "--directives"}, "--directives applies to text and markdown input"},
		{[]string{"--input-format", "rtf"}, "must be text, markdown, script or spoken"},
	} {
		m := newMultiEnv(t)
		r := m.run(append([]string{"Hello.", "-o", "a.wav"}, tc.args...)...)
		if r.code != exitcode.Usage || !strings.Contains(r.stderr, tc.want) {
			t.Errorf("%v: %+v", tc.args, r)
		}
		m.untouched()
	}
}

// A {{lang}} span adds its language to the STT hint of a chunk whose own language is
// certain; an uncertain chunk language sends no hint at all (TXT-04). Asserted on the
// multipart body the transcription fake receives.
// accept: TXT-13#2
func TestLangMarkSTTHintInMultipartBody(t *testing.T) {
	for _, tc := range []struct {
		in, said string
		want     []string
	}{
		{"東京へ行きます、{{lang:fr|bonjour}}。", "東京へ行きます、bonjour。", []string{"ja", "fr"}},
		{"Hello {{lang:fr|bonjour}} there.", "Hello bonjour there.", nil},
	} {
		var mu sync.Mutex
		var got [][]string
		hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseMultipartForm(32 << 20); err != nil {
				t.Errorf("multipart: %v", err)
			}
			mu.Lock()
			got = append(got, r.MultipartForm.Value["languages[]"])
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			b, _ := json.Marshal(map[string]any{"text": tc.said, "languages": []map[string]string{}})
			_, _ = w.Write(b)
		}))
		e := newSayEnv(t)
		e.newTr = func(creds *auth.Credentials) stt.Transcriber {
			hc, base := testClient(t, hs.URL)
			return &stt.HTTP{Client: hc, BaseURL: base, Creds: creds}
		}
		r := e.run(tc.in, "--marks", "-o", "a.wav")
		hs.Close()
		if r.code != exitcode.OK {
			t.Fatalf("%s: %+v", tc.in, r)
		}
		mu.Lock()
		if len(got) == 0 || !reflect.DeepEqual(got[0], tc.want) {
			t.Errorf("%s: languages[] %v, want %v", tc.in, got, tc.want)
		}
		mu.Unlock()
	}
}
