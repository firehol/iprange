//go:build windows

// Retained-directory machine for the Windows live surface (Rust
// publication/namespace/windows.rs Directory + windows_mutation.rs +
// windows_scan.rs). Parent directories open through CreateFileW with
// FILE_FLAG_BACKUP_SEMANTICS and FILE_FLAG_OPEN_REPARSE_POINT, must be
// plain directories on local NTFS with a proven component limit, and
// every entry open goes through the retained final path with
// FILE_FLAG_OPEN_REPARSE_POINT so a junction or symlink final
// component is inspected as the link itself and refused. Renames are
// atomic handle-based NtSetInformationFile moves (FileRenameInformationEx),
// removal uses the POSIX-semantics disposition, and the scan enumerates
// through one reused FileIdBothDirectoryInfo buffer.
//
// The live-layer identity is the (volume serial, 64-bit file index)
// projection of BY_HANDLE_FILE_INFORMATION: the sidecar never persists
// identities, so the projection is unobservable outside this process,
// and the full 128-bit FILE_ID_INFO identity stays in the publication
// namespace machine where identity bytes are observable.

package live

import (
	"errors"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// Entry is one retained directory entry (Rust namespace::Entry).
type Entry struct {
	Identity FileIdentity
	Links    uint64
	Regular  bool
}

// Directory is one open retained directory (Rust Directory).
type Directory struct {
	file    *os.File
	id      FileIdentity
	nameMax int
}

// Close releases the retained directory handle.
func (d *Directory) Close() { _ = d.file.Close() }

// Identity returns the retained directory identity.
func (d *Directory) Identity() FileIdentity { return d.id }

// OpenDirectory binds one directory path: CreateFileW with
// FILE_FLAG_BACKUP_SEMANTICS and FILE_FLAG_OPEN_REPARSE_POINT (a
// junction or symlink final component is opened as the link itself and
// then refused), proves the plain-directory attributes, captures the
// volume identity, and proves the volume is local NTFS with its
// component limit (Rust Directory::open). A missing path is the
// Missing class; every other open failure stays the Io class exactly
// like Rust, which special-cases only the not-found errors.
func OpenDirectory(path string) (*Directory, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, nsInvalidNameError()
	}
	handle, err := windows.CreateFile(ptr, windows.GENERIC_READ|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		if err == windows.ERROR_FILE_NOT_FOUND || err == windows.ERROR_PATH_NOT_FOUND {
			return nil, nsMissingError()
		}
		return nil, nsPlainIoError("open directory", err)
	}
	info, err := handleInfo(os.NewFile(uintptr(handle), path))
	if err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		windows.CloseHandle(handle)
		return nil, nsNotDirectoryError()
	}
	f := os.NewFile(uintptr(handle), path)
	if err := requireLocalFilesystem(f); err != nil {
		f.Close()
		return nil, err
	}
	nameMax, err := directoryNameMax(f)
	if err != nil || nameMax <= 0 {
		f.Close()
		return nil, nsUnsupportedError()
	}
	return &Directory{
		file:    f,
		id:      identityFromInfo(info),
		nameMax: nameMax,
	}, nil
}

// Entry inspects one name without following a final reparse point
// (Rust Directory::entry: open with FILE_READ_ATTRIBUTES|READ_CONTROL
// and FILE_FLAG_OPEN_REPARSE_POINT); the bool reports an absent name.
func (d *Directory) Entry(name string) (Entry, bool, error) {
	file, err := d.openEntry(name, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL, false)
	if err != nil {
		return Entry{}, false, err
	}
	if file == nil {
		return Entry{}, false, nil
	}
	defer file.Close()
	info, err := handleInfo(file)
	if err != nil {
		return Entry{}, false, err
	}
	return Entry{
		Identity: identityFromInfo(info),
		Links:    uint64(info.NumberOfLinks),
		Regular:  info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) == 0,
	}, true, nil
}

// create creates one name exclusively with the protected creator-only
// descriptor of the captured profile (Rust Directory::create:
// require_name_lengths, security::create_private with write-through).
// ERROR_FILE_EXISTS is the Exists class; an overlong name fails the
// name_max proof as InvalidName before any syscall.
func (d *Directory) create(name string, profile security.Profile) (*os.File, error) {
	if err := d.RequireNameLengths(name); err != nil {
		return nil, err
	}
	path, err := d.entryPath(name)
	if err != nil {
		return nil, err
	}
	f, err := security.CreatePrivate(path, profile, true)
	if err != nil {
		var fe *format.Error
		if errors.As(err, &fe) && fe.Code == format.CodeNameExists {
			return nil, nsExistsError()
		}
		return nil, nsIoError("create private file", err)
	}
	return f, nil
}

// OpenRegular opens one name without following symlinks and proves the
// retained regular identity with the single-link and cross-volume
// rules (Rust Directory::open_regular + regular_identity). An absent
// name reports (nil, nil).
func (d *Directory) OpenRegular(name string, writable bool) (*RegularFile, error) {
	return d.openRegularWithLinks(name, writable, true)
}

// openRegularWithLinks opens one name and applies the caller-selected
// link rule (Rust open_regular_any_link).
func (d *Directory) openRegularWithLinks(name string, writable bool, requireSingleLink bool) (*RegularFile, error) {
	access := uint32(windows.GENERIC_READ | windows.READ_CONTROL)
	if writable {
		access |= windows.GENERIC_WRITE | windows.FILE_WRITE_ATTRIBUTES | windows.DELETE
	}
	file, err := d.openEntry(name, access, writable)
	if err != nil {
		return nil, err
	}
	if file == nil {
		return nil, nil
	}
	fail := func(class error) (*RegularFile, error) {
		file.Close()
		return nil, class
	}
	info, err := handleInfo(file)
	if err != nil {
		file.Close()
		return nil, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return fail(nsNotRegularError())
	}
	identity := identityFromInfo(info)
	if identity.device != d.id.device {
		return fail(nsCrossFilesystemError())
	}
	if requireSingleLink && info.NumberOfLinks != 1 {
		return fail(nsLinkCountError(uint64(info.NumberOfLinks)))
	}
	return &RegularFile{File: file, Identity: identity}, nil
}

// RequireAbsent refuses a name that exists (Rust Directory::require_absent).
func (d *Directory) RequireAbsent(name string) error {
	_, found, err := d.Entry(name)
	if err != nil {
		return err
	}
	if found {
		return nsExistsError()
	}
	return nil
}

// VerifyName proves the name still names the expected identity as one
// regular single-link file (Rust Directory::verify_name).
func (d *Directory) VerifyName(name string, expected FileIdentity) error {
	found, present, err := d.Entry(name)
	if err != nil {
		return err
	}
	if !present {
		return nsMissingError()
	}
	if !found.Regular {
		return nsNotRegularError()
	}
	if found.Identity != expected {
		return nsIdentityChangedError()
	}
	if found.Links != 1 {
		return nsLinkCountError(found.Links)
	}
	return nil
}

// RenameNoReplace atomically renames source over an absent destination
// (Rust windows_mutation rename_noreplace: NtSetInformationFile with
// FileRenameInformationEx and no flags). The source file handle must
// be the retained regular file of source; the machine proves its
// identity, performs the atomic handle-based move with write-through
// sync, and re-proves both names exactly like Rust.
func (d *Directory) RenameNoReplace(source string, sourceFile *os.File, destination string) error {
	return d.rename(source, sourceFile, destination, 0)
}

// RenamePlain atomically renames source over destination, discarding
// the destination (Rust windows_mutation
// replace_discarding_destination: FILE_RENAME_REPLACE_IF_EXISTS |
// FILE_RENAME_POSIX_SEMANTICS). The source is opened through the
// retained directory so the machine proves its exact identity before
// the move.
func (d *Directory) RenamePlain(source string, destination string) error {
	regular, err := d.openRegularWithLinks(source, true, true)
	if err != nil {
		return err
	}
	if regular == nil {
		return nsMissingError()
	}
	defer regular.File.Close()
	return d.rename(source, regular.File, destination, 0x1|0x2)
}

// RenameExchange is unsupported on Windows (Rust windows_mutation
// exchange: no atomic exchange primitive exists).
func (d *Directory) RenameExchange(source, destination string) error {
	return nsUnsupportedError()
}

// rename is the atomic handle-based move machine (Rust
// windows_mutation rename): prove the source via the retained source
// file, build the FILE_RENAME_INFORMATION buffer over the destination
// units with the directory handle as the rename root, call
// NtSetInformationFile with FileRenameInformationEx, sync the moved
// file, and prove both names.
func (d *Directory) rename(source string, sourceFile *os.File, destination string, flags uint32) error {
	identity, err := RegularIdentity(sourceFile, d.id)
	if err != nil {
		return err
	}
	if err := d.VerifyName(source, identity); err != nil {
		return err
	}
	buffer, byteLen, err := windowsRenameBuffer(flags, d.file.Fd(), destination)
	if err != nil {
		return err
	}
	ioStatus := ioStatusBlock{}
	status, _, _ := procNtSetInformationFile.Call(
		sourceFile.Fd(),
		uintptr(unsafe.Pointer(&ioStatus)),
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(byteLen),
		fileRenameInformationEx,
	)
	if int64(status) < 0 {
		dos, _, _ := procRtlNtStatusToDosError.Call(status)
		source := windows.Errno(dos)
		switch source {
		case windows.ERROR_ALREADY_EXISTS, windows.ERROR_FILE_EXISTS:
			return nsExistsError()
		case windows.ERROR_FILE_NOT_FOUND, windows.ERROR_PATH_NOT_FOUND:
			return nsMissingError()
		default:
			return nsIoError("atomically rename retained Windows file", source)
		}
	}
	if err := SyncFile(sourceFile); err != nil {
		return nsPlainIoError("synchronize retained file", err)
	}
	if err := d.RequireAbsent(source); err != nil {
		return err
	}
	return d.VerifyName(destination, identity)
}

// ioStatusBlock is the IO_STATUS_BLOCK of the NT rename call.
type ioStatusBlock struct {
	Status      uintptr
	Information uintptr
}

// windowsRenameBuffer builds the FILE_RENAME_INFORMATION buffer over
// the destination component (Rust rename_buffer layout: flags at 0,
// root handle at 8, name byte length at 16, UTF-16LE name bytes at
// 20, total size 24 + name bytes).
func windowsRenameBuffer(flags uint32, root uintptr, destination string) ([]byte, uint32, error) {
	nameBytes := len(destination) * 2
	total := 24 + nameBytes
	buffer := make([]byte, total)
	buffer[0] = byte(flags)
	buffer[1] = byte(flags >> 8)
	buffer[2] = byte(flags >> 16)
	buffer[3] = byte(flags >> 24)
	buffer[8] = byte(root)
	buffer[9] = byte(root >> 8)
	buffer[10] = byte(root >> 16)
	buffer[11] = byte(root >> 24)
	buffer[12] = byte(root >> 32)
	buffer[13] = byte(root >> 40)
	buffer[14] = byte(root >> 48)
	buffer[15] = byte(root >> 56)
	buffer[16] = byte(nameBytes)
	buffer[17] = byte(nameBytes >> 8)
	buffer[18] = byte(nameBytes >> 16)
	buffer[19] = byte(nameBytes >> 24)
	for i := 0; i < len(destination); i++ {
		letter := destination[i]
		if letter > 0x7F {
			return nil, 0, nsInvalidNameError()
		}
		buffer[20+i*2] = letter
		buffer[20+i*2+1] = 0
	}
	return buffer, uint32(total), nil
}

var (
	procNtSetInformationFile  = syscall.NewLazyDLL("ntdll.dll").NewProc("NtSetInformationFile")
	procRtlNtStatusToDosError = syscall.NewLazyDLL("ntdll.dll").NewProc("RtlNtStatusToDosError")
)

const fileRenameInformationEx = 22

// UnlinkExact removes one name only when it still names the expected
// identity, using the POSIX-semantics disposition so an open handle
// does not block the removal (Rust Directory::unlink_exact). An
// absent name reports (false, nil).
func (d *Directory) UnlinkExact(name string, expected FileIdentity) (bool, error) {
	regular, err := d.openRegularWithLinks(name, true, false)
	if err != nil {
		return false, err
	}
	if regular == nil {
		return false, nil
	}
	defer regular.File.Close()
	if regular.Identity != expected {
		return false, nsIdentityChangedError()
	}
	disposition := fileDispositionInfoEx{Flags: fileDispositionFlagDelete | fileDispositionFlagPosixSemantics}
	if err := windows.SetFileInformationByHandle(windows.Handle(regular.File.Fd()), windows.FileDispositionInfoEx, (*byte)(unsafe.Pointer(&disposition)), uint32(unsafe.Sizeof(disposition))); err != nil {
		return false, nsIoError("remove exact retained Windows file", err)
	}
	if err := d.RequireAbsent(name); err != nil {
		return false, err
	}
	return true, nil
}

// Sync synchronizes the directory (Rust Directory::sync windows arm:
// verify only - Windows directory handles do not expose a name-sync
// primitive, so the retained directory and its identity are re-proved
// instead).
func (d *Directory) Sync() error {
	return d.Verify()
}

// RequireNameLengths proves every name fits the directory name_max
// (Rust Directory::require_name_lengths; Windows measures the UTF-16
// component length).
func (d *Directory) RequireNameLengths(names ...string) error {
	for _, name := range names {
		if len(name) > d.nameMax {
			return nsInvalidNameError()
		}
	}
	return nil
}

// Verify proves the retained directory still names the same plain
// directory on the same local NTFS volume (Rust Directory::verify).
func (d *Directory) Verify() error {
	info, err := handleInfo(d.file)
	if err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || identityFromInfo(info) != d.id {
		return nsIdentityChangedError()
	}
	return requireLocalFilesystem(d.file)
}

// Scan visits every entry of the retained directory in constant
// memory (Rust Directory::scan over windows_scan.rs): the directory
// proves before and after the enumeration stream, "." and ".." are
// skipped, and the visitor receives each raw ASCII name. The final
// verify runs even when the visitor failed and takes precedence over
// its error, exactly like Rust.
func (d *Directory) Scan(visitor func([]byte) error) error {
	if err := d.Verify(); err != nil {
		return err
	}
	stream, err := d.stream()
	if err != nil {
		return err
	}
	defer stream.Close()
	buffer := make([]byte, 64<<10)
	visited := scanWindowsStream(stream, buffer, visitor)
	if err := d.Verify(); err != nil {
		return err
	}
	return visited
}

// stream re-opens the retained directory through its final path and
// proves the identity (Rust Directory::stream).
func (d *Directory) stream() (*os.File, error) {
	units, err := finalPath(d.file)
	if err != nil {
		return nil, err
	}
	path := windows.UTF16ToString(units)
	dir, err := OpenDirectory(path)
	if err != nil {
		return nil, err
	}
	if dir.id != d.id {
		dir.Close()
		return nil, nsIdentityChangedError()
	}
	return dir.file, nil
}

// openEntry opens one final component without following a reparse
// point (Rust Directory::open_entry): the retained final path plus the
// name, FILE_FLAG_OPEN_REPARSE_POINT, optional write-through, and the
// full share set. Missing components report (nil, nil).
func (d *Directory) openEntry(name string, access uint32, writeThrough bool) (*os.File, error) {
	path, err := d.entryPath(name)
	if err != nil {
		return nil, err
	}
	return openWindowsPath(path, access, writeThrough)
}

// entryPath is the retained final path plus one name component (Rust
// Directory::entry_path: final_path + backslash + name units).
func (d *Directory) entryPath(name string) (string, error) {
	units, err := finalPath(d.file)
	if err != nil {
		return "", err
	}
	if len(units) == 0 || units[len(units)-1] != '\\' {
		units = append(units, '\\')
	}
	for _, r := range name {
		if r > 0xFF {
			return "", nsInvalidNameError()
		}
		units = append(units, uint16(r))
	}
	return windows.UTF16ToString(units), nil
}

// openWindowsPath opens one absolute Windows path with the exact
// share, creation, and reparse flags of the Rust open_path arm.
func openWindowsPath(path string, access uint32, writeThrough bool) (*os.File, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, nsInvalidNameError()
	}
	flags := uint32(windows.FILE_ATTRIBUTE_NORMAL | windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if writeThrough {
		flags |= windows.FILE_FLAG_WRITE_THROUGH
	}
	handle, err := windows.CreateFile(ptr, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		if err == windows.ERROR_FILE_NOT_FOUND || err == windows.ERROR_PATH_NOT_FOUND {
			return nil, nil
		}
		return nil, nsIoError("open retained Windows path", err)
	}
	return os.NewFile(uintptr(handle), path), nil
}

// finalPath resolves the retained handle's exact path (Rust
// final_path: GetFinalPathNameByHandleW with size-then-fill).
func finalPath(f *os.File) ([]uint16, error) {
	required, err := windows.GetFinalPathNameByHandle(windows.Handle(f.Fd()), nil, 0, 0)
	if err != nil {
		return nil, nsPlainIoError("size retained Windows directory path", err)
	}
	buffer := make([]uint16, required+1)
	written, err := windows.GetFinalPathNameByHandle(windows.Handle(f.Fd()), &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return nil, nsPlainIoError("read retained Windows directory path", err)
	}
	if written == 0 {
		return nil, nsPlainIoError("read retained Windows directory path", windows.ERROR_INVALID_FUNCTION)
	}
	return buffer[:written], nil
}

// scanWindowsStream walks one enumeration buffer (Rust
// windows_scan.rs): FILE_ID_BOTH_DIR_INFO records until
// ERROR_NO_MORE_FILES, ASCII name projection, "." and ".." skipped.
func scanWindowsStream(stream *os.File, buffer []byte, visitor func([]byte) error) error {
	for {
		if err := windows.GetFileInformationByHandleEx(windows.Handle(stream.Fd()), windows.FileIdBothDirectoryInfo, &buffer[0], uint32(len(buffer))); err != nil {
			if err == windows.ERROR_NO_MORE_FILES {
				return nil
			}
			return nsIoError("enumerate retained Windows directory", err)
		}
		offset := 0
		for {
			headerSize := unsafe.Offsetof(fileIDBothDirInfo{}.FileName)
			if offset+int(headerSize) > len(buffer) {
				return nsIdentityChangedError()
			}
			entry := (*fileIDBothDirInfo)(unsafe.Pointer(&buffer[offset]))
			unitsLen := int(entry.FileNameLength)
			if unitsLen%2 != 0 {
				return nsIdentityChangedError()
			}
			unitsLen /= 2
			nameEnd := int(headerSize) + unitsLen*2
			if offset+nameEnd > len(buffer) {
				return nsIdentityChangedError()
			}
			units := unsafe.Slice((*uint16)(unsafe.Pointer(&buffer[offset+int(headerSize)])), unitsLen)
			name, ok := asciiNameFromUnits(units)
			if ok && string(name) != "." && string(name) != ".." {
				if err := visitor(name); err != nil {
					return err
				}
			}
			if entry.NextEntryOffset == 0 {
				break
			}
			next := int(entry.NextEntryOffset)
			if next <= 0 || offset+next >= len(buffer) {
				return nsIdentityChangedError()
			}
			offset += next
		}
	}
}

// asciiNameFromUnits projects one UTF-16 name to ASCII bytes when
// every unit is an ASCII character (Rust ascii_name), reporting
// whether the name is representable.
func asciiNameFromUnits(units []uint16) ([]byte, bool) {
	if len(units) > 255 {
		return nil, false
	}
	name := make([]byte, len(units))
	for i, unit := range units {
		if unit > 0x7F {
			return nil, false
		}
		name[i] = byte(unit)
	}
	return name, true
}

// fileDispositionInfoEx is the FILE_DISPOSITION_INFO_EX structure of
// the POSIX-semantics delete (windows_mutation.rs).
type fileDispositionInfoEx struct {
	Flags uint32
}

const (
	fileDispositionFlagDelete         = 0x00000001
	fileDispositionFlagPosixSemantics = 0x00000002
)

// fileIDBothDirInfo is the FILE_ID_BOTH_DIR_INFO enumeration record of
// the Windows SDK (offset-exact tail: FileName begins at 104).
type fileIDBothDirInfo struct {
	NextEntryOffset uint32
	FileIndex       uint32
	CreationTime    int64
	LastAccessTime  int64
	LastWriteTime   int64
	ChangeTime      int64
	EndOfFile       int64
	AllocationSize  int64
	FileAttributes  uint32
	FileNameLength  uint32
	EaSize          uint32
	ShortNameLength uint16
	ShortName       [12]uint16
	_               [2]byte
	FileID          int64
	FileName        uint16
}

// RegularFile is one opened retained regular file (Rust namespace::Regular).
type RegularFile struct {
	File     *os.File
	Identity FileIdentity
}
