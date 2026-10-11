package guard

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Code identifies a guard rule (design §7.6).
type Code string

// The guard codes of design §7.6.
const (
	Encoding    Code = "G-ENCODING"
	Control     Code = "G-CONTROL"
	Whitespace  Code = "G-WHITESPACE"
	RepeatPunct Code = "G-REPEAT-PUNCT"
	Markup      Code = "G-MARKUP"
	URL         Code = "G-URL"
	Email       Code = "G-EMAIL"
	Path        Code = "G-PATH"
	CodeToken   Code = "G-CODE"
	LongToken   Code = "G-LONG-TOKEN"
	Footnote    Code = "G-FOOTNOTE"
	Emoji       Code = "G-EMOJI"
	Symbol      Code = "G-SYMBOL"
	Homoglyph   Code = "G-HOMOGLYPH"
	Request     Code = "G-REQUEST"
	Marker      Code = "G-MARKER"
	Mark        Code = "G-MARK"
	Typo        Code = "G-TYPO"
	Empty       Code = "G-EMPTY"
)

// Action is what the guard does about a finding.
type Action string

// Actions. Not every action applies to every code; see Allowed.
const (
	ActRemove   Action = "remove"
	ActFail     Action = "fail"
	ActCollapse Action = "collapse"
	ActKeep     Action = "keep"
	ActStrip    Action = "strip"
	ActDrop     Action = "drop"
	ActHost     Action = "host"
	ActFull     Action = "full"
	ActSpeak    Action = "speak"
	ActReplace  Action = "replace"
	ActFlag     Action = "flag"
	ActEscape   Action = "escape"
	ActWarn     Action = "warn"
	ActSkip     Action = "skip"
)

// Severity orders findings; strict stops on Warn or worse.
type Severity int

// Severities.
const (
	Info Severity = iota
	Warn
	Fatal
)

func (s Severity) String() string {
	switch s {
	case Info:
		return "info"
	case Warn:
		return "warn"
	}
	return "error"
}

// rule is one code's default action, the actions it may be set to, and the
// severity of acting on it.
type rule struct {
	def     Action
	allowed []Action
	sev     Severity
}

// rules is design §7.6. Codes with no configurable action allow only their
// default. Severities: warn where the table says "with a warning" or "warn",
// and for G-CONTROL and G-MARKER, whose inputs are the usual vehicles for
// hidden or injected text.
var rules = map[Code]rule{
	Encoding:    {ActFail, nil, Fatal},
	Control:     {ActRemove, []Action{ActFail}, Warn},
	Whitespace:  {ActCollapse, []Action{ActKeep}, Info},
	RepeatPunct: {ActCollapse, []Action{ActKeep}, Info},
	Markup:      {ActStrip, []Action{ActDrop, ActFail}, Info},
	URL:         {ActHost, []Action{ActFull, ActDrop, ActFail}, Info},
	Email:       {ActSpeak, []Action{ActDrop, ActFail}, Info},
	Path:        {ActDrop, []Action{ActSpeak, ActFail}, Warn},
	CodeToken:   {ActKeep, []Action{ActDrop, ActFail}, Info},
	LongToken:   {ActDrop, []Action{ActFail}, Warn},
	Footnote:    {ActRemove, []Action{ActKeep}, Info},
	Emoji:       {ActRemove, []Action{ActKeep, ActFail}, Info},
	Symbol:      {ActRemove, []Action{ActKeep, ActFail}, Warn},
	Homoglyph:   {ActReplace, []Action{ActFail}, Warn},
	Request:     {ActFlag, nil, Info},
	Marker:      {ActEscape, nil, Warn},
	Mark:        {ActStrip, nil, Warn},
	Empty:       {ActSkip, nil, Warn},
}

// Codes lists every code in report order.
func Codes() []Code {
	return []Code{Encoding, Control, Whitespace, RepeatPunct, Markup, URL, Email, Path, CodeToken,
		LongToken, Footnote, Emoji, Symbol, Homoglyph, Request, Marker, Mark, Typo, Empty}
}

// Allowed lists the actions code may be set to, default first.
func Allowed(code Code) []Action {
	r, ok := rules[code]
	if !ok {
		return nil
	}
	return append([]Action{r.def}, r.allowed...)
}

// Finding is one guard observation. Line and Col are 1-based; Col counts
// characters (runes) in the normalised input. Before is the original text of
// the span, After what the guard put in its place (equal to Before when the
// text is kept).
type Finding struct {
	Code     Code
	Line     int
	Col      int
	Severity Severity
	Action   Action
	Before   string
	After    string
}

// String is the one-line report form: `line:col CODE action: before → after`.
func (f Finding) String() string {
	return fmt.Sprintf("%d:%d %s %s: %s → %s", f.Line, f.Col, f.Code, f.Action, quote(f.Before), quote(f.After))
}

// quote makes s safe for one terminal line: control, bidi and other
// non-printing characters are escaped, and long spans are shortened.
func quote(s string) string {
	const max = 60
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return strconv.Quote(s)
}

// Edit is one span edit: the original byte range [Start, End) of the input
// was replaced by Replacement, by the rules in Codes.
type Edit struct {
	Start, End  int
	Replacement string
	Codes       []Code
}

// Policy is the --guard setting.
type Policy string

// Policies (design §7.6).
const (
	PolicyFix    Policy = "fix"
	PolicyStrict Policy = "strict"
	PolicyWarn   Policy = "warn"
	PolicyOff    Policy = "off"
)

// ParsePolicy validates a --guard value.
func ParsePolicy(s string) (Policy, error) {
	switch p := Policy(s); p {
	case PolicyFix, PolicyStrict, PolicyWarn, PolicyOff:
		return p, nil
	}
	return "", fmt.Errorf("--guard must be fix, strict, warn or off, not %q", s)
}

// ParseAction validates a per-code action from config [guard] (TXT-09).
func ParseAction(code Code, s string) (Action, error) {
	if code == Typo {
		return "", fmt.Errorf("G-TYPO is not detected; its report action needs the Codex proofreading pass (INT-04)")
	}
	allowed := Allowed(code)
	if allowed == nil {
		return "", fmt.Errorf("unknown guard code %q", code)
	}
	if act := Action(s); slices.Contains(allowed, act) {
		return act, nil
	}
	names := make([]string, len(allowed))
	for i, a := range allowed {
		names[i] = string(a)
	}
	return "", fmt.Errorf("%s takes %s, not %q", code, strings.Join(names, ", "), s)
}

// Error is a guard failure: a finding whose action is fail, a strict policy
// with warn-level findings, or an input with nothing to speak. It is a usage
// failure (exit 2). Findings holds the full report.
type Error struct {
	Reason   string
	Findings []Finding
}

func (e *Error) Error() string { return "text guard: " + e.Reason }
