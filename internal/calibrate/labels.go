package calibrate

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Label is a listener's verdict: does the audio say the original passage exactly?
type Label string

// Labels.
const (
	LabelGood      Label = "good"
	LabelCorrupted Label = "corrupted"
)

// WriteLabelsTemplate writes the file the listener fills: for each held-out clip, a comment
// with the passage it should say, then its ID and a blank label. Nothing in it reveals how a
// clip was constructed.
func WriteLabelsTemplate(w io.Writer, heldOut []Sample, clipDir string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# recite calibration labels (VER-07)\n")
	fmt.Fprintf(&b, "# Play each clip <id>.wav in %s and compare it with the passage above its line.\n", clipDir)
	fmt.Fprintf(&b, "# After the id write \"good\" if the audio says the passage exactly, \"bad\" otherwise.\n")
	fmt.Fprintf(&b, "# Lines starting with # are ignored.\n")
	for _, s := range heldOut {
		fmt.Fprintf(&b, "\n# %s: %s\n%s\t\n", s.ID, s.Original, s.ID)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// ParseLabels reads a filled labels file. Every non-comment line is an ID and a label (good,
// g, bad, b, corrupted or c); a blank label, an unknown label or a repeated ID is an error.
func ParseLabels(r io.Reader) (map[string]Label, error) {
	out := map[string]Label{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			return nil, fmt.Errorf("calibrate: labels line %d: %s is not labelled", n, f[0])
		}
		if len(f) > 2 {
			return nil, fmt.Errorf("calibrate: labels line %d: want \"<id> <label>\", got %q", n, line)
		}
		var l Label
		switch strings.ToLower(f[1]) {
		case "good", "g":
			l = LabelGood
		case "bad", "b", "corrupted", "c":
			l = LabelCorrupted
		default:
			return nil, fmt.Errorf("calibrate: labels line %d: label %q: want good or bad", n, f[1])
		}
		if _, dup := out[f[0]]; dup {
			return nil, fmt.Errorf("calibrate: labels line %d: %s is labelled twice", n, f[0])
		}
		out[f[0]] = l
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("calibrate: read labels: %w", err)
	}
	return out, nil
}
