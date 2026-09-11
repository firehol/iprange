//go:build !windows

package handlers

import (
	"os"
	"syscall"
)

// captureFileIdentityPlatform returns (device, inode) from the stat
// result, like Rust std metadata dev()/ino().
func captureFileIdentityPlatform(path string) (dev, ino uint64, ok bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return uint64(stat.Dev), uint64(stat.Ino), true
}
