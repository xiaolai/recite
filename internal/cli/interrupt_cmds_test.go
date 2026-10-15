package cli

import (
	"bytes"
	"os"
	"testing"
	"time"
)

// doctor and usage stop at the first interrupt: the Codex child is ended, nothing is printed,
// and the exit is 130 (audit: both ran on context.Background and ignored signals).
func TestInterruptDuringDoctorAndUsageExits130(t *testing.T) {
	for _, cmd := range []string{"doctor", "usage"} {
		t.Run(cmd, func(t *testing.T) {
			t.Setenv("FAKECODEX_MODE", "slow")
			t.Setenv("FAKECODEX_DELAY_MS", "6000")
			var out, errb bytes.Buffer
			c := New(&out, &errb)
			c.Getenv = func(string) string { return "" }
			c.Now = func() time.Time { return cliNow }
			sigs := make(chan os.Signal, 1)
			c.Signals = func() (<-chan os.Signal, func()) { return sigs, func() {} }
			go func() {
				time.Sleep(300 * time.Millisecond) // the probe is waiting on the slow fake
				sigs <- os.Interrupt
			}()
			start := time.Now()
			code := c.Run([]string{"--codex-bin", fakeCodex, "--codex-home", t.TempDir(), "--auth-source", "codex", cmd})
			if code != ExitInterrupted || out.Len() != 0 {
				t.Fatalf("code %d, stdout %d bytes, stderr %q", code, out.Len(), errb.String())
			}
			if d := time.Since(start); d > 4*time.Second {
				t.Fatalf("returned after %v: the interrupt did not end the probe", d)
			}
		})
	}
}
