//go:build !windows && !linux && !darwin && !freebsd

// Local-filesystem proof for targets without a live-lock surface:
// every filesystem is refused (Rust require_local_filesystem fallback
// Unsupported). Live coordination is already refused earlier on these
// targets; the refusal keeps the machine total.

package live

import (
	"os"

	"golang.org/x/sys/unix"
)

func requireLocalFilesystem(*os.File) error { return nsUnsupportedError() }

func directoryNameMax(*os.File) (int, error) { return 0, nsUnsupportedError() }

const atNofollow = unix.AT_SYMLINK_NOFOLLOW
