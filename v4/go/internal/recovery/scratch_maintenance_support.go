package recovery

// Shared authentication, result, and namespace helpers of the
// abandoned-scratch machines (Rust
// recovery/scratch_maintenance/support.rs).

import (
	"errors"
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// authenticateScratchFile authenticates one regular artifact through
// its fixed ownership header (Rust support::authenticate): a file
// shorter than the header, an undecodable header, or a header whose
// attempt and ordinal do not match the parsed name is
// unauthenticated; a valid owner kind authenticates the entry.
func authenticateScratchFile(file *os.File, attempt [16]byte, ordinal uint32) (AbandonedScratchAuthentication, error) {
	header, ok, err := readScratchHeader(file)
	if err != nil {
		return unauthenticatedScratch(), err
	}
	if !ok {
		return unauthenticatedScratch(), nil
	}
	if header.attemptID != attempt || header.ordinal != ordinal {
		return unauthenticatedScratch(), nil
	}
	owner, err := scratchOwnerOf(header.ownerKind)
	if err != nil {
		return unauthenticatedScratch(), err
	}
	return authenticatedScratch(owner), nil
}

// readScratchHeader maps and decodes the fixed ownership header of
// one regular artifact (Rust support::authenticate/require_header
// over Mapping::read_only_view); a file shorter than the header
// reports absent.
func readScratchHeader(file *os.File) (scratchDecodedHeader, bool, error) {
	st, err := file.Stat()
	if err != nil {
		return scratchDecodedHeader{}, false, &format.Error{Code: format.CodeIO, Detail: "stat abandoned scratch: " + err.Error()}
	}
	if st.Size() < scratchHeaderSize {
		return scratchDecodedHeader{}, false, nil
	}
	mapped, err := mapping.MapFile(file, scratchHeaderSize, false)
	if err != nil {
		return scratchDecodedHeader{}, false, err
	}
	defer mapped.Close()
	var bytes [scratchHeaderSize]byte
	view, err := mapped.View(0, scratchHeaderSize)
	if err != nil {
		return scratchDecodedHeader{}, false, err
	}
	copy(bytes[:], view)
	header, ok := decodeScratchHeader(&bytes)
	return header, ok, nil
}

// requireScratchHeader reads and authenticates the header of one
// artifact being removed (Rust support::require_header): the length
// proof is the Corrupt class (Rust require_file_extent), and a header
// that does not decode or match is the CleanupConflict class.
func requireScratchHeader(file *os.File, attempt [16]byte, ordinal uint32) (scratchDecodedHeader, error) {
	st, err := file.Stat()
	if err != nil {
		return scratchDecodedHeader{}, maintenanceCleanupError(&format.Error{Code: format.CodeIO, Detail: "stat abandoned scratch: " + err.Error()})
	}
	if st.Size() < scratchHeaderSize {
		return scratchDecodedHeader{}, &format.Error{Code: format.CodeFormatInvalid, Detail: "mapping exceeds the file extent"}
	}
	header, ok, err := readScratchHeader(file)
	if err != nil {
		return scratchDecodedHeader{}, maintenanceCleanupError(err)
	}
	if !ok {
		return scratchDecodedHeader{}, &format.Error{Code: format.CodeCleanupConflict, Detail: "abandoned scratch header is unauthenticated"}
	}
	if header.attemptID != attempt || header.ordinal != ordinal {
		return scratchDecodedHeader{}, &format.Error{Code: format.CodeCleanupConflict, Detail: "abandoned scratch header is unauthenticated"}
	}
	return header, nil
}

// scratchOwnerOf maps one header owner kind (Rust support::owner).
func scratchOwnerOf(value uint16) (ScratchOwnerKind, error) {
	switch value {
	case uint16(ScratchOwnerValidation):
		return ScratchOwnerValidation, nil
	case uint16(ScratchOwnerRecovery):
		return ScratchOwnerRecovery, nil
	default:
		return 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "scratch owner kind is invalid"}
	}
}

// requireMaintenanceDirectory proves the retained directory identity
// (Rust support::require_directory).
func requireMaintenanceDirectory(directory *live.Directory, expected live.FileIdentity) error {
	if directory.Identity() != expected {
		return &format.Error{Code: format.CodeDirectoryIdentityMismatch, Detail: "scratch directory identity does not match"}
	}
	return nil
}

// requireMaintenanceOwned proves one entry is exactly the expected
// regular single-link artifact (Rust support::require_owned).
func requireMaintenanceOwned(regular bool, links uint64, found live.FileIdentity, expected live.FileIdentity) error {
	if !regular || links != 1 || found != expected {
		return &format.Error{Code: format.CodeCleanupConflict, Detail: "abandoned scratch identity or link count changed"}
	}
	return nil
}

// durableScratchAbsence proves one name durably absent (Rust
// support::durable_absence: the directory sync, verify, and the
// absent-name proof).
func durableScratchAbsence(directory *live.Directory, name string) error {
	if err := directory.Sync(); err != nil {
		return maintenanceNamespaceError(err)
	}
	if err := directory.Verify(); err != nil {
		return maintenanceNamespaceError(err)
	}
	if err := directory.RequireAbsent(name); err != nil {
		return maintenanceCleanupError(err)
	}
	return nil
}

// maintenanceIdentity decodes one portable identity to the retained
// form (Rust support::identity: the kind proof and the payload
// decode).
func maintenanceIdentity(identity publication.LocalFileIdentity) (live.FileIdentity, error) {
	device, inode, ok := identity.DeviceInode()
	if !ok {
		if identity.Kind != 1 && identity.Kind != 2 {
			return live.FileIdentity{}, &format.Error{Code: format.CodeInvalidArgument, Detail: "unsupported abandoned scratch identity kind"}
		}
		return live.FileIdentity{}, &format.Error{Code: format.CodeInvalidArgument, Detail: "invalid abandoned scratch identity"}
	}
	return live.IdentityFromDeviceInode(device, inode), nil
}

// maintenanceRemoval builds the factual removal outcome (Rust
// support::removal).
func maintenanceRemoval(sourcePresent bool, cause error, housekeeping publication.Housekeeping, visible []publication.HousekeepingArtifact) publication.AbandonedArtifactRemoval {
	state := publication.CleanupStateClean
	if cause != nil {
		state = publication.CleanupStateResiduePossible
	}
	return publication.AbandonedArtifactRemoval{
		SourcePresent:       sourcePresent,
		CleanupState:        state,
		Housekeeping:        housekeeping,
		VisibleHousekeeping: visible,
		Cause:               cause,
	}
}

// maintenanceCleanupError maps one namespace error of the removal
// machine (Rust support::cleanup_error: Io stays Io, ForkedHandle
// stays, every other class is the ownership-changed conflict).
func maintenanceCleanupError(err error) error {
	nerr, ok := live.AsNamespaceError(err)
	if !ok {
		var fe *format.Error
		if errors.As(err, &fe) {
			return fe
		}
		return &format.Error{Code: format.CodeCleanupConflict, Detail: "abandoned scratch ownership changed"}
	}
	switch nerr.Kind {
	case live.NamespaceIo, live.NamespaceIoAt:
		return &format.Error{Code: format.CodeIO, Detail: nerr.Error()}
	case live.NamespaceForkedHandle:
		return &format.Error{Code: format.CodeForkedHandle, Detail: "scratch owner crossed fork"}
	default:
		return &format.Error{Code: format.CodeCleanupConflict, Detail: "abandoned scratch ownership changed"}
	}
}

// maintenanceNamespaceError maps one namespace error of the listing
// and open machines (Rust support::namespace_error).
func maintenanceNamespaceError(err error) error {
	nerr, ok := live.AsNamespaceError(err)
	if !ok {
		var fe *format.Error
		if errors.As(err, &fe) {
			return fe
		}
		return &format.Error{Code: format.CodeCleanupConflict, Detail: "scratch directory entry changed"}
	}
	switch nerr.Kind {
	case live.NamespaceInvalidName:
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "invalid abandoned scratch name"}
	case live.NamespaceForkedHandle:
		return &format.Error{Code: format.CodeForkedHandle, Detail: "scratch owner crossed fork"}
	case live.NamespaceIo, live.NamespaceIoAt:
		return &format.Error{Code: format.CodeIO, Detail: nerr.Error()}
	case live.NamespaceUnsupported, live.NamespaceCrossFilesystem:
		// Rust maps these to Error::Unsupported, whose SDK code is
		// 58 (OsUnsupported); the publication machine keeps 34 for
		// its own authority, the recovery arm follows recovery.rs.
		return &format.Error{Code: format.CodeOSUnsupported, Detail: "scratch directory lacks required local operations"}
	default:
		return &format.Error{Code: format.CodeCleanupConflict, Detail: "scratch directory entry changed"}
	}
}
