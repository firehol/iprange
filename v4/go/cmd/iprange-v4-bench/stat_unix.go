//go:build linux || darwin || freebsd

package main

import (
	"os"
	"syscall"
)

// statBlocks reports the physical block count of one file (st_blocks);
// unix platforms expose it through syscall.Stat_t.
func statBlocks(info os.FileInfo) int64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return stat.Blocks
	}
	return 0
}
