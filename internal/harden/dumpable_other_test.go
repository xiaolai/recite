//go:build !linux

package harden

// dumpable has no equivalent off Linux; RLIMIT_CORE alone governs core dumps.
func dumpable() int { return 0 }
