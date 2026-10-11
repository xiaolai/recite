package cli

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/manifest"
)

// book is a Markdown chapter: text before the first heading, then three sections, one with a
// heading no file system would take as a name.
const book = `An opening line comes before any heading. It has two sentences.

# The Beginning

The first section has one paragraph. It is short.

## Part 2: "Quotes" / Slashes?

The second section starts here.

Its second paragraph follows.

# 第三章 结局

第三部分在这里结束。
`

func writeBook(t *testing.T, m *multiEnv) {
	t.Helper()
	if err := os.WriteFile(m.path("book.md"), []byte(book), 0o600); err != nil {
		t.Fatal(err)
	}
}

var wantTitles = []string{"The Beginning", `Part 2: "Quotes" / Slashes?`, "第三章 结局"}

// srtCues reads an SRT file's timings strictly: sequential indexes, a timing line, text.
func srtCues(t *testing.T, path string) [][2]time.Duration {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`^(\d\d):(\d\d):(\d\d)[,.](\d\d\d) --> (\d\d):(\d\d):(\d\d)[,.](\d\d\d)$`)
	body := string(b)
	vtt := strings.HasPrefix(body, "WEBVTT\n\n")
	body = strings.TrimPrefix(body, "WEBVTT\n\n")
	var out [][2]time.Duration
	for i, block := range strings.Split(strings.TrimSuffix(body, "\n\n"), "\n\n") {
		lines := strings.Split(block, "\n")
		if !vtt {
			if lines[0] != strconv.Itoa(i+1) {
				t.Fatalf("%s cue %d index %q", path, i+1, lines[0])
			}
			lines = lines[1:]
		}
		m := re.FindStringSubmatch(lines[0])
		if m == nil || len(lines) < 2 {
			t.Fatalf("%s cue %d: %q", path, i+1, block)
		}
		n := func(k int) time.Duration { v, _ := strconv.Atoi(m[k]); return time.Duration(v) }
		out = append(out, [2]time.Duration{
			n(1)*time.Hour + n(2)*time.Minute + n(3)*time.Second + n(4)*time.Millisecond,
			n(5)*time.Hour + n(6)*time.Minute + n(7)*time.Second + n(8)*time.Millisecond,
		})
	}
	return out
}

func secs(p *float64) time.Duration { return time.Duration(math.Round(*p * float64(time.Second))) }

// The subtitles written next to the output are timed from the manifest's assembly timeline:
// every chunk's first cue starts and its last cue ends on the chunk's edges within 10 ms, the
// cues go forward, and none passes the end of the audio. Both formats parse.
// accept: OUT-03#1
// accept: OUT-03#2
// accept: OUT-03#3
func TestSaySubtitlesFollowTimeline(t *testing.T) {
	sch := manifestSchema(t)
	for _, f := range []string{"srt", "vtt"} {
		m := newMultiEnv(t)
		writeBook(t, m)
		if r := m.run("-f", "book.md", "-o", "book.wav", "--subtitles", f); r.code != exitcode.OK {
			t.Fatalf("%s: %+v", f, r)
		}
		man := validManifest(t, sch, m.path("book.wav.json"))
		cues := srtCues(t, m.path("book."+f))
		duration := time.Duration(math.Round(man.Output.DurationS * float64(time.Second)))
		var prev time.Duration
		for i, c := range cues {
			if c[0] < prev || c[1] < c[0] || c[1] > duration+time.Millisecond {
				t.Fatalf("%s cue %d %v after %v, audio %v", f, i+1, c, prev, duration)
			}
			prev = c[1]
		}
		// Every chunk edge is a cue edge.
		for _, ch := range man.Chunks {
			st, en := secs(ch.StartS), secs(ch.EndS)
			hasStart := slices.ContainsFunc(cues, func(c [2]time.Duration) bool { return (c[0] - st).Abs() <= 10*time.Millisecond })
			hasEnd := slices.ContainsFunc(cues, func(c [2]time.Duration) bool { return (c[1] - en).Abs() <= 10*time.Millisecond })
			if !hasStart || !hasEnd {
				t.Fatalf("%s: chunk %s [%v, %v] has no cue edge within 10 ms: %v", f, ch.ID, st, en, cues)
			}
		}
		if len(cues) < len(man.Chunks) {
			t.Fatalf("%s: %d cues for %d chunks", f, len(cues), len(man.Chunks))
		}
	}
}

// The manifest lists the headings as chapters on the assembly timeline for every format, and
// labels each chunk with its section; the text before the first heading has none.
func TestManifestChapters(t *testing.T) {
	m := newMultiEnv(t)
	writeBook(t, m)
	if r := m.run("-f", "book.md", "-o", "book.wav"); r.code != exitcode.OK {
		t.Fatalf("%+v", r)
	}
	man := validManifest(t, manifestSchema(t), m.path("book.wav.json"))
	if len(man.Chapters) != len(wantTitles) {
		t.Fatalf("chapters %+v", man.Chapters)
	}
	byID := map[string]manifest.Chunk{}
	for _, c := range man.Chunks {
		byID[c.ID] = c
	}
	for i, ch := range man.Chapters {
		first := byID[ch.FirstChunk]
		if ch.Title != wantTitles[i] || ch.Path != nil || *ch.StartS != *first.StartS || first.Section == nil || *first.Section != ch.Title {
			t.Fatalf("chapter %d %+v, first chunk %+v", i+1, ch, first)
		}
		if i > 0 && *man.Chapters[i-1].EndS != *ch.StartS {
			t.Fatalf("chapter %d does not start where %d ends", i+1, i)
		}
	}
	if last := man.Chapters[len(man.Chapters)-1]; math.Abs(*last.EndS-man.Output.DurationS) > 1e-6 {
		t.Fatalf("last chapter ends at %v, audio %v", *last.EndS, man.Output.DurationS)
	}
	if man.Chunks[0].Section != nil {
		t.Fatalf("the opening text has section %q", *man.Chunks[0].Section)
	}
}

// An m4a gets the chapters through ffmpeg's ffmetadata input, at the manifest's start times,
// in the same pass that applies the default podcast loudness.
func TestSayM4AChapterMarkers(t *testing.T) {
	m := newMultiEnv(t)
	writeBook(t, m)
	dir := t.TempDir()
	bin := filepath.Join(dir, "ffmpeg")
	// The loudness analysis pass (output format null) prints loudnorm's JSON and writes nothing.
	script := "#!/bin/sh\nfor a in \"$@\"; do if [ \"$a\" = null ]; then cat > /dev/null\n" +
		"printf '{\\n\"input_i\" : \"-22.00\",\\n\"input_tp\" : \"-6.50\",\\n\"input_lra\" : \"3.20\",\\n" +
		"\"input_thresh\" : \"-32.10\",\\n\"target_offset\" : \"0.40\"\\n}\\n' >&2\nexit 0\nfi; done\n" +
		"seen=0\nfor a in \"$@\"; do if [ \"$prev\" = -i ]; then seen=$((seen+1)); [ $seen = 2 ] && meta=${a#file:}; fi\n" +
		"if [ \"$prev\" = -af ]; then printf '%s' \"$a\" > \"$FAKE_META.af\"; fi; prev=$a; done\n" +
		"for last in \"$@\"; do :; done\nlast=${last#file:}\nif [ -n \"$meta\" ]; then cat \"$meta\" > \"$FAKE_META\"; fi\nexec cat > \"$last\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	metaOut := filepath.Join(dir, "meta.txt")
	t.Setenv("FAKE_META", metaOut)
	m.look = func(name string) (string, error) { return bin, nil }
	if r := m.run("-f", "book.md", "-o", "book.m4a"); r.code != exitcode.OK {
		t.Fatalf("%+v", r)
	}
	man := readManifest(t, m.path("book.m4a.json"))
	if af, err := os.ReadFile(metaOut + ".af"); err != nil || !strings.Contains(string(af), "loudnorm=") {
		t.Fatalf("the chaptered pass did not normalize loudness: %q %v", af, err)
	}
	meta, err := os.ReadFile(metaOut)
	if err != nil {
		t.Fatalf("no chapters reached ffmpeg: %v", err)
	}
	starts := regexp.MustCompile(`START=(\d+)`).FindAllStringSubmatch(string(meta), -1)
	starts = starts[1:] // the untitled lead-in before the first heading
	if len(starts) != len(man.Chapters) || !strings.Contains(string(meta), `title=Part 2: "Quotes" / Slashes?`) {
		t.Fatalf("metadata:\n%s", meta)
	}
	for i, s := range starts {
		ms, _ := strconv.Atoi(s[1])
		if math.Abs(float64(ms)/1000-*man.Chapters[i].StartS) > 0.001 {
			t.Fatalf("chapter %d START=%d, manifest %v", i+1, ms, *man.Chapters[i].StartS)
		}
	}
	if err := os.Remove(metaOut); err != nil {
		t.Fatal(err)
	}
	if r := m.run("-f", "book.md", "-o", "plain.m4a", "--chapters=false"); r.code != exitcode.OK {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(metaOut); !os.IsNotExist(err) {
		t.Fatalf("--chapters=false still wrote chapters: %v", err)
	}
}

// Settings that cannot work are exit 2 before any request.
func TestSayExtrasUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"-f", "book.md", "-o", "x.wav", "--chapters"},
		{"-f", "book.md", "-o", "-", "--subtitles", "srt"},
		{"-f", "book.md", "-o", "-", "--split-sections"},
		{"-f", "book.md", "-o", "x.wav", "--subtitles", "ass"},
		{"-f", "book.md", "-o", "out", "--split-sections", "--chapters"},
		{"Plain text has no headings.", "-o", "out", "--split-sections"},
	} {
		m := newMultiEnv(t)
		writeBook(t, m)
		if r := m.run(args...); r.code != exitcode.Usage {
			t.Fatalf("%q: %+v", args, r)
		}
		m.untouched()
	}
	m := newMultiEnv(t)
	writeBook(t, m)
	if err := os.WriteFile(m.path("book.srt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if r := m.run("-f", "book.md", "-o", "book.wav", "--subtitles", "srt"); r.code != exitcode.Usage || !strings.Contains(r.stderr, "book.srt already exists") {
		t.Fatalf("existing subtitles: %+v", r)
	}
	m.untouched()
}

// --split-sections writes one file per heading, NN-<slug>, plus one manifest for the run; the
// first file also holds the text before the first heading. A re-run with --force gives the
// same files, byte for byte, and nothing else; without --force it stops before the network.
// accept: OUT-05#1
func TestSaySplitSections(t *testing.T) {
	m := newMultiEnv(t)
	writeBook(t, m)
	run := func(extra ...string) map[string][]byte {
		t.Helper()
		if r := m.run(append([]string{"-f", "book.md", "-o", "parts", "--split-sections", "--subtitles", "vtt"}, extra...)...); r.code != exitcode.OK {
			t.Fatalf("%+v", r)
		}
		ents, err := os.ReadDir(m.path("parts"))
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][]byte{}
		for _, e := range ents {
			b, err := os.ReadFile(filepath.Join(m.path("parts"), e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			out[e.Name()] = b
		}
		return out
	}
	first := run()
	var audioFiles []string
	for name := range first {
		if strings.HasSuffix(name, ".wav") {
			audioFiles = append(audioFiles, name)
		}
	}
	slices.Sort(audioFiles)
	want := []string{"01-the-beginning.wav", "02-part-2-quotes-slashes.wav", "03-第三章-结局.wav"}
	if !slices.Equal(audioFiles, want) || len(first) != 2*len(want) {
		t.Fatalf("files %v", first)
	}
	man := validManifest(t, manifestSchema(t), m.path("parts.json"))
	if len(man.Chapters) != len(want) || man.Output == nil || man.Output.Path != "parts" {
		t.Fatalf("manifest chapters %+v output %+v", man.Chapters, man.Output)
	}
	var total float64
	for i, ch := range man.Chapters {
		if ch.Path == nil || *ch.Path != filepath.Join("parts", want[i]) {
			t.Fatalf("chapter %d path %v", i+1, ch.Path)
		}
		total += readWAV(t, m.path(*ch.Path)).Duration().Seconds()
	}
	// The files hold all the audio but the gaps between sections.
	if total <= 0 || total > man.Output.DurationS {
		t.Fatalf("split audio %v s of %v s", total, man.Output.DurationS)
	}

	if r := m.run("-f", "book.md", "-o", "parts", "--split-sections"); r.code != exitcode.Usage || !strings.Contains(r.stderr, "already exists") {
		t.Fatalf("re-run without --force: %+v", r)
	}
	second := run("--force")
	if len(second) != len(first) {
		t.Fatalf("re-run files %d, first %d", len(second), len(first))
	}
	for name, b := range first {
		if !bytes.Equal(second[name], b) {
			t.Fatalf("%s differs after a re-run", name)
		}
	}
}

// --play with --split-sections warns that there is no single file and starts no player.
func TestSaySplitSectionsPlayWarns(t *testing.T) {
	m := newMultiEnv(t)
	writeBook(t, m)
	look, log := fakePlayers(t, 0)
	m.look = look
	r := m.run("-f", "book.md", "-o", "parts", "--split-sections", "--play")
	if r.code != exitcode.OK || !strings.Contains(r.stderr, "no single file to play") {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("a player ran on the section directory")
	}
}
