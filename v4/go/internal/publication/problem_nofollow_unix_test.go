//go:build unix

// Platform probe for the no-follow symlink problem arm (Rust
// Problem::namespace is_nofollow_symlink: ELOOP on unix; the
// classifier is always false on windows, where the arm folds to the
// operation's Io class).

package publication

import (
	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

func probeNofollowCase() problemCase {
	return problemCase{name: "io at symlink", err: &live.NamespaceError{Kind: live.NamespaceIoAt, Op: "inspect retained name", Err: unix.ELOOP}, code: format.CodeConflict, detail: "publication name is a symlink"}
}
