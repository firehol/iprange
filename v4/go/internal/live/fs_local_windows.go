//go:build windows

package live

import (
	"os"

	"golang.org/x/sys/windows"
)

// Windows filesystem-local proof (Rust namespace/windows.rs
// require_local_ntfs): the retained directory must sit on local NTFS,
// otherwise the engine cannot prove the durability and name semantics
// of the live coordination contract. The call also returns the
// volume's maximum component length (in UTF-16 units), which becomes
// the directory name_max.

// requireLocalFilesystem refuses every non-local, non-NTFS volume of
// the retained handle (Rust require_local_ntfs -> Unsupported).
func requireLocalFilesystem(f *os.File) error {
	_, err := directoryNameMax(f)
	return err
}

// directoryNameMax returns the NTFS maximum component length in units
// (Rust require_local_ntfs: GetVolumeInformationByHandleW, exact NTFS
// name match, Unsupported for any other filesystem).
func directoryNameMax(f *os.File) (int, error) {
	const ntfs = "NTFS"
	var maximum uint32
	var filesystem [16]uint16
	if err := windows.GetVolumeInformationByHandle(windows.Handle(f.Fd()), nil, 0, nil, &maximum, nil, &filesystem[0], uint32(len(filesystem))); err != nil {
		return 0, nsIoError("inspect publication volume", err)
	}
	length := 0
	for length < len(filesystem) && filesystem[length] != 0 {
		length++
	}
	if !wideEqualASCII(filesystem[:length], ntfs) {
		return 0, nsUnsupportedError()
	}
	if maximum == 0 || maximum > 255 {
		return 0, nsUnsupportedError()
	}
	return int(maximum), nil
}

// wideEqualASCII compares one UTF-16 buffer with an ASCII byte string
// case-insensitively (Rust wide_ascii_eq in the NTFS check; only the
// ASCII letters fold).
func wideEqualASCII(wide []uint16, ascii string) bool {
	if len(wide) != len(ascii) {
		return false
	}
	for i, unit := range wide {
		if unit > 0xFF || asciiFold(byte(unit)) != asciiFold(ascii[i]) {
			return false
		}
	}
	return true
}

// asciiFold lowercases one ASCII letter (Rust eq_ignore_ascii_case).
func asciiFold(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}
