//go:build !linux && !darwin && !windows

// Live-install rename primitives on platforms without a proven
// implementation (Rust namespace_mutation.rs non-linux/apple arms).
// The live surface refuses before path access on these platforms
// (lock_refuse.go), so these arms stay typed refusals for defense in
// depth: the exchange primitive is Unsupported everywhere else (Rust
// exchange is linux/apple only), and the no-replace machine on FreeBSD
// (the linkat transition) is unreachable because requireLiveSupported
// refuses first.

package live

import (
	"golang.org/x/sys/unix"
)

// renameNoReplace reports the no-primitive class: the crash-safe linkat
// machine of the Rust FreeBSD arm is unreachable here because the live
// surface refuses before path access.
func (d *directory) renameNoReplace(source, destination string) error {
	return nsUnsupportedError()
}

// renameExchange reports the no-primitive class (Rust exchange is
// linux/apple only; every other platform refuses).
func (d *directory) renameExchange(source, destination string) error {
	return nsUnsupportedError()
}

// renamePlain renames source to destination replacing an existing
// destination (renameat(2); Rust
// replace_discarding_destination is available on linux/apple/freebsd).
func (d *directory) renamePlain(source, destination string) error {
	err := unix.Renameat(int(d.file.Fd()), source, int(d.file.Fd()), destination)
	return renameNamespaceResult(err, unix.ENOENT, "atomically replace and discard publication destination")
}
