package subtitle

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// Format is a subtitle file format (--subtitles).
type Format string

// Subtitle formats.
const (
	SRT Format = "srt"
	VTT Format = "vtt"
)

// ParseFormat parses a --subtitles value.
func ParseFormat(s string) (Format, error) {
	switch f := Format(strings.ToLower(s)); f {
	case SRT, VTT:
		return f, nil
	}
	return "", fmt.Errorf("--subtitles %q: want srt or vtt", s)
}

// Ext is the file extension of f, with the dot.
func (f Format) Ext() string { return "." + string(f) }

// stamp formats d rounded to the millisecond as HH:MM:SS<sep>mmm.
func stamp(d time.Duration, sep string) string {
	ms := (d + time.Millisecond/2) / time.Millisecond
	return fmt.Sprintf("%02d:%02d:%02d%s%03d", ms/3_600_000, ms/60_000%60, ms/1000%60, sep, ms%1000)
}

// vttEscape escapes the characters WebVTT cue text gives meaning to; escaping ">" also keeps
// "-->" out of the text.
var vttEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// Write writes cues in format f.
func Write(w io.Writer, f Format, cues []Cue) error {
	var b strings.Builder
	switch f {
	case SRT:
		for i, c := range cues {
			fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", i+1, stamp(c.Start, ","), stamp(c.End, ","), strings.Join(c.Lines, "\n"))
		}
	case VTT:
		b.WriteString("WEBVTT\n\n")
		for _, c := range cues {
			fmt.Fprintf(&b, "%s --> %s\n%s\n\n", stamp(c.Start, "."), stamp(c.End, "."), vttEscape.Replace(strings.Join(c.Lines, "\n")))
		}
	default:
		return fmt.Errorf("subtitle: unknown format %q", f)
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("subtitle: write: %w", err)
	}
	return nil
}
