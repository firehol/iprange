//go:build windows

// Windows creator-only security machine (Rust
// publication/security/windows.rs): the effective token's user SID is
// captured at attempt start, every artifact is created with a
// protected non-inheriting DACL containing exactly one allow ACE for
// that SID with exactly FILE_ALL_ACCESS, and the ownership commitment
// is SHA-256("IPR4PSEC" || sid_len:u32le || exact_sid_bytes ||
// FILE_ALL_ACCESS:u32le || SE_DACL_PROTECTED:u16le). The proof reads
// the live descriptor of the retained handle and refuses anything that
// deviates (spec section 15.6, creation-security kind 2).

package security

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// aclSupported reports the creator-only machine availability (true on
// windows: the pure-Go SID/DACL machine below replaces the POSIX ACL
// surface).
const aclSupported = true

// fileAllAccess is FILE_ALL_ACCESS for files (STANDARD_RIGHTS_REQUIRED |
// SYNCHRONIZE | 0x1FF); x/sys does not publish the constant.
const fileAllAccess = 0x001F01FF

// seDaclProtected is the SECURITY_DESCRIPTOR_CONTROL bit that makes a
// DACL protected against inheritable ACEs (winnt.h SE_DACL_PROTECTED).
const seDaclProtected windows.SECURITY_DESCRIPTOR_CONTROL = 0x1000

// sidMaxLength bounds a SID copy (Rust copy_sid bound).
const sidMaxLength = 68

// advapi32 procedures missing from x/sys: SetEntriesInAclW builds the
// exact one-ACE DACL, InitializeSecurityDescriptor prepares the
// absolute descriptor (same calls as the Rust windows-sys bindings).
var (
	procSetEntriesInAclW       = windows.NewLazySystemDLL("advapi32.dll").NewProc("SetEntriesInAclW")
	procInitializeSecurityDesc = windows.NewLazySystemDLL("advapi32.dll").NewProc("InitializeSecurityDescriptor")
)

// Profile is the creator identity captured before creation (Rust
// security::Profile windows arm): the exact user SID bytes and their
// commitment.
type Profile struct {
	sid        []byte
	commitment [32]byte
}

// Capture records the effective token user SID and its commitment
// (Rust Profile::capture): the thread impersonation token when
// present, otherwise the process token, exactly like the Rust arm.
func Capture() (Profile, error) {
	sid, err := effectiveUserSID()
	if err != nil {
		return Profile{}, err
	}
	return Profile{sid: sid, commitment: commitment(sid)}, nil
}

// Commitment returns the captured commitment.
func (p *Profile) Commitment() [32]byte {
	return p.commitment
}

// CreatorOnlySupported reports the secure creator-only machine
// availability (true on windows).
func CreatorOnlySupported() bool { return aclSupported }

// removeInheritedACL is a no-op on Windows: the protected descriptor
// is established at creation by CreatePrivate, so no post-create
// strip exists (Rust windows security has no strip arm).
func removeInheritedACL(*os.File) error { return nil }

// requireTrivialACL is a no-op on Windows for the same reason: the
// creator-only proof is the DACL verification in
// creatorOnlyCommitment, not an ACL strip probe.
func requireTrivialACL(*os.File) error { return nil }

// CreatePrivate exclusively creates one file at path with a protected
// single-user DACL for the profile SID (Rust security::create_private):
// CREATE_NEW, FILE_ALL_ACCESS, share read/write/delete,
// FILE_FLAG_OPEN_REPARSE_POINT (a reparse-point destination is opened
// as the link itself, never followed), optional FILE_FLAG_WRITE_THROUGH,
// and a non-inheritable handle. An existing destination reports the
// exact exists class; other failures carry the Rust operation label.
func CreatePrivate(path string, profile Profile, writeThrough bool) (*os.File, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &format.Error{Code: format.CodeNameInvalid, Detail: "invalid file name"}
	}
	descriptor, cleanup, err := buildDescriptor(profile.sid)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	flags := uint32(windows.FILE_ATTRIBUTE_NORMAL | windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if writeThrough {
		flags |= windows.FILE_FLAG_WRITE_THROUGH
	}
	handle, err := windows.CreateFile(ptr, fileAllAccess, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, &descriptor, windows.CREATE_NEW, flags, 0)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_EXISTS) {
			return nil, &format.Error{Code: format.CodeNameExists, Detail: "destination exists"}
		}
		return nil, ioError("create private file", err)
	}
	return os.NewFile(uintptr(handle), path), nil
}

// SecureCreatorOnly proves the creator-only policy of one open
// artifact against the captured profile (Rust
// security::secure_creator_only): the artifact's live commitment must
// equal the profile commitment, otherwise the access-policy class.
func SecureCreatorOnly(f *os.File, profile Profile) error {
	commit, err := creatorOnlyCommitment(f)
	if err != nil {
		return err
	}
	if commit != profile.commitment {
		return accessPolicy()
	}
	return nil
}

// CreatorOnlyCommitment proves the creator-only policy of one open
// artifact and returns the commitment of its current owner (Rust
// security::creator_only_commitment windows arm): the descriptor must
// be protected (SE_DACL_PROTECTED), carry exactly one allow ACE with
// exactly FILE_ALL_ACCESS and no flags for exactly the owner SID. Any
// deviation fails the access-policy class, exactly like Rust.
func CreatorOnlyCommitment(f *os.File) ([32]byte, error) {
	return creatorOnlyCommitment(f)
}

// creatorOnlyCommitment implements the proof over the retained handle
// (Rust SecurityInfo::read + verify).
func creatorOnlyCommitment(f *os.File) ([32]byte, error) {
	sd, err := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return [32]byte{}, ioError("read creator-only DACL", err)
	}
	control, _, err := sd.Control()
	if err != nil {
		return [32]byte{}, ioError("read creator-only DACL", err)
	}
	if control&seDaclProtected == 0 {
		return [32]byte{}, accessPolicy()
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return [32]byte{}, ioError("read creator-only DACL", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return [32]byte{}, accessPolicy()
	}
	if dacl.AceCount != 1 {
		return [32]byte{}, accessPolicy()
	}
	var rawAce *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &rawAce); err != nil {
		return [32]byte{}, ioError("read creator-only DACL entry", err)
	}
	if rawAce.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
		rawAce.Header.AceFlags != 0 ||
		rawAce.Mask != windows.ACCESS_MASK(fileAllAccess) {
		return [32]byte{}, accessPolicy()
	}
	sid := (*windows.SID)(unsafe.Pointer(&rawAce.SidStart))
	if !windows.EqualSid(owner, sid) {
		return [32]byte{}, accessPolicy()
	}
	sidBytes, err := copySID(sid)
	if err != nil {
		return [32]byte{}, err
	}
	return commitment(sidBytes), nil
}

// buildDescriptor constructs the SECURITY_ATTRIBUTES of one protected
// creator-only descriptor (Rust Descriptor::new): SetEntriesInAclW with
// one SET_ACCESS/FILE_ALL_ACCESS/NO_INHERITANCE entry for the SID, then
// initialize + owner + DACL + SE_DACL_PROTECTED. The cleanup releases
// the ACL allocation.
func buildDescriptor(sid []byte) (windows.SecurityAttributes, func(), error) {
	trustee := windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_USER, TrusteeValue: windows.TrusteeValue(uintptr(unsafe.Pointer(&sid[0])))}
	explicit := windows.EXPLICIT_ACCESS{AccessPermissions: windows.ACCESS_MASK(fileAllAccess), AccessMode: windows.SET_ACCESS, Inheritance: windows.NO_INHERITANCE, Trustee: trustee}
	var acl *windows.ACL
	ret, _, _ := procSetEntriesInAclW.Call(1, uintptr(unsafe.Pointer(&explicit)), 0, uintptr(unsafe.Pointer(&acl)))
	if ret != 0 {
		return windows.SecurityAttributes{}, func() {}, &format.Error{Code: format.CodeIO, Detail: "build creator-only DACL: " + windows.Errno(ret).Error()}
	}
	cleanup := func() {
		_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(acl))) // LocalFree takes HLOCAL (a handle-sized pointer)
	}
	var sd windows.SECURITY_DESCRIPTOR
	if ret, _, callErr := procInitializeSecurityDesc.Call(uintptr(unsafe.Pointer(&sd)), 1); ret == 0 {
		cleanup()
		if callErr == nil {
			callErr = windows.Errno(0)
		}
		return windows.SecurityAttributes{}, func() {}, ioError("build creator-only security descriptor", callErr)
	}
	owner := (*windows.SID)(unsafe.Pointer(&sid[0]))
	if err := sd.SetOwner(owner, false); err != nil {
		cleanup()
		return windows.SecurityAttributes{}, func() {}, ioError("build creator-only security descriptor", err)
	}
	if err := sd.SetDACL(acl, true, false); err != nil {
		cleanup()
		return windows.SecurityAttributes{}, func() {}, ioError("build creator-only security descriptor", err)
	}
	if err := sd.SetControl(seDaclProtected, seDaclProtected); err != nil {
		cleanup()
		return windows.SecurityAttributes{}, func() {}, ioError("build creator-only security descriptor", err)
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: &sd, InheritHandle: 0}
	return attributes, cleanup, nil
}

// effectiveUserSID captures the effective token's user SID (Rust
// effective_user_sid): OpenThreadToken with TOKEN_QUERY, falling back
// to OpenProcessToken on ERROR_NO_TOKEN.
func effectiveUserSID() ([]byte, error) {
	var token windows.Token
	thread, err := windows.GetCurrentThread()
	if err != nil {
		return nil, ioError("open effective thread", err)
	}
	err = windows.OpenThreadToken(thread, windows.TOKEN_QUERY, true, &token)
	if err != nil {
		if !errors.Is(err, windows.ERROR_NO_TOKEN) {
			return nil, ioError("open effective thread token", err)
		}
		process, err := windows.GetCurrentProcess()
		if err != nil {
			return nil, ioError("open effective process", err)
		}
		if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
			return nil, ioError("open effective process token", err)
		}
	}
	defer windows.CloseHandle(windows.Handle(token))
	var length uint32
	// The size probe always reports ERROR_INSUFFICIENT_BUFFER; the
	// x/sys wrapper returns that error directly, so the preceding
	// last-error value is never read (it may already be cleared on
	// the Go runtime and would nil-panic in ioError).
	err = windows.GetTokenInformation(token, windows.TokenUser, nil, 0, &length)
	if err != windows.ERROR_INSUFFICIENT_BUFFER {
		if err == nil {
			err = windows.Errno(0)
		}
		return nil, ioError("size effective token user", err)
	}
	buffer := make([]byte, length)
	if err := windows.GetTokenInformation(token, windows.TokenUser, &buffer[0], length, &length); err != nil {
		return nil, ioError("read effective token user", err)
	}
	if uint32(unsafe.Sizeof(windows.Tokenuser{})) > length {
		return nil, accessPolicy()
	}
	user := (*windows.Tokenuser)(unsafe.Pointer(&buffer[0]))
	return copySID(user.User.Sid)
}

// copySID copies the exact bytes of one valid SID (Rust copy_sid):
// validity first, then the length bound, then the raw bytes.
func copySID(sid *windows.SID) ([]byte, error) {
	if sid == nil || !sid.IsValid() {
		return nil, accessPolicy()
	}
	length := int(windows.GetLengthSid(sid))
	if length == 0 || length > sidMaxLength {
		return nil, accessPolicy()
	}
	bytes := unsafe.Slice((*byte)(unsafe.Pointer(sid)), length)
	owned := make([]byte, length)
	copy(owned, bytes)
	return owned, nil
}

// commitment is the SHA-256 ownership commitment over the domain, the
// SID length and bytes, FILE_ALL_ACCESS, and the protected-DACL
// control bit (Rust commitment).
func commitment(sid []byte) [32]byte {
	lengthLE := make([]byte, 4)
	binary.LittleEndian.PutUint32(lengthLE, uint32(len(sid)))
	allAccessLE := make([]byte, 4)
	binary.LittleEndian.PutUint32(allAccessLE, fileAllAccess)
	protectedLE := []byte{uint8(seDaclProtected & 0xFF), uint8((seDaclProtected >> 8) & 0xFF)}
	hasher := sha256.New()
	hasher.Write([]byte(commitmentDomain))
	hasher.Write(lengthLE)
	hasher.Write(sid)
	hasher.Write(allAccessLE)
	hasher.Write(protectedLE)
	var digest [32]byte
	hasher.Sum(digest[:0])
	return digest
}
