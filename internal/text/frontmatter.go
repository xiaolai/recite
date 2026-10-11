package text

import "strings"

// FrontMatterFence opens and closes a TOML front-matter block (TXT-08).
const FrontMatterFence = "+++"

// FrontMatter is the TOML block at the top of a Markdown or script input.
type FrontMatter struct {
	TOML string
	// Line is the 1-based input line of the block's first TOML line, so a TOML line n is
	// input line Line+n-1.
	Line int
}

// SplitFrontMatter takes a leading front-matter block off s: a first line that is exactly
// "+++", the TOML, and the next line that is exactly "+++". It returns nil when s does not
// open with a fence. An unclosed block is an input error (exit 2), not text to read aloud.
func SplitFrontMatter(s string) (*FrontMatter, string, error) {
	first, rest, ok := strings.Cut(s, "\n")
	if !ok || strings.TrimRight(first, " \t") != FrontMatterFence {
		return nil, s, nil
	}
	off := 0
	for {
		line, after, more := strings.Cut(rest[off:], "\n")
		if strings.TrimRight(line, " \t") == FrontMatterFence {
			return &FrontMatter{TOML: rest[:off], Line: 2}, after, nil
		}
		if !more {
			return nil, s, &InputError{Msg: "front matter opened with +++ on line 1 is never closed", Offset: -1}
		}
		off += len(line) + 1
	}
}
