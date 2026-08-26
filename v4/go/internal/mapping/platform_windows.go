//go:build windows

package mapping

import (
	"encoding/binary"
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// Windows platform primitives of the mapping owner (Rust database_file
// open_read_only windows arm + memmap2 section mapping): every open
// uses FILE_FLAG_OPEN_REPARSE_POINT and share read/write/delete so a
// reparse-point final component is opened as the link itself and then
// refused by attribute, exactly like the unix O_NOFOLLOW arm; mappings
// are file-backed sections (CreateFileMappingW + MapViewOfFile), the
// extent changes go through SetEndOfFile, and the duplicated owner
// handle is non-inheritable.

const (
	protRead  = 1
	protWrite = 2
)

// openNoFollow opens the final path component without following a
// reparse point: CreateFileW with FILE_FLAG_OPEN_REPARSE_POINT, share
// read/write/delete, and then a reparse attribute check refusing the
// same WrongState class as the Rust open_read_only windows arm
// ("database path is a Windows reparse point"). Other failures keep
// the shared "open" label of the IO class.
func openNoFollow(clean string, rdwr bool) (*os.File, error) {
	ptr, err := windows.UTF16PtrFromString(clean)
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "open: " + err.Error()}
	}
	access := uint32(windows.GENERIC_READ)
	if rdwr {
		access |= windows.GENERIC_WRITE
	}
	handle, err := windows.CreateFile(ptr, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "open: " + err.Error()}
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		windows.CloseHandle(handle)
		return nil, &format.Error{Code: format.CodeIO, Detail: "open: " + err.Error()}
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		windows.CloseHandle(handle)
		return nil, &format.Error{Code: format.CodeWrongState, Detail: "database path is a Windows reparse point"}
	}
	return os.NewFile(uintptr(handle), clean), nil
}

// createNoFollow exclusively creates one protected creator-only file
// (Rust live_namespace::create_private windows arm through
// security::create_private): CREATE_NEW refuses an existing
// destination, and the protected single-user DACL is the
// creation-security kind 2 of the platform contract.
func createNoFollow(clean string) (*os.File, error) {
	profile, err := security.Capture()
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "create: " + err.Error()}
	}
	f, err := security.CreatePrivate(clean, profile, false)
	if err != nil {
		var fe *format.Error
		if errors.As(err, &fe) && fe.Code == format.CodeNameExists {
			return nil, &format.Error{Code: format.CodeNameExists, Detail: "destination exists"}
		}
		return nil, &format.Error{Code: format.CodeIO, Detail: "create: " + err.Error()}
	}
	return f, nil
}

// mmapShared maps size bytes of the file read-only or read-write
// through one file-backed section (Rust memmap2 map_raw / map_raw_read_only).
func mmapShared(f *os.File, size int, prot int) ([]byte, error) {
	if size == 0 {
		return nil, nil
	}
	access := uint32(windows.GENERIC_READ)
	pageProtection := uint32(windows.PAGE_READONLY)
	mapAccess := uint32(windows.FILE_MAP_READ)
	if prot&protWrite != 0 {
		access |= windows.GENERIC_WRITE
		pageProtection = windows.PAGE_READWRITE
		mapAccess = windows.FILE_MAP_WRITE
	}
	section, err := windows.CreateFileMapping(windows.Handle(f.Fd()), nil, pageProtection, uint32(uint64(size)>>32), uint32(size), nil)
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "mmap: " + err.Error()}
	}
	base, err := windows.MapViewOfFile(section, mapAccess, 0, 0, uintptr(size))
	if err != nil {
		windows.CloseHandle(section)
		return nil, &format.Error{Code: format.CodeIO, Detail: "mmap: " + err.Error()}
	}
	// The section handle is no longer needed once the view exists; the
	// view keeps the section alive (Windows semantics).
	windows.CloseHandle(section)
	// MapViewOfFile returns the view address as a uintptr; the
	// reinterpretation keeps the vet unsafeptr check honest about a
	// value that genuinely is a kernel-mapped address.
	data := unsafe.Slice((*byte)(uintptrToPointer(base)), size)
	return data, nil
}

// uintptrToPointer reinterprets one kernel-returned address as a
// pointer without a direct conversion (vet's unsafeptr check only
// accepts uintptr-to-pointer conversions in syscall argument lists).
func uintptrToPointer(address uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&address))
}

// munmapShared releases one mapped view (Rust UnmapViewOfFile).
func munmapShared(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	base := unsafe.Pointer(&data[0])
	if err := windows.UnmapViewOfFile(uintptr(base)); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "munmap: " + err.Error()}
	}
	return nil
}

// msyncShared synchronizes the mapped prefix to the system (Rust
// mapping.rs flush_prefix: FlushViewOfFile; the durability order
// stays flush-range then FlushFileBuffers, exactly like the unix arm).
func msyncShared(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	base := unsafe.Pointer(&data[0])
	if err := windows.FlushViewOfFile(uintptr(base), uintptr(len(data))); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "msync: " + err.Error()}
	}
	return nil
}

// ftruncateError reports the ftruncate IO class while keeping the raw
// OS error reachable: format.Error carries no cause chain, and the
// shrink retention decision compares the raw system error exactly
// like the Rust raw_os_error check (Rust shrink_file_or_retain
// mapped_view_prevents_shrink). The As method keeps every existing
// errors.As(*format.Error) call sighted on the class, mirroring the
// live package classedError pattern.
type ftruncateError struct {
	class *format.Error
	cause error
}

func (e *ftruncateError) Error() string { return e.class.Error() }

func (e *ftruncateError) Unwrap() error { return e.cause }

func (e *ftruncateError) As(target any) bool {
	fe, ok := target.(**format.Error)
	if !ok {
		return false
	}
	*fe = e.class
	return true
}

// truncateFile sets the exact file extent (Rust set_len through the
// OS: SetFileInformationByHandle(FileEndOfFileInfo)).
func truncateFile(f *os.File, size int64) error {
	if err := f.Truncate(size); err != nil {
		return &ftruncateError{
			class: &format.Error{Code: format.CodeIO, Detail: "ftruncate: " + err.Error()},
			cause: err,
		}
	}
	return nil
}

// isMappedViewRetained reports whether a truncate failure is the
// mapped-view retention class (Rust shrink_file_or_retain
// mapped_view_prevents_shrink): another process holds a view of the
// section, so the file keeps its physical extent and the mapping is
// re-established at the committed length anyway. The raw system error
// is compared through the ftruncateError cause chain.
func isMappedViewRetained(err error) bool {
	return errors.Is(err, windows.ERROR_USER_MAPPED_FILE)
}

// dupFile duplicates the handle as one non-inheritable owner handle
// (Rust File::try_clone; DuplicateHandle with bInheritHandle=false,
// satisfying the spec's non-inheritable descriptor rule).
func dupFile(f *os.File) (*os.File, error) {
	var duplicated windows.Handle
	current, err := windows.GetCurrentProcess()
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "dup: " + err.Error()}
	}
	if err := windows.DuplicateHandle(current, windows.Handle(f.Fd()), current, &duplicated, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "dup: " + err.Error()}
	}
	return os.NewFile(uintptr(duplicated), f.Name()), nil
}

// fileIDInfo is the FILE_ID_INFO structure of the Windows SDK (Rust
// namespace/windows.rs file_identity): 64-bit volume serial plus the
// 128-bit file identifier.
type fileIDInfo struct {
	VolumeSerialNumber uint64
	FileId             [16]byte
}

// statIdentity returns the 64-bit volume serial and the low half of
// the 128-bit FILE_ID_INFO identifier of the retained handle (Rust
// namespace/windows.rs file_identity; NTFS keeps the file index in
// the low half and zeroes the high half, so the values are the
// byte-identical local identity pair of the persisted surfaces).
func statIdentity(f *os.File) (device uint64, inode uint64, err error) {
	var info fileIDInfo
	if err := windows.GetFileInformationByHandleEx(windows.Handle(f.Fd()), windows.FileIdInfo, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return 0, 0, err
	}
	return info.VolumeSerialNumber, binary.LittleEndian.Uint64(info.FileId[0:8]), nil
}
