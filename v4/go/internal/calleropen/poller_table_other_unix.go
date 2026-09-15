//go:build unix && !linux

package calleropen

import "golang.org/x/sys/unix"

// FreeDescriptors is the non-Linux unix arm of the table read: the portable
// fcntl(F_GETFD) scan. It reads the kernel's per-descriptor state directly,
// opens no filesystem path (see poller_table_note.go), and bounds the work
// at maxScanDescriptors so a table read cannot fan out with the soft limit.
// The kqueue platforms have no /proc/self/fd, and their poller demand differs
// from Linux's (design section 6 platform clause).
func FreeDescriptors() (free int, ok bool) {
	var rlimit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &rlimit); err != nil {
		return 0, false
	}
	return freeDescriptorsByScan(uint64(rlimit.Cur)), true
}
