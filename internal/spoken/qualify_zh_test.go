//go:build qualify

package spoken_test

import (
	"os"
	"strings"
	"testing"
)

// Qualification (TXT-12#3): 30 number-dense Chinese sentences, normalised, one take each:
// at least 24 verify on the first attempt and all 30 within the attempt limit (the bar
// amended by owner decision, 2026-10-11). It spends
// real usage: RECITE_LIVE=1 go test -tags qualify -run TestQualifyChineseSpokenForm -count=1
// ./internal/spoken, once per shard with RECITE_SHARD=k.
//
// accept: TXT-12#3
func TestQualifyChineseSpokenForm(t *testing.T) {
	raw, err := os.ReadFile("testdata/qualify-zh.txt")
	if err != nil {
		t.Fatal(err)
	}
	corpus := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(corpus) != 30 {
		t.Fatalf("corpus has %d chunks, want 30", len(corpus))
	}
	qualifyCorpus(t, "TXT-12#3", "txt12", corpus, 24)
}
