//go:build unix

package calleropen

import "golang.org/x/sys/unix"

// maxScanDescriptors bounds the descriptor-number window the table read
// examines. Descriptors are allocated at the lowest free number, so the
// count of free numbers inside this bound is a sound lower bound on the
// descriptors the process can still claim, whatever the soft limit is
// above it. A process whose entire low table is dense is not this CLI (the
// reference measures its working set in single digits), and answering it
// conservatively is the honest direction.
const maxScanDescriptors = 4096

// freeDescriptorsByScan counts free descriptor numbers from 0 to
// min(soft, maxScanDescriptors) with fcntl(F_GETFD). It is the portable
// arm of the table read: it reads the kernel's per-descriptor state
// directly, so it can neither block on a planted node nor register
// anything with the runtime poller.
func freeDescriptorsByScan(soft uint64) (free int) {
	window := soft
	if window > maxScanDescriptors {
		window = maxScanDescriptors
	}
	inUse := 0
	for fd := 0; fd < int(window); fd++ {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err == nil {
			inUse++
		}
	}
	return int(window) - inUse
}
