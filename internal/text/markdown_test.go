package text

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

// renderMarkdown prints a parsed document one block per line, each with the kind of break
// before it, then the headings and the skipped-content warning.
func renderMarkdown(t *testing.T, md Markdown) string {
	t.Helper()
	var b strings.Builder
	if md.FrontMatter != nil {
		fmt.Fprintf(&b, "front matter (line %d): %q\n", md.FrontMatter.Line, md.FrontMatter.TOML)
	}
	prev, kind := 0, Boundary("start")
	for _, br := range md.Breaks {
		fmt.Fprintf(&b, "%-9s %q\n", kind, md.Text[prev:br.Display])
		sep := "\n\n"
		if br.Kind == BoundaryListItem {
			sep = "\n"
		}
		if !strings.HasPrefix(md.Text[br.Display:], sep) {
			t.Fatalf("break %+v not followed by %q", br, sep)
		}
		prev, kind = br.Display+len(sep), br.Kind
	}
	fmt.Fprintf(&b, "%-9s %q\n", kind, md.Text[prev:])
	for _, h := range md.Headings {
		if !strings.HasPrefix(md.Text[h.At:], h.Title) {
			t.Fatalf("heading %+v not at its offset", h)
		}
		fmt.Fprintf(&b, "heading %d %q\n", h.Level, h.Title)
	}
	fmt.Fprintf(&b, "warning: %q\n", md.Skipped.String())
	return b.String()
}

// accept: TXT-05#1
func TestMarkdownGolden(t *testing.T) {
	for _, name := range []string{"readme", "blog", "chapter", "code"} {
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join("testdata", "markdown", name+".md"))
			if err != nil {
				t.Fatal(err)
			}
			md, err := ParseMarkdown(Normalize(string(src)), MarkdownOptions{})
			if err != nil {
				t.Fatal(err)
			}
			got := renderMarkdown(t, md)
			golden := filepath.Join("testdata", "markdown", name+".golden")
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("%s mismatch (run go test ./internal/text -run TestMarkdownGolden -update)\ngot:\n%s", golden, got)
			}
		})
	}
}

// The skipped-content counts over the corpus, stated independently of the goldens.
// accept: TXT-05#1
func TestMarkdownSkippedCounts(t *testing.T) {
	for _, tc := range []struct {
		name string
		want Skipped
		warn string
	}{
		// Two badge/logo images; the <p> block; one fence; one table.
		{"readme", Skipped{Code: 1, HTML: 1, Tables: 1, Images: 2}, "skipped 1 code block, 1 HTML element, 1 table, 2 images"},
		{"blog", Skipped{Images: 1}, "skipped 1 image"},
		{"chapter", Skipped{}, ""},
		// Three fences and one indented block; the <div> block plus <code> and </code>; two tables.
		{"code", Skipped{Code: 4, HTML: 3, Tables: 2}, "skipped 4 code blocks, 3 HTML elements, 2 tables"},
	} {
		src, err := os.ReadFile(filepath.Join("testdata", "markdown", tc.name+".md"))
		if err != nil {
			t.Fatal(err)
		}
		md, err := ParseMarkdown(string(src), MarkdownOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if md.Skipped != tc.want || md.Skipped.String() != tc.warn {
			t.Errorf("%s: skipped %+v %q, want %+v %q", tc.name, md.Skipped, md.Skipped.String(), tc.want, tc.warn)
		}
	}
}

func TestMarkdownOptions(t *testing.T) {
	src := "Intro.\n\n![A red door](door.png)\n\n```\nx := 1\n```\n"
	md, err := ParseMarkdown(src, MarkdownOptions{ReadAlt: true, AnnounceCode: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := "Intro.\n\nA red door\n\n" + CodeAnnouncement; md.Text != want {
		t.Errorf("text %q, want %q", md.Text, want)
	}
	// An announced block is still skipped content; a read image is not.
	if md.Skipped != (Skipped{Code: 1}) {
		t.Errorf("skipped %+v", md.Skipped)
	}
	md, err = ParseMarkdown(src, MarkdownOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if md.Text != "Intro." || md.Skipped != (Skipped{Code: 1, Images: 1}) {
		t.Errorf("default: %q %+v", md.Text, md.Skipped)
	}
}

func TestMarkdownBreakKinds(t *testing.T) {
	md, err := ParseMarkdown("# A\n\nPara.\n\n- one\n- two\n  - deep\n\n- loose\n\nAfter.\n\n## B\n", MarkdownOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []Boundary
	for _, b := range md.Breaks {
		kinds = append(kinds, b.Kind)
	}
	want := []Boundary{BoundaryParagraph, BoundaryParagraph, BoundaryListItem, BoundaryListItem, BoundaryListItem, BoundaryParagraph, BoundarySection}
	if fmt.Sprint(kinds) != fmt.Sprint(want) {
		t.Errorf("kinds %v, want %v (text %q)", kinds, want, md.Text)
	}
}

func TestSplitFrontMatter(t *testing.T) {
	for _, tc := range []struct {
		in, toml, body string
		ok, err        bool
	}{
		{"+++\nvoice = \"marin\"\n+++\n# Hi\n", "voice = \"marin\"\n", "# Hi\n", true, false},
		{"+++\n+++\nBody", "", "Body", true, false},
		{"+++ \ntitle = \"x\"\n+++", "title = \"x\"\n", "", true, false},
		{"Text\n+++\nx\n+++\n", "", "Text\n+++\nx\n+++\n", false, false},
		{"++++\nx\n", "", "++++\nx\n", false, false},
		{"+++\nvoice = \"marin\"\n", "", "", false, true},
	} {
		fm, body, err := SplitFrontMatter(tc.in)
		if (err != nil) != tc.err {
			t.Errorf("%q: err %v", tc.in, err)
			continue
		}
		if tc.err {
			if !IsInputError(err) {
				t.Errorf("%q: %v is not an input error", tc.in, err)
			}
			continue
		}
		if (fm != nil) != tc.ok || body != tc.body || (fm != nil && (fm.TOML != tc.toml || fm.Line != 2)) {
			t.Errorf("%q: fm %+v body %q", tc.in, fm, body)
		}
	}
}

func FuzzMarkdown(f *testing.F) {
	for _, s := range []string{"# H\n\n- a\n  - b\n", "+++\nx=1\n+++\ntext", "```\ncode", "<div>\n", "| a |\n|---|\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		md, err := ParseMarkdown(s, MarkdownOptions{AnnounceCode: true})
		if err != nil {
			return
		}
		last := -1
		for _, b := range md.Breaks {
			if b.Display <= last || b.Display > len(md.Text) {
				t.Fatalf("break %+v out of order in %q", b, md.Text)
			}
			last = b.Display
		}
	})
}
