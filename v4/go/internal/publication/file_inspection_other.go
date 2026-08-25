//go:build !freebsd

// POSIX arms of the output inspection machine (Rust
// file_inspection.rs cfg(not(target_os = "freebsd"))): the main
// output opens with the single-link rule and the atomic no-replace
// rename leaves no transition to finish.

package publication

import (
	"github.com/firehol/iprange/v4/go/internal/live"
)

// openMainRegular opens the main output with the single-link rule
// (Rust open_regular; a multi-link main is the link-count refusal
// class of the open machine).
func openMainRegular(destination *destination, header reservationHeader) (*live.RegularFile, error) {
	return destination.directory().OpenRegular(destination.mainName(), false)
}
