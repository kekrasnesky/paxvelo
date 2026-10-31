//go:build !linux && !darwin

package main

// diskFree can't tell on this system (the site runs on Linux), so uploads
// aren't stopped for a full disk here.
func diskFree(path string) int64 { return -1 }
