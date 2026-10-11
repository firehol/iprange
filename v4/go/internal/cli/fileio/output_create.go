package fileio

import (
	"os"
	"runtime"

	"github.com/firehol/iprange/v4/go/internal/calleropen"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// CreateOutputFile creates one private CLI output at its temporary path
// under the source database's recorded creator-only choice (Rust twin:
// io/export_writer.rs create_output_file). POSIX creates through the
// owner-side open and then applies the creator-only security authority
// when the source is creator-only — exact 0600, inherited ACL entries
// removed, the single-link ownership proof — independent of the process
// umask; Windows cannot apply the protection after the fact, so a
// creator-only output is created
// through the engine's protected single-user DACL path instead of a
// plain descriptor the mode argument of which Windows ignores.
func CreateOutputFile(temporary string, creatorOnly bool) (*os.File, error) {
	if runtime.GOOS == "windows" && creatorOnly {
		profile, err := live.CaptureSecurityProfile()
		if err != nil {
			return nil, err
		}
		file, err := live.CreatePrivateOutput(temporary, profile)
		if err != nil {
			return nil, err
		}
		return file, nil
	}
	mode := os.FileMode(0o666)
	if creatorOnly {
		mode = 0o600
	}
	file, err := calleropen.Open(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL|calleropen.NonBlocking, mode)
	if err != nil {
		return nil, err
	}
	if creatorOnly {
		// The security authority, not a bare chmod: a default ACL on the
		// parent directory leaves named entries on the new file that
		// chmod cannot remove, and the protected contract is a proven
		// trivial ACL, not just mode bits (sol round-6 P1; the engine's
		// live and publication artifacts go through the same authority).
		profile, perr := live.CaptureSecurityProfile()
		if perr != nil {
			file.Close()
			return nil, perr
		}
		if serr := security.SecureCreatorOnly(file, profile); serr != nil {
			file.Close()
			return nil, serr
		}
	}
	return file, nil
}
