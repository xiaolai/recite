//go:build live

package audio

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// The external tools read the file recite wrote with the right duration. Each
// tool is checked only where it is installed.
// accept: AUD-01#2
func TestLiveWAVOpensInSystemTools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "three.wav")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteWAV(f, samples(SampleRate*3)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	ran := 0
	if bin, err := exec.LookPath("afinfo"); err == nil {
		ran++
		out, err := exec.Command(bin, path).CombinedOutput()
		if err != nil {
			t.Fatalf("afinfo: %v\n%s", err, out)
		}
		m := regexp.MustCompile(`estimated duration: ([0-9.]+) sec`).FindSubmatch(out)
		if m == nil || !near(t, string(m[1]), 3) {
			t.Fatalf("afinfo duration: %s", out)
		}
	}
	if bin, err := exec.LookPath("ffprobe"); err == nil {
		ran++
		out, err := exec.Command(bin, "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path).Output()
		if err != nil {
			t.Fatalf("ffprobe: %v", err)
		}
		if !near(t, string(regexp.MustCompile(`[0-9.]+`).Find(out)), 3) {
			t.Fatalf("ffprobe duration: %s", out)
		}
	}
	if ran == 0 {
		t.Skip("neither afinfo nor ffprobe is installed")
	}
}

func near(t *testing.T, s string, want float64) bool {
	t.Helper()
	v, err := strconv.ParseFloat(s, 64)
	return err == nil && v > want-0.01 && v < want+0.01
}
