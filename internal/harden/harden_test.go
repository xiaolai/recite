package harden

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

// childEnv re-executes this test binary as a child that hardens itself, so the
// test process keeps its own limits.
const childEnv = "RECITE_HARDEN_CHILD"

func TestMain(m *testing.M) {
	if os.Getenv(childEnv) == "1" {
		if err := Process(); err != nil {
			fmt.Println("error:", err)
			os.Exit(1)
		}
		var lim syscall.Rlimit
		if err := syscall.Getrlimit(syscall.RLIMIT_CORE, &lim); err != nil {
			fmt.Println("error:", err)
			os.Exit(1)
		}
		fmt.Printf("core=%d/%d dumpable=%d\n", lim.Cur, lim.Max, dumpable())
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runChild(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), childEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// accept: AUTH-05#6
func TestCoreLimitZeroAfterStartup(t *testing.T) {
	out := runChild(t)
	if !strings.HasPrefix(out, "core=0/0 ") {
		t.Fatalf("child reported %q", out)
	}
}
