package text

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/xiaolai/recite/internal/spoken"
)

func testReader(m Mark) (string, string) {
	switch m.Kind {
	case MarkSay:
		return m.Value, ""
	case MarkNum:
		return "", "cannot be read as a number"
	}
	return m.Text, ""
}

// One chunk per paragraph, cut again only at a pause; the display comment, the join and the
// marks carry through, and the guard and spoken-form rules never run ("5kg" stays).
func TestParseSpokenScript(t *testing.T) {
	in := `#| recite: spoken script

#| display: "Hello, world."
#| finding: 1:1 G-EMOJI remove
Hello world, it weighs 5kg.

#| display: "Read the API now."
#| join: sentence
Read the {{say:A P I|API}} now.{{pause:800ms}}

Second part {{emph|here}}.
It continues {{pause:1.2s}} after a pause.`
	doc, err := ParseSpokenScript(in, true, testReader, 1)
	if err != nil {
		t.Fatal(err)
	}
	type want struct {
		text, display string
		b             Boundary
	}
	wants := []want{
		{"Hello world, it weighs 5kg.", "Hello, world.", BoundaryParagraph},
		{"Read the A P I now.", "Read the API now.", BoundaryPause},
		{"Second part here.\nIt continues", "Second part here.\nIt continues", BoundaryPause},
		{"after a pause.", "after a pause.", BoundaryEnd},
	}
	if len(doc.Seg.Chunks) != len(wants) {
		t.Fatalf("%d chunks: %+v", len(doc.Seg.Chunks), doc.Seg.Chunks)
	}
	for i, w := range wants {
		c := doc.Seg.Chunks[i]
		if c.Text != w.text || c.DisplayText != w.display || c.Boundary != w.b {
			t.Errorf("chunk %d: %q / %q / %s, want %+v", i, c.Text, c.DisplayText, c.Boundary, w)
		}
		if doc.Spoken[c.Spoken.Start:c.Spoken.End] != c.Text || doc.Display[c.Display.Start:c.Display.End] != c.DisplayText {
			t.Errorf("chunk %d ranges do not match its text", i)
		}
		if c.Seconds != EstimateSeconds(c.Text, 1) || c.Seconds <= 0 {
			t.Errorf("chunk %d seconds %v", i, c.Seconds)
		}
	}
	if len(doc.Pauses) != 2 || doc.Pauses[0] != 800*time.Millisecond || doc.Pauses[1] != 1200*time.Millisecond {
		t.Fatalf("pauses %v", doc.Pauses)
	}
	if len(doc.Marks) != 2 || doc.Spoken[doc.Marks[0].Spoken.Start:doc.Marks[0].Spoken.End] != "A P I" ||
		doc.Spoken[doc.Marks[1].Spoken.Start:doc.Marks[1].Spoken.End] != "here" {
		t.Fatalf("marks %+v", doc.Marks)
	}
	// With marks off the braces are text.
	doc, err = ParseSpokenScript("Say {{spell|X}}.", false, testReader, 1)
	if err != nil || doc.Seg.Chunks[0].Text != "Say {{spell|X}}." {
		t.Fatalf("marks off: %+v %v", doc.Seg.Chunks, err)
	}
}

// The estimate of an unsplit block equals the one Segment gives it.
func TestEstimateSecondsMatchesSegment(t *testing.T) {
	for _, s := range []string{"Hello there, world.", "你好，世界。Hello.", "Mixed 中文 and English text, 123.", "R, U, R prime, U — and then R'."} {
		seg, err := Segment(spoken.Result{Display: s, Spoken: s, Segments: []spoken.Segment{{Display: spoken.Range{End: len(s)}, Spoken: spoken.Range{End: len(s)}}}},
			SegmentOptions{Budget: 1000, Speed: 1.25})
		if err != nil || len(seg.Chunks) != 1 {
			t.Fatal(err, seg)
		}
		if got := EstimateSeconds(s, 1.25); got != seg.Chunks[0].Seconds {
			t.Errorf("%q: %v, Segment %v", s, got, seg.Chunks[0].Seconds)
		}
	}
}

// A capital letter standing alone is spelled, and spoken as its name: "U" is "you", not one
// character's worth of sound. Counted as one character, a move list like "R, U, R prime, U" was
// estimated at a third of its length, and every take of it failed the duration guard (2.5×–2.9×).
// "A" and "I" are words, a letter inside a word is not spelled, and a lower-case letter is not.
func TestSpelledLettersCostTheirNames(t *testing.T) {
	per := 1 / SpeechRate(ScriptLatin)
	extra := func(s string) float64 { return EstimateSeconds(s, 1) - float64(utf8.RuneCountInString(s))*per }
	near := func(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }
	for s, letters := range map[string]int{
		"R, U, R prime, U":       4,
		"Turn R' then F2.":       1, // R' is spelled; F2 is a token the letter does not stand alone in
		"X marks the spot.":      1,
		"I think A is a letter.": 0,
		"a b c":                  0,
		"RUF and DRUL are words": 0,
		"Ünter Ölberg":           0,
	} {
		if got, want := extra(s), float64(letters*(spelledLetterChars-1))*per; !near(got, want) {
			t.Errorf("%q: %.3fs beyond its characters, want %.3fs (%d spelled letters)", s, got, want, letters)
		}
	}
	// Measured, not derived: three takes of this line (a children's lesson, 2026-10-14, voice coral)
	// ran 8.70s, 8.84s and 9.80s, all saying it word for word. The guard allows 2.2× the estimate,
	// so the estimate must reach the longest of them over 2.2. Counted as characters it was 3.43s.
	if est, longest := EstimateSeconds("R, U, R prime, U — and then R, U, U, R prime, U.", 1), 9.80; longest/est > 2.2 {
		t.Errorf("estimate %.2fs: the longest measured take is %.2f× it, past the guard's 2.2", est, longest/est)
	}
}

// A pause mark is where a reader pauses: a run of them ("?!", "...") is one pause, and a mark
// inside a token ("3.5", "e.g") is still counted, which only ever allows a take more time.
func TestPauseMarks(t *testing.T) {
	for s, want := range map[string]int{
		"D, R, F prime.":          3,
		"R. U. R prime. U prime.": 4,
		"Wait... what?!":          2,
		"No pause here":           0,
		"A dash — then a stop.":   2,
		"你好，世界。":                  2,
	} {
		if got := PauseMarks(s); got != want {
			t.Errorf("PauseMarks(%q) = %d, want %d", s, got, want)
		}
	}
}

func TestParseSpokenScriptErrors(t *testing.T) {
	cases := []struct{ in, want string }{
		{"#| colour: red\nHi.", `line 1: unknown comment key "colour"`},
		{"#| display: Hi.\nHi.", "line 1: display Hi.: want a quoted string"},
		{"#| display: \"a\"\n#| display: \"b\"\nHi.", "line 2: a second display comment"},
		{"#| join: pause\nHi.", `line 1: join "pause": want one of`},
		{"#| no colon here\nHi.", "line 1: comment"},
		{"Hi.\n\n#| display: \"x\"", "line 3: a display or join comment with no text"},
		{"Fine.\n\nRead {{spel|API}}.", `line 3: G-MARK: unknown mark kind "spel"`},
		{"Read {{num|abc}}.", "line 1: N-AMBIGUOUS"},
		{"{{pause:1s}} Hi.", "line 1: a pause before any text"},
		{"Hi.{{pause:1s}}", "a pause after the last text"},
	}
	for _, c := range cases {
		_, err := ParseSpokenScript(c.in, true, testReader, 1)
		var se *ScriptError
		if !errors.As(err, &se) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v, want %q", c.in, err, c.want)
		}
	}
	if _, err := ParseSpokenScript("#| recite: header only\n", true, testReader, 1); !errors.Is(err, ErrNothingToSpeak) {
		t.Errorf("empty script: %v", err)
	}
}

func TestCheckScriptText(t *testing.T) {
	for in, want := range map[string]string{
		"fine text":     "",
		"a\n#| b":       "starts with",
		"a\n \nb":       "blank line",
		"x {{spell|y}}": "speech mark",
	} {
		if got := CheckScriptText(in, true); (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("%q: %q, want ~%q", in, got, want)
		}
	}
}
