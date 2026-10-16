package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// doctor and usage stop at the first interrupt: the Codex child is ended, nothing is printed,
// and the exit is 130 (audit: both ran on context.Background and ignored signals).
// accept: CLI-05#4
func TestInterruptDuringDoctorAndUsageExits130(t *testing.T) {
	for _, cmd := range []string{"doctor", "usage"} {
		t.Run(cmd, func(t *testing.T) {
			t.Setenv("FAKECODEX_MODE", "slow")
			t.Setenv("FAKECODEX_DELAY_MS", "6000")
			var out, errb bytes.Buffer
			c := New(&out, &errb)
			dir := t.TempDir()
			env := map[string]string{"RECITE_CACHE_DIR": filepath.Join(dir, "cache"), "XDG_STATE_HOME": filepath.Join(dir, "state")}
			c.Getenv = func(k string) string { return env[k] }
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
			// No check ran after the interrupt: the directory checks would have created these.
			for _, p := range []string{env["RECITE_CACHE_DIR"], env["XDG_STATE_HOME"]} {
				if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s exists: a check ran after the interrupt (%v)", filepath.Base(p), err)
				}
			}
		})
	}
}

// An interrupt that arrives during the encoder checks stops doctor there: the directory checks, which write probe
// files, never start (audit).
// accept: CLI-05#4
func TestInterruptDuringDoctorEncoderChecksStopsTheDirectoryChecks(t *testing.T) {
	var out, errb bytes.Buffer
	c := New(&out, &errb)
	dir := t.TempDir()
	env := map[string]string{"RECITE_CACHE_DIR": filepath.Join(dir, "cache"), "XDG_STATE_HOME": filepath.Join(dir, "state")}
	c.Getenv = func(k string) string { return env[k] }
	c.Now = func() time.Time { return cliNow }
	sigs := make(chan os.Signal, 1)
	c.Signals = func() (<-chan os.Signal, func()) { return sigs, func() {} }
	sent := false
	c.LookPath = func(name string) (string, error) {
		if !sent {
			sent = true
			sigs <- os.Interrupt
			time.Sleep(300 * time.Millisecond) // the watcher cancels the run
		}
		return "/usr/bin/" + name, nil
	}
	code := c.Run([]string{"--codex-bin", filepath.Join(dir, "missing-codex"), "--codex-home", t.TempDir(), "--auth-source", "file", "doctor"})
	if code != ExitInterrupted || out.Len() != 0 {
		t.Fatalf("code %d, stdout %d bytes, stderr %q", code, out.Len(), errb.String())
	}
	if !sent {
		t.Fatal("the encoder checks never ran")
	}
	for _, p := range []string{env["RECITE_CACHE_DIR"], env["XDG_STATE_HOME"]} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s exists: a check ran after the interrupt (%v)", filepath.Base(p), err)
		}
	}
}

// An interrupt that arrives while doctor resolves the settings for the directory checks stops it before the cache
// probe starts (audit).
// accept: CLI-05#4
func TestInterruptDuringDoctorSettingsStopsTheCacheProbe(t *testing.T) {
	var out, errb bytes.Buffer
	c := New(&out, &errb)
	dir := t.TempDir()
	env := map[string]string{"RECITE_CACHE_DIR": filepath.Join(dir, "cache"), "XDG_STATE_HOME": filepath.Join(dir, "state")}
	sigs := make(chan os.Signal, 1)
	c.Signals = func() (<-chan os.Signal, func()) { return sigs, func() {} }
	encoders, sent := 0, false
	c.LookPath = func(name string) (string, error) {
		encoders++
		return "/usr/bin/" + name, nil
	}
	c.Getenv = func(k string) string {
		// The encoder checks are the last LookPath calls; the settings resolve right after them.
		if encoders > 0 && !sent && k == "RECITE_CACHE_DIR" {
			sent = true
			sigs <- os.Interrupt
			time.Sleep(300 * time.Millisecond) // the watcher cancels the run
		}
		return env[k]
	}
	c.Now = func() time.Time { return cliNow }
	code := c.Run([]string{"--codex-bin", filepath.Join(dir, "missing-codex"), "--codex-home", t.TempDir(), "--auth-source", "file", "doctor"})
	if code != ExitInterrupted || out.Len() != 0 {
		t.Fatalf("code %d, stdout %d bytes, stderr %q", code, out.Len(), errb.String())
	}
	if !sent {
		t.Fatal("the settings were never resolved after the encoder checks")
	}
	for _, p := range []string{env["RECITE_CACHE_DIR"], env["XDG_STATE_HOME"]} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s exists: a probe ran after the interrupt (%v)", filepath.Base(p), err)
		}
	}
}
