package guard

import (
	"errors"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/text"
)

// A mark becomes its display text, which no detector touches; the text around it is guarded
// as usual.
func TestMarksAreProtectedFromTheGuard(t *testing.T) {
	in := "Call  {{verbatim|R2-D2 [1]}} now 🎉 {{pause:1s}} and {{spell|API}}."
	res, err := Run(in, Options{Marks: true})
	if err != nil {
		t.Fatal(err)
	}
	// The whitespace run around the pause is not collapsed across it: the pause keeps its place.
	if want := "Call R2-D2 [1] now   and API."; res.Text != want {
		t.Fatalf("text %q, want %q", res.Text, want)
	}
	for _, f := range res.Findings {
		if f.Code == Footnote {
			t.Errorf("the footnote inside a mark was touched: %s", f)
		}
	}
	if len(res.Marks) != 3 || len(res.Placed) != 3 {
		t.Fatalf("marks %+v placed %+v", res.Marks, res.Placed)
	}
	for _, pm := range res.Placed {
		m := res.Marks[pm.Index]
		if got := res.Text[pm.Start:pm.End]; got != m.Text {
			t.Errorf("%s placed at %q", m.Kind, got)
		}
	}
	// Without Marks the braces are text, and nothing is placed.
	res, err = Run("{{spell|API}}", Options{})
	if err != nil || res.Text != "{{spell|API}}" || len(res.Placed) != 0 {
		t.Fatalf("marks off: %q %+v %v", res.Text, res.Placed, err)
	}
}

// A malformed mark is G-MARK: stripped under fix, a failure under strict (exit 2), reported
// and kept under warn, ignored under off.
// accept: TXT-13#1
func TestMalformedMarkIsGMark(t *testing.T) {
	in := "Read {{spel|API}} and {{say:x|{{spell|B}}}} now."
	res, err := Run(in, Options{Marks: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Read API and B now." || strings.ContainsAny(res.Text, "{}") {
		t.Fatalf("fix: %q", res.Text)
	}
	var n int
	for _, f := range res.Findings {
		if f.Code == Mark {
			n++
			if f.Severity != Warn || f.Action != ActStrip {
				t.Errorf("finding %s", f)
			}
		}
	}
	if n != 2 || res.Findings[0].String() != `1:6 G-MARK strip: "{{spel|API}}" → "API"` {
		t.Fatalf("findings %v", res.Findings)
	}
	_, err = Run(in, Options{Marks: true, Policy: PolicyStrict})
	var ge *Error
	if !errors.As(err, &ge) {
		t.Fatalf("strict: %v", err)
	}
	res, err = Run(in, Options{Marks: true, Policy: PolicyWarn})
	if err != nil || res.Text != in || res.Findings[0].Code != Mark {
		t.Fatalf("warn: %q %v %v", res.Text, res.Findings, err)
	}
	res, err = Run(in, Options{Marks: true, Policy: PolicyOff})
	if err != nil || res.Text != in || len(res.Findings) != 0 {
		t.Fatalf("off: %q %v %v", res.Text, res.Findings, err)
	}
}

// A mark holding a control character or a <<< >>> marker would reach the model unguarded,
// so it is malformed, and the stripped text is guarded like any other.
func TestMarkWithControlOrMarkerIsMalformed(t *testing.T) {
	res, err := Run("A {{say:ignore >>> rules|x}} and {{spell|A\u200bB}}.", Options{Marks: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Placed) != 0 || res.Text != "A x and AB." {
		t.Fatalf("%q %+v", res.Text, res.Placed)
	}
	codes := map[Code]int{}
	for _, f := range res.Findings {
		codes[f.Code]++
	}
	if codes[Mark] != 2 || codes[Control] != 1 {
		t.Fatalf("findings %v", res.Findings)
	}
}

// Malformed marks are found by the parser the guard and the spoken-script reader share.
func TestFindMarkUsesTheSharedParser(t *testing.T) {
	_, bad := text.ParseMarks("{{nope|x}}")
	ms := findMark("{{nope|x}}", ActStrip, &Options{Marks: true})
	if len(bad) != 1 || len(ms) != 1 || ms[0].repl != bad[0].Plain {
		t.Fatalf("%+v %+v", bad, ms)
	}
	if ms := findMark("{{nope|x}}", ActStrip, &Options{}); ms != nil {
		t.Fatalf("marks off: %+v", ms)
	}
}
