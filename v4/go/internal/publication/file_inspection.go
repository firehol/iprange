// Stable, bounded inspection of publication output bytes (Rust
// publication/file_inspection.rs). The resolver inspects the main and
// private outputs without validating the whole file: the meta pair,
// byte length, and SHA-512 are compared with the reservation record,
// the output lifetime lock is held for the inspection, and custody is
// re-proved at the exact names. The gc-barrier availability call runs
// at the private position (windows arm in gc_barrier_windows.go).

package publication

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// outputContent classifies one inspected output against the
// reservation header (Rust file_inspection::Content).
type outputContent uint8

const (
	outputContentDesired outputContent = iota
	outputContentOther
)

// inspectedOutput is one inspected publication output with its
// lifetime lock held (Rust file_inspection::Inspected). Close
// releases the mapped view and the descriptor exactly where Rust
// drops the value.
type inspectedOutput struct {
	name       string
	file       *os.File
	mapping    *mapping.Mapping
	identity   live.FileIdentity
	meta       format.Meta
	byteLength uint64
	sha512     [64]byte
	content    outputContent
	access     AccessPolicy
	location   outputLocation
	attemptID  [16]byte
}

// Close releases the inspected output resources (Rust drop of
// Inspected; the mapping close also releases its duplicated
// descriptor).
func (o *inspectedOutput) Close() error {
	var first error
	if o.mapping != nil {
		if err := o.mapping.Close(); err != nil && first == nil {
			first = err
		}
	}
	if o.file != nil {
		if err := o.file.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// verify re-proves one inspected output exactly as inspected (Rust
// Inspected::verify): the geo facts must be unchanged and the name
// must still carry the retained inode after a directory proof.
func (o *inspectedOutput) verify(destination *destination) error {
	meta, byteLength, err := readBootstrap(o.file, o.mapping)
	if err != nil {
		return err
	}
	if meta != o.meta || byteLength != o.byteLength {
		return conflictProblem("publication output changed after inspection")
	}
	if err := destination.directory().Verify(); err != nil {
		return namespaceProblem(err)
	}
	if err := destination.directory().VerifyName(o.name, o.identity); err != nil {
		return namespaceProblem(err)
	}
	return nil
}

// inspectMainOutput inspects the main destination output (Rust
// file_inspection::main): the lifetime lock is shared because the
// main may be live-published by a concurrent writer.
func inspectMainOutput(destination *destination, header reservationHeader, check func() error) (*inspectedOutput, error) {
	regular, err := openMainRegular(destination, header)
	if err != nil {
		return nil, err
	}
	if regular == nil {
		return nil, nil
	}
	inspected, err := inspectOutput(destination, destination.mainName(), regular, header, outputLocationMain, false, check)
	if err != nil {
		_ = regular.File.Close()
		return nil, err
	}
	return inspected, nil
}

// inspectPrivateOutput opens the private output and refuses it
// unless it exactly matches the reservation record (Rust
// file_inspection::private_owned: require_exact_private; the
// resolved-main and abandon cleanups only discard an exact private
// output).
func inspectPrivateOutput(destination *destination, header reservationHeader, check func() error) (*inspectedOutput, error) {
	inspected, err := inspectPrivateOutputUnchecked(destination, header, check)
	if err != nil {
		return nil, err
	}
	if inspected == nil {
		return nil, nil
	}
	if err := requireExactPrivate(inspected, header); err != nil {
		_ = inspected.Close()
		return nil, err
	}
	return inspected, nil
}

// inspectPrivateOutputExact opens the private output and refuses it
// unless it is creator-only and exactly matches the reservation
// (Rust file_inspection::private: the completion path adds the
// creator-only access proof).
func inspectPrivateOutputExact(destination *destination, header reservationHeader, check func() error) (*inspectedOutput, error) {
	inspected, err := inspectPrivateOutput(destination, header, check)
	if err != nil {
		return nil, err
	}
	if inspected == nil {
		return nil, nil
	}
	if inspected.access != AccessPolicyCreatorOnly {
		_ = inspected.Close()
		return nil, conflictProblem("private publication output access no longer matches its reservation")
	}
	return inspected, nil
}

// inspectPrivateOutputUnchecked runs the exact inspection machine
// over the private output without the exact-record refusal (Rust
// private_owned without require_exact_private).
func inspectPrivateOutputUnchecked(destination *destination, header reservationHeader, check func() error) (*inspectedOutput, error) {
	name, err := destination.outputName(header.attemptID)
	if err != nil {
		return nil, err
	}
	regular, err := destination.directory().OpenRegular(name, true)
	if err != nil {
		return nil, err
	}
	if regular == nil {
		return nil, nil
	}
	inspected, err := inspectOutput(destination, name, regular, header, outputLocationPrivate, true, check)
	if err != nil {
		_ = regular.File.Close()
		return nil, err
	}
	return inspected, nil
}

// requireExactPrivate refuses one inspected private output whose
// inode or content no longer matches the reservation (Rust
// require_exact_private).
func requireExactPrivate(inspected *inspectedOutput, header reservationHeader) error {
	if reservationIdentityBytes(inspected.identity) != header.outputIdentity ||
		inspected.content != outputContentDesired {
		return conflictProblem("private publication output does not match its reservation")
	}
	return nil
}

// inspectOutput runs the exact inspection machine over one open
// output (Rust file_inspection::inspect; the gc-barrier availability
// call runs at the private position before the output lock, exactly
// like the Rust require_available).
func inspectOutput(destination *destination, name string, regular *live.RegularFile, header reservationHeader, location outputLocation, exclusive bool, check func() error) (*inspectedOutput, error) {
	if err := checkCancellation(check); err != nil {
		return nil, err
	}
	if location == outputLocationPrivate {
		if err := requireSourceAvailable(destination.directory(), header.attemptID, 0, ArtifactPrivateOutput, DirectoryRoleDestination, name, regular.Identity); err != nil {
			return nil, err
		}
	}
	if err := lockOutputFile(regular.File, exclusive, check); err != nil {
		return nil, err
	}
	if err := destination.directory().VerifyName(name, regular.Identity); err != nil {
		return nil, err
	}
	mapped, meta, byteLength, err := mapBootstrap(regular.File)
	if err != nil {
		return nil, err
	}
	sha512, err := digestCancellable(mapped, byteLength, check)
	if err != nil {
		_ = mapped.Close()
		return nil, outputProblem(err)
	}
	finalMeta, finalLength, err := readBootstrap(regular.File, mapped)
	if err != nil {
		_ = mapped.Close()
		return nil, err
	}
	if finalMeta != meta || finalLength != byteLength {
		_ = mapped.Close()
		return nil, conflictProblem("publication output changed while hashing")
	}
	if err := destination.directory().Verify(); err != nil {
		_ = mapped.Close()
		return nil, err
	}
	if err := destination.directory().VerifyName(name, regular.Identity); err != nil {
		_ = mapped.Close()
		return nil, err
	}
	return &inspectedOutput{
		name:       name,
		file:       regular.File,
		mapping:    mapped,
		identity:   regular.Identity,
		meta:       meta,
		byteLength: byteLength,
		sha512:     sha512,
		content:    classifyOutput(meta, byteLength, sha512, header),
		access:     classifyOutputAccess(regular, header),
		location:   location,
		attemptID:  header.attemptID,
	}, nil
}

// lockOutputFile takes the output lifetime lock cancellably (Rust
// file_inspection::lock_output; main outputs lock shared, the exact
// private output locks exclusive).
func lockOutputFile(file *os.File, exclusive bool, check func() error) error {
	mode := live.LockShared
	if exclusive {
		mode = live.LockExclusive
	}
	if err := live.LockFileCancellable(file, live.MainLifetimeOffset, mode, check); err != nil {
		return err
	}
	return checkCancellation(check)
}

// mapBootstrap maps the output read-only and reads its meta pair
// (Rust file_inspection::map_bootstrap: the geometry must be valid
// for the v4 format before the mapping is created).
func mapBootstrap(file *os.File) (*mapping.Mapping, format.Meta, uint64, error) {
	byteLength, err := fstatSize(file)
	if err != nil {
		return nil, format.Meta{}, 0, err
	}
	if !reservationGeometryValid(byteLength) {
		return nil, format.Meta{}, 0, conflictProblem("publication destination has invalid v4 file geometry")
	}
	mapped, err := mapping.MapFile(file, byteLength, false)
	if err != nil {
		return nil, format.Meta{}, 0, err
	}
	meta, _, err := readBootstrap(file, mapped)
	if err != nil {
		_ = mapped.Close()
		return nil, format.Meta{}, 0, err
	}
	return mapped, meta, byteLength, nil
}

// readBootstrap re-reads the meta pair and physical length and proves
// nothing changed (Rust file_inspection::read_bootstrap).
func readBootstrap(file *os.File, mapped *mapping.Mapping) (format.Meta, uint64, error) {
	byteLength, err := fstatSize(file)
	if err != nil {
		return format.Meta{}, 0, sdkProblem(err)
	}
	if byteLength != mapped.Size() {
		return format.Meta{}, 0, conflictProblem("publication destination changed while reading metadata")
	}
	page0, err := mapped.Page(0)
	if err != nil {
		return format.Meta{}, 0, sdkProblem(err)
	}
	page1, err := mapped.Page(1)
	if err != nil {
		return format.Meta{}, 0, sdkProblem(err)
	}
	meta, err := bootstrap.OpenMeta(page0, page1, byteLength, bootstrap.ModeImmutableReader)
	if err != nil {
		return format.Meta{}, 0, conflictProblem("publication destination is not a complete v4 file")
	}
	return meta, byteLength, nil
}

// classifyOutput compares one inspected output with the reservation
// header facts (Rust file_inspection::classify).
func classifyOutput(meta format.Meta, byteLength uint64, sha512 [64]byte, header reservationHeader) outputContent {
	if meta.DatabaseID == header.databaseID &&
		meta.TxnID == header.transactionID &&
		meta.CommitNonce == header.commitNonce &&
		byteLength == header.outputByteLength &&
		sha512 == header.outputSHA512 {
		return outputContentDesired
	}
	return outputContentOther
}

// classifyOutputAccess derives the creator-only policy of one
// inspected output (Rust file_inspection::classify_access).
func classifyOutputAccess(regular *live.RegularFile, header reservationHeader) AccessPolicy {
	commitment, err := security.CreatorOnlyCommitment(regular.File)
	if err == nil && commitment == header.securityCommitment {
		return AccessPolicyCreatorOnly
	}
	return AccessPolicyChangedOrUnproven
}
