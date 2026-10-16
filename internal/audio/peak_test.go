package audio

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// peakFFmpeg is an ffmpeg whose analysis of the assembly (pipe:0) measures -22 LUFS and
// -6.5 dBTP, whose analysis of an encoded file reports the next true peak from peaks, and
// whose encodes record their -af filter, one per line, in the returned log.
func peakFFmpeg(t *testing.T, peaks ...string) (bin, filters string) {
	t.Helper()
	dir := t.TempDir()
	bin, filters = filepath.Join(dir, "ffmpeg"), filepath.Join(dir, "filters")
	if err := os.WriteFile(filepath.Join(dir, "peaks"), []byte(strings.Join(peaks, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ndir=$(dirname \"$0\")\nfor a in \"$@\"; do if [ \"$a\" = null ]; then\n" +
		"tp=-6.50\nif [ \"$5\" != -f ]; then n=$(cat \"$dir/n\" 2>/dev/null || echo 0); n=$((n+1)); echo $n > \"$dir/n\"\n" +
		"tp=$(sed -n \"${n}p\" \"$dir/peaks\"); else cat > /dev/null; fi\n" +
		"printf '{\"input_i\" : \"-22.00\", \"input_tp\" : \"%s\", \"input_lra\" : \"3.20\", \"input_thresh\" : \"-32.10\", \"target_offset\" : \"0.40\"}\\n' \"$tp\" >&2\nexit 0\nfi; done\n" +
		"for a in \"$@\"; do if [ \"$prev\" = -af ]; then printf '%s\\n' \"$a\" >> \"$dir/filters\"; fi; prev=$a; done\n" +
		"for last in \"$@\"; do :; done\nlast=${last#file:}\nexec cat > \"$last\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, filters
}

// The encoded file's true peak is checked against the ceiling: an overshoot re-encodes with
// loudnorm's pass UNCHANGED, followed by a gain cut of the overshoot plus peakStep, summed over
// passes, and a file still over after maxPeakPasses is an error. Lowering loudnorm's own TP=
// instead does not converge on a take loudnorm cannot normalise linearly.
func TestEncodeKeepsTruePeakUnderCeiling(t *testing.T) {
	for _, tc := range []struct {
		name    string
		peaks   []string
		wantCut []string // the gain cut after loudnorm per encode, "" for none
		wantErr bool
	}{
		{"at the ceiling", []string{"-3.00"}, []string{""}, false},
		{"one overshoot", []string{"-2.94", "-3.20"}, []string{"", "0.16"}, false},
		// The codec's overshoot is not constant: a deeper cut landed higher twice before it
		// landed under (a real take's sequence, shifted to this test's ceiling of -3.00).
		{"higher before under", []string{"-2.95", "-2.89", "-2.78", "-3.66"}, []string{"", "0.15", "0.36", "0.68"}, false},
		{"never under", []string{"-2.50", "-2.50", "-2.50", "-2.50", "-2.50", "-2.50"}, []string{"", "0.60", "1.20", "1.80", "2.40", "3.00"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, filters := peakFFmpeg(t, tc.peaks...)
			e := Encoding{Format: FormatM4A, Rate: SampleRate, Tool: ToolFFmpeg, Bin: bin, Loudness: LoudnessAudiobook}
			out := filepath.Join(t.TempDir(), "o.m4a")
			err := EncodeFile(t.Context(), e, make(PCM, 4800), out)
			if tc.wantErr != (err != nil) {
				t.Fatalf("err %v, want error %v", err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "-2.50 dBTP after "+strconv.Itoa(maxPeakPasses)+" passes, above the audiobook ceiling") {
				t.Fatalf("error %q", err)
			}
			b, rerr := os.ReadFile(filters)
			if rerr != nil {
				t.Fatal(rerr)
			}
			lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
			if len(lines) != len(tc.wantCut) {
				t.Fatalf("%d encodes, want %d:\n%s", len(lines), len(tc.wantCut), b)
			}
			for i, f := range lines {
				// Every pass is loudnorm toward the TARGET ceiling from the assembly's measurement.
				if !strings.Contains(f, ":TP=-3.00:") || !strings.Contains(f, "measured_TP=-6.50") {
					t.Fatalf("encode %d filter %s, want loudnorm at TP=-3.00 from the assembly's measurement", i+1, f)
				}
				cut := ""
				if j := strings.Index(f, ",volume=-"); j >= 0 {
					cut = strings.TrimSuffix(f[j+len(",volume=-"):], "dB")
				}
				if cut != tc.wantCut[i] {
					t.Fatalf("encode %d filter %s, want a gain cut of %q dB", i+1, f, tc.wantCut[i])
				}
			}
		})
	}
}
