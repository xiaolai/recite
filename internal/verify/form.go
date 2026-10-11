package verify

import "github.com/xiaolai/recite/internal/spoken"

// form is a text taken through the spoken-form rules, with the span map back to the text.
// STT writes numbers, dates, times and symbols in written form ("3:15 p.m.", "1990s") while
// the reference is what was rendered ("three fifteen p m", "nineteen nineties"). Running both
// sides through the same rules compares them in one vocabulary: a correct take scores as one,
// and a wrong number becomes different words, so the guards still see it. The rules are
// idempotent, so a reference already in spoken form is unchanged.
type form struct {
	text string
	segs []spoken.Segment // empty means text is the original
}

func toForm(s, lang string) form {
	r, err := spoken.Normalize(s, lang, spoken.Options{})
	if err != nil {
		// Only invalid input fails; comparing the original text can only lower the score.
		return form{text: s}
	}
	return form{text: r.Spoken, segs: r.Segments}
}

// back maps the range [start, end) of the form to the range of the original it came from. A
// range touching a rewritten span covers that span's whole original.
func (f form) back(start, end int) (int, int) {
	if len(f.segs) == 0 {
		return start, end
	}
	return f.start(start), f.end(end)
}

func (f form) start(off int) int {
	for _, sg := range f.segs {
		if off < sg.Spoken.End {
			if sg.Rule == "" && off >= sg.Spoken.Start {
				return sg.Display.Start + off - sg.Spoken.Start
			}
			return sg.Display.Start
		}
	}
	return f.segs[len(f.segs)-1].Display.End
}

func (f form) end(off int) int {
	for _, sg := range f.segs {
		if off <= sg.Spoken.Start {
			return sg.Display.Start
		}
		if off <= sg.Spoken.End {
			if sg.Rule == "" {
				return sg.Display.Start + off - sg.Spoken.Start
			}
			return sg.Display.End
		}
	}
	return f.segs[len(f.segs)-1].Display.End
}
