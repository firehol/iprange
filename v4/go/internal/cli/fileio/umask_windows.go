//go:build windows

package fileio

func setUmask(int) int { return 0 }
