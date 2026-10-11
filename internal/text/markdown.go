package text

import (
	"fmt"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	gtext "github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// CodeAnnouncement is spoken in place of each skipped code block under --code announce.
const CodeAnnouncement = "Code block omitted."

// MarkdownOptions are the reading choices of design §7.1.
type MarkdownOptions struct {
	ReadAlt      bool // read image alt text (--read-alt)
	AnnounceCode bool // speak CodeAnnouncement for each code block (--code announce)
}

// Skipped counts the Markdown content that is not read aloud (TXT-05).
type Skipped struct {
	Code   int // fenced and indented code blocks
	HTML   int // HTML blocks and inline HTML tags
	Tables int
	Images int // images whose alt text was not read
}

// Total is the number of skipped items.
func (s Skipped) Total() int { return s.Code + s.HTML + s.Tables + s.Images }

// String is the skipped-content warning, e.g. "skipped 2 code blocks, 1 table"; "" when nothing
// was skipped. Code blocks that were announced still count: their content is not read.
func (s Skipped) String() string {
	var parts []string
	for _, p := range []struct {
		n           int
		one, plural string
	}{
		{s.Code, "code block", "code blocks"},
		{s.HTML, "HTML element", "HTML elements"},
		{s.Tables, "table", "tables"},
		{s.Images, "image", "images"},
	} {
		switch {
		case p.n == 1:
			parts = append(parts, "1 "+p.one)
		case p.n > 1:
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.plural))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "skipped " + strings.Join(parts, ", ")
}

// Heading is a Markdown heading: a section start in the speech text.
type Heading struct {
	Level int
	Title string
	At    int // byte offset of the title in Markdown.Text
}

// Markdown is a Markdown document turned into speech text (design §7.1). Text holds one block
// per heading, paragraph or list item; list items of one list are joined by "\n", every other
// pair of blocks by "\n\n". Breaks marks every block edge with its kind, at the byte offset in
// Text where the previous block ends.
type Markdown struct {
	FrontMatter *FrontMatter
	Text        string
	Breaks      []Break
	Headings    []Heading
	Skipped     Skipped
}

// ParseMarkdown parses src (decoded and normalised input) with goldmark, after taking off a
// leading `+++` front-matter block.
func ParseMarkdown(src string, opts MarkdownOptions) (Markdown, error) {
	fm, body, err := SplitFrontMatter(src)
	if err != nil {
		return Markdown{}, err
	}
	b := []byte(body)
	md := goldmark.New(goldmark.WithExtensions(extension.Table, extension.Strikethrough))
	doc := md.Parser().Parse(gtext.NewReader(b))
	w := mdWriter{src: b, opts: opts}
	w.blocks(doc)
	out := w.out
	out.FrontMatter = fm
	out.Text = w.text.String()
	return out, nil
}

// mdWriter builds the speech text of one document.
type mdWriter struct {
	src  []byte
	opts MarkdownOptions
	text strings.Builder
	out  Markdown
	// list is the outermost list the previous block belonged to, nil outside lists.
	list ast.Node
}

// blocks walks the block children of n.
func (w *mdWriter) blocks(n ast.Node) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		w.block(c)
	}
}

func (w *mdWriter) block(n ast.Node) {
	switch n := n.(type) {
	case *ast.Heading:
		w.emit(w.inline(n), BoundarySection, nil, n.Level)
	case *ast.Paragraph, *ast.TextBlock:
		w.emit(w.inline(n), w.kindIn(n), outerList(n), 0)
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		w.out.Skipped.Code++
		if w.opts.AnnounceCode {
			w.emit(CodeAnnouncement, BoundaryParagraph, nil, 0)
		}
	case *ast.HTMLBlock:
		w.out.Skipped.HTML++
	case *east.Table:
		w.out.Skipped.Tables++
	case *ast.ThematicBreak:
		// A visual rule has nothing to say.
	default:
		w.blocks(n)
	}
}

// kindIn is the break before a paragraph: a list-item break when it opens an item that follows
// another item of the same outermost list, else a paragraph break.
func (w *mdWriter) kindIn(n ast.Node) Boundary {
	item, ok := n.Parent().(*ast.ListItem)
	if !ok || item.FirstChild() != n {
		return BoundaryParagraph
	}
	if l := outerList(n); l != nil && l == w.list {
		return BoundaryListItem
	}
	return BoundaryParagraph
}

func outerList(n ast.Node) ast.Node {
	var out ast.Node
	for p := n.Parent(); p != nil; p = p.Parent() {
		if _, ok := p.(*ast.List); ok {
			out = p
		}
	}
	return out
}

// emit appends one block of speech text; an empty block is dropped.
func (w *mdWriter) emit(s string, kind Boundary, list ast.Node, level int) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	if w.text.Len() > 0 {
		w.out.Breaks = append(w.out.Breaks, Break{Display: w.text.Len(), Kind: kind})
		if kind == BoundaryListItem {
			w.text.WriteString("\n")
		} else {
			w.text.WriteString("\n\n")
		}
	}
	if level > 0 {
		w.out.Headings = append(w.out.Headings, Heading{Level: level, Title: s, At: w.text.Len()})
	}
	w.text.WriteString(s)
	w.list = list
}

// inline renders the visible text of n's inline children.
func (w *mdWriter) inline(n ast.Node) string {
	var b strings.Builder
	w.inlineTo(&b, n)
	return b.String()
}

func (w *mdWriter) inlineTo(b *strings.Builder, n ast.Node) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch c := c.(type) {
		case *ast.Text:
			v := c.Segment.Value(w.src)
			if !c.IsRaw() {
				v = util.UnescapePunctuations(v)
				v = util.ResolveNumericReferences(v)
				v = util.ResolveEntityNames(v)
			}
			b.Write(v)
			switch {
			case c.HardLineBreak():
				b.WriteByte('\n')
			case c.SoftLineBreak():
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(c.Value)
		case *ast.CodeSpan:
			w.inlineTo(b, c)
		case *ast.RawHTML:
			w.out.Skipped.HTML++
		case *ast.Image:
			if w.opts.ReadAlt {
				w.inlineTo(b, c)
			} else {
				w.out.Skipped.Images++
			}
		case *ast.AutoLink:
			b.Write(c.Label(w.src))
		case *east.TaskCheckBox:
		default:
			// Emphasis, links, strikethrough: their visible text.
			w.inlineTo(b, c)
		}
	}
}
