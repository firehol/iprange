//go:build windows

package handlers

import "golang.org/x/sys/windows"

// captureFileIdentityPlatform returns (volume serial, file index)
// through GetFileInformationByHandle, exactly like Rust
// windows_file_identity: FILE_READ_ATTRIBUTES without read access,
// FILE_SHARE_DELETE so a pinned reader's file stays nameable, and
// FILE_FLAG_BACKUP_SEMANTICS so directories work too.
func captureFileIdentityPlatform(path string) (dev, ino uint64, ok bool) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, false
	}
	handle, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return 0, 0, false
	}
	defer windows.CloseHandle(handle)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return 0, 0, false
	}
	dev = uint64(info.VolumeSerialNumber)
	ino = uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)
	return dev, ino, true
}
