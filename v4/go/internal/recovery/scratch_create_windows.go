//go:build windows

package recovery

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// scratchCreateFile creates one ownership-namespace artifact with the
// protected creator-only descriptor (Rust Directory::create windows
// arm: security::create_private with the captured profile; the later
// creator-only proof verifies the descriptor).
func scratchCreateFile(directory *live.Directory, name string, profile security.Profile) (*os.File, error) {
	return directory.CreateSecured(name, profile)
}
