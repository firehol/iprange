//go:build windows && (amd64 || arm64)

package worker

import (
	"encoding/binary"
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fileIDInfoEx is the FILE_ID_INFO structure of the Windows SDK (Rust
// namespace/windows.rs file_identity): 64-bit volume serial plus the
// 128-bit file identifier.
type fileIDInfoEx struct {
	VolumeSerialNumber uint64
	FileId             [16]byte
}

// testFileIdentity returns the volume serial and low half of the
// 128-bit FILE_ID_INFO identifier of one retained file (Rust
// namespace/windows.rs file_identity; NTFS keeps the file index in
// the low half, so the pair is the byte-identical local identity of
// the persisted surfaces).
func testFileIdentity(t *testing.T, f *os.File) (device, inode uint64) {
	t.Helper()
	var info fileIDInfoEx
	if err := windows.GetFileInformationByHandleEx(windows.Handle(f.Fd()), windows.FileIdInfo, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		t.Fatal("file id:", err)
	}
	return info.VolumeSerialNumber, binary.LittleEndian.Uint64(info.FileId[0:8])
}
