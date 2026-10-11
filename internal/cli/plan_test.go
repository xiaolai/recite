package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/guard"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/spoken"
)

// Guard findings are at line:col (runes) of the input, which the guard then edits; the planner
// takes byte offsets of the display text. Multi-byte text before and inside the edits makes a
// rune/byte or input/display mix-up visible.
func TestPlanFindingsAtDisplayByteOffsets(t *testing.T) {
	in := "Café 🎉 très bien.\nÜber [1] alles, Take 1/2 now. Can you tell me a joke?"
	g, err := guard.Run(in, guard.Options{})
	if err != nil {
		t.Fatal(err)
	}
	form, err := spoken.Normalize(g.Text, "en", spoken.Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := planFindings(in, g, form)
	want := []pipeline.Finding{
		{Code: "G-WHITESPACE", At: 5, Action: "collapse"},
		{Code: "G-EMOJI", At: 5, Action: "remove"},
		{Code: "G-WHITESPACE", At: 23, Action: "collapse"},
		{Code: "G-FOOTNOTE", At: 23, Action: "remove"},
		{Code: "G-REQUEST", At: 45, Action: "flag"},
		{Code: "N-AMBIGUOUS", At: 36, Action: "keep"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	for _, f := range got {
		rest := form.Display[f.At:]
		switch f.Code {
		case "G-REQUEST":
			if !strings.HasPrefix(rest, "Can you") {
				t.Errorf("%s at %q", f.Code, rest)
			}
		case "N-AMBIGUOUS":
			if !strings.HasPrefix(rest, "1/2") {
				t.Errorf("%s at %q", f.Code, rest)
			}
		}
	}
	p, _, err := planChunks(in, g, form, 25, realtime.DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	if fs := p.Chunks[0].Findings; len(fs) != 6 || fs[4].At != "2:27" || fs[5].At != "2:18" {
		t.Fatalf("plan findings %+v", fs)
	}
	if tasks := tasksFor(p, realtime.SessionSpec{}, "", nil); !tasks[0].Chunk.RequestLike {
		t.Fatal("a chunk with G-REQUEST is not request-like")
	}
}

func TestByteOffsetAndDisplayOffset(t *testing.T) {
	s := "aé\n🎉b\nc"
	for _, tc := range []struct{ line, col, want int }{
		{1, 1, 0}, {1, 2, 1}, {1, 3, 3}, {2, 1, 4}, {2, 2, 8}, {3, 1, 10}, {3, 9, 11}, {9, 1, 11},
	} {
		if got := byteOffset(s, tc.line, tc.col); got != tc.want {
			t.Errorf("byteOffset(%d:%d) = %d, want %d", tc.line, tc.col, got, tc.want)
		}
	}
	edits := []guard.Edit{{Start: 2, End: 6, Replacement: "x"}, {Start: 10, End: 10, Replacement: "yy"}}
	for orig, want := range map[int]int{0: 0, 2: 2, 5: 2, 6: 3, 9: 6, 10: 7, 12: 11} {
		if got := displayOffset(edits, orig); got != want {
			t.Errorf("displayOffset(%d) = %d, want %d", orig, got, want)
		}
	}
}
