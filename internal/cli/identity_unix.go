//go:build unix

package cli

import (
	"fmt"
	"os"
	"syscall"
)

// fileIdentity is the device and inode of fi, which every name of one file shares. recite
// builds for unix only (internal/fsx uses unix system calls), so there is no fallback: a
// FileInfo without a Stat_t is an error rather than a slower comparison.
func fileIdentity(fi os.FileInfo) (fileID, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fileID{}, fmt.Errorf("no device and inode for %s (%T)", fi.Name(), fi.Sys())
	}
	return fileID{dev: uint64(st.Dev), ino: uint64(st.Ino)}, nil // widths differ across unix platforms
}
