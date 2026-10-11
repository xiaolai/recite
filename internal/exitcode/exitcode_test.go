package exitcode

import "testing"

func TestWorstFollowsSeverityOrder(t *testing.T) {
	order := []int{Interrupted, Auth, RouteDenied, Internal, Failure, RateLimited, VerifyFailed, Usage, OK}
	for i, hi := range order {
		for _, lo := range order[i:] {
			if got := Worst(lo, hi); got != hi {
				t.Errorf("Worst(%d, %d) = %d, want %d", lo, hi, got, hi)
			}
			if got := Worst(hi, lo); got != hi {
				t.Errorf("Worst(%d, %d) = %d, want %d", hi, lo, got, hi)
			}
		}
	}
	if got := Worst(); got != OK {
		t.Errorf("Worst() = %d, want 0", got)
	}
	if got := Worst(Usage, 42); got != Internal {
		t.Errorf("Worst(2, 42) = %d, want %d", got, Internal)
	}
}
