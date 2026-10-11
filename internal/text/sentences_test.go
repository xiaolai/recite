package text

import (
	"slices"
	"testing"
)

func TestSentences(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"  ", nil},
		{"One sentence", []string{"One sentence"}},
		{"First one. Second one! Third?", []string{"First one.", "Second one!", "Third?"}},
		{"Mr. Smith paid 3.5 dollars. J. Doe agreed.", []string{"Mr. Smith paid 3.5 dollars.", "J. Doe agreed."}},
		{"Visit example.com today. Then stop.", []string{"Visit example.com today.", "Then stop."}},
		{`He said "go." She went...  Then`, []string{`He said "go."`, "She went...", "Then"}},
		{"第一句。第二句！第三句", []string{"第一句。", "第二句！", "第三句"}},
	} {
		if got := Sentences(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("Sentences(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
