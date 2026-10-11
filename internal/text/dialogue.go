package text

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxSpeakerName bounds a speaker name in runes.
const MaxSpeakerName = 32

// ValidSpeakerName reports whether name can label a script line (design §7.4): 1 to
// MaxSpeakerName letters, digits, marks, '_', '-' or '.', with no spaces, so an ordinary
// sentence such as "In short: yes" is never taken for a label.
func ValidSpeakerName(name string) bool {
	if name == "" || utf8.RuneCountInString(name) > MaxSpeakerName {
		return false
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.IsMark(r) && r != '_' && r != '-' && r != '.' {
			return false
		}
	}
	return true
}

// utterance is one NAME: line and its continuation lines.
type utterance struct {
	line      int
	speaker   string
	direction string
	text      strings.Builder
	pauses    []PauseAt // At relative to text
}

// ParseDialogue reads the body of a dialogue script (TXT-06, design §7.4), the input after its
// front matter, whose first line is input line firstLine. speakers are the names of the
// front-matter speaker map.
//
//   - "NAME: text" starts a line spoken by NAME; NAME must be in the speaker map. A leading
//     "(direction)" after the colon sets that line's delivery.
//   - A non-blank line right after it continues it (joined with a space).
//   - A blank line ends it; the next text line needs a NAME: again.
//   - [pause DUR] and [section Title] on their own line sit between lines; [pause DUR] may also
//     sit inside a line.
//
// The result's Text holds the spoken lines only, one paragraph each, with no labels and no
// directives.
func ParseDialogue(body string, firstLine int, speakers []string) (Directed, error) {
	var lines []*utterance
	var pendingPauses []PauseAt // line-level pauses before the next utterance
	var pendingSection *SectionAt
	sectionLine := 0
	type gap struct {
		pauses  []PauseAt
		section *SectionAt
	}
	gaps := map[int]gap{} // directives before utterance i
	var cur *utterance
	flush := func() { cur = nil }
	srcLines := strings.Split(body, "\n")
	for i, raw := range srcLines {
		n := firstLine + i
		line := strings.TrimRight(raw, " \t\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flush()
			continue
		}
		dirs, err := parseDirectives(line, n)
		if err != nil {
			return Directed{}, err
		}
		if len(dirs) == 1 && dirs[0].start == len(line)-len(strings.TrimLeft(line, " \t")) && dirs[0].end == len(line) {
			flush()
			if dirs[0].title != "" {
				if pendingSection != nil {
					return Directed{}, &DirectiveError{sectionLine, fmt.Sprintf("[section %s] has no line before the next section", pendingSection.Title)}
				}
				pendingSection, sectionLine = &SectionAt{Title: dirs[0].title}, n
			} else {
				pendingPauses = append(pendingPauses, PauseAt{D: dirs[0].pause, Line: n})
			}
			continue
		}
		for _, x := range dirs {
			if x.title != "" {
				return Directed{}, &DirectiveError{n, fmt.Sprintf("[section %s] must stand on its own line", x.title)}
			}
		}
		name, rest, labelled := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		switch {
		case labelled && slices.Contains(speakers, name):
			u := &utterance{line: n, speaker: name}
			body := strings.TrimLeft(rest, " \t")
			if dir, after, ok := leadingDirection(body); ok {
				u.direction, body = dir, body[after:]
			}
			if err := u.add(body, n); err != nil {
				return Directed{}, err
			}
			if !speakable(u.text.String()) {
				return Directed{}, &DirectiveError{n, fmt.Sprintf("%s: the line has no text to speak", name)}
			}
			if len(pendingPauses) > 0 || pendingSection != nil {
				gaps[len(lines)] = gap{pendingPauses, pendingSection}
				pendingPauses, pendingSection = nil, nil
			}
			lines = append(lines, u)
			cur = u
		case labelled && ValidSpeakerName(name):
			return Directed{}, &DirectiveError{n, fmt.Sprintf("unknown speaker %q; the front-matter [voices] map names %s", name, speakerList(speakers))}
		case cur != nil:
			cur.text.WriteByte(' ')
			if err := cur.add(strings.TrimSpace(line), n); err != nil {
				return Directed{}, err
			}
		default:
			return Directed{}, &DirectiveError{n, "a line needs a speaker: NAME: text"}
		}
	}
	if len(lines) == 0 {
		return Directed{}, &DirectiveError{firstLine, "the script has no NAME: line to speak"}
	}
	if pendingSection != nil {
		return Directed{}, &DirectiveError{sectionLine, fmt.Sprintf("[section %s] has no line after it", pendingSection.Title)}
	}
	if len(pendingPauses) > 0 {
		return Directed{}, &DirectiveError{pendingPauses[0].Line, fmt.Sprintf("[pause %s] must sit between two lines", pendingPauses[0].D)}
	}
	var d Directed
	var b strings.Builder
	for i, u := range lines {
		g := gaps[i]
		if i == 0 && len(g.pauses) > 0 {
			return Directed{}, &DirectiveError{g.pauses[0].Line, fmt.Sprintf("[pause %s] must sit between two lines", g.pauses[0].D)}
		}
		if i > 0 {
			end := b.Len()
			kind := BoundaryParagraph
			if g.section != nil {
				kind = BoundarySection
			}
			d.Breaks = append(d.Breaks, Break{Display: end, Kind: kind})
			for _, p := range g.pauses {
				d.Pauses = append(d.Pauses, PauseAt{At: end, D: p.D, Line: p.Line})
			}
			b.WriteString("\n\n")
		}
		start := b.Len()
		if g.section != nil {
			d.Sections = append(d.Sections, SectionAt{At: start, Title: g.section.Title})
		}
		b.WriteString(u.text.String())
		for _, p := range u.pauses {
			d.Pauses = append(d.Pauses, PauseAt{At: start + p.At, D: p.D, Line: p.Line})
		}
		d.Spans = append(d.Spans, Voiced{Start: start, End: b.Len(), Speaker: u.speaker, Direction: u.direction})
	}
	d.Text = b.String()
	for _, p := range d.Pauses {
		if !speakable(d.Text[:p.At]) || !speakable(d.Text[p.At:]) {
			return Directed{}, &DirectiveError{p.Line, fmt.Sprintf("[pause %s] must sit between two pieces of text", p.D)}
		}
	}
	return d, nil
}

// add appends one line of an utterance's text without its inline pauses.
func (u *utterance) add(s string, n int) error {
	dirs, err := parseDirectives(s, n)
	if err != nil {
		return err
	}
	pos := 0
	for _, x := range dirs {
		u.text.WriteString(s[pos:x.start])
		u.pauses = append(u.pauses, PauseAt{At: u.text.Len(), D: x.pause, Line: n})
		pos = x.end
	}
	u.text.WriteString(s[pos:])
	return nil
}

func speakerList(names []string) string {
	if len(names) == 0 {
		return "no speakers"
	}
	s := slices.Clone(names)
	slices.Sort(s)
	return strings.Join(s, ", ")
}
