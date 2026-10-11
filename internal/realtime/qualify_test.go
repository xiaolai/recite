//go:build qualify

package realtime_test

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/realtime"
)

const (
	qualifyTakes       = 3
	qualifyAttempts    = 4 // the default limit of 3, plus one for request-like chunks (design §8.5)
	qualifyFirstMin    = 83
	qualifyWithinLimit = 86
)

func wilson(k, n int) (float64, float64) {
	const z = 1.96
	p, nf := float64(k)/float64(n), float64(n)
	d := 1 + z*z/nf
	c := (p + z*z/(2*nf)) / d
	h := z * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf)) / d
	return c - h, c + h
}

// The 29-item request-like corpus × 3 takes on gpt-realtime-2.1 (E22, E23).
//
// accept: SPK-05#3
func TestQualifyInstructionsV2(t *testing.T) {
	raw, err := os.ReadFile("testdata/qualify-instr-v2.txt")
	if err != nil {
		t.Fatal(err)
	}
	corpus := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(corpus) != 29 {
		t.Fatalf("corpus has %d texts, want 29", len(corpus))
	}
	ss := liveOpen(t, liveSynth(t), realtime.DefaultSettings())
	first, within, n := 0, 0, 0
	for i, text := range corpus {
		for take := 0; take < qualifyTakes; take++ {
			n++
			for attempt := 1; attempt <= qualifyAttempts; attempt++ {
				tk, err := ss.Render(context.Background(), realtime.RenderRequest{
					ChunkID: fmt.Sprintf("c%04d", i*qualifyTakes+take+1), Text: text, Attempt: attempt,
				})
				if err != nil {
					t.Fatalf("%q take %d attempt %d: %v", text, take, attempt, err)
				}
				if fold(tk.Said) == fold(text) {
					if attempt == 1 {
						first++
					}
					within++
					break
				}
				t.Logf("MISS %q take %d attempt %d -> %q", text, take, attempt, tk.Said)
			}
		}
	}
	lo, hi := wilson(first, n)
	t.Logf("first attempt %d/%d (Wilson 95%% %.3f–%.3f), within %d attempts %d/%d", first, n, lo, hi, qualifyAttempts, within, n)
	if first < qualifyFirstMin || within < qualifyWithinLimit {
		t.Fatalf("first %d (want ≥ %d), within limit %d (want ≥ %d)", first, qualifyFirstMin, within, qualifyWithinLimit)
	}
}
