package verify

import (
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/stt"
	"github.com/xiaolai/recite/internal/text"
)

// --no-trim only keeps the silence in the output: the empty and duration guards still judge
// the trimmed audio, so a completed take of pure silence is empty either way.
func TestPreSTTJudgesTrimmedAudioUnderNoTrim(t *testing.T) {
	const spokenText = "Hello there."
	silence := audio.PCM(make([]byte, 2*audio.SampleRate)) // one second of silence
	c := Chunk{ID: "c1", Spoken: spokenText, Estimate: silence.Duration()}
	take := realtime.Take{Status: realtime.StatusCompleted, Said: spokenText, PCM: silence}
	for _, noTrim := range []bool{false, true} {
		pcm, rej := PreSTT(c, take, noTrim)
		if rej == nil || rej.Reason != ReasonEmpty {
			t.Errorf("noTrim=%v: rejection %+v, want %s", noTrim, rej, ReasonEmpty)
		}
		if noTrim && len(pcm) != len(silence) {
			t.Errorf("noTrim: kept %d bytes, want the untrimmed %d", len(pcm), len(silence))
		}
	}
}

// Lexicon matching ignores case without moving offsets: "İ" lowercases to one byte fewer, and
// the range must still cover "api", not part of "the".
func TestProtectedRangesKeepOffsetsUnderCaseFolding(t *testing.T) {
	c := Chunk{Spoken: "İİİ the api", Lexicon: []string{"API"}}
	want := strings.Index(c.Spoken, "api")
	found := false
	for _, r := range protectedRanges(c) {
		if c.Spoken[r[0]:r[1]] == "api" && r[0] == want {
			found = true
		}
		if c.Spoken[r[0]:r[1]] == "the" || (r[0] < want && want < r[1]) {
			t.Errorf("range %v covers %q", r, c.Spoken[r[0]:r[1]])
		}
	}
	if !found {
		t.Errorf("no range on api: %v", protectedRanges(c))
	}
}

// Two substitutions that cross over a matched word reverse the meaning ("to him from" heard
// "from him to"): the word-order guard rejects them, while the neutral cases stay accepted.
func TestTranspositionCrossedOverMatch(t *testing.T) {
	ref := strings.Join(fillerWords[:57], " ") + " to him from"
	_, al := score(ref, strings.Join(fillerWords[:57], " ")+" from him to", text.MetricWER)
	exp, heard, ok := transposition(al, text.MetricWER)
	if !ok || exp != "to him from" || heard != "from him to" {
		t.Errorf("got %q / %q, %v", exp, heard, ok)
	}
	for _, hyp := range []string{
		strings.Join(fillerWords[:57], " ") + " to him from", // identical
		strings.Join(fillerWords[:57], " ") + " at him by",   // two plain substitutions
		strings.Join(fillerWords[:57], " ") + " from him at", // one crossed half only
	} {
		_, al := score(ref, hyp, text.MetricWER)
		if e, h, ok := transposition(al, text.MetricWER); ok {
			t.Errorf("%q: false word-order %q / %q", hyp, e, h)
		}
	}
	// Past the window, two swapped words are no longer neighbours.
	far := "to river garden yellow window from"
	_, al = score(far, "from river garden yellow window to", text.MetricWER)
	if e, h, ok := transposition(al, text.MetricWER); ok {
		t.Errorf("beyond the window: %q / %q", e, h)
	}
}

var fillerWords = strings.Fields(`river garden yellow window travel market silver morning
	teacher pocket orange castle bridge winter forest kitchen letter mountain painter island
	harbor village candle feather ladder blanket lantern meadow saddle thunder pepper rocket
	basket button carpet dragon engine finger glacier hammer jacket kettle lemon magnet napkin
	oyster parrot quilt rabbit salmon tunnel umbrella velvet wagon walnut zipper anchor bottle`)

// When a lexicon equivalent rewrote the transcript, the recorded edits still point into the
// transcript as STT wrote it, and the confidences use those offsets.
func TestJudgeReportsHeardOffsetsInOriginalTranscript(t *testing.T) {
	c := Chunk{
		Spoken:      "Write the sequel query and run it fast now.",
		Language:    text.Resolve("Write the sequel query and run it fast now.", text.ResolveOptions{}),
		Equivalents: []SayPair{{Written: "SQL", Spoken: "sequel"}},
	}
	tr := stt.Transcript{Text: "Write the SQL query and run it slow now."}
	j := judge(c, tr)
	if len(j.result.Edits) != 1 {
		t.Fatalf("edits %+v", j.result.Edits)
	}
	e := j.result.Edits[0]
	if got := tr.Text[e.HeardStart:e.HeardEnd]; got != "slow" || e.Heard != "slow" {
		t.Errorf("heard span %q, Heard %q, want slow", got, e.Heard)
	}
	toks := []stt.TokenLogprob{}
	for _, w := range strings.SplitAfter(tr.Text, " ") {
		lp := 0.0
		if strings.TrimSpace(w) == "slow" {
			lp = -3
		}
		toks = append(toks, stt.TokenLogprob{Token: w, Logprob: lp})
	}
	tr.Tokens = toks
	conf := confidences(tr, j.result.Edits)
	if len(conf) != 1 || !conf[0].Low {
		t.Errorf("confidences %+v, want slow low", conf)
	}
}

// A spelled code keeps its exact letters: {{spell|GREY}} heard "G R A Y" is a wrong letter and
// a protected-token rejection, though grey and gray are one word when spoken as words.
func TestSpelledLettersAreNotFolded(t *testing.T) {
	sp := "Type the code G R E Y now please."
	start := strings.Index(sp, "G R E Y")
	c := Chunk{
		Spoken:    sp,
		Language:  text.Resolve(sp, text.ResolveOptions{}),
		Protected: []spoken.Range{{Start: start, End: start + len("G R E Y")}},
	}
	j := judge(c, stt.Transcript{Text: "Type the code G R A Y now please."})
	if j.rejection == nil || j.rejection.Reason != ReasonProtected {
		t.Errorf("rejection %+v, want %s (edits %+v)", j.rejection, ReasonProtected, j.result.Edits)
	}
	for _, heard := range []string{"Type the code G R E Y now please.", "Type the code GREY now please.", "Type the code G-R-E-Y now please."} {
		if j := judge(c, stt.Transcript{Text: heard}); j.rejection != nil || j.result.Score != 1 {
			t.Errorf("%q: rejection %+v, score %v", heard, j.rejection, j.result.Score)
		}
	}
	// Spoken as a word, grey still folds to gray.
	if r := Score("The sky is grey today.", "The sky is gray today.", text.MetricWER); r.Errors != 0 {
		t.Errorf("word grey: %d errors", r.Errors)
	}
}

// Under CER (a Han-dominant chunk) a protected spelled code accepts its letters spaced,
// hyphenated or compact, and rejects a wrong letter.
func TestSpelledLettersInHanChunk(t *testing.T) {
	sp := "请输入代码 G R E Y 然后按确认键。"
	start := strings.Index(sp, "G R E Y")
	c := Chunk{
		Spoken:    sp,
		Language:  text.Resolve(sp, text.ResolveOptions{}),
		Protected: []spoken.Range{{Start: start, End: start + len("G R E Y")}},
	}
	if m := ScriptMetric(sp); m != text.MetricCER {
		t.Fatalf("metric %s, want CER", m)
	}
	for _, heard := range []string{"请输入代码 G R E Y 然后按确认键。", "请输入代码 GREY 然后按确认键。", "请输入代码 G-R-E-Y 然后按确认键。"} {
		if j := judge(c, stt.Transcript{Text: heard}); j.rejection != nil || j.result.Score != 1 {
			t.Errorf("%q: rejection %+v, score %v", heard, j.rejection, j.result.Score)
		}
	}
	for _, heard := range []string{"请输入代码 GRAY 然后按确认键。", "请输入代码 G R A Y 然后按确认键。"} {
		if j := judge(c, stt.Transcript{Text: heard}); j.rejection == nil || j.rejection.Reason != ReasonProtected {
			t.Errorf("%q: rejection %+v, want %s", heard, j.rejection, ReasonProtected)
		}
	}
}

// A Han lexicon key is protected inside a run of Han text, where no word boundary exists; a
// Latin key still needs whole-word edges.
func TestLexiconProtectsEmbeddedHanKey(t *testing.T) {
	sp := "请于先生稍后到客厅等候我们大家都准备好再去庭院看花。"
	c := Chunk{Spoken: sp, Language: text.Resolve(sp, text.ResolveOptions{}), Lexicon: []string{"于"}}
	j := judge(c, stt.Transcript{Text: strings.Replace(sp, "于", "以", 1)})
	if j.rejection == nil || j.rejection.Reason != ReasonProtected {
		t.Errorf("rejection %+v, want %s", j.rejection, ReasonProtected)
	}
	en := Chunk{Spoken: "we concatenate files", Lexicon: []string{"cat"}}
	if got := protectedRanges(en); len(got) != 0 {
		t.Errorf("cat inside concatenate protected: %v", got)
	}
}

// A transcript too long to align is rejected as unverifiable, never scored as a match.
func TestJudgeRejectsUnalignableTranscript(t *testing.T) {
	sp := strings.Repeat("alpha ", 10_000)
	c := Chunk{Spoken: sp, Language: text.Language{Code: "en"}}
	j := judge(c, stt.Transcript{Text: strings.Repeat("beta ", 5_000)})
	if j.rejection == nil || j.rejection.Reason != ReasonScore || !strings.Contains(j.rejection.Detail, "unverifiable") {
		t.Errorf("rejection %+v, want an unverifiable score rejection", j.rejection)
	}
}
