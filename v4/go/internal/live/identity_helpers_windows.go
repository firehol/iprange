//go:build windows

package live

import (
	"encoding/binary"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows retained-file identity helpers (Rust namespace/windows.rs
// regular_identity / retained_regular_identity / regular_link_count):
// the identity pair is the 64-bit volume serial and the low half of
// the 128-bit FILE_ID_INFO identifier of the Rust file_identity arm
// (GetFileInformationByHandleEx(FileIdInfo), windows.rs). NTFS keeps
// the file index in the low half of the identifier and zeroes the
// high half, so the portable pair and the encoded tail match the Rust
// arm byte-exactly. Every helper checks the regular-file and reparse
// attributes, and the volume compare proves the cross-filesystem rule.

// RegularIdentityAnyLink proves one regular file on the directory
// volume and returns its identity (Rust regular_identity_any_link).
func RegularIdentityAnyLink(f *os.File, directoryIdentity FileIdentity) (FileIdentity, error) {
	info, err := handleInfo(f)
	if err != nil {
		return FileIdentity{}, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return FileIdentity{}, nsNotRegularError()
	}
	identity, err := fileIdentity(f)
	if err != nil {
		return FileIdentity{}, err
	}
	if identity.device != directoryIdentity.device {
		return FileIdentity{}, nsCrossFilesystemError()
	}
	return identity, nil
}

// RegularIdentity proves one regular single-link file on the
// directory volume (Rust regular_identity).
func RegularIdentity(f *os.File, directoryIdentity FileIdentity) (FileIdentity, error) {
	info, err := handleInfo(f)
	if err != nil {
		return FileIdentity{}, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return FileIdentity{}, nsNotRegularError()
	}
	identity, err := fileIdentity(f)
	if err != nil {
		return FileIdentity{}, err
	}
	if identity.device != directoryIdentity.device {
		return FileIdentity{}, nsCrossFilesystemError()
	}
	if info.NumberOfLinks != 1 {
		return FileIdentity{}, nsLinkCountError(uint64(info.NumberOfLinks))
	}
	return identity, nil
}

// regularIdentityAnyLink captures the retained identity of one regular
// file without a directory-volume proof (Rust
// retained_regular_identity with require_single_link=false).
func regularIdentityAnyLink(f *os.File) (FileIdentity, error) {
	info, err := handleInfo(f)
	if err != nil {
		return FileIdentity{}, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return FileIdentity{}, nsNotRegularError()
	}
	return fileIdentity(f)
}

// RegularLinkCount returns the hard-link count of one retained file
// (Rust regular_link_count).
func RegularLinkCount(f *os.File) (uint64, error) {
	info, err := handleInfo(f)
	if err != nil {
		return 0, err
	}
	return uint64(info.NumberOfLinks), nil
}

// handleInfo reads the BY_HANDLE_FILE_INFORMATION of the retained
// handle; failures keep the IoAt class with the exact Rust operation
// label.
func handleInfo(f *os.File) (windows.ByHandleFileInformation, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return info, nsIoError("inspect retained Windows handle", err)
	}
	return info, nil
}

// fileIDInfo is the FILE_ID_INFO structure of the Windows SDK
// (windows.rs file_identity): 64-bit volume serial plus the 128-bit
// file identifier.
type fileIDInfo struct {
	VolumeSerialNumber uint64
	FileId             [16]byte
}

// fileIdentity captures the Rust windows.rs file_identity pair: the
// 64-bit volume serial and the low half of the 128-bit FILE_ID_INFO
// identifier. NTFS zeroes the high half, so the encoding matches the
// Rust arm byte-exactly.
func fileIdentity(f *os.File) (FileIdentity, error) {
	var info fileIDInfo
	if err := windows.GetFileInformationByHandleEx(windows.Handle(f.Fd()), windows.FileIdInfo, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return FileIdentity{}, nsIoError("read retained Windows file identity", err)
	}
	return FileIdentity{
		device: info.VolumeSerialNumber,
		inode:  binary.LittleEndian.Uint64(info.FileId[0:8]),
	}, nil
}
