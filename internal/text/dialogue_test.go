package text

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var sampleSpeakers = []string{"HOST", "GUEST", "NARRATOR"}

// The 40-line sample script parses to a fixed result: one paragraph per spoken line with no
// labels and no directives, each line's speaker and direction, the exact pauses and the
// sections (golden, testdata/dialogue-40.golden.json).
// accept: TXT-06#1
func TestParseDialogueGolden40(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "dialogue-40.tts"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(src), "\n"); n != 40 {
		t.Fatalf("the sample has %d lines, want 40", n)
	}
	fm, body, err := SplitFrontMatter(string(src))
	if err != nil || fm == nil {
		t.Fatalf("front matter %v %v", fm, err)
	}
	d, err := ParseDialogue(body, 8, sampleSpeakers)
	if err != nil {
		t.Fatal(err)
	}
	type span struct {
		Speaker, Direction, Text string
	}
	type pause struct {
		After string
		D     string
	}
	type section struct{ Title, First string }
	var got struct {
		Lines    []span
		Pauses   []pause
		Sections []section
		Breaks   []string
	}
	for _, sp := range d.Spans {
		got.Lines = append(got.Lines, span{sp.Speaker, sp.Direction, d.Text[sp.Start:sp.End]})
	}
	for _, p := range d.Pauses {
		got.Pauses = append(got.Pauses, pause{tail(d.Text[:p.At]), p.D.String()})
	}
	for _, sc := range d.Sections {
		got.Sections = append(got.Sections, section{sc.Title, head(d.Text[sc.At:])})
	}
	for _, b := range d.Breaks {
		got.Breaks = append(got.Breaks, string(b.Kind)+" after "+tail(d.Text[:b.Display]))
	}
	b, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	golden := filepath.Join("testdata", "dialogue-40.golden.json")
	if *update {
		if err := os.WriteFile(golden, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(want) {
		t.Fatalf("parse mismatch (go test ./internal/text -run Golden40 -update)\n--- got\n%s--- want\n%s", b, want)
	}
	// No label, bracket or direction reaches the text.
	for _, bad := range []string{"HOST:", "GUEST:", "NARRATOR:", "[", "]", "(", ")"} {
		if strings.Contains(d.Text, bad) {
			t.Errorf("text holds %q:\n%s", bad, d.Text)
		}
	}
	if len(d.Spans) != 19 {
		t.Errorf("%d spoken lines, want 19", len(d.Spans))
	}
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 20 {
		return "…" + string(r[len(r)-20:])
	}
	return s
}

func head(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 20 {
		return string(r[:20]) + "…"
	}
	return s
}

// An unknown speaker or a line with no text is exit 2 (an input error) at its input line.
// accept: TXT-06#1
func TestParseDialogueErrorsCarryLineNumbers(t *testing.T) {
	for _, tc := range []struct {
		body string
		line int
		want string
	}{
		{"HOST: Hi.\nGUST: Hello.\n", 11, `unknown speaker "GUST"`},
		{"HOST: Hi.\n\nGUEST:\n", 12, "GUEST: the line has no text"},
		{"HOST: Hi.\nGUEST:   (laughs)  \n", 11, "GUEST: the line has no text"},
		{"Just text.\n", 10, "a line needs a speaker"},
		{"HOST: Hi.\n\ncontinued after a blank line\n", 12, "a line needs a speaker"},
		{"[pause 1s]\nHOST: Hi.\nGUEST: Yo.\n", 10, "must sit between two lines"},
		{"HOST: Hi.\n[pause 1s]\n", 11, "must sit between two lines"},
		{"HOST: Hi.\n[pause soon]\nGUEST: Yo.\n", 11, "want a duration"},
		{"HOST: Hi.\n[pause 11s]\nGUEST: Yo.\n", 11, "want a duration"},
		{"HOST: Hi.\n[section]\nGUEST: Yo.\n", 11, "needs a title"},
		{"HOST: Hi.\n[section End]\n", 11, "has no line after it"},
		{"HOST: Hi. [section X]\n", 10, "must stand on its own line"},
		{"HOST: [pause 1s] Hi.\n", 10, "must sit between two pieces of text"},
		{"\n\n", 10, "no NAME: line"},
	} {
		_, err := ParseDialogue(tc.body, 10, sampleSpeakers)
		var de *DirectiveError
		if !errors.As(err, &de) || de.Line != tc.line || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: %v, want line %d %q", tc.body, err, tc.line, tc.want)
		}
	}
}

// A label-like word that is not a speaker is an error, but ordinary prose with a colon is a
// continuation: "In short: yes" has a space before the colon, so it is no label.
func TestParseDialogueContinuation(t *testing.T) {
	d, err := ParseDialogue("HOST: First part\nIn short: yes.\nGUEST: (dry) Fine. [pause 300ms] Go on.\n", 1, sampleSpeakers)
	if err != nil {
		t.Fatal(err)
	}
	if d.Text != "First part In short: yes.\n\nFine.  Go on." {
		t.Fatalf("text %q", d.Text)
	}
	if len(d.Pauses) != 1 || d.Pauses[0].D != 300*time.Millisecond || d.Text[:d.Pauses[0].At] != "First part In short: yes.\n\nFine. " {
		t.Fatalf("pauses %+v", d.Pauses)
	}
	if d.Spans[1].Direction != "dry" || d.Spans[1].Speaker != "GUEST" {
		t.Fatalf("spans %+v", d.Spans)
	}
}

// --directives in plain text: the directives leave the text, a direction's line is its own
// range, and offsets of the source map to the new text.
func TestExtractDirectives(t *testing.T) {
	src := "Intro text here.\n\n[section Part two]\n(whispering) A secret line.\nThen more. [pause 1.5s] After.\n"
	d, err := ExtractDirectives(src)
	if err != nil {
		t.Fatal(err)
	}
	if d.Text != "Intro text here.\n\n\nA secret line.\nThen more.  After.\n" {
		t.Fatalf("text %q", d.Text)
	}
	if len(d.Sections) != 1 || d.Sections[0].Title != "Part two" || d.Text[d.Sections[0].At:][:1] != "\n" {
		t.Fatalf("sections %+v", d.Sections)
	}
	if len(d.Spans) != 1 || d.Spans[0].Direction != "whispering" || d.Text[d.Spans[0].Start:d.Spans[0].End] != "A secret line." {
		t.Fatalf("spans %+v", d.Spans)
	}
	if len(d.Pauses) != 1 || d.Pauses[0].D != 1500*time.Millisecond || !strings.HasSuffix(d.Text[:d.Pauses[0].At], "Then more. ") {
		t.Fatalf("pauses %+v", d.Pauses)
	}
	if got := d.Offset(strings.Index(src, "After")); d.Text[got:got+5] != "After" {
		t.Fatalf("offset maps to %q", d.Text[got:])
	}
	for _, tc := range []struct{ in, want string }{
		{"[pause 1s] Hello.", "line 1: [pause 1s] must sit between two pieces of text"},
		{"Hello.\n[section]", "line 2: [section] needs a title"},
		{"Hello. [section X] there.", "must stand on its own line"},
		{"Hello.\n(sigh)\n", "line 2: (sigh) has no text after it"},
		{"Hello.\n[section End]\n", "has no text before the next section or the end"},
	} {
		if _, err := ExtractDirectives(tc.in); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: %v, want %q", tc.in, err, tc.want)
		}
	}
	// Ordinary brackets and parentheses that are not directives stay.
	d, err = ExtractDirectives("See [1] and [Pause here], (not a direction) mid-line.")
	if err != nil || d.Text != "See [1] and [Pause here], (not a direction) mid-line." {
		t.Fatalf("%q %v", d.Text, err)
	}
}

func TestValidSpeakerName(t *testing.T) {
	for name, ok := range map[string]bool{"HOST": true, "Dr.Smith": true, "主持人": true, "guest_2": true,
		"": false, "TWO WORDS": false, "A:B": false, strings.Repeat("x", 33): false} {
		if ValidSpeakerName(name) != ok {
			t.Errorf("%q: want %v", name, ok)
		}
	}
}
