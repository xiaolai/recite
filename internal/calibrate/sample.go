package calibrate

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"slices"
	"strings"
)

// Sample is one calibration candidate. Its ID is anonymised: it says nothing about Kind, so a
// listener labelling by ID cannot see how the candidate was constructed.
type Sample struct {
	ID       string `json:"id"`
	Passage  int    `json:"passage"` // 0-based index in the corpus
	Kind     Kind   `json:"kind"`
	Original string `json:"original"` // the passage: the reference the take is verified against
	Rendered string `json:"rendered"` // what the synthesizer is asked to say
}

// Corrupted reports whether the sample was constructed as corrupted.
func (s Sample) Corrupted() bool { return s.Kind != Good }

// ReadPassages reads a corpus: one passage per line; blank lines and lines starting with '#'
// are skipped.
func ReadPassages(r io.Reader) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("calibrate: read passages: %w", err)
	}
	return out, nil
}

// Build makes good candidates (the first good passages, unedited) and corrupted candidates
// (cycling through Corruptions, each on a passage chosen with the seed that the corruption
// applies to, no passage edited twice the same way). IDs are assigned in a seeded shuffle.
// The same inputs always give the same samples.
func Build(passages []string, good, corrupted int, seed uint64) ([]Sample, error) {
	if good < 0 || corrupted < 0 || good > len(passages) {
		return nil, fmt.Errorf("calibrate: %d good candidates from %d passages", good, len(passages))
	}
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	var out []Sample
	for i := range good {
		out = append(out, Sample{Passage: i, Kind: Good, Original: passages[i], Rendered: passages[i]})
	}
	used := map[[2]any]bool{}
	for j := range corrupted {
		k := Corruptions[j%len(Corruptions)]
		s, err := corruptOne(passages, k, rng, used)
		if err != nil {
			return nil, fmt.Errorf("calibrate: candidate %d: %w", j+1, err)
		}
		out = append(out, s)
	}
	rng.Shuffle(len(out), func(a, b int) { out[a], out[b] = out[b], out[a] })
	width := len(fmt.Sprint(len(out)))
	for i := range out {
		out[i].ID = fmt.Sprintf("s%0*d", width, i+1)
	}
	return out, nil
}

func corruptOne(passages []string, k Kind, rng *rand.Rand, used map[[2]any]bool) (Sample, error) {
	for _, p := range rng.Perm(len(passages)) {
		if used[[2]any{p, k}] {
			continue
		}
		edited, err := Corrupt(passages[p], k, rng)
		if errors.Is(err, ErrNotApplicable) {
			continue
		}
		if err != nil {
			return Sample{}, err
		}
		used[[2]any{p, k}] = true
		return Sample{Passage: p, Kind: k, Original: passages[p], Rendered: edited}, nil
	}
	return Sample{}, fmt.Errorf("%s: no passage left that it applies to", k)
}

// HeldOutFraction is the evaluation share of the 60/40 split.
const HeldOutFraction = 0.4

// Split divides samples 60/40, stratified by construction kind (so by label and corruption
// type): in each stratum, round(0.4 × n), halves up, samples chosen with the seed are held
// out. Thresholds may be proposed on the first part only. Both parts are in ID order.
func Split(samples []Sample, seed uint64) (propose, heldOut []Sample) {
	strata := map[Kind][]Sample{}
	for _, s := range samples {
		strata[s.Kind] = append(strata[s.Kind], s)
	}
	kinds := make([]Kind, 0, len(strata))
	for k := range strata {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	rng := rand.New(rand.NewPCG(seed, ^seed))
	for _, k := range kinds {
		st := strata[k]
		slices.SortFunc(st, byID)
		rng.Shuffle(len(st), func(a, b int) { st[a], st[b] = st[b], st[a] })
		n := int(float64(len(st))*HeldOutFraction + 0.5)
		heldOut = append(heldOut, st[:n]...)
		propose = append(propose, st[n:]...)
	}
	slices.SortFunc(propose, byID)
	slices.SortFunc(heldOut, byID)
	return propose, heldOut
}

func byID(a, b Sample) int { return strings.Compare(a.ID, b.ID) }
