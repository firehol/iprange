//go:build !unix

// Windows probe for the no-follow arm: the Go classifier reports
// false on windows (Rust non-unix is_nofollow_symlink), so the same
// IoAt failure folds to its operation detail.

package publication

import (
	"errors"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

func probeNofollowCase() problemCase {
	return problemCase{name: "io at symlink", err: &live.NamespaceError{Kind: live.NamespaceIoAt, Op: "inspect retained name", Err: errors.New("symlink")}, code: format.CodeIO, detail: "inspect retained name"}
}
