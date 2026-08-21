// One-shot attempt staging, preparation, and publication of a finished
// immutable output (Rust publication/{workflow,output,output_digest,
// cleanup,result,types}.rs). The writer builds into a private attempt
// name, proves custody and a SHA-512 digest over the finished mapping,
// then publishes per policy: no-replace rename, atomic exchange, or
// plain replacement. All namespace syscalls stay in internal/mapping.

package writer

import (
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/firehol/iprange/v4/go/internal/fault"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// PublicationPolicy is the namespace policy selected for one immutable
// publication (Rust PublicationPolicy).
type PublicationPolicy uint8

const (
	PolicyFailIfExists PublicationPolicy = iota
	PolicyReplaceExisting
	PolicyReplaceExistingNoRollback
)

// CleanupState reports the attempt-artifact state after one publication
// attempt exactly like Rust CleanupState: Clean means either the artifact
// was provably removed or nothing needed removal (Rust outcome_unknown
// retains the private output as recovery residue and still reports
// Clean); ResiduePossible means removal was attempted but could not be
// proved.
type CleanupState uint8

const (
	CleanupStateClean CleanupState = iota
	CleanupStateResiduePossible
)

// PublicationStatus classifies one publication outcome (Rust
// PublicationStatus).
type PublicationStatus uint8

const (
	PublicationNotPublished PublicationStatus = iota
	PublicationPublished
	PublicationOutcomeUnknown
)

// DestinationContent describes the destination slot after one
// publication attempt (Rust DestinationContent; the Go port has no
// previous-content tracking, so Previous collapses into
// Unclassified).
type DestinationContent uint8

const (
	DestinationContentDesired DestinationContent = iota
	DestinationContentAbsent
	DestinationContentUnclassified
)

// PublicationPreparationFailure classes one failed publication before
// the destination provably held the output (Rust
// PublicationPreparationFailure). Cause carries the Rust-verbatim
// problem detail; Cleanup reports whether the attempt artifact was
// provably removed.
type PublicationPreparationFailure struct {
	Cause   error
	Cleanup CleanupState
}

func (f *PublicationPreparationFailure) Error() string {
	if f == nil {
		return "<nil>"
	}
	return "iprange v4 publication preparation: " + f.Cause.Error()
}

func (f *PublicationPreparationFailure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.Cause
}

// PublicationResult is the factual outcome of one publish call (Rust
// PublicationResult). A refusal or unprovable outcome still returns a
// result: Status and Cause classify it, and Cleanup reports the state
// of the attempt artifact exactly like Rust CleanupState (see above).
type PublicationResult struct {
	Status             PublicationStatus
	DestinationContent DestinationContent
	Cleanup            CleanupState
	Cause              error
}

// OutputAttempt is the identity of one staged private output (Rust
// OutputAttempt): the destination path, the captured directory
// identity, and the random attempt name the builder writes into.
type OutputAttempt struct {
	destination string
	attemptID   [16]byte
	name        string
	dirDevice   uint64
	dirInode    uint64
	// fileDevice/fileInode are the captured identity of the attempt file
	// (Rust bind/verify_name: cleanup discard is identity-guarded, so an
	// unlink can never remove a path that no longer names the file the
	// builder created).
	fileDevice uint64
	fileInode  uint64
	fileProven bool
}

// Destination returns the publication destination path.
func (a *OutputAttempt) Destination() string { return a.destination }

// AttemptID returns the random attempt identity (Rust
// publication_attempt_id, nonzero by construction).
func (a *OutputAttempt) AttemptID() [16]byte { return a.attemptID }

// Name returns the private attempt basename.
func (a *OutputAttempt) Name() string { return a.name }

// DirectoryIdentity returns the captured destination directory
// device+inode (Rust directory_identity).
func (a *OutputAttempt) DirectoryIdentity() (device uint64, inode uint64) {
	return a.dirDevice, a.dirInode
}

// SetFileIdentity records the attempt file's captured device+inode from
// the output builder's own descriptor (Rust CreatedOutput::create_with
// captures the identity at creation; verifyCustody refreshes it from a
// path probe before publish).
func (a *OutputAttempt) SetFileIdentity(device, inode uint64) {
	a.fileDevice = device
	a.fileInode = inode
	a.fileProven = true
}

// AttemptPath returns the attempt file path inside the destination
// directory.
func (a *OutputAttempt) AttemptPath() string {
	return filepath.Join(filepath.Dir(a.destination), a.name)
}

// Publication attempt naming (Rust publication/namespace.rs OUTPUT_
// PREFIX + artifact_name.rs write_attempt): a 128-bit nonzero attempt
// identity hex-encoded lowercase, private suffix .tmp.
const (
	attemptPrefix     = ".iprange-publish-"
	attemptHexChars   = 32
	attemptSuffix     = ".tmp"
	attemptNameLength = len(attemptPrefix) + attemptHexChars + len(attemptSuffix)
)

func attemptName(attemptID [16]byte) string {
	var name [attemptNameLength]byte
	copy(name[:], attemptPrefix)
	hex.Encode(name[len(attemptPrefix):len(attemptPrefix)+attemptHexChars], attemptID[:])
	copy(name[len(attemptPrefix)+attemptHexChars:], attemptSuffix)
	return string(name[:])
}

// invalidDestinationName mirrors Rust path::validate_main_name: one
// exact path component, never the reserved .iprange- prefix or the
// .readers coordination suffix (Rust "invalid destination name"). The
// reserved matches are byte-wise ASCII-case-insensitive (Rust
// eq_ignore_ascii_case); Unicode folding is not applied, so spellings
// Rust accepts (for example ".İPRANGE-" with the Turkish dotted I) are
// accepted here too.
func invalidDestinationName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') || strings.IndexByte(name, 0) >= 0 {
		return true
	}
	if format.AsciiFoldHasPrefix(name, format.ReservedBasenamePrefix) {
		return true
	}
	if format.AsciiFoldHasSuffix(name, format.CoordinationSuffix) {
		return true
	}
	return false
}

// destinationNameMax is the component-length bound of the targeted
// POSIX filesystems (Rust _PC_NAME_MAX: 255 on the supported linux,
// darwin, freebsd, and netbsd filesystems). The Windows stub refuses
// opens, so no Windows bound is needed.
const destinationNameMax = 255

// CreateAttempt validates the destination and names one publication
// attempt (Rust workflow::create + CreatedOutput::create_with):
// rollback-safe replacement requires the atomic name exchange, and the
// fail-if-exists policy requires the destination slot absent. The
// attempt file itself is created later by the output builder with
// O_EXCL over the random private name, so no attempt name can collide
// and every create failure is an early failure with nothing to clean.
func CreateAttempt(destination string, policy PublicationPolicy) (*OutputAttempt, error) {
	switch policy {
	case PolicyFailIfExists, PolicyReplaceExisting, PolicyReplaceExistingNoRollback:
	default:
		return nil, &format.Error{Code: format.CodeInvalidArgument, Detail: "publication policy is invalid"}
	}
	if policy == PolicyReplaceExisting && !mapping.ExchangeAvailable() {
		return nil, &format.Error{Code: format.CodeDurabilityUnsupported, Detail: "rollback-safe replacement requires atomic name exchange"}
	}
	clean := filepath.Clean(destination)
	name := filepath.Base(clean)
	if invalidDestinationName(name) {
		return nil, &format.Error{Code: format.CodeNameInvalid, Detail: "invalid destination name"}
	}
	// Rust Directory::require_name_lengths: the main component and its
	// .readers coordination twin must both fit the directory name bound;
	// an overlong basename refuses here with the name error instead of
	// failing at the rename with a generic IO class.
	if len(name) > destinationNameMax || len(name)+len(format.CoordinationSuffix) > destinationNameMax {
		return nil, &format.Error{Code: format.CodeNameInvalid, Detail: "invalid destination name"}
	}
	dir := filepath.Dir(clean)
	fi, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &format.Error{Code: format.CodeNameNotFound, Detail: "publication name is missing"}
		}
		return nil, &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
	}
	if !fi.IsDir() {
		return nil, &format.Error{Code: format.CodeConflict, Detail: "destination parent is not a directory"}
	}
	if policy == PolicyFailIfExists {
		// Rust require_fail_if_exists_available checks the main name
		// and its .readers coordination twin (namespace.rs
		// require_absent twice): a live sidecar or crash residue
		// occupies the destination slot under the fail-if-exists
		// contract, and the immutable reader refuses a published
		// database next to a sidecar. Replace policies never check
		// absence (Rust workflow::create uses create() for those).
		for _, candidate := range []string{clean, clean + format.CoordinationSuffix} {
			if _, err := os.Lstat(candidate); err == nil {
				return nil, &format.Error{Code: format.CodeNameExists, Detail: "publication name already exists"}
			} else if !os.IsNotExist(err) {
				return nil, &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
			}
		}
	}
	device, inode, err := mapping.StatIdentity(dir)
	if err != nil {
		return nil, err
	}
	attemptID, err := randomNonce()
	if err != nil {
		return nil, err
	}
	return &OutputAttempt{
		destination: clean,
		attemptID:   attemptID,
		name:        attemptName(attemptID),
		dirDevice:   device,
		dirInode:    inode,
	}, nil
}

// Discard removes an abandoned attempt file (Rust
// cleanup::discard_attempt/discard_created): exact-name unlink plus the
// retained-directory sync. The unlink is identity-guarded (Rust binds
// the cleanup to the captured identity): when the attempt identity was
// captured, a path that no longer names the created file - a replaced
// entry or a directory entry bound to a different inode - is left
// untouched. The link count is a custody proof, not a cleanup blocker:
// with the identity still bound, the exact-name unlink proceeds exactly
// like Rust. Clean is returned only when the attempt name provably no
// longer exists; any unprovable or failed step is ResiduePossible.
func (a *OutputAttempt) Discard() CleanupState {
	path := a.AttemptPath()
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return CleanupStateClean
		}
		return CleanupStateResiduePossible
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return CleanupStateResiduePossible
	}
	if a.fileProven {
		if device, inode, statErr := mapping.StatIdentity(path); statErr != nil || device != a.fileDevice || inode != a.fileInode {
			return CleanupStateResiduePossible
		}
	}
	if err := mapping.Unlink(path); err != nil {
		return CleanupStateResiduePossible
	}
	if err := mapping.SyncDirectory(filepath.Dir(a.destination)); err != nil {
		return CleanupStateResiduePossible
	}
	return CleanupStateClean
}

// Publish completes one staged publication (Rust workflow::publish):
// custody verify, SHA-512 digest over the finished mapping through
// mapped views only, finish sync, the policy rename, and the retained
// directory sync. Failures before the rename discard the attempt and
// return *PublicationPreparationFailure (Rust Early). A rename refusal
// or unprovable outcome returns a result: outcome_unknown retains the
// private attempt artifact (Rust recovery residue) and reports Clean;
// the fail-if-exists coordination twin refusal reports NotPublished and
// discards the attempt. All classifications mirror the Rust result
// surface (attempt.rs from_armed/not_published/outcome_unknown).
func Publish(attempt *OutputAttempt, b *OutputBuilder, policy PublicationPolicy) (*PublicationResult, error) {
	if !b.finished {
		return nil, &PublicationPreparationFailure{
			Cause:   &format.Error{Code: format.CodeWrongState, Detail: "output builder is not finished"},
			Cleanup: CleanupStateClean,
		}
	}
	fileDevice, fileInode, err := verifyCustody(attempt, policy)
	if err != nil {
		return nil, &PublicationPreparationFailure{Cause: err, Cleanup: attempt.Discard()}
	}
	byteLength, _, err := outputDigest(b.Mapping())
	if err != nil {
		return nil, &PublicationPreparationFailure{Cause: err, Cleanup: attempt.Discard()}
	}
	if err := b.Mapping().SyncFile(); err != nil {
		return nil, &PublicationPreparationFailure{Cause: err, Cleanup: attempt.Discard()}
	}
	if size := b.Mapping().Size(); size != byteLength {
		return nil, &PublicationPreparationFailure{
			Cause:   &format.Error{Code: format.CodeConflict, Detail: "finished output length changed"},
			Cleanup: attempt.Discard(),
		}
	}
	// Rust requires the fail-if-exists coordination twin and the main
	// name still absent at the reservation steps (reservation_file.rs
	// acquire + arm_with require_absent): a twin or a main appearing
	// between CreateAttempt and Publish is the NotPublished/NameExists
	// classification - the foreign file is preserved and the attempt is
	// discarded - never a publication next to a live sidecar and never
	// a rename-race outcome_unknown.
	if policy == PolicyFailIfExists {
		if _, statErr := os.Lstat(attempt.destination + format.CoordinationSuffix); statErr == nil {
			content := DestinationContentUnclassified
			if _, mainErr := os.Lstat(attempt.destination); os.IsNotExist(mainErr) {
				content = DestinationContentAbsent
			}
			return &PublicationResult{
				Status:             PublicationNotPublished,
				DestinationContent: content,
				Cleanup:            attempt.Discard(),
				Cause:              &format.Error{Code: format.CodeNameExists, Detail: "publication name already exists"},
			}, nil
		} else if !os.IsNotExist(statErr) {
			return nil, &PublicationPreparationFailure{Cause: statErr, Cleanup: attempt.Discard()}
		}
		// Rust arm_with require_absent(destination.main) (reservation_
		// file.rs): a main present before the arm is the NotPublished
		// classification with Unclassified content (attempt.rs
		// not_published: the foreign main is never read) and the
		// attempt discarded.
		if _, mainErr := os.Lstat(attempt.destination); mainErr == nil {
			return &PublicationResult{
				Status:             PublicationNotPublished,
				DestinationContent: DestinationContentUnclassified,
				Cleanup:            attempt.Discard(),
				Cause:              &format.Error{Code: format.CodeNameExists, Detail: "publication name already exists"},
			}, nil
		} else if !os.IsNotExist(mainErr) {
			return nil, &PublicationPreparationFailure{Cause: mainErr, Cleanup: attempt.Discard()}
		}
	}
	source := attempt.AttemptPath()
	switch policy {
	case PolicyFailIfExists:
		// Test-only fault point for the rename race window (Rust
		// from_armed): once the twin/main re-checks pass, a racing
		// destination turns the no-replace rename into the
		// outcome_unknown refusal handled below. Production builds
		// compile the fault to nothing.
		if ferr := fault.Fail("publish.fie_before_rename"); ferr != nil {
			err = ferr
		} else {
			err = mapping.RenameNoReplace(source, attempt.destination, fileDevice, fileInode)
		}
	case PolicyReplaceExisting:
		err = mapping.RenameExchange(source, attempt.destination)
	case PolicyReplaceExistingNoRollback:
		err = mapping.RenamePlain(source, attempt.destination)
	}
	if err != nil {
		// A target without the no-replace primitive refuses the first
		// namespace mutation with Unsupported (netbsd and windows; Rust
		// rename_noreplace Unsupported on non-linux/apple/freebsd).
		// That refusal is the acquire-failure classification: Rust
		// from_private returns Ok(not_published(...)) with both
		// artifacts discarded and the content computed from the main
		// slot at cleanup time (attempt.rs not_published) - a result,
		// never a preparation error and never retained residue. Every
		// other rename refusal before the destination provably held
		// the output is Rust outcome_unknown (attempt.rs from_armed:
		// !desired_proven keeps the private artifact as recovery
		// residue and reports CleanupState::Clean).
		var nsErr *format.Error
		if errors.As(err, &nsErr) && nsErr.Code == format.CodeOSUnsupported {
			content := DestinationContentUnclassified
			if _, mainErr := os.Lstat(attempt.destination); os.IsNotExist(mainErr) {
				content = DestinationContentAbsent
			}
			return &PublicationResult{
				Status:             PublicationNotPublished,
				DestinationContent: content,
				Cleanup:            attempt.Discard(),
				Cause:              err,
			}, nil
		}
		return &PublicationResult{
			Status:             PublicationOutcomeUnknown,
			DestinationContent: DestinationContentUnclassified,
			Cleanup:            CleanupStateClean,
			Cause:              err,
		}, nil
	}
	// The destination now holds the attempt inode: sync the retained
	// directory, then prove the destination names the published file
	// (Rust synchronize_main + prove_main).
	if err := mapping.SyncDirectory(filepath.Dir(attempt.destination)); err != nil {
		return &PublicationResult{
			Status:             PublicationOutcomeUnknown,
			DestinationContent: DestinationContentUnclassified,
			Cleanup:            CleanupStateClean,
			Cause:              err,
		}, nil
	}
	device, inode, err := mapping.StatIdentity(attempt.destination)
	if err != nil || device != fileDevice || inode != fileInode {
		return &PublicationResult{
			Status:             PublicationOutcomeUnknown,
			DestinationContent: DestinationContentUnclassified,
			Cleanup:            CleanupStateClean,
			Cause:              &format.Error{Code: format.CodeConflict, Detail: "publication inode identity changed"},
		}, nil
	}
	return &PublicationResult{
		Status:             PublicationPublished,
		DestinationContent: DestinationContentDesired,
		Cleanup:            CleanupStateClean,
	}, nil
}

// verifyCustody proves the attempt file is still the one staged in the
// captured destination namespace (Rust output.rs verify_custody +
// secure_created): the parent directory kept its identity, the attempt
// name names a regular symlink-free file on the same filesystem, and a
// replacement destination is not the attempt file itself (Rust
// replacement::bind SameIdentity).
func verifyCustody(attempt *OutputAttempt, policy PublicationPolicy) (device uint64, inode uint64, err error) {
	dir := filepath.Dir(attempt.destination)
	d, i, err := mapping.StatIdentity(dir)
	if err != nil {
		return 0, 0, err
	}
	if d != attempt.dirDevice || i != attempt.dirInode {
		return 0, 0, &format.Error{Code: format.CodeConflict, Detail: "publication inode identity changed"}
	}
	path := attempt.AttemptPath()
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, &format.Error{Code: format.CodeNameNotFound, Detail: "publication name is missing"}
		}
		return 0, 0, &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return 0, 0, &format.Error{Code: format.CodeConflict, Detail: "publication name is a symlink"}
	}
	if !fi.Mode().IsRegular() {
		return 0, 0, &format.Error{Code: format.CodeConflict, Detail: "publication name is not a regular file"}
	}
	device, inode, err = mapping.StatIdentity(path)
	if err != nil {
		return 0, 0, err
	}
	if device != attempt.dirDevice {
		return 0, 0, &format.Error{Code: format.CodePublicationUnsupported, Detail: "publication inode is on another filesystem"}
	}
	if policy != PolicyFailIfExists {
		// Rust replacement::bind refuses the coordination twin and the
		// missing destination BEFORE any rename (publication/replacement.rs
		// open(): require_absent(coordination), then open_regular(main)
		// or Missing): both are early preparation failures with the
		// attempt discarded.
		if _, twinErr := os.Lstat(attempt.destination + format.CoordinationSuffix); twinErr == nil {
			return 0, 0, &format.Error{Code: format.CodeNameExists, Detail: "publication name already exists"}
		} else if !os.IsNotExist(twinErr) {
			return 0, 0, &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
		}
		dfi, err := os.Lstat(attempt.destination)
		if err != nil {
			if os.IsNotExist(err) {
				return 0, 0, &format.Error{Code: format.CodeNameNotFound, Detail: "publication name is missing"}
			}
			return 0, 0, &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
		}
		if dfi.Mode()&os.ModeSymlink != 0 {
			return 0, 0, &format.Error{Code: format.CodeConflict, Detail: "publication name is a symlink"}
		}
		if !dfi.Mode().IsRegular() {
			return 0, 0, &format.Error{Code: format.CodeConflict, Detail: "publication name is not a regular file"}
		}
		if destDevice, destInode, err := mapping.StatIdentity(attempt.destination); err == nil &&
			destDevice == device && destInode == inode {
			return 0, 0, &format.Error{Code: format.CodeConflict, Detail: "replacement source and destination identities match"}
		}
	}
	// Rust verify_name requires the attempt file to be a regular file
	// with exactly one hard link (namespace.rs: link count is part of
	// the custody proof; cleanup discard is identity-guarded the same
	// way). A changed link count is a conflict class.
	if nlink, ok := regularLinkCount(fi); !ok || nlink != 1 {
		if ok && nlink == 0 {
			return 0, 0, &format.Error{Code: format.CodeConflict, Detail: "publication inode has no links"}
		}
		return 0, 0, &format.Error{Code: format.CodeConflict, Detail: "publication inode link count changed"}
	}
	// The attempt identity must still be the one captured from the
	// builder descriptor at creation (Rust verify_name compares the
	// probed identity to the expected one; a path swap is a conflict,
	// never a silent rebinding). Attempts created without a builder
	// capture the probe here so Discard is still identity-guarded.
	if !attempt.fileProven {
		attempt.SetFileIdentity(device, inode)
		return device, inode, nil
	}
	if device != attempt.fileDevice || inode != attempt.fileInode {
		return 0, 0, &format.Error{Code: format.CodeConflict, Detail: "publication inode identity changed"}
	}
	return device, inode, nil
}

// digestChunkSize is the fixed digest read span (Rust output_digest.rs
// DIGEST_BUFFER_SIZE): every read is a constant 1024-byte mapped span,
// copied into the owned chunk buffer below a complete page, then fed to
// the SHA-512 hasher. v4 outputs are page-aligned (a multiple of 4096),
// so the exact final chunk never splits and the constant span is always
// valid.
const digestChunkSize = 1024

// outputDigest computes the SHA-512 digest of the entire mapped output
// through mapped views only (Rust output_digest.rs digest): the file is
// a multiple of the chunk size by construction, and any mismatch is the
// finished-length-changed conflict.
func outputDigest(m *mapping.Mapping) (byteLength uint64, digest [64]byte, err error) {
	byteLength = m.Size()
	if byteLength%digestChunkSize != 0 {
		return 0, digest, &format.Error{Code: format.CodeConflict, Detail: "finished output length changed"}
	}
	var hasher = sha512.New()
	var buffer [digestChunkSize]byte
	for offset := uint64(0); offset < byteLength; offset += digestChunkSize {
		view, err := m.View(offset, digestChunkSize)
		if err != nil {
			return 0, digest, &format.Error{Code: format.CodeConflict, Detail: "finished output length changed"}
		}
		copy(buffer[:], view)
		hasher.Write(buffer[:])
	}
	copy(digest[:], hasher.Sum(nil))
	return byteLength, digest, nil
}
