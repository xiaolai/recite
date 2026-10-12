package subtitle

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
)

const rate = 24000

// Cue times past the range of samples*time.Second stay positive and exact.
func TestCueTimeLongTimeline(t *testing.T) {
	const hours = 108
	if got := at(hours*3600*rate, rate); got != hours*time.Hour {
		t.Fatalf("at(%d h) = %v", hours, got)
	}
}

// parsedCue is a cue read back from a file.
type parsedCue struct {
	Start, End time.Duration
	Lines      []string
}

var (
	srtTime = regexp.MustCompile(`^(\d{2}):(\d{2}):(\d{2}),(\d{3}) --> (\d{2}):(\d{2}):(\d{2}),(\d{3})$`)
	vttTime = regexp.MustCompile(`^(\d{2,}):([0-5]\d):([0-5]\d)\.(\d{3}) --> (\d{2,}):([0-5]\d):([0-5]\d)\.(\d{3})$`)
)

func stampOf(m []string) (time.Duration, time.Duration) {
	d := func(h, mi, s, ms string) time.Duration {
		n := func(v string) time.Duration { x, _ := strconv.Atoi(v); return time.Duration(x) }
		return n(h)*time.Hour + n(mi)*time.Minute + n(s)*time.Second + n(ms)*time.Millisecond
	}
	return d(m[1], m[2], m[3], m[4]), d(m[5], m[6], m[7], m[8])
}

// parseSRT is a strict SRT reader: blocks of a sequential index from 1, a timing line, and one
// or more non-empty text lines, separated by exactly one blank line.
func parseSRT(t *testing.T, s string) []parsedCue {
	t.Helper()
	if !strings.HasSuffix(s, "\n\n") {
		t.Fatalf("SRT does not end with a blank line: %q", s)
	}
	var out []parsedCue
	for i, block := range strings.Split(strings.TrimSuffix(s, "\n\n"), "\n\n") {
		lines := strings.Split(block, "\n")
		if len(lines) < 3 {
			t.Fatalf("SRT block %d: %q", i+1, block)
		}
		if lines[0] != strconv.Itoa(i+1) {
			t.Fatalf("SRT block %d has index %q", i+1, lines[0])
		}
		m := srtTime.FindStringSubmatch(lines[1])
		if m == nil {
			t.Fatalf("SRT block %d timing %q", i+1, lines[1])
		}
		for _, l := range lines[2:] {
			if strings.TrimSpace(l) == "" {
				t.Fatalf("SRT block %d has a blank text line", i+1)
			}
		}
		st, en := stampOf(m)
		out = append(out, parsedCue{st, en, lines[2:]})
	}
	return out
}

// parseVTT validates WebVTT the way the W3C validator does for the subset recite writes: the
// WEBVTT signature, cues of a timing line (hours, minutes and seconds in range, end after
// start) and text with no "-->", no raw "<" and only the named escapes, separated by blank
// lines.
func parseVTT(t *testing.T, s string) []parsedCue {
	t.Helper()
	rest, ok := strings.CutPrefix(s, "WEBVTT\n\n")
	if !ok {
		t.Fatalf("no WEBVTT header: %q", s)
	}
	if rest == "" {
		return nil
	}
	if !strings.HasSuffix(rest, "\n\n") {
		t.Fatalf("VTT does not end with a blank line")
	}
	entity := regexp.MustCompile(`&(amp|lt|gt);`)
	var out []parsedCue
	for i, block := range strings.Split(strings.TrimSuffix(rest, "\n\n"), "\n\n") {
		lines := strings.Split(block, "\n")
		m := vttTime.FindStringSubmatch(lines[0])
		if m == nil || len(lines) < 2 {
			t.Fatalf("VTT cue %d: %q", i+1, block)
		}
		var text []string
		for _, l := range lines[1:] {
			if l == "" || strings.Contains(l, "-->") || strings.Contains(l, "<") || strings.Contains(entity.ReplaceAllString(l, ""), "&") {
				t.Fatalf("VTT cue %d: bad text line %q", i+1, l)
			}
			text = append(text, strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&").Replace(l))
		}
		st, en := stampOf(m)
		if en <= st {
			t.Fatalf("VTT cue %d ends at %v, not after its start %v", i+1, en, st)
		}
		out = append(out, parsedCue{st, en, text})
	}
	return out
}

func sec(s float64) int64 { return int64(s * rate) }

// A three-chunk timeline: a gap after each chunk but the last.
var timeline = []Chunk{
	{Text: "The first sentence is here. A second, much longer sentence follows it and runs on for a good while before it ends.", Start: 0, End: sec(7.3)},
	{Text: "One chunk, one sentence & <tags> --> safe.", Start: sec(7.9), End: sec(10.04167)},
	{Text: "第一句话在这里。第二句话比第一句话长得多，一直说到很远很远的地方才结束。", Start: sec(11.2), End: sec(19.5)},
}

func writeBoth(t *testing.T, cues []Cue) (srt, vtt []parsedCue) {
	t.Helper()
	var s, v bytes.Buffer
	if err := Write(&s, SRT, cues); err != nil {
		t.Fatal(err)
	}
	if err := Write(&v, VTT, cues); err != nil {
		t.Fatal(err)
	}
	return parseSRT(t, s.String()), parseVTT(t, v.String())
}

// Both files parse strictly and carry the same cues; VTT text is escaped and round-trips.
// accept: OUT-03#1
func TestSRTAndVTTParse(t *testing.T) {
	cues, err := Cues(timeline, rate)
	if err != nil {
		t.Fatal(err)
	}
	srt, vtt := writeBoth(t, cues)
	if len(srt) != len(cues) || len(vtt) != len(cues) {
		t.Fatalf("%d cues, SRT %d, VTT %d", len(cues), len(srt), len(vtt))
	}
	for i := range cues {
		if srt[i].Start != vtt[i].Start || srt[i].End != vtt[i].End || strings.Join(srt[i].Lines, "|") != strings.Join(vtt[i].Lines, "|") {
			t.Fatalf("cue %d differs: SRT %+v, VTT %+v", i+1, srt[i], vtt[i])
		}
	}
	if !strings.Contains(strings.Join(vtt[3].Lines, " "), "& <tags> --> safe.") {
		t.Fatalf("escaped text did not round-trip: %q", vtt[3].Lines)
	}
	var empty bytes.Buffer
	if err := Write(&empty, VTT, nil); err != nil || parseVTT(t, empty.String()) != nil {
		t.Fatalf("empty VTT: %v %q", err, empty.String())
	}
}

// Cue times only go forward and stay inside the audio, for many random-length timelines.
// accept: OUT-03#2
func TestCuesMonotonicWithinDuration(t *testing.T) {
	words := strings.Fields("alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi omicron pi rho")
	for seed := range 200 {
		var chunks []Chunk
		var pos int64
		for k := range 1 + seed%7 {
			var b strings.Builder
			for s := range 1 + (seed+k)%5 {
				for w := range 1 + (seed*7+s*3+k)%23 {
					b.WriteString(words[(seed+s+w)%len(words)] + " ")
				}
				b.WriteString("end. ")
			}
			length := int64(rate + (seed*131+k*17)%(rate*20))
			chunks = append(chunks, Chunk{Text: b.String(), Start: pos, End: pos + length})
			pos += length + int64((seed*13+k)%(rate/2))
		}
		duration := time.Duration(chunks[len(chunks)-1].End * int64(time.Second) / rate)
		cues, err := Cues(chunks, rate)
		if err != nil {
			t.Fatal(err)
		}
		srt, _ := writeBoth(t, cues)
		var prev time.Duration
		for i, c := range srt {
			if c.Start < prev || c.End < c.Start || c.End > duration.Round(time.Millisecond) {
				t.Fatalf("seed %d cue %d [%v, %v] after %v, audio %v", seed, i+1, c.Start, c.End, prev, duration)
			}
			prev = c.End
		}
	}
	if _, err := Cues([]Chunk{{Text: "a", Start: 10, End: 20}, {Text: "b", Start: 15, End: 30}}, rate); err == nil {
		t.Fatal("overlapping chunks accepted")
	}
}

// The first cue of a chunk starts, and the last one ends, on the chunk's edges to within
// 10 ms in the written file (the rounding is half a millisecond).
// accept: OUT-03#3
func TestChunkBoundaryCuesWithin10ms(t *testing.T) {
	cues, err := Cues(timeline, rate)
	if err != nil {
		t.Fatal(err)
	}
	srt, vtt := writeBoth(t, cues)
	edges := func(c Chunk) (time.Duration, time.Duration) {
		return time.Duration(c.Start * int64(time.Second) / rate), time.Duration(c.End * int64(time.Second) / rate)
	}
	// Each chunk's cues, counted by timing it alone.
	var firsts, lasts []int
	for _, c := range timeline {
		own, err := Cues([]Chunk{c}, rate)
		if err != nil || len(own) == 0 {
			t.Fatal(err)
		}
		n := 0
		if len(lasts) > 0 {
			n = lasts[len(lasts)-1] + 1
		}
		firsts, lasts = append(firsts, n), append(lasts, n+len(own)-1)
	}
	for k, c := range timeline {
		st, en := edges(c)
		for _, got := range [][]parsedCue{srt, vtt} {
			if d := (got[firsts[k]].Start - st).Abs(); d > 10*time.Millisecond {
				t.Fatalf("chunk %d first cue starts %v from its edge", k+1, d)
			}
			if d := (got[lasts[k]].End - en).Abs(); d > 10*time.Millisecond {
				t.Fatalf("chunk %d last cue ends %v from its edge", k+1, d)
			}
		}
	}
	if lasts[len(lasts)-1] != len(cues)-1 || len(cues) <= len(timeline) {
		t.Fatalf("%d cues: %+v", len(cues), cues)
	}
}

// Lines stay within 2 × 42 (CJK 2 × 16); a sentence too long for one cue becomes several, and
// no text is lost.
func TestCueLineLimits(t *testing.T) {
	long := strings.Repeat("word ", 40) + "supercalifragilisticexpialidocious-and-then-some-more-letters end."
	cjk := strings.Repeat("汉字", 30) + "。"
	cues, err := Cues([]Chunk{{Text: long + " " + cjk, Start: 0, End: sec(30)}}, rate)
	if err != nil {
		t.Fatal(err)
	}
	var got strings.Builder
	for _, c := range cues {
		if len(c.Lines) == 0 || len(c.Lines) > MaxLines {
			t.Fatalf("cue has %d lines", len(c.Lines))
		}
		for _, l := range c.Lines {
			limit := MaxLineLen
			if isCJKText(l) {
				limit = MaxLineCJK
			}
			if utf8.RuneCountInString(l) > limit {
				t.Fatalf("line %q over %d", l, limit)
			}
			got.WriteString(l)
		}
	}
	strip := func(s string) string { return strings.Join(strings.Fields(s), "") }
	if strip(got.String()) != strip(long+cjk) {
		t.Fatalf("text lost:\n%s\n%s", strip(got.String()), strip(long+cjk))
	}
}

// Slugs are filesystem-safe for any heading: non-empty, valid UTF-8, bounded, letters, marks,
// digits and single inner hyphens only; so no path separator, dot, control or reserved
// character can reach a file name.
// accept: OUT-05#1
func FuzzSlug(f *testing.F) {
	for _, s := range []string{"Chapter 1: The Beginning", "第一章 开始", "../../etc/passwd", "CON", "", "   ", "a/b\\c:d*e?f\"g<h>i|j",
		"\x00\x01\x7f", "Ünïcödé — ñ", "é", "́leading mark", "🎉🎉", strings.Repeat("ab ", 100), "नमस्ते दुनिया", "\xff\xfe"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, title string) {
		s := Slug(title)
		checkSlug(t, title, s)
		name := FileName(0, 3, title, ".m4a")
		if !strings.HasPrefix(name, "01-") || strings.ContainsAny(name, "/\\\x00") || len(name) > 255 {
			t.Fatalf("FileName(%q) = %q", title, name)
		}
	})
}

func checkSlug(t *testing.T, title, s string) {
	t.Helper()
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > maxSlug ||
		strings.HasPrefix(s, "-") || strings.HasSuffix(s, "-") || strings.Contains(s, "--") {
		t.Fatalf("Slug(%q) = %q", title, s)
	}
	for _, r := range s {
		if r != '-' && !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r) {
			t.Fatalf("Slug(%q) = %q holds %q", title, s, r)
		}
	}
}

func TestSlugExamples(t *testing.T) {
	for in, want := range map[string]string{
		"Chapter 1: The Beginning": "chapter-1-the-beginning",
		"第一章 开始":                   "第一章-开始",
		"../../etc/passwd":         "etc-passwd",
		"!!!":                      fallbackSlug,
		"été":                     "été",
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
	if got := FileName(9, 120, "Intro", ".wav"); got != "010-intro.wav" {
		t.Fatalf("FileName = %q", got)
	}
}

func TestFFMetadata(t *testing.T) {
	got := FFMetadata([]Chapter{
		{Title: "One", Start: 0, End: 1500400 * time.Microsecond},
		{Title: "a=b;c#d\\e\nf", Start: 1500400 * time.Microsecond, End: 3 * time.Second},
	})
	want := ";FFMETADATA1\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=0\nEND=1500\ntitle=One\n" +
		"[CHAPTER]\nTIMEBASE=1/1000\nSTART=1500\nEND=3000\ntitle=a\\=b\\;c\\#d\\\\e\\\nf\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestLeadIn(t *testing.T) {
	for in, want := range map[string]string{
		"Short opening. Then more.": "Short opening.",
		"An opening sentence that runs on well past the forty rune limit. Next.": "An opening sentence that runs on well…",
		"第一句话很长很长很长很长很长很长很长很长很长很长很长很长很长很长很长很长很长很长很长很长很长。":                        "第一句话很长很长很长很长很长很长很长很长很长很长很长很长很长很长很长很长很长很长…",
	} {
		if got := LeadIn(in); got != want {
			t.Errorf("LeadIn(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseFormat(t *testing.T) {
	for _, s := range []string{"srt", "VTT"} {
		if _, err := ParseFormat(s); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ParseFormat("ass"); err == nil {
		t.Fatal("ass accepted")
	}
}
