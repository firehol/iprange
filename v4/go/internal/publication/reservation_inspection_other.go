//go:build !freebsd && !windows

// POSIX arms of the reservation inspection machine (Rust
// reservation_inspection.rs cfg(not(target_os = "freebsd"))): the
// coordination twin opens with the single-link rule and the atomic
// no-replace rename leaves no transition to finish.

package publication

import (
	"github.com/firehol/iprange/v4/go/internal/live"
)

// openCanonicalRegular opens the coordination twin with the
// single-link rule (Rust open_regular).
func openCanonicalRegular(destination *destination) (*live.RegularFile, error) {
	return destination.directory().OpenRegular(destination.coordinationName(), true)
}

// finishCanonicalTransition is a no-op where the rename primitive is
// atomic (Rust has no freebsd arm and nothing to finish).
func finishCanonicalTransition(destination *destination, privateName string, identity live.FileIdentity) error {
	return nil
}
