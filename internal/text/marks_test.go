package text

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// Every mark kind of design §7.8 parses to its kind, value and text; String gives the
// syntax back.
// accept: TXT-13#1
func TestParseMarksGoldenPerKind(t *testing.T) {
	cases := []struct {
		in   string
		want Mark
	}{
		{"{{say:twenty twenty-six|2026}}", Mark{Kind: MarkSay, Value: "twenty twenty-six", Text: "2026"}},
		{"{{spell|API}}", Mark{Kind: MarkSpell, Text: "API"}},
		{"{{digits|2026}}", Mark{Kind: MarkDigits, Text: "2026"}},
		{"{{num|1/2}}", Mark{Kind: MarkNum, Text: "1/2"}},
		{"{{date|10/11}}", Mark{Kind: MarkDate, Text: "10/11"}},
		{"{{fraction|1/2}}", Mark{Kind: MarkFraction, Text: "1/2"}},
		{"{{verbatim|R2-D2}}", Mark{Kind: MarkVerbatim, Text: "R2-D2"}},
		{"{{pause:800ms}}", Mark{Kind: MarkPause, Pause: 800 * time.Millisecond}},
		{"{{hint:oh-KAH-for|Okafor}}", Mark{Kind: MarkHint, Value: "oh-KAH-for", Text: "Okafor"}},
		{"{{emph|never}}", Mark{Kind: MarkEmph, Text: "never"}},
		{"{{lang:fr|bonjour}}", Mark{Kind: MarkLang, Value: "fr", Text: "bonjour"}},
		{"{{say:a: b|x|y}}", Mark{Kind: MarkSay, Value: "a: b", Text: "x|y"}}, // first '|' and ':' split
	}
	if len(markSyntax) != 11 {
		t.Fatalf("%d mark kinds, the golden covers 11", len(markSyntax))
	}
	for _, c := range cases {
		src := "Before " + c.in + " after."
		ms, bad := ParseMarks(src)
		if len(bad) != 0 || len(ms) != 1 {
			t.Fatalf("%s: marks %+v bad %+v", c.in, ms, bad)
		}
		got := ms[0]
		c.want.Start, c.want.End = 7, 7+len(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.in, got, c.want)
		}
		if again, _ := ParseMarks(got.String()); len(again) != 1 || again[0].Kind != got.Kind || again[0].Value != got.Value ||
			again[0].Text != got.Text || again[0].Pause != got.Pause {
			t.Errorf("%s: String %q does not parse back", c.in, got.String())
		}
	}
}

// A mark inside a mark is rejected as a whole, and every malformed shape is a BadMark whose
// plain text carries no brace.
// accept: TXT-13#1
func TestParseMarksMalformed(t *testing.T) {
	cases := []struct {
		in, reason, plain string
	}{
		{"{{say:x|{{spell|A}}}}", "nested", "A"},
		{"{{spell|{{digits|12}} B}}", "nested", "12 B"},
		{"{{spel|API}}", "unknown mark kind", "API"},
		{"{{say|2026}}", "needs a value", "2026"},
		{"{{say:x}}", "needs a text", "say:x"},
		{"{{spell:x|API}}", "takes no value", "API"},
		{"{{spell}}", "needs a text", "spell"},
		{"{{spell| }}", "needs a text", " "},
		{"{{pause:soon}}", "want a duration", "pause:soon"},
		{"{{pause:0s}}", "want a duration", "pause:0s"},
		{"{{pause:11s}}", "want a duration", "pause:11s"},
		{"{{pause:1s|x}}", "takes no text", "x"},
		{"{{lang:not a tag!|bonjour}}", "BCP 47", "bonjour"},
		{"{{}}", "unknown mark kind", ""},
	}
	for _, c := range cases {
		ms, bad := ParseMarks("a " + c.in + " b")
		if len(ms) != 0 || len(bad) != 1 {
			t.Fatalf("%s: marks %+v bad %+v", c.in, ms, bad)
		}
		b := bad[0]
		if !strings.Contains(b.Reason, c.reason) || b.Plain != c.plain || b.Start != 2 || b.End != 2+len(c.in) {
			t.Errorf("%s: %+v, want reason ~%q plain %q", c.in, b, c.reason, c.plain)
		}
	}
	// Unclosed on its line: only the opening braces are bad, and the rest still parses.
	ms, bad := ParseMarks("x {{spell|A\n{{emph|B}}")
	if len(bad) != 1 || bad[0].Start != 2 || bad[0].End != 4 || !strings.Contains(bad[0].Reason, "unclosed") {
		t.Fatalf("unclosed: %+v", bad)
	}
	if len(ms) != 1 || ms[0].Kind != MarkEmph {
		t.Fatalf("after an unclosed mark: %+v", ms)
	}
	// A lone }} and single braces are text.
	if ms, bad := ParseMarks("a }} b {x} c"); len(ms)+len(bad) != 0 {
		t.Fatalf("plain braces: %+v %+v", ms, bad)
	}
}
