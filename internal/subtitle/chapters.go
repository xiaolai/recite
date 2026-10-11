package subtitle

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Chapter is one section on the assembly timeline (OUT-04).
type Chapter struct {
	Title      string
	Start, End time.Duration
}

// ffEscape escapes the characters ffmetadata gives meaning to.
var ffEscape = strings.NewReplacer(`\`, `\\`, "=", `\=`, ";", `\;`, "#", `\#`, "\n", `\`+"\n")

// FFMetadata is the ffmetadata file that puts chapters into an m4a or m4b through ffmpeg
// (-map_chapters), in milliseconds. An MP4 chapter track has no hole before its first
// chapter, and an untitled chapter is dropped from it: give every chapter a title, and start
// the first at zero, or a player stretches it back to zero.
func FFMetadata(chapters []Chapter) string {
	var b strings.Builder
	b.WriteString(";FFMETADATA1\n")
	for _, c := range chapters {
		fmt.Fprintf(&b, "[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\ntitle=%s\n",
			c.Start.Round(time.Millisecond).Milliseconds(), c.End.Round(time.Millisecond).Milliseconds(), ffEscape.Replace(c.Title))
	}
	return b.String()
}

// maxLeadIn bounds the title of a lead-in chapter, in runes.
const maxLeadIn = 40

// LeadIn titles the chapter of the text before the first heading: its first sentence, cut
// at a word (or, for unspaced text, a character) with an ellipsis when it is long.
func LeadIn(text string) string {
	s := ""
	if ss := textSentences(text); len(ss) > 0 {
		s = ss[0]
	}
	r := []rune(s)
	if len(r) <= maxLeadIn {
		return s
	}
	cut := string(r[:maxLeadIn])
	if i := strings.LastIndexByte(cut, ' '); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:") + "…"
}

// maxSlug bounds a slug in runes; with the NN- prefix and an extension the name stays far
// below the 255-byte limit of common filesystems even in 4-byte runes.
const maxSlug = 48

// fallbackSlug names a section whose title has no letter or digit.
const fallbackSlug = "section"

// Slug turns a heading into a filesystem-safe name part (OUT-05): NFC letters, marks and
// digits, lower-cased, with every other run (separators, punctuation, control and path
// characters) becoming one hyphen, and no leading or trailing hyphen.
func Slug(title string) string {
	var out []rune
	dash := false
	for _, r := range norm.NFC.String(title) {
		if len(out) >= maxSlug {
			break
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) && len(out) > 0 {
			if dash {
				out = append(out, '-')
				dash = false
			}
			out = append(out, unicode.ToLower(r))
			continue
		}
		dash = len(out) > 0
	}
	s := strings.TrimRight(string(out[:min(len(out), maxSlug)]), "-")
	if s == "" {
		return fallbackSlug
	}
	return s
}

// FileName is the name of section i (0-based) of n: NN-<slug><ext>, the number zero-padded
// to two digits or to as many as n needs, so names sort in section order.
func FileName(i, n int, title, ext string) string {
	width := max(2, len(fmt.Sprint(n)))
	return fmt.Sprintf("%0*d-%s%s", width, i+1, Slug(title), ext)
}
