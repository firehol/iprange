//go:build freebsd

// FreeBSD arms of the output inspection machine (Rust
// file_inspection.rs cfg(target_os = "freebsd")): the main output
// opens with the any-link rule and the no-replace link transition is
// finished before the inspection when the main already carries the
// reserved inode.

package publication

import (
	"github.com/firehol/iprange/v4/go/internal/live"
)

// openMainRegular opens the main output accepting any link count on
// freebsd (Rust open_regular_any_link) and finishes the no-replace
// link transition when the main already names the reserved output
// inode (Rust finish_noreplace_transition).
func openMainRegular(destination *destination, header reservationHeader) (*live.RegularFile, error) {
	regular, err := destination.directory().OpenRegularAnyLink(destination.mainName(), false)
	if err != nil {
		return nil, err
	}
	if regular == nil {
		return nil, nil
	}
	if reservationIdentityBytes(regular.Identity) == header.outputIdentity {
		private, err := destination.outputName(header.attemptID)
		if err != nil {
			_ = regular.File.Close()
			return nil, err
		}
		if err := destination.directory().FinishNoreplaceTransition(private, destination.mainName(), regular.Identity); err != nil {
			_ = regular.File.Close()
			return nil, err
		}
	}
	return regular, nil
}
