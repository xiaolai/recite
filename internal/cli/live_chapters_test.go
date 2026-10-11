//go:build live

package cli

import (
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"strconv"
	"testing"

	"github.com/xiaolai/recite/internal/exitcode"
)

// A Markdown chapter rendered over the fakes and encoded by the real ffmpeg into an m4b: ffprobe
// lists every heading as a chapter starting where the manifest's assembly timeline puts it,
// within 10 ms. Needs ffmpeg and ffprobe, not the network.
// accept: OUT-04#1
func TestLiveChaptersInM4B(t *testing.T) {
	if os.Getenv("RECITE_LIVE") == "" {
		t.Skip("set RECITE_LIVE=1 to run against the real tools")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	for _, rate := range []string{"24000", "44100"} {
		m := newMultiEnv(t)
		m.look = exec.LookPath
		writeBook(t, m)
		out := "book-" + rate + ".m4b"
		if r := m.run("-f", "book.md", "-o", out, "--sample-rate", rate); r.code != exitcode.OK {
			t.Fatalf("%+v", r)
		}
		man := readManifest(t, m.path(out+".json"))
		b, err := exec.Command(ffprobe, "-v", "error", "-show_chapters", "-of", "json", m.path(out)).Output()
		if err != nil {
			t.Fatalf("ffprobe: %v", err)
		}
		var probed struct {
			Chapters []struct {
				StartTime string            `json:"start_time"`
				Tags      map[string]string `json:"tags"`
			} `json:"chapters"`
		}
		if err := json.Unmarshal(b, &probed); err != nil {
			t.Fatal(err)
		}
		// The text before the first heading is a lead-in chapter at zero: an MP4 chapter
		// track starts there.
		if len(probed.Chapters) != len(wantTitles)+1 || len(man.Chapters) != len(wantTitles) ||
			probed.Chapters[0].Tags["title"] != "An opening line comes before any…" || probed.Chapters[0].StartTime != "0.000000" {
			t.Fatalf("%s: ffprobe chapters %s, manifest %+v", rate, b, man.Chapters)
		}
		for i, ch := range probed.Chapters[1:] {
			start, err := strconv.ParseFloat(ch.StartTime, 64)
			if err != nil {
				t.Fatal(err)
			}
			if ch.Tags["title"] != wantTitles[i] || math.Abs(start-*man.Chapters[i].StartS) > 0.010 {
				t.Fatalf("%s: chapter %d %q at %v s, want %q at %v s", rate, i+1, ch.Tags["title"], start, wantTitles[i], *man.Chapters[i].StartS)
			}
		}
	}
}
