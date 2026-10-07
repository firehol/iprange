//go:build !windows

package recovery

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// scratchCreateFile creates one ownership-namespace artifact following
// the recorded choice (Rust scratch.rs create): creator-only under mode
// 0600 through Directory.Create, unprotected under the process default
// 0666; the creator-only proof runs later in Scratch::create.
func scratchCreateFile(directory *live.Directory, name string, _ security.Profile, creatorOnly bool) (*os.File, error) {
	if creatorOnly {
		return directory.Create(name)
	}
	return directory.CreateUnprotected(name)
}
