package harden

import (
	"fmt"
	"syscall"
)

const prSetDumpable = 4 // PR_SET_DUMPABLE from <linux/prctl.h>

func platform() error {
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetDumpable, 0, 0); errno != 0 {
		return fmt.Errorf("harden: prctl(PR_SET_DUMPABLE, 0): %w", errno)
	}
	return nil
}
