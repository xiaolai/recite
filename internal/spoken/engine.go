package spoken

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// RuleID names one spoken-form rule, such as "en.currency.scale" (design §7.7).
type RuleID string

// Range is a byte range [Start, End) of a string.
type Range struct{ Start, End int }

// Segment maps a display range to the spoken range it became. Rule is empty
// for text copied as written (including spans left alone as ambiguous).
// Segments of a Result are in order, do not overlap, and cover both texts.
type Segment struct {
	Display Range
	Spoken  Range
	Rule    RuleID
}

// Match is what a rule found at a position: the display text up to End
// becomes Spoken. A non-empty Ambiguous is the reason the rule could not
// decide; the span is then left as written and reported.
type Match struct {
	End       int
	Spoken    string
	Ambiguous string
}

// Rule is one rewrite. Match is called at a rune boundary i of s and reports
// whether the rule applies there; it must return End > i.
type Rule struct {
	ID    RuleID
	Match func(s string, i int, o *Options) (Match, bool)
}

// Table is a language's rules in priority order: at each position the first
// rule that matches wins.
type Table []Rule

// Mode is the --normalize setting.
type Mode string

// Modes (design §7.7).
const (
	ModeAuto Mode = "auto"
	ModeOff  Mode = "off"
)

// DateStyle is the --date-style setting: the reading order of spoken dates,
// and how a numeric date such as 10/11/2026 is read.
type DateStyle string

// Date styles (design §12.2).
const (
	DateUS  DateStyle = "us"  // October eleventh, twenty twenty-six; 10/11 is month/day
	DateUK  DateStyle = "uk"  // the eleventh of October, twenty twenty-six; 10/11 is day/month
	DateISO DateStyle = "iso" // twenty twenty-six, October eleventh; 10/11 is ambiguous
)

// Options configures one normalisation.
type Options struct {
	Mode      Mode // "" means auto
	Disabled  []RuleID
	DateStyle DateStyle // "" means us
	// Protected display ranges are copied as written (speech marks, §7.8).
	// They must be sorted, non-overlapping, in bounds and on rune boundaries.
	Protected []Range
}

// AmbiguousCode is the finding code for a span no rule could decide.
const AmbiguousCode = "N-AMBIGUOUS"

// Finding is an N-AMBIGUOUS span. Line and Col are 1-based; Col counts runes
// of the display text.
type Finding struct {
	Code   string
	Rule   RuleID
	Line   int
	Col    int
	Text   string
	Reason string
}

// String is the one-line report form, matching the guard's.
func (f Finding) String() string {
	return fmt.Sprintf("%d:%d %s %s: %s left as written (%s)", f.Line, f.Col, f.Code, f.Rule, strconv.Quote(f.Text), f.Reason)
}

// Rewrite is one rule application, for the report.
type Rewrite struct {
	Rule      RuleID
	Line, Col int
	Before    string
	After     string
}

func (r Rewrite) String() string {
	return fmt.Sprintf("%d:%d %s: %s → %s", r.Line, r.Col, r.Rule, strconv.Quote(r.Before), strconv.Quote(r.After))
}

// Result is the display text, its spoken form and the map between them.
type Result struct {
	Display  string
	Spoken   string
	Segments []Segment
	Findings []Finding
}

// Rewrites lists every rule application in text order.
func (r Result) Rewrites() []Rewrite {
	var out []Rewrite
	for _, sg := range r.Segments {
		if sg.Rule == "" {
			continue
		}
		line, col := position(r.Display, sg.Display.Start)
		out = append(out, Rewrite{Rule: sg.Rule, Line: line, Col: col,
			Before: r.Display[sg.Display.Start:sg.Display.End], After: r.Spoken[sg.Spoken.Start:sg.Spoken.End]})
	}
	return out
}

// Normalize applies the rules for language lang ("en", "en-US", …) to s.
// Languages without a table pass through unchanged.
func Normalize(s, lang string, o Options) (Result, error) {
	return Run(s, TableFor(lang), o)
}

// TableFor returns the rule table for a language code, or nil.
func TableFor(lang string) Table {
	base, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(lang)), "-")
	if base == "en" {
		return English
	}
	return nil
}

// KnownRules lists every built-in rule ID, sorted.
func KnownRules() []RuleID {
	var ids []RuleID
	for _, r := range English {
		if !slices.Contains(ids, r.ID) {
			ids = append(ids, r.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

// Run applies table t to s left to right. At each position outside the
// protected ranges the first enabled rule that matches rewrites its span;
// everything else is copied as written.
func Run(s string, t Table, o Options) (Result, error) {
	if err := o.validate(len(s), s); err != nil {
		return Result{}, err
	}
	b := builder{display: s}
	if o.Mode == ModeOff || len(t) == 0 {
		b.copy(len(s))
		return b.result(), nil
	}
	rules := make([]Rule, 0, len(t))
	for _, r := range t {
		if !slices.Contains(o.Disabled, r.ID) {
			rules = append(rules, r)
		}
	}
	pi := 0
	for b.pos < len(s) {
		limit := len(s)
		if pi < len(o.Protected) {
			p := o.Protected[pi]
			if b.pos >= p.Start {
				b.copy(p.End)
				pi++
				continue
			}
			limit = p.Start
		}
		if !b.tryRules(rules, &o, limit) {
			_, n := utf8.DecodeRuneInString(s[b.pos:])
			b.copy(b.pos + n)
		}
	}
	return b.result(), nil
}

func (o *Options) validate(n int, s string) error {
	switch o.Mode {
	case "", ModeAuto, ModeOff:
	default:
		return fmt.Errorf("spoken form: unknown mode %q", o.Mode)
	}
	switch o.DateStyle {
	case "", DateUS, DateUK, DateISO:
	default:
		return fmt.Errorf("spoken form: unknown date style %q", o.DateStyle)
	}
	prev := 0
	for _, p := range o.Protected {
		if p.Start < prev || p.End < p.Start || p.End > n {
			return fmt.Errorf("spoken form: protected range [%d,%d) is out of order or out of bounds", p.Start, p.End)
		}
		if !utf8.RuneStart(byteAt(s, p.Start)) || !utf8.RuneStart(byteAt(s, p.End)) {
			return fmt.Errorf("spoken form: protected range [%d,%d) splits a character", p.Start, p.End)
		}
		prev = p.End
	}
	return nil
}

func byteAt(s string, i int) byte {
	if i >= len(s) {
		return 0
	}
	return s[i]
}

// builder accumulates the spoken form and its segments.
type builder struct {
	display  string
	pos      int
	spoken   strings.Builder
	segs     []Segment
	findings []Finding
}

// tryRules applies the first matching rule at b.pos whose span ends by limit.
func (b *builder) tryRules(rules []Rule, o *Options, limit int) bool {
	for _, r := range rules {
		m, ok := r.Match(b.display, b.pos, o)
		if !ok || m.End <= b.pos || m.End > limit {
			continue
		}
		if m.Ambiguous != "" {
			line, col := position(b.display, b.pos)
			b.findings = append(b.findings, Finding{Code: AmbiguousCode, Rule: r.ID, Line: line, Col: col,
				Text: b.display[b.pos:m.End], Reason: m.Ambiguous})
			b.copy(m.End)
			return true
		}
		start := b.spoken.Len()
		b.spoken.WriteString(m.Spoken)
		b.segs = append(b.segs, Segment{Display: Range{b.pos, m.End}, Spoken: Range{start, b.spoken.Len()}, Rule: r.ID})
		b.pos = m.End
		return true
	}
	return false
}

// copy copies the display text up to end as written, extending the previous
// verbatim segment when there is one.
func (b *builder) copy(end int) {
	if end <= b.pos {
		return
	}
	start := b.spoken.Len()
	b.spoken.WriteString(b.display[b.pos:end])
	if n := len(b.segs); n > 0 && b.segs[n-1].Rule == "" {
		b.segs[n-1].Display.End = end
		b.segs[n-1].Spoken.End = b.spoken.Len()
	} else {
		b.segs = append(b.segs, Segment{Display: Range{b.pos, end}, Spoken: Range{start, b.spoken.Len()}})
	}
	b.pos = end
}

func (b *builder) result() Result {
	return Result{Display: b.display, Spoken: b.spoken.String(), Segments: b.segs, Findings: b.findings}
}

// position is the 1-based line and column (in runes) of byte offset off in s.
func position(s string, off int) (line, col int) {
	line, col = 1, 1
	for _, r := range s[:off] {
		if r == '\n' {
			line, col = line+1, 1
			continue
		}
		col++
	}
	return line, col
}

// ParseMode validates a --normalize value.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(s); m {
	case ModeAuto, ModeOff:
		return m, nil
	}
	return "", fmt.Errorf("--normalize must be auto or off, not %q", s)
}

// ParseDateStyle validates a --date-style value.
func ParseDateStyle(s string) (DateStyle, error) {
	switch d := DateStyle(s); d {
	case DateUS, DateUK, DateISO:
		return d, nil
	}
	return "", fmt.Errorf("--date-style must be us, uk or iso, not %q", s)
}

// ParseDisabled parses a comma-separated --normalize-disable value against
// the built-in rule IDs.
func ParseDisabled(spec string) ([]RuleID, error) {
	known := KnownRules()
	var out []RuleID
	for _, f := range strings.Split(spec, ",") {
		id := RuleID(strings.TrimSpace(f))
		if id == "" {
			continue
		}
		if !slices.Contains(known, id) {
			names := make([]string, len(known))
			for i, k := range known {
				names[i] = string(k)
			}
			return nil, fmt.Errorf("--normalize-disable: unknown rule %q (rules: %s)", id, strings.Join(names, ", "))
		}
		out = append(out, id)
	}
	return out, nil
}
