//go:build !linux && !darwin && !freebsd && !windows

// Live-install rename primitives on platforms without a proven
// implementation (Rust namespace_mutation.rs non-linux/apple/freebsd
// arms). The live surface refuses before path access on these
// platforms (lock_refuse.go), so these arms stay typed refusals for
// defense in depth: the exchange primitive is Unsupported everywhere
// except linux/apple (Rust exchange is linux/apple only), and the
// no-replace machine on FreeBSD (the linkat transition) has its own
// arm (namespace_install_freebsd.go); every other platform refuses.

package live

import (
	"os"

	"golang.org/x/sys/unix"
)

// RenameNoReplace reports the no-primitive class: the crash-safe linkat
// machine of the Rust FreeBSD arm is unreachable here because the live
// surface refuses before path access.
func (d *Directory) RenameNoReplace(source string, _ *os.File, destination string) error {
	return nsUnsupportedError()
}

// RenameExchange reports the no-primitive class (Rust exchange is
// linux/apple only; every other platform refuses).
func (d *Directory) RenameExchange(source, destination string) error {
	return nsUnsupportedError()
}

// RenamePlain renames source to destination replacing an existing
// destination (renameat(2); Rust
// replace_discarding_destination is available on linux/apple/freebsd).
func (d *Directory) RenamePlain(source, destination string) error {
	err := unix.Renameat(int(d.file.Fd()), source, int(d.file.Fd()), destination)
	return renameNamespaceResult(err, unix.ENOENT, nsMissingError(), "atomically replace and discard publication destination")
}
