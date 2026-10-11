//go:build live

package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// WP-3.7 live check: `recite doctor --deep` passes against the real service: every check is
// ok or warn, and the deep render is transcribed back.
func TestLiveDoctorDeep(t *testing.T) {
	if os.Getenv("RECITE_LIVE") == "" {
		t.Skip("set RECITE_LIVE=1 to run against the real service")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "recite")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/recite").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, "doctor", "--deep")
	cmd.Env = realEnv // TestMain isolated this process from the real sign-in
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("doctor --deep: %v\n%s%s", err, stdout.String(), stderr.String())
	}
	t.Logf("doctor --deep:\n%s", stdout.String())
	for _, check := range []string{"codex", "auth", "plan", "route", "deep"} {
		if l := line(t, stdout.String(), check); !strings.HasPrefix(l, "ok") {
			t.Fatalf("%s: %q\n%s", check, l, stdout.String())
		}
	}
}
