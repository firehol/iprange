package fileio

import (
	"os"
	"runtime"

	"github.com/firehol/iprange/v4/go/internal/calleropen"
	"github.com/firehol/iprange/v4/go/internal/live"
)

// CreateOutputFile creates one private CLI output at its temporary path
// under the source database's recorded creator-only choice (Rust twin:
// io/export_writer.rs create_output_file). POSIX creates through the
// owner-side open and then sets exactly 0600 when the source is
// creator-only, independent of the process umask; Windows cannot apply
// the protection after the fact, so a creator-only output is created
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
		if err := file.Chmod(0o600); err != nil {
			file.Close()
			return nil, err
		}
	}
	return file, nil
}
