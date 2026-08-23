//go:build freebsd

// FreeBSD arms of the reservation inspection machine (Rust
// reservation_inspection.rs cfg(target_os = "freebsd")): the
// canonical coordination twin opens with the any-link rule and the
// no-replace link transition is finished before the final proof,
// exactly like the freebsd publication path.

package publication

import (
	"github.com/firehol/iprange/v4/go/internal/live"
)

// openCanonicalRegular opens the coordination twin accepting any link
// count on freebsd (Rust open_regular_any_link).
func openCanonicalRegular(destination *destination) (*live.RegularFile, error) {
	return destination.directory().OpenRegularAnyLink(destination.coordinationName(), true)
}

// finishCanonicalTransition completes the freebsd no-replace link
// transition of the reservation (Rust finish_noreplace_transition).
func finishCanonicalTransition(destination *destination, privateName string, identity live.FileIdentity) error {
	return destination.directory().FinishNoreplaceTransition(privateName, destination.coordinationName(), identity)
}
