//go:build unix

package worker

import (
	"errors"
	"syscall"
)

// isDescriptorExhaustion reports whether an OS failure is the kernel refusing
// another descriptor (EMFILE for this process, ENFILE system-wide). The
// worker-spawn owners consult it so an unspawnable worker keeps the io class
// the reference gives it instead of folding into Conflict (design sections
// 7 and 9.4).
func isDescriptorExhaustion(err error) bool {
	return errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE)
}
