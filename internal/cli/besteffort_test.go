package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readme is the repository README, the user-facing documentation the ledger's best-effort
// bullets are about (design D22).
func readme(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Q11 was not run (design D22): the README and `say --help` call `hint` best-effort and
// unverifiable by STT, and recommend `say`.
//
// accept: SPK-08#3
func TestHintDocumentedBestEffort(t *testing.T) {
	doc := readme(t)
	for _, want := range []string{
		"`{ hint = \"…\" }` (`hint` is best-effort; prefer `say`",
		"A lexicon `hint` entry leaves the text unchanged",
		"so `hint` is unverifiable by STT",
		"Prefer `say`, which rewrites the spoken text and is verified",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("README lacks %q", want)
		}
	}
	help, err := os.ReadFile(filepath.Join("testdata", "help-say.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "{ hint = PRONUNCIATION } (hint is best-effort and unverifiable by STT; prefer say)"; !strings.Contains(string(help), want) {
		t.Errorf("say --help lacks %q", want)
	}
}

// Q12 was not run (design D22): the README calls `{{emph}}` best-effort. `say --help` does not
// list individual speech marks, so the README is where it is documented.
//
// accept: SPK-12#2
func TestEmphDocumentedBestEffort(t *testing.T) {
	doc := readme(t)
	for _, want := range []string{
		"Two pronunciation and stress controls are best-effort, not verified",
		"The `{{emph|words}}` speech mark asks the model to stress the words",
		"Whether the stress is audible is not verified.",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("README lacks %q", want)
		}
	}
}
