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

// Simultaneous auth, route-denied, verification and network failures report 3,
// in every arrival order.
// accept: CLI-03#3
func TestSimultaneousFailuresReportAuth(t *testing.T) {
	codes := []int{Auth, RouteDenied, VerifyFailed, Failure}
	var permute func(k int)
	n := 0
	permute = func(k int) {
		if k == len(codes) {
			n++
			if got := Worst(codes...); got != Auth {
				t.Errorf("Worst(%v) = %d, want %d", codes, got, Auth)
			}
			return
		}
		for i := k; i < len(codes); i++ {
			codes[k], codes[i] = codes[i], codes[k]
			permute(k + 1)
			codes[k], codes[i] = codes[i], codes[k]
		}
	}
	permute(0)
	if n != 24 {
		t.Fatalf("checked %d orders, want 24", n)
	}
}
