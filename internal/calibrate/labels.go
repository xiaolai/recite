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
// clip was constructed. run names the calibration in the header; "" is VER-07's mini run.
func WriteLabelsTemplate(w io.Writer, heldOut []Sample, clipDir, run string) error {
	if run == "" {
		run = "VER-07"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# recite calibration labels (%s)\n", run)
	fmt.Fprintf(&b, "# Play each clip <id>.wav in %s and compare it with the passage above its line.\n", clipDir)
	fmt.Fprintf(&b, "# After the id write \"good\" if the audio says the passage exactly, \"bad\" otherwise.\n")
	fmt.Fprintf(&b, "# Lines starting with # are ignored.\n")
	for _, s := range heldOut {
		fmt.Fprintf(&b, "\n# %s: %s\n%s\t\n", s.ID, s.Original, s.ID)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// Labels is a parsed labels file.
type Labels struct {
	ByID map[string]Label
	// Labeller names the machine labeller that filled the file (design D22); "" means the
	// labels come from human listening.
	Labeller string
}

// labellerDirective declares a machine labeller; it is the one "#" line that is not a comment.
const labellerDirective = "#! labeller:"

// ParseLabels reads a filled labels file. Every non-comment line is an ID and a label (good,
// g, bad, b, corrupted or c); a blank label, an unknown label or a repeated ID is an error.
// One optional "#! labeller: <name>" line declares a machine labeller; a second one or an
// empty name is an error.
func ParseLabels(r io.Reader) (Labels, error) {
	out := map[string]Label{}
	labeller := ""
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if rest, ok := strings.CutPrefix(line, labellerDirective); ok {
			name := strings.TrimSpace(rest)
			if name == "" {
				return Labels{}, fmt.Errorf("calibrate: labels line %d: the labeller directive names no labeller", n)
			}
			if labeller != "" {
				return Labels{}, fmt.Errorf("calibrate: labels line %d: a second labeller directive", n)
			}
			labeller = name
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			return Labels{}, fmt.Errorf("calibrate: labels line %d: %s is not labelled", n, f[0])
		}
		if len(f) > 2 {
			return Labels{}, fmt.Errorf("calibrate: labels line %d: want \"<id> <label>\", got %q", n, line)
		}
		var l Label
		switch strings.ToLower(f[1]) {
		case "good", "g":
			l = LabelGood
		case "bad", "b", "corrupted", "c":
			l = LabelCorrupted
		default:
			return Labels{}, fmt.Errorf("calibrate: labels line %d: label %q: want good or bad", n, f[1])
		}
		if _, dup := out[f[0]]; dup {
			return Labels{}, fmt.Errorf("calibrate: labels line %d: %s is labelled twice", n, f[0])
		}
		out[f[0]] = l
	}
	if err := sc.Err(); err != nil {
		return Labels{}, fmt.Errorf("calibrate: read labels: %w", err)
	}
	return Labels{ByID: out, Labeller: labeller}, nil
}
