package text

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Directives (TXT-07, design §7.4): [pause 800ms] and [pause 1.5s] insert an exact silence,
// [section Title] starts a chapter, and a leading (direction) sets the delivery of its line.
// They are read in script input always, and in text or Markdown only with --directives. No
// directive is ever part of the text the model is asked to say.

// PauseAt is a [pause] at a byte offset of Directed.Text.
type PauseAt struct {
	At   int
	D    time.Duration
	Line int // the input line, for errors
}

// SectionAt is a [section] whose chapter starts at a byte offset of Directed.Text.
type SectionAt struct {
	At    int
	Title string
}

// Voiced is a range of Directed.Text spoken by one speaker (script input) or under one
// leading (direction). Speaker is "" outside a script.
type Voiced struct {
	Start, End int
	Speaker    string
	Direction  string
}

// Directed is a text with its directives taken out.
type Directed struct {
	Text     string
	Breaks   []Break // the chunk boundaries the directives force, besides the pauses
	Pauses   []PauseAt
	Sections []SectionAt
	Spans    []Voiced
	removed  []removal // ExtractDirectives only: the source ranges taken out
}

type removal struct{ start, end int }

// Offset maps a byte offset of the text ExtractDirectives read to the same place in Text; an
// offset inside a removed directive maps to where the directive was.
func (d Directed) Offset(src int) int {
	delta := 0
	for _, r := range d.removed {
		if src < r.start {
			break
		}
		if src < r.end {
			return r.start - delta
		}
		delta += r.end - r.start
	}
	return src - delta
}

// DirectiveError is a script or directive that cannot be read (exit 2), at a 1-based input line.
type DirectiveError struct {
	Line int
	Msg  string
}

func (e *DirectiveError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// directiveRe finds a bracketed directive; the name is matched case-sensitively, so ordinary
// bracketed text such as [1] or [Pause here] is left alone.
var directiveRe = regexp.MustCompile(`\[(pause|section)(?:[ \t]+([^\]\n]*))?\]`)

// inlineDirective is one directive of a line, at byte offsets of the line.
type inlineDirective struct {
	start, end int
	pause      time.Duration // a [pause]
	title      string        // a [section]
}

func parseDirectives(line string, n int) ([]inlineDirective, error) {
	var out []inlineDirective
	for _, m := range directiveRe.FindAllStringSubmatchIndex(line, -1) {
		d := inlineDirective{start: m[0], end: m[1]}
		arg := ""
		if m[4] >= 0 {
			arg = strings.TrimSpace(line[m[4]:m[5]])
		}
		switch line[m[2]:m[3]] {
		case "pause":
			p, err := time.ParseDuration(arg)
			if err != nil || p <= 0 || p > MaxPause {
				return nil, &DirectiveError{n, fmt.Sprintf("[pause %s]: want a duration such as 800ms or 1.5s, up to %s", arg, MaxPause)}
			}
			d.pause = p
		default:
			if arg == "" {
				return nil, &DirectiveError{n, "[section] needs a title: [section Title]"}
			}
			d.title = arg
		}
		out = append(out, d)
	}
	return out, nil
}

// leadingDirection splits a leading "(direction)" off s: the direction, and the offset in s
// where the text after it (and its spaces) starts. ok is false when s does not open with one.
func leadingDirection(s string) (dir string, rest int, ok bool) {
	lead := len(s) - len(strings.TrimLeft(s, " \t"))
	if !strings.HasPrefix(s[lead:], "(") {
		return "", 0, false
	}
	end := strings.IndexByte(s[lead:], ')')
	if end < 0 {
		return "", 0, false
	}
	inner := s[lead+1 : lead+end]
	if strings.ContainsAny(inner, "()") || strings.TrimSpace(inner) == "" {
		return "", 0, false
	}
	rest = lead + end + 1
	rest += len(s[rest:]) - len(strings.TrimLeft(s[rest:], " \t"))
	return strings.TrimSpace(inner), rest, true
}

func speakable(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) >= 0
}

// lineAt is the 1-based line of offset off in s.
func lineAt(s string, off int) int { return 1 + strings.Count(s[:min(off, len(s))], "\n") }

// ExtractDirectives takes the directives out of a text or Markdown speech text (TXT-07,
// --directives). A leading (direction) applies to the rest of its line, which becomes its own
// chunk; a [section] must stand on its own line; a [pause] may sit anywhere between two
// pieces of text. Line numbers in errors count lines of s.
func ExtractDirectives(s string) (Directed, error) {
	var d Directed
	var b strings.Builder
	off := 0
	n := 0
	for off < len(s) || (off == 0 && len(s) == 0) {
		n++
		end := strings.IndexByte(s[off:], '\n')
		line := s[off:]
		if end >= 0 {
			line = s[off : off+end]
		}
		if err := d.extractLine(&b, line, off, n); err != nil {
			return Directed{}, err
		}
		if end < 0 {
			break
		}
		b.WriteByte('\n')
		off += end + 1
	}
	d.Text = b.String()
	if err := d.check(func(at int) int { return lineAt(d.Text, at) }); err != nil {
		return Directed{}, err
	}
	return d, nil
}

// extractLine copies one line (at source offset off) to b without its directives.
func (d *Directed) extractLine(b *strings.Builder, line string, off, n int) error {
	keep := func(seg string) { b.WriteString(seg) }
	dirs, err := parseDirectives(line, n)
	if err != nil {
		return err
	}
	for _, x := range dirs {
		if x.title != "" && strings.TrimSpace(line[:x.start]+line[x.end:]) != "" {
			return &DirectiveError{n, fmt.Sprintf("[section %s] must stand on its own line", x.title)}
		}
	}
	pos := 0 // copied up to here, in line
	dir, rest, hasDir := leadingDirection(line)
	if hasDir {
		if len(dirs) > 0 && dirs[0].start < rest {
			hasDir = false // "([pause 1s])" is not a direction
		} else {
			keep(line[:len(line)-len(strings.TrimLeft(line, " \t"))])
			d.removed = append(d.removed, removal{off + len(line) - len(strings.TrimLeft(line, " \t")), off + rest})
			pos = rest
		}
	}
	spanStart := b.Len()
	for _, x := range dirs {
		keep(line[pos:x.start])
		d.removed = append(d.removed, removal{off + x.start, off + x.end})
		if x.title != "" {
			d.Sections = append(d.Sections, SectionAt{At: b.Len(), Title: x.title})
		} else {
			d.Pauses = append(d.Pauses, PauseAt{At: b.Len(), D: x.pause, Line: n})
		}
		pos = x.end
	}
	keep(line[pos:])
	if hasDir {
		end := spanStart + len(strings.TrimRight(b.String()[spanStart:], " \t"))
		if !speakable(b.String()[spanStart:end]) {
			return &DirectiveError{n, fmt.Sprintf("(%s) has no text after it on its line", dir)}
		}
		d.Spans = append(d.Spans, Voiced{Start: spanStart, End: end, Direction: dir})
	}
	return nil
}

// check validates the directives against the finished text and adds the breaks they force:
// a pause needs text on both sides, a section text after it; a direction's line is a chunk
// of its own.
func (d *Directed) check(line func(int) int) error {
	for _, p := range d.Pauses {
		if !speakable(d.Text[:p.At]) || !speakable(d.Text[p.At:]) {
			return &DirectiveError{p.Line, fmt.Sprintf("[pause %s] must sit between two pieces of text", p.D)}
		}
	}
	for i, sc := range d.Sections {
		next := len(d.Text)
		if i+1 < len(d.Sections) {
			next = d.Sections[i+1].At
		}
		if !speakable(d.Text[sc.At:next]) {
			return &DirectiveError{line(sc.At), fmt.Sprintf("[section %s] has no text before the next section or the end", sc.Title)}
		}
		if speakable(d.Text[:sc.At]) {
			d.Breaks = append(d.Breaks, Break{Display: sc.At, Kind: BoundarySection})
		}
	}
	for _, sp := range d.Spans {
		if speakable(d.Text[:sp.Start]) {
			d.Breaks = append(d.Breaks, Break{Display: sp.Start, Kind: BoundarySentence})
		}
		if speakable(d.Text[sp.End:]) {
			d.Breaks = append(d.Breaks, Break{Display: sp.End, Kind: BoundarySentence})
		}
	}
	return nil
}
