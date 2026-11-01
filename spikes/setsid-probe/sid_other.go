//go:build unix && !darwin

package main

import "syscall"

// sid returns the session ID of this process, or -1 on error. The syscall
// package exports Getsid only on darwin, so use the raw syscall elsewhere.
func sid() int {
	s, _, errno := syscall.RawSyscall(syscall.SYS_GETSID, 0, 0, 0)
	if errno != 0 {
		return -1
	}
	return int(s)
}
