//go:build !windows

package recovery

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// scratchCreateFile creates one ownership-namespace artifact (Rust
// Directory::create unix arm: mode 0600, exclusive, no-follow; the
// creator-only proof runs later in Scratch::create).
func scratchCreateFile(directory *live.Directory, name string, _ security.Profile) (*os.File, error) {
	return directory.Create(name)
}
