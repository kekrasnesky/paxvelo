//go:build linux || darwin

package main

import "syscall"

// diskFree returns the bytes free on the disk holding path, or -1 if it
// can't tell.
func diskFree(path string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize)
}
