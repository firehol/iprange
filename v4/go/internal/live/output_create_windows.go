//go:build windows

package live

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/security"
)

func createPrivateOutput(path string, profile security.Profile) (*os.File, error) {
	return security.CreatePrivate(path, profile, true)
}
