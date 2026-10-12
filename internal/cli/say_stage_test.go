package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/manifest"
)

// countingEncoder installs an ffmpeg that copies stdin to its output and counts its runs. On
// run failOn it exits 1; on run appearOn it first creates the file at $FAKE_APPEAR, as if
// another process had claimed that target during the run.
func countingEncoder(t *testing.T, failOn, appearOn int) func(string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "ffmpeg")
	t.Setenv("FAKE_COUNT", filepath.Join(dir, "count"))
	script := "#!/bin/sh\nn=$(cat \"$FAKE_COUNT\" 2>/dev/null || echo 0)\nn=$((n+1))\necho $n > \"$FAKE_COUNT\"\n" +
		"if [ $n = " + strconv.Itoa(failOn) + " ]; then echo encoder exploded >&2; exit 1; fi\n" +
		"if [ $n = " + strconv.Itoa(appearOn) + " ]; then echo theirs > \"$FAKE_APPEAR\"; fi\n" +
		"for last in \"$@\"; do :; done\nlast=${last#file:}\nexec cat > \"$last\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return func(string) (string, error) { return bin, nil }
}

// A chapter 108 hours into the timeline gets a positive, exact timestamp: the conversion
// divides before it multiplies, so samples*time.Second cannot overflow.
func TestSampleTimeLongTimeline(t *testing.T) {
	const hours = 108
	if got := sampleTime(hours * 3600 * audio.SampleRate); got != hours*time.Hour {
		t.Fatalf("sampleTime(%d h) = %v", hours, got)
	}
}

var splitMP3 = []string{"01-the-beginning.mp3", "02-part-2-quotes-slashes.mp3", "03-第三章-结局.mp3"}

// Every split file is staged before any is published: an encoder failure on the last section
// leaves no section file behind, and the failed manifest names none.
func TestSaySplitStagesBeforePublishing(t *testing.T) {
	m := newMultiEnv(t)
	writeBook(t, m)
	m.look = countingEncoder(t, 3, 0)
	r := m.run("-f", "book.md", "-o", "parts", "--split-sections", "--format", "mp3", "--loudness", "none")
	if r.code == exitcode.OK || !strings.Contains(r.stderr, "encoder exploded") {
		t.Fatalf("code %d, stderr %q", r.code, r.stderr)
	}
	if got := files(t, m.path("parts")); len(got) != 0 {
		t.Fatalf("files published before the failure: %q", got)
	}
	man := readManifest(t, m.path("parts.json"))
	if man.Status != manifest.StatusFailed {
		t.Fatalf("status %q", man.Status)
	}
	for i, ch := range man.Chapters {
		if ch.Path != nil {
			t.Fatalf("chapter %d records unpublished %s", i+1, *ch.Path)
		}
	}
}

// When publication fails part-way, the files already published are recorded in the failed
// manifest: here the third target appears during the run, so the first two are published and
// the third is refused.
func TestSaySplitRecordsPartialPublication(t *testing.T) {
	m := newMultiEnv(t)
	writeBook(t, m)
	if err := os.Mkdir(m.path("parts"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_APPEAR", m.path(filepath.Join("parts", splitMP3[2])))
	m.look = countingEncoder(t, 0, 3)
	r := m.run("-f", "book.md", "-o", "parts", "--split-sections", "--format", "mp3", "--loudness", "none")
	if r.code != exitcode.Usage || !strings.Contains(r.stderr, "created during the run") {
		t.Fatalf("code %d, stderr %q", r.code, r.stderr)
	}
	man := readManifest(t, m.path("parts.json"))
	if man.Status != manifest.StatusFailed || len(man.Chapters) != 3 {
		t.Fatalf("status %q, %d chapters", man.Status, len(man.Chapters))
	}
	for i, ch := range man.Chapters[:2] {
		want := filepath.Join("parts", splitMP3[i])
		if ch.Path == nil || *ch.Path != want {
			t.Fatalf("chapter %d path %v, want %s", i+1, ch.Path, want)
		}
		if _, err := os.Stat(m.path(want)); err != nil {
			t.Fatal(err)
		}
	}
	if ch := man.Chapters[2]; ch.Path != nil {
		t.Fatalf("chapter 3 records %s, which another process owns", *ch.Path)
	}
	if b, _ := os.ReadFile(m.path(filepath.Join("parts", splitMP3[2]))); string(b) != "theirs\n" {
		t.Fatalf("the third target was replaced: %q", b)
	}
}

var splitSRT = []string{"01-the-beginning.srt", "02-part-2-quotes-slashes.srt", "03-第三章-结局.srt"}

// A complete run records every subtitle file it published, each with its split section, and
// the manifest validates against the published schema.
func TestSayManifestRecordsSubtitles(t *testing.T) {
	m := newMultiEnv(t)
	writeBook(t, m)
	m.look = countingEncoder(t, 0, 0)
	r := m.run("-f", "book.md", "-o", "parts", "--split-sections", "--format", "mp3", "--loudness", "none", "--subtitles", "srt")
	if r.code != exitcode.OK {
		t.Fatalf("code %d, stderr %q", r.code, r.stderr)
	}
	man := validManifest(t, manifestSchema(t), m.path("parts.json"))
	if len(man.Subtitles) != len(splitSRT) {
		t.Fatalf("subtitles %+v, want %d", man.Subtitles, len(splitSRT))
	}
	for i, s := range man.Subtitles {
		want := filepath.Join("parts", splitSRT[i])
		if s.Path != want || s.Format != "srt" || s.Section == nil || *s.Section != wantTitles[i] {
			t.Fatalf("subtitle %d = %+v, want %s srt in %q", i+1, s, want, wantTitles[i])
		}
		if _, err := os.Stat(m.path(want)); err != nil {
			t.Fatal(err)
		}
	}
}

// A single-file run records its one subtitle without a section.
func TestSayManifestRecordsSingleSubtitle(t *testing.T) {
	m := newMultiEnv(t)
	writeBook(t, m)
	r := m.run("-f", "book.md", "-o", "book.wav", "--subtitles", "vtt")
	if r.code != exitcode.OK {
		t.Fatalf("code %d, stderr %q", r.code, r.stderr)
	}
	man := validManifest(t, manifestSchema(t), m.path("book.wav.json"))
	if len(man.Subtitles) != 1 || man.Subtitles[0].Path != "book.vtt" || man.Subtitles[0].Format != "vtt" || man.Subtitles[0].Section != nil {
		t.Fatalf("subtitles %+v, want book.vtt vtt without a section", man.Subtitles)
	}
}

// When publication fails after a subtitle was published, the failed manifest records exactly
// the subtitles published: the second section's subtitle target appears during the run, so
// the first section's audio and subtitle and the second's audio are published, then the run
// stops.
func TestSaySplitRecordsPublishedSubtitlesOnFailure(t *testing.T) {
	m := newMultiEnv(t)
	writeBook(t, m)
	if err := os.Mkdir(m.path("parts"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_APPEAR", m.path(filepath.Join("parts", splitSRT[1])))
	m.look = countingEncoder(t, 0, 3)
	r := m.run("-f", "book.md", "-o", "parts", "--split-sections", "--format", "mp3", "--loudness", "none", "--subtitles", "srt")
	if r.code != exitcode.Usage || !strings.Contains(r.stderr, "created during the run") {
		t.Fatalf("code %d, stderr %q", r.code, r.stderr)
	}
	man := validManifest(t, manifestSchema(t), m.path("parts.json"))
	if man.Status != manifest.StatusFailed {
		t.Fatalf("status %q", man.Status)
	}
	want := filepath.Join("parts", splitSRT[0])
	if len(man.Subtitles) != 1 || man.Subtitles[0].Path != want || man.Subtitles[0].Format != "srt" ||
		man.Subtitles[0].Section == nil || *man.Subtitles[0].Section != wantTitles[0] {
		t.Fatalf("subtitles %+v, want only %s", man.Subtitles, want)
	}
	if man.Chapters[1].Path == nil || man.Chapters[2].Path != nil {
		t.Fatalf("chapter paths %v %v, want the second published and the third not", man.Chapters[1].Path, man.Chapters[2].Path)
	}
}
