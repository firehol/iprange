//go:build !windows

package live

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/security"
)

func createPrivateOutput(string, security.Profile) (*os.File, error) {
	// Unreachable: the POSIX creator-only output applies exactly 0600
	// after the owner-side open instead of a protected DACL.
	return nil, os.ErrInvalid
}
