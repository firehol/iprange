//go:build windows

package live

import (
	"os"

	"golang.org/x/sys/windows"
)

// Windows retained-file identity helpers (Rust namespace/windows.rs
// regular_identity / retained_regular_identity / regular_link_count):
// the identity pair is the volume serial and the 64-bit file index of
// BY_HANDLE_FILE_INFORMATION, the Go surface projection of the live
// layer (the 128-bit FILE_ID_INFO stays in the publication namespace
// machine where identity bytes are observable). Every helper checks
// the regular-file and reparse attributes, and the volume compare
// proves the cross-filesystem rule.

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
	identity := identityFromInfo(info)
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
	identity := identityFromInfo(info)
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
	return identityFromInfo(info), nil
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
		return info, nsPlainIoError("inspect retained Windows handle", err)
	}
	return info, nil
}

// identityFromInfo projects the volume serial and file index of one
// handle information block (the live-layer identity pair).
func identityFromInfo(info windows.ByHandleFileInformation) FileIdentity {
	return FileIdentity{
		device: uint64(info.VolumeSerialNumber),
		inode:  uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow),
	}
}
