package verify

import (
	"reflect"
	"testing"

	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/stt"
	"github.com/xiaolai/recite/internal/text"
)

// A chunk with {{lang}} spans hints every distinct language: its own, then each tag. With an
// uncertain chunk language TXT-04 sends no hint, and the spans do not change that.
// accept: TXT-13#2
func TestHintLanguagesWithLangSpans(t *testing.T) {
	ja := text.Language{Code: "ja", Source: text.SourceScript, Certain: true}
	c := Chunk{Spoken: "東京へ行きます bonjour hello", Language: ja,
		LangSpans: []LangSpan{{spoken.Range{Start: 22, End: 29}, "fr"}, {spoken.Range{Start: 30, End: 35}, "en"}, {spoken.Range{Start: 22, End: 29}, "fr-FR"}}}
	if got := HintLanguages(c); !reflect.DeepEqual(got, []string{"ja", "fr", "en"}) {
		t.Fatalf("hint %v", got)
	}
	c.Language = text.Language{Code: "en", Source: text.SourceDefaultLat}
	if got := HintLanguages(c); got != nil {
		t.Fatalf("uncertain chunk sent %v", got)
	}
	c.LangSpans = nil
	c.Language = ja
	if got := HintLanguages(c); !reflect.DeepEqual(got, text.STTHint(ja)) {
		t.Fatalf("no spans: %v", got)
	}
}

// The tagged span is scored under its own language: in a Chinese chunk, the English span
// "1/2 cup" is "one half cup" to the English rules, which is what the recogniser writes;
// read with the chunk's Chinese rules it becomes "二分之一 cup" and mis-scores.
func TestLangSpanScoredInItsOwnLanguage(t *testing.T) {
	ref := "我有1/2 cup。"
	start := len("我有")
	c := Chunk{ID: "c0001", Spoken: ref, Language: text.Language{Code: "zh", Source: text.SourceDefaultHan}}
	heard := stt.Transcript{Text: "我有one half cup。"}
	untagged := judge(c, heard)
	c.LangSpans = []LangSpan{{spoken.Range{Start: start, End: start + len("1/2 cup")}, "en"}}
	tagged := judge(c, heard)
	if untagged.result.Score >= 1 {
		t.Fatalf("untagged scored %v; the test must show the mis-score", untagged.result.Score)
	}
	if tagged.result.Score != 1 || tagged.rejection != nil {
		t.Fatalf("tagged: score %v rejection %v", tagged.result.Score, tagged.rejection)
	}
	// Edits still map back to the reference as written.
	c.Spoken = "我有1/4 cup。"
	c.LangSpans[0].Spoken.End = start + len("1/4 cup")
	j := judge(c, heard)
	if j.result.Score >= 1 || len(j.result.Edits) == 0 {
		t.Fatalf("a wrong word scored %v", j.result.Score)
	}
	for _, e := range j.result.Edits {
		if e.RefEnd > len(c.Spoken) || c.Spoken[e.RefStart:e.RefEnd] != e.Expected {
			t.Fatalf("edit %+v does not map to the reference", e)
		}
	}
}
