package verify

import (
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/stt"
	"github.com/xiaolai/recite/internal/text"
)

func judgeChunk(spoken string) Chunk {
	return Chunk{ID: "c", Spoken: spoken, Language: text.Resolve(spoken, text.ResolveOptions{}), Estimate: time.Second}
}

func judged(spoken, heard string) judgement {
	return judge(judgeChunk(spoken), stt.Transcript{Text: heard})
}

func TestSpellingVariantsJudgedEqual(t *testing.T) {
	for _, p := range [][2]string{
		{"The harbour master counted the boats.", "The harbormaster counted the boats."},
		{"The harbour-master counted the boats.", "The harbor master counted the boats."},
		{"She painted it a different colour.", "She painted it a different color."},
		{"Our neighbours were travelling to the theatre.", "Our neighbors were traveling to the theater."},
		{"They organised a grey parade in the centre.", "They organized a gray parade in the center."},
		{"A weathervane stood on the roof.", "A weather vane stood on the roof."},
		{"The coastguard rescued them.", "The Coast Guard rescued them."},
		{"Everyone complained about the draught.", "Everyone complained about the draft."},
	} {
		j := judged(p[0], p[1])
		if j.rejection != nil || len(j.content) != 0 || j.result.Score != 1 {
			t.Errorf("%q / %q: rejection %v, content %v, score %.4f; want an exact match", p[0], p[1], j.rejection, j.content, j.result.Score)
		}
	}
}

// The table is enumerated, so words that merely end like a variant are left alone.
func TestSpellingVariantsBoundary(t *testing.T) {
	for _, p := range [][2]string{
		{"We waited an hour by the river.", "We waited an hor by the river."},
		{"They sold our boat at noon.", "They sold or boat at noon."},
		{"The harbour was quiet.", "The harbor master was quiet."}, // a word added, not a compound
		{"The master of the harbour left.", "The harbormaster left."},
		{"The tour ended at noon.", "The tor ended at noon."},
	} {
		if j := judged(p[0], p[1]); j.result.Score == 1 {
			t.Errorf("%q / %q compared equal; want a mismatch", p[0], p[1])
		}
	}
	for _, w := range []string{"four", "for", "hour", "our", "your", "tour", "pour", "flour", "metre", "centre"} {
		got := foldVariants([]CanonToken{{Text: w}})[0].Text
		if keep := w == "metre" || w == "centre"; keep == (got == w) {
			t.Errorf("fold(%q) = %q", w, got)
		}
	}
}

func TestSpellingVariantsMixedLanguage(t *testing.T) {
	for _, p := range [][2]string{
		{"他说这个 colour 很好看，就像 harbour master 的船。", "他说这个 color 很好看，就像 harbormaster 的船。"},
		{"The sign by the harbour said 你好 in red colour.", "The sign by the harbor said 你好 in red color."},
	} {
		j := judged(p[0], p[1])
		if j.rejection != nil || len(j.content) != 0 || j.result.Score != 1 {
			t.Errorf("%q / %q: rejection %v, content %v, score %.4f", p[0], p[1], j.rejection, j.content, j.result.Score)
		}
	}
}

func TestHanPronounFold(t *testing.T) {
	j := judged("她说那里的土是冷的，它不会变。", "他说那里的土是冷的，他不会变。")
	if j.rejection != nil || j.result.Score != 1 {
		t.Fatalf("她/它 heard as 他: rejection %v, score %.4f", j.rejection, j.result.Score)
	}
	// Only the pronouns fold: 的/地/得 are read alike too but checked as written.
	if j := judged("他慢慢地走回家去了。", "他慢慢的走回家去了。"); j.result.Score == 1 {
		t.Fatal("地 heard as 的 compared equal")
	}
	// A name homophone stays a mismatch.
	if j := judged("云溪县的人都认得他。", "云西县的人都认得他。"); len(j.content) == 0 {
		t.Fatal("云溪 heard as 云西 left no content mismatch")
	}
}

func TestWordOrderGuard(t *testing.T) {
	const s14 = "In the spring of that year the council voted to rebuild the old granary, though only four of the nine members had ever seen it full."
	const s40 = "Professor Hale believed that every map was a kind of argument, and he made his students redraw the county from memory before they saw a real one."
	const s058 = "Ivy Coulthard walked her three dogs along the cliff path every morning, and the dogs knew the way so well that she sometimes simply followed them."
	rejects := []struct{ name, spoken, heard string }{
		{"function words swapped (s14)", s14, "In the spring of that year the council voted to rebuild the old granary, though only four the of nine members had ever seen it full."},
		{"function word moved past a content word (s40)", s40, "Professor Hale believed that every map was a of kind argument, and he made his students redraw the county from memory before they saw a real one."},
		{"function and content word swapped (s058)", s058, "Ivy Coulthard walked her three dogs along the cliff path every morning, and the dogs the knew way so well that she sometimes simply followed them."},
		{"content words swapped", "The gossip had only just begun that morning.", "The gossip had just only begun that morning."},
		{"Han particle moved", "他当场红了眼眶，什么也没说。", "他当场红眼眶了，什么也没说。"},
		{"Han adjacent swap", "他一句话也没说就走了。", "他一句话没也说就走了。"},
	}
	for _, tc := range rejects {
		j := judged(tc.spoken, tc.heard)
		if j.rejection == nil || j.rejection.Reason != ReasonWordOrder {
			t.Errorf("%s: rejection %v, want %s", tc.name, j.rejection, ReasonWordOrder)
		}
	}
	keeps := []struct{ name, spoken, heard string }{
		{"identical repeats", "the dogs the dogs ran home", "the dogs the dogs ran home"},
		{"a repeat dropped", "the dogs the dogs ran home", "dogs the dogs ran home"},
		{"a stutter", "the dogs ran home to the farm", "the the dogs ran home to the farm"},
		{"one word substituted", "the dogs ran home to the farm", "the dogs ran back to the farm"},
		{"recogniser restored the order (s14 as recorded)", s14, "In the spring of that year, the council voted to rebuild the old granary, though only four of the nine members had ever seen it full."},
		{"Han unchanged", "他当场红了眼眶。", "他当场红了眼眶。"},
	}
	for _, tc := range keeps {
		if j := judged(tc.spoken, tc.heard); j.rejection != nil && j.rejection.Reason == ReasonWordOrder {
			t.Errorf("%s: rejected for word order: %s", tc.name, j.rejection.Detail)
		}
	}
}

// The connectives that carry a clause's logic are content words for the guard.
func TestConnectivesAreContentWords(t *testing.T) {
	for _, w := range []string{"because", "so", "but", "although", "though", "unless", "not", "never", "no"} {
		if isFunctionWord("en", w) {
			t.Errorf("%q is on the function-word list", w)
		}
	}
	const s079 = "The ferryman at Rookwater charged one penny to cross the river and two to come back, because, he said, the return journey was uphill."
	j := judged(s079, "The ferryman at Rookwater charged one penny to cross the river and two to come back, he said. The return journey was uphill.")
	if len(j.content) != 1 || j.content[0].Ref != "because" {
		t.Fatalf("content mismatches %v, want the deleted because", j.content)
	}
	const s070 = "The market at Brindle opened at dawn on Thursdays, and by eight o'clock the best cheese had gone and the gossip had only just begun."
	j = judged(s070, "The market at Brindle opened at dawn on Thursdays, and by eight o'clock the best cheese had gone and the gossip had only unjust begun.")
	if len(j.content) != 1 || j.content[0].Hyp != "unjust" {
		t.Fatalf("content mismatches %v, want just heard as unjust", j.content)
	}
}
