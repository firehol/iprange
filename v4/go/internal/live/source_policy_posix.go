//go:build !windows

package live

import (
	"os"
	"syscall"

	"github.com/firehol/iprange/v4/go/internal/security"
)

// mainSatisfiesProtectedContract reports whether the main file's own
// on-disk state already satisfies the protected contract: a regular
// single-linked file whose permission bits are exactly the creator
// mode (0600). Rust twin: live_sidecar's main_satisfies_protected_
// contract (the sol gate's round-2 P1 fallback).
func mainSatisfiesProtectedContract(main string) bool {
	info, err := os.Stat(main)
	if err != nil {
		return false
	}
	mode := info.Mode()
	if !mode.IsRegular() || uint32(mode.Perm()) != security.CreatorMode {
		return false
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return stat.Nlink == 1
	}
	return false
}
