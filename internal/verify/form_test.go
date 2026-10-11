package verify

import (
	"testing"
	"unicode/utf8"
)

// Every range of a form maps back to an ordered range inside the original, so an edit can
// always be sliced out of the reference or the transcript.
func FuzzFormBack(f *testing.F) {
	for _, s := range []string{"", "In 2026, the fee rose from 3,000 yen.", "Meet at 3:15 p.m. in room 204.",
		"Henry VIII, 1990s, $29.99, v1.2.3", "第1/2季度", "St. A", "价格是¥1,280.50，增长了12.5%。"} {
		f.Add(s, uint16(0), uint16(7), false)
	}
	f.Fuzz(func(t *testing.T, s string, a, b uint16, zh bool) {
		if !utf8.ValidString(s) {
			return
		}
		lang := "en"
		if zh {
			lang = "zh"
		}
		fm := toForm(s, lang)
		n := len(fm.text) + 1
		lo, hi := int(a)%n, int(b)%n
		if lo > hi {
			lo, hi = hi, lo
		}
		x, y := fm.back(lo, hi)
		if x < 0 || x > y || y > len(s) {
			t.Fatalf("form %q [%d,%d) maps to [%d,%d) of %q", fm.text, lo, hi, x, y, s)
		}
	})
}
