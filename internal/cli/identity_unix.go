//go:build unix

package cli

import (
	"os"
	"syscall"
)

// fileIdentity is the device and inode of fi, which every name of one file shares.
func fileIdentity(fi os.FileInfo) (fileID, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fileID{}, false
	}
	return fileID{dev: uint64(st.Dev), ino: uint64(st.Ino)}, true // widths differ across unix platforms
}
