package verify

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

// promptTokens counts tokens the way the 30% bound is defined: keyword tokens of the text.
func promptTokens(s string) int { return len(keywordTokens(s)) }

// accept: VER-10#1
func FuzzKeywords(f *testing.F) {
	for _, s := range []string{
		"On Monday, Mrs. Okafor approved the 42 new permits.",
		"A B C D E F G H I J K L M N O P",
		"x1 x2 x3 x4 x5 x6 x7 x8 x9 x10",
		"Zyxw qwer asdf zxcv",
		"我们在2024年访问了北京",
		"",
	} {
		f.Add(s, "Okafor")
	}
	f.Fuzz(func(t *testing.T, chunk, lex string) {
		kws := Keywords(chunk, strings.Split(lex, "|"))
		prompt := KeywordPrompt(chunk, strings.Split(lex, "|"))
		if len(kws) > MaxKeywords {
			t.Fatalf("%d keywords", len(kws))
		}
		if p, c := promptTokens(prompt), promptTokens(chunk); p > 0 && 10*p >= 3*c {
			t.Fatalf("prompt %q has %d tokens, chunk %d: not under 30%%", prompt, p, c)
		}
	})
}

func TestKeywordsUnderThirtyPercent(t *testing.T) {
	// Every word qualifies; the bound alone limits the prompt.
	chunk := "Alpha1 Beta2 Gamma3 Delta4 Epsilon5 Zeta6 Eta7 Theta8 Iota9 Kappa10"
	kws := Keywords(chunk, nil)
	if len(kws) != 2 { // 10 × 2 < 3 × 10, 10 × 3 is not
		t.Fatalf("keywords = %q", kws)
	}
	if got := Keywords("Hi Okafor", []string{"Okafor"}); len(got) != 0 {
		t.Fatalf("two-word chunk got %q", got)
	}
}

func TestKeywordsRules(t *testing.T) {
	chunk := "Yesterday the committee met. Mrs. Okafor said the 3.5% figure was wrong, and Zorblat agreed with Teodor about the quokka. I agreed."
	got := Keywords(chunk, []string{"committee"})
	want := []string{"committee", "3.5", "Okafor", "Zorblat", "Teodor", "quokka"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("keywords = %q, want %q", got, want)
	}
	if strings.Contains(KeywordPrompt(chunk, nil), "Yesterday") {
		t.Fatal("sentence-initial word chosen")
	}
	if KeywordPrompt(chunk, nil) != "3.5, Okafor, Zorblat, Teodor, quokka" {
		t.Fatalf("prompt = %q", KeywordPrompt(chunk, nil))
	}
}

func TestCommonEnglishSize(t *testing.T) {
	if n := CommonEnglishSize(); n != 10000 {
		t.Fatalf("frequency list has %d words, want 10000", n)
	}
	for _, w := range []string{"the", "and", "student", "arrive", "approve"} {
		if !commonEnglish[w] {
			t.Errorf("%q missing from the frequency list", w)
		}
	}
}

// accept: VER-10#2
func TestKeywordPromptGolden(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "keywords", "corpus.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, chunk := range strings.Split(strings.TrimSpace(string(data)), "\n---\n") {
		b.WriteString(KeywordPrompt(chunk, []string{"harbour district"}))
		b.WriteByte('\n')
	}
	golden := filepath.Join("testdata", "keywords", "prompts.golden")
	if *update {
		if err := os.WriteFile(golden, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if b.String() != string(want) {
		t.Fatalf("prompts:\n%s\nwant:\n%s", b.String(), want)
	}
}
