package harden

import (
	"strings"
	"syscall"
	"testing"
)

const prGetDumpable = 3 // PR_GET_DUMPABLE from <linux/prctl.h>

func dumpable() int {
	r, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prGetDumpable, 0, 0)
	if errno != 0 {
		return -1
	}
	return int(r)
}

// accept: AUTH-05#6
func TestNotDumpableAfterStartup(t *testing.T) {
	if out := runChild(t); !strings.HasSuffix(out, " dumpable=0") {
		t.Fatalf("child reported %q", out)
	}
}
