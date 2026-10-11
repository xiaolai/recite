package text

import (
	"fmt"
	"strings"
	"time"

	"golang.org/x/text/language"
)

// MarkKind is the kind of a speech mark (design §7.8).
type MarkKind string

// Speech mark kinds.
const (
	MarkSay      MarkKind = "say"
	MarkSpell    MarkKind = "spell"
	MarkDigits   MarkKind = "digits"
	MarkNum      MarkKind = "num"
	MarkDate     MarkKind = "date"
	MarkFraction MarkKind = "fraction"
	MarkVerbatim MarkKind = "verbatim"
	MarkPause    MarkKind = "pause"
	MarkHint     MarkKind = "hint"
	MarkEmph     MarkKind = "emph"
	MarkLang     MarkKind = "lang"
)

// markSyntax is what each kind takes: a value after ":" and a text after "|".
var markSyntax = map[MarkKind]struct{ value, text bool }{
	MarkSay: {true, true}, MarkSpell: {false, true}, MarkDigits: {false, true}, MarkNum: {false, true},
	MarkDate: {false, true}, MarkFraction: {false, true}, MarkVerbatim: {false, true},
	MarkPause: {true, false}, MarkHint: {true, true}, MarkEmph: {false, true}, MarkLang: {true, true},
}

// MaxPause bounds a {{pause}}; it equals the longest gap assembly accepts (audio.MaxGap).
const MaxPause = 10 * time.Second

// Mark is one well-formed speech mark. Start and End are the byte range of the
// whole `{{…}}` in the parsed string.
type Mark struct {
	Kind       MarkKind
	Value      string        // after ":"; "" when the kind takes none
	Text       string        // after "|": the display text; "" for pause
	Pause      time.Duration // MarkPause only
	Start, End int
}

// String is the mark's canonical syntax; parsing it gives the same mark.
func (m Mark) String() string {
	var b strings.Builder
	b.WriteString("{{")
	b.WriteString(string(m.Kind))
	if markSyntax[m.Kind].value {
		b.WriteByte(':')
		if m.Kind == MarkPause {
			b.WriteString(m.Pause.String())
		} else {
			b.WriteString(m.Value)
		}
	}
	if markSyntax[m.Kind].text {
		b.WriteByte('|')
		b.WriteString(m.Text)
	}
	b.WriteString("}}")
	return b.String()
}

// BadMark is a malformed mark (G-MARK). Plain is the text that remains once
// the mark syntax is stripped, so no brace reaches the model.
type BadMark struct {
	Start, End int
	Reason     string
	Plain      string
}

// ParseMarks finds every `{{…}}` in s. A mark must close on its line, may not
// contain another mark, and must name a known kind with the parts that kind
// takes; anything else is a BadMark. Text outside `{{` is never a mark: a lone
// `}}` is ordinary text.
func ParseMarks(s string) ([]Mark, []BadMark) {
	var marks []Mark
	var bad []BadMark
	for i := 0; i < len(s); {
		j := strings.Index(s[i:], "{{")
		if j < 0 {
			break
		}
		start := i + j
		end, nested, ok := markEnd(s, start)
		if !ok {
			bad = append(bad, BadMark{Start: start, End: start + 2, Reason: "unclosed mark: no }} on its line"})
			i = start + 2
			continue
		}
		body := s[start+2 : end-2]
		if nested {
			bad = append(bad, BadMark{Start: start, End: end, Reason: "marks cannot be nested", Plain: plainOf(body)})
			i = end
			continue
		}
		m, reason := parseMark(body)
		if reason != "" {
			bad = append(bad, BadMark{Start: start, End: end, Reason: reason, Plain: plainOf(body)})
		} else {
			m.Start, m.End = start, end
			marks = append(marks, m)
		}
		i = end
	}
	return marks, bad
}

// markEnd finds the `}}` closing the mark at start, counting inner `{{`, on the
// same line. nested reports an inner mark.
func markEnd(s string, start int) (end int, nested, ok bool) {
	depth := 0
	for k := start; k+1 < len(s); {
		switch {
		case s[k] == '\n':
			return 0, false, false
		case s[k] == '{' && s[k+1] == '{':
			depth++
			if depth > 1 {
				nested = true
			}
			k += 2
		case s[k] == '}' && s[k+1] == '}':
			depth--
			k += 2
			if depth == 0 {
				return k, nested, true
			}
		default:
			k++
		}
	}
	return 0, false, false
}

func parseMark(body string) (Mark, string) {
	head, txt, hasText := strings.Cut(body, "|")
	name, val, hasValue := strings.Cut(head, ":")
	kind := MarkKind(name)
	syn, known := markSyntax[kind]
	switch {
	case !known:
		return Mark{}, fmt.Sprintf("unknown mark kind %q", name)
	case syn.value && (!hasValue || strings.TrimSpace(val) == ""):
		return Mark{}, fmt.Sprintf("{{%s}} needs a value: {{%s:…%s}}", kind, kind, map[bool]string{true: "|…", false: ""}[syn.text])
	case !syn.value && hasValue:
		return Mark{}, fmt.Sprintf("{{%s}} takes no value after ':'", kind)
	case syn.text && (!hasText || strings.TrimSpace(txt) == ""):
		return Mark{}, fmt.Sprintf("{{%s}} needs a text after '|'", kind)
	case !syn.text && hasText:
		return Mark{}, fmt.Sprintf("{{%s}} takes no text after '|'", kind)
	}
	m := Mark{Kind: kind, Value: val, Text: txt}
	switch kind {
	case MarkPause:
		d, err := time.ParseDuration(strings.TrimSpace(val))
		if err != nil || d <= 0 || d > MaxPause {
			return Mark{}, fmt.Sprintf("pause %q: want a duration such as 800ms or 1.5s, up to %s", val, MaxPause)
		}
		m.Value, m.Pause = "", d
	case MarkLang:
		if _, err := language.Parse(val); err != nil {
			return Mark{}, fmt.Sprintf("language %q is not a BCP 47 tag", val)
		}
	}
	return m, ""
}

// plainOf is what a malformed mark's body leaves once stripped: inner marks
// are reduced the same way first, then the text after the first '|' is kept
// when there is one, else the whole body.
func plainOf(body string) string {
	for {
		shut := strings.Index(body, "}}")
		if shut < 0 {
			break
		}
		open := strings.LastIndex(body[:shut], "{{")
		if open < 0 {
			body = body[:shut] + body[shut+2:]
			continue
		}
		body = body[:open] + plainOf(body[open+2:shut]) + body[shut+2:]
	}
	body = strings.ReplaceAll(body, "{{", "")
	if _, txt, ok := strings.Cut(body, "|"); ok {
		return txt
	}
	return body
}
