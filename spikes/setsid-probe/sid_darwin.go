//go:build darwin

package main

import "syscall"

// sid returns the session ID of this process, or -1 on error.
func sid() int {
	s, err := syscall.Getsid(0)
	if err != nil {
		return -1
	}
	return s
}
