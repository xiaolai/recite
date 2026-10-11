package calibrate

import "math"

// z95 is the two-sided 95% normal quantile.
const z95 = 1.959963984540054

// Wilson is the Wilson score 95% interval of k successes in n trials; [0, 1] when n is 0.
func Wilson(k, n int) (lo, hi float64) {
	if n <= 0 {
		return 0, 1
	}
	p, nf := float64(k)/float64(n), float64(n)
	z2 := z95 * z95
	centre := (p + z2/(2*nf)) / (1 + z2/nf)
	half := z95 * math.Sqrt(p*(1-p)/nf+z2/(4*nf*nf)) / (1 + z2/nf)
	return max(0, centre-half), min(1, centre+half)
}
