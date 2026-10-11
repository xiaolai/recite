package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/text/language"

	"github.com/xiaolai/recite/internal/config"
	"github.com/xiaolai/recite/internal/text"
)

// Front-matter-only settings (TXT-08): no flag, environment variable or settings file sets them.
const (
	titleKey = "title"
	langKey  = "lang"
)

// --code modes for Markdown code blocks (design §7.1).
const (
	codeSkip     = "skip"
	codeAnnounce = "announce"
)

func checkLang(s string) error {
	if _, err := language.Parse(s); err != nil {
		return fmt.Errorf("%q: want a BCP 47 language tag such as en or zh-Hans", s)
	}
	return nil
}

// format is --input-format, or by default the format the file's extension names: .md and
// .markdown are Markdown, everything else (and stdin or text arguments) is plain text.
func (f *textFlags) format(file string) string {
	if f.inputFormat != "" {
		return f.inputFormat
	}
	if file != "" && file != "-" {
		switch strings.ToLower(filepath.Ext(file)) {
		case ".md", ".markdown":
			return formatMarkdown
		}
	}
	return formatText
}

// markdownOptions resolves --read-alt and --code.
func (f *textFlags) markdownOptions() (text.MarkdownOptions, error) {
	o := text.MarkdownOptions{ReadAlt: f.readAlt}
	switch f.code {
	case codeSkip:
	case codeAnnounce:
		o.AnnounceCode = true
	default:
		return o, fmt.Errorf("--code must be %s or %s, not %q", codeSkip, codeAnnounce, f.code)
	}
	return o, nil
}

// prereadInput is a Markdown input read before the settings resolve, because its front
// matter is a settings layer (TXT-08) and stdin cannot be read twice.
type prereadInput struct {
	input  string // decoded and normalised, front matter included
	origin string // the input as named in a front-matter value's source
	fm     *text.FrontMatter
}

// frontMatterSource is the front matter of an input and where it came from.
type frontMatterSource struct {
	fm     *text.FrontMatter
	origin string
}

func (p *prereadInput) frontMatter() *frontMatterSource {
	if p == nil || p.fm == nil {
		return nil
	}
	return &frontMatterSource{fm: p.fm, origin: p.origin}
}

// values parses the front matter against schema; nil has no values.
func (s *frontMatterSource) values(schema *config.Schema) (config.Values, error) {
	if s == nil {
		return nil, nil
	}
	v, err := config.ParseFrontMatter(schema, s.origin, s.fm.TOML, s.fm.Line)
	if err != nil {
		return nil, fmt.Errorf("front matter: %w", err)
	}
	return v, nil
}

// prereadMarkdown reads src when the input is Markdown and returns it with its front matter;
// for any other format it reads nothing and returns nil. A non-zero code has been reported.
func (c *CLI) prereadMarkdown(cmd string, tf *textFlags, src text.Source, file string) (*prereadInput, int) {
	if tf.format(file) != formatMarkdown {
		return nil, ExitOK
	}
	input, err := text.Read(src, tf.maxInput)
	if err != nil {
		c.errorf("%s: %v", cmd, err)
		return nil, textErrorCode(err)
	}
	fm, _, err := text.SplitFrontMatter(input)
	if err != nil {
		c.errorf("%s: %v", cmd, err)
		return nil, ExitUsage
	}
	origin := file
	if origin == "" || origin == "-" {
		origin = "stdin"
	}
	return &prereadInput{input: input, origin: origin, fm: fm}, ExitOK
}

// textInput is how to read an input's text: its format, the Markdown options and the
// front-matter language.
type textInput struct {
	format   string
	markdown text.MarkdownOptions
	lang     string
	preread  *prereadInput
}

// textDoc is the text the guard reads: the input itself for plain text, or the speech text of
// a Markdown document with its block breaks (input coordinates) and skipped content.
type textDoc struct {
	text    string
	breaks  []text.Break // nil means the paragraph breaks of plain text
	lang    string       // front-matter language; "" means detect
	skipped text.Skipped
	// headingFirst says the speech text opens with a heading, so its first chunk starts a
	// section (OUT-04); every later section follows a section break.
	headingFirst bool
}

func plainDoc(input string) textDoc { return textDoc{text: input} }

// readInput returns the input already read for its front matter, or reads src. A non-zero
// code has been reported.
func (c *CLI) readInput(cmd string, in textInput, src text.Source, max int64) (string, int) {
	if in.preread != nil {
		return in.preread.input, ExitOK
	}
	input, err := text.Read(src, max)
	if err != nil {
		c.errorf("%s: %v", cmd, err)
		return "", textErrorCode(err)
	}
	return input, ExitOK
}

// speechDoc turns input into the text the guard reads (TXT-05).
func speechDoc(input string, in textInput) (textDoc, error) {
	if in.format != formatMarkdown {
		return textDoc{text: input, lang: in.lang}, nil
	}
	md, err := text.ParseMarkdown(input, in.markdown)
	if err != nil {
		return textDoc{}, err
	}
	if strings.TrimSpace(md.Text) == "" {
		return textDoc{skipped: md.Skipped}, text.ErrNothingToSpeak
	}
	breaks := md.Breaks
	if breaks == nil {
		breaks = []text.Break{}
	}
	return textDoc{text: md.Text, breaks: breaks, lang: in.lang, skipped: md.Skipped,
		headingFirst: len(md.Headings) > 0 && md.Headings[0].At == 0}, nil
}

// skippedWarning reports the Markdown content not read aloud, unless -q.
func (c *CLI) skippedWarning(cmd string, s text.Skipped) {
	if w := s.String(); w != "" && !c.globals.Quiet {
		c.errorf("%s: markdown: %s", cmd, w)
	}
}

// docBreaks are the forced breaks of doc in display coordinates: Markdown block edges moved
// through the guard's edits, or the blank-line paragraphs of plain text.
func docBreaks(doc textDoc, display string, edits func(int) int) []text.Break {
	if doc.breaks == nil {
		return text.ParagraphBreaks(display)
	}
	out := make([]text.Break, 0, len(doc.breaks))
	for _, b := range doc.breaks {
		out = append(out, text.Break{Display: min(edits(b.Display), len(display)), Kind: b.Kind})
	}
	return out
}
