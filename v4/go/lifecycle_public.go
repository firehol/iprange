// Public live-coordination lifecycle facade: CreateLive,
// InitializeLive, ResetLiveCoordination, and the three exact
// transition resolvers with the full Rust result surfaces (Rust
// live_writer::create_live and live_lifecycle::{initialize_live,
// reset_live_coordination, resolve_live_transition,
// resolve_create_live, resolve_interrupted_live_transition}). The
// public types mirror the Rust library API exactly: CreationState,
// LiveTransitionOperation/LiveResetPolicy/LiveTransitionStatus/
// LiveCoordinationLocation, LiveTransitionResolutionMode,
// LiveResidueKind/LiveResidueStatus/LiveResidueResult, Housekeeping,
// LocalBasename, and FileIdentity.

package iprangedb

import (
	"encoding/binary"

	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// LocalBasename is one platform basename copied without allocation
// (Rust live_writer::LocalBasename), bounded to the portable result
// bound of 512 bytes.
type LocalBasename struct {
	encoding uint16
	length   uint16
	bytes    [512]byte
}

// Encoding returns the platform encoding tag of the basename (1 =
// POSIX bytes).
func (b LocalBasename) Encoding() uint16 { return b.encoding }

// Bytes returns the basename content.
func (b LocalBasename) Bytes() []byte { return b.bytes[:b.length] }

// Housekeeping is the fact class of one attempted cleanup (Rust
// publication::Housekeeping). The type is the publication machine enum
// so the recovery terminal carries the same class.
type Housekeeping = publication.Housekeeping

const (
	HousekeepingNone                      = publication.HousekeepingNone
	HousekeepingCrashReappearancePossible = publication.HousekeepingCrashReappearancePossible
	HousekeepingVisible                   = publication.HousekeepingVisible
)

// HousekeepingArtifact is one ledger entry of the retirement machinery
// (Rust publication::HousekeepingArtifact). The POSIX live lifecycle
// never produces entries; the full field surface is the publication
// machine type, aliased here so the recovery terminal carries the same
// ledger shape.
type HousekeepingArtifact = publication.HousekeepingArtifact

// CreationState is the factual terminal state of one creation attempt
// (Rust live_writer::CreationState).
type CreationState uint8

const (
	CreationStateNotCreated CreationState = iota
	CreationStateCreated
	CreationStateOutcomeUnknown
)

// LiveTransitionOperation is one offline live-coordination operation
// (Rust live_lifecycle::LiveTransitionOperation).
type LiveTransitionOperation uint8

const (
	LiveTransitionInitialize LiveTransitionOperation = iota
	LiveTransitionReset
)

// LiveResetPolicy is the namespace guarantee selected for replacing
// existing live coordination (Rust LiveResetPolicy).
type LiveResetPolicy uint8

const (
	LiveResetRollbackSafe LiveResetPolicy = iota
	LiveResetDiscardPrevious
)

// LiveTransitionStatus is the factual state after one offline
// transition attempt (Rust LiveTransitionStatus).
type LiveTransitionStatus uint8

const (
	LiveTransitionStatusUnchanged LiveTransitionStatus = iota
	LiveTransitionStatusInitialized
	LiveTransitionStatusOutcomeUnknown
)

// LiveCoordinationLocation is the last proven location of the new
// coordination inode (Rust LiveCoordinationLocation).
type LiveCoordinationLocation uint8

const (
	LiveCoordinationLocationAbsent LiveCoordinationLocation = iota
	LiveCoordinationLocationCanonical
	LiveCoordinationLocationPrivate
	LiveCoordinationLocationUnclassified
)

// CreateResult is the identity and terminal state of one creation
// attempt (Rust live_writer::CreateResult). Identities are nil when the
// corresponding artifact was never created or no longer exists.
type CreateResult struct {
	Family              AddressFamily
	ValueKind           ValueKind
	StructureKind       StructureKind
	ValueTag            ValueTag
	DatabaseID          [16]byte
	CommitNonce         [16]byte
	SidecarID           [16]byte
	DirectoryIdentity   *FileIdentity
	MainBasename        LocalBasename
	MainIdentity        *FileIdentity
	SidecarIdentity     *FileIdentity
	ReaderCapacity      uint32
	State               CreationState
	ResiduePossible     bool
	Housekeeping        Housekeeping
	VisibleHousekeeping []HousekeepingArtifact
	Cause               error
}

// LiveTransitionResult is the exact facts retained for one transition
// attempt (Rust live_lifecycle::LiveTransitionResult).
type LiveTransitionResult struct {
	Operation               LiveTransitionOperation
	ResetPolicy             *LiveResetPolicy
	Status                  LiveTransitionStatus
	DatabaseID              [16]byte
	TransactionID           uint64
	CommitNonce             [16]byte
	DirectoryIdentity       *FileIdentity
	MainIdentity            *FileIdentity
	MainBasename            LocalBasename
	ReaderCapacity          uint32
	SidecarID               [16]byte
	PreviousSidecarIdentity *FileIdentity
	NewSidecarIdentity      *FileIdentity
	NewSidecarLocation      LiveCoordinationLocation
	ResiduePossible         bool
	Housekeeping            Housekeeping
	VisibleHousekeeping     []HousekeepingArtifact
	Cause                   error
}

// CreateLive creates one empty transaction-1 live database and reader
// table at path (Rust live_writer::create_live), with the sidecar-first
// ordering: the canonical .readers sidecar is reserved, initialized as
// creating, parent-synced, and the destination re-verified absent before
// the main file is created privately with creator-only access, written
// empty, parent-synced, and the sidecar published ready. cancellation,
// when non-nil, is checked between every bounded step. Capacity-zero,
// invalid-kind, and invalid destination arguments are hard errors;
// every later failure returns a CreateResult with the factual state.
func CreateLive(path string, family AddressFamily, kind ValueKind, structure StructureKind, tag ValueTag, readerCapacity uint32, cancellation *CancellationToken) (CreateResult, error) {
	created, err := live.CreateLive(path, uint8(family), uint8(kind), uint8(structure), tag.Wire(), readerCapacity, cancellation.check)
	if err != nil {
		return CreateResult{}, publicError(err)
	}
	return publicCreateResult(*created), nil
}

// InitializeLive converts one quiescent immutable database into a live
// database (Rust live_lifecycle::initialize_live): the main file is
// opened read-write and locked for its lifetime, its committed
// generation is proven with the exact committed length, the canonical
// .readers sidecar must be absent, and the sidecar is reserved and
// initialized to the ready state. cancellation, when non-nil, is
// checked between every bounded step. Capacity-zero and every
// open/proof failure are hard errors; later failures return a
// LiveTransitionResult with the factual state.
func InitializeLive(path string, readerCapacity uint32, cancellation *CancellationToken) (LiveTransitionResult, error) {
	transitioned, err := live.InitializeLive(path, readerCapacity, cancellation.check)
	if err != nil {
		return LiveTransitionResult{}, publicError(err)
	}
	return publicTransitionResult(*transitioned), nil
}

// publicCreateResult maps the internal creation facts to the public
// surface (Rust live_namespace::public_identity projections).
func publicCreateResult(created live.CreateResult) CreateResult {
	return CreateResult{
		Family:              AddressFamily(created.AddressFamily),
		ValueKind:           ValueKind(created.ValueKind),
		StructureKind:       StructureKind(created.StructureKind),
		ValueTag:            ValueTag{wire: created.ValueTag},
		DatabaseID:          created.DatabaseID,
		CommitNonce:         created.CommitNonce,
		SidecarID:           created.SidecarID,
		DirectoryIdentity:   publicIdentity(created.DirectoryIdentity),
		MainBasename:        publicBasename(created.MainBasename),
		MainIdentity:        publicIdentity(created.MainIdentity),
		SidecarIdentity:     publicIdentity(created.SidecarIdentity),
		ReaderCapacity:      created.ReaderCapacity,
		State:               CreationState(created.State),
		ResiduePossible:     created.ResiduePossible,
		Housekeeping:        Housekeeping(live.HousekeepingValue(created.Housekeeping)),
		VisibleHousekeeping: publicHousekeeping(created.VisibleHousekeeping),
		Cause:               publicError(created.Cause),
	}
}

// publicTransitionResult maps the internal transition facts to the
// public surface. The directory and main identities are always present
// on a transition result (Rust carries non-optional LocalFileIdentity
// values); the new-sidecar identity is absent when the sidecar was
// never created; the previous-sidecar identity is absent on initialize
// and present on reset.
func publicTransitionResult(result live.LiveTransitionResult) LiveTransitionResult {
	return LiveTransitionResult{
		Operation:               LiveTransitionOperation(result.Operation),
		ResetPolicy:             publicResetPolicy(result.ResetPolicy),
		Status:                  LiveTransitionStatus(result.Status),
		DatabaseID:              result.DatabaseID,
		TransactionID:           result.TransactionID,
		CommitNonce:             result.CommitNonce,
		DirectoryIdentity:       publicIdentity(result.DirectoryIdentity),
		MainIdentity:            publicIdentity(result.MainIdentity),
		MainBasename:            publicBasename(result.MainBasename),
		ReaderCapacity:          result.ReaderCapacity,
		SidecarID:               result.SidecarID,
		PreviousSidecarIdentity: publicIdentity(result.PreviousSidecarIdentity),
		NewSidecarIdentity:      publicIdentity(result.NewSidecarIdentity),
		NewSidecarLocation:      LiveCoordinationLocation(result.NewSidecarLocation),
		ResiduePossible:         result.ResiduePossible,
		Housekeeping:            Housekeeping(live.HousekeepingValue(result.Housekeeping)),
		VisibleHousekeeping:     publicHousekeeping(result.VisibleHousekeeping),
		Cause:                   publicError(result.Cause),
	}
}

// publicIdentity projects one retained identity as the portable
// device+inode pair with the POSIX kind tag (Rust
// live_namespace::public_identity + LocalFileIdentity encoding).
func publicIdentity(id *live.FileIdentity) *FileIdentity {
	if id == nil {
		return nil
	}
	device, inode := live.IdentityDeviceInode(id)
	var identity FileIdentity
	identity.Kind = 1
	binary.LittleEndian.PutUint64(identity.Bytes[0:8], device)
	binary.LittleEndian.PutUint64(identity.Bytes[8:16], inode)
	return &identity
}

// publicBasename copies the internal portable basename.
func publicBasename(b live.LocalBasename) LocalBasename {
	encoding, bytes := live.BasenameParts(b)
	var out LocalBasename
	out.encoding = encoding
	out.length = uint16(len(bytes))
	copy(out.bytes[:], bytes)
	return out
}

// publicHousekeeping maps the empty POSIX ledger (the artifact struct
// carries no fields; the copy is the same zero-filled slice shape).
func publicHousekeeping(artifacts []live.HousekeepingArtifact) []HousekeepingArtifact {
	if len(artifacts) == 0 {
		return nil
	}
	return make([]HousekeepingArtifact, len(artifacts))
}

// publicResetPolicy maps a reset-policy pointer; initialize carries
// nil (Rust Option<LiveResetPolicy>).
func publicResetPolicy(policy *live.LiveResetPolicy) *LiveResetPolicy {
	if policy == nil {
		return nil
	}
	out := LiveResetPolicy(*policy)
	return &out
}

// LiveTransitionResolutionMode is the requested terminal action for an
// exact interrupted transition (Rust
// live_lifecycle::LiveTransitionResolutionMode).
type LiveTransitionResolutionMode uint8

const (
	// LiveTransitionResolutionComplete finishes the interrupted
	// transition (Rust Complete).
	LiveTransitionResolutionComplete LiveTransitionResolutionMode = iota
	// LiveTransitionResolutionRollback removes the attempt artifacts
	// (Rust Rollback).
	LiveTransitionResolutionRollback
)

// LiveResidueKind is the location of an interrupted live-coordination
// artifact (Rust live_lifecycle::LiveResidueKind).
type LiveResidueKind uint8

const (
	// LiveResidueKindCanonical: the canonical .readers sidecar.
	LiveResidueKindCanonical LiveResidueKind = iota
	// LiveResidueKindPrivateReset: the private .readers.reset sidecar.
	LiveResidueKindPrivateReset
)

// LiveResidueStatus is the factual terminal state of resultless
// transition recovery (Rust live_lifecycle::LiveResidueStatus).
type LiveResidueStatus uint8

const (
	LiveResidueStatusAbsent LiveResidueStatus = iota
	LiveResidueStatusReady
	LiveResidueStatusCompleted
	LiveResidueStatusRemoved
	LiveResidueStatusOutcomeUnknown
)

// LiveResidueResult is the facts recovered directly from the retained
// main and sidecar of one interrupted transition (Rust
// live_lifecycle::LiveResidueResult). Facts are nil when the
// corresponding artifact does not exist or its header cannot be read.
type LiveResidueResult struct {
	Status              LiveResidueStatus
	Kind                *LiveResidueKind
	DatabaseID          *[16]byte
	SidecarID           *[16]byte
	ReaderCapacity      *uint32
	MainIdentity        *FileIdentity
	SidecarIdentity     *FileIdentity
	ResiduePossible     bool
	Housekeeping        Housekeeping
	VisibleHousekeeping []HousekeepingArtifact
	Cause               error
}

// ResetLiveCoordination replaces missing, corrupt, or obsolete live
// coordination while the main is quiescent (Rust
// live_lifecycle::reset_live_coordination): a fresh sidecar is prepared
// at the private .readers.reset name and installed at the canonical
// .readers name with the selected guarantee. RollbackSafe requires the
// atomic name exchange when existing coordination is replaced (the
// exchange is linux/apple only); DiscardPrevious replaces the previous
// sidecar without rollback. cancellation, when non-nil, is checked
// between every bounded step. Capacity-zero and every open/proof
// failure are hard errors; later failures return a LiveTransitionResult
// with the factual state.
func ResetLiveCoordination(path string, readerCapacity uint32, policy LiveResetPolicy, cancellation *CancellationToken) (LiveTransitionResult, error) {
	transitioned, err := live.ResetLiveCoordination(path, readerCapacity, live.LiveResetPolicy(policy), cancellation.check)
	if err != nil {
		return LiveTransitionResult{}, publicError(err)
	}
	return publicTransitionResult(*transitioned), nil
}

// ResolveLiveTransition resolves only the exact transition identified
// by supplied (Rust live_lifecycle::resolve_live_transition): the
// locked main must still match the supplied facts, the canonical and
// private coordination names are observed, and mode completes or rolls
// back the interrupted attempt. cancellation, when non-nil, is checked
// between every bounded step. Validation and open failures are hard
// errors; the resolution outcome is the factual LiveTransitionResult.
func ResolveLiveTransition(path string, supplied LiveTransitionResult, mode LiveTransitionResolutionMode, cancellation *CancellationToken) (LiveTransitionResult, error) {
	internal, err := live.ResolveLiveTransition(path, internalTransitionResult(supplied), live.LiveTransitionResolutionMode(mode), cancellation.check)
	if err != nil {
		return LiveTransitionResult{}, publicError(err)
	}
	return publicTransitionResult(*internal), nil
}

// ResolveCreateLive resolves only the exact creation attempt
// identified by supplied (Rust live_lifecycle::resolve_create_live):
// the main and canonical sidecar are observed under the lifetime lock,
// a ready pair short-circuits to Created, and mode completes the
// missing artifacts or removes the attempt identity-guarded.
// cancellation, when non-nil, is checked between every bounded step.
// Validation and open failures are hard errors; the resolution outcome
// is the factual CreateResult.
func ResolveCreateLive(path string, supplied CreateResult, mode LiveTransitionResolutionMode, cancellation *CancellationToken) (CreateResult, error) {
	resolved, err := live.ResolveCreateLive(path, internalCreateResult(supplied), live.LiveTransitionResolutionMode(mode), cancellation.check)
	if err != nil {
		return CreateResult{}, publicError(err)
	}
	return publicCreateResult(*resolved), nil
}

// ResolveInterruptedLiveTransition resolves one interrupted canonical
// create/initialize or private reset without the lost in-memory result
// (Rust live_lifecycle::resolve_interrupted_live_transition): the
// retained main is opened under the lifetime lock when present, the
// canonical and private coordination names are observed, and the
// artifact matrix is completed, retired, or reported ready per mode.
// cancellation, when non-nil, is checked between every bounded step.
func ResolveInterruptedLiveTransition(path string, mode LiveTransitionResolutionMode, cancellation *CancellationToken) (LiveResidueResult, error) {
	resolved, err := live.ResolveInterruptedLiveTransition(path, live.LiveTransitionResolutionMode(mode), cancellation.check)
	if err != nil {
		return LiveResidueResult{}, publicError(err)
	}
	return publicResidueResult(*resolved), nil
}

// publicResidueResult maps the internal residue facts to the public
// surface (Rust public projections; every optional fact stays a
// pointer).
func publicResidueResult(result live.LiveResidueResult) LiveResidueResult {
	return LiveResidueResult{
		Status:              LiveResidueStatus(result.Status),
		Kind:                publicResidueKind(result.Kind),
		DatabaseID:          result.DatabaseID,
		SidecarID:           result.SidecarID,
		ReaderCapacity:      result.ReaderCapacity,
		MainIdentity:        publicIdentity(result.MainIdentity),
		SidecarIdentity:     publicIdentity(result.SidecarIdentity),
		ResiduePossible:     result.ResiduePossible,
		Housekeeping:        Housekeeping(live.HousekeepingValue(result.Housekeeping)),
		VisibleHousekeeping: publicHousekeeping(result.VisibleHousekeeping),
		Cause:               publicError(result.Cause),
	}
}

// publicResidueKind maps the internal residue-kind pointer.
func publicResidueKind(kind *live.LiveResidueKind) *LiveResidueKind {
	if kind == nil {
		return nil
	}
	out := LiveResidueKind(*kind)
	return &out
}

// internalIdentity maps one public portable identity back to the
// internal retained identity (the inverse of publicIdentity; the
// resolver entry points receive their facts as public values).
func internalIdentity(id *FileIdentity) *live.FileIdentity {
	if id == nil {
		return nil
	}
	identity := live.IdentityFromDeviceInode(
		binary.LittleEndian.Uint64(id.Bytes[0:8]),
		binary.LittleEndian.Uint64(id.Bytes[8:16]),
	)
	return &identity
}

// internalBasename maps one public basename back to the internal form.
func internalBasename(b LocalBasename) live.LocalBasename {
	return live.BasenameFromParts(b.Encoding(), b.Bytes())
}

// internalCreateResult maps one public creation result back to the
// internal attempt facts (the inverse of publicCreateResult).
func internalCreateResult(result CreateResult) *live.CreateResult {
	return &live.CreateResult{
		AddressFamily:       uint8(result.Family),
		ValueKind:           uint8(result.ValueKind),
		StructureKind:       uint8(result.StructureKind),
		ValueTag:            result.ValueTag.Wire(),
		DatabaseID:          result.DatabaseID,
		CommitNonce:         result.CommitNonce,
		SidecarID:           result.SidecarID,
		DirectoryIdentity:   internalIdentity(result.DirectoryIdentity),
		MainBasename:        internalBasename(result.MainBasename),
		MainIdentity:        internalIdentity(result.MainIdentity),
		SidecarIdentity:     internalIdentity(result.SidecarIdentity),
		ReaderCapacity:      result.ReaderCapacity,
		State:               live.CreationState(result.State),
		ResiduePossible:     result.ResiduePossible,
		Housekeeping:        live.HousekeepingFromValue(uint8(result.Housekeeping)),
		VisibleHousekeeping: internalHousekeepingArtifacts(result.VisibleHousekeeping),
		Cause:               result.Cause,
	}
}

// internalHousekeepingArtifacts maps one public artifact ledger back to
// the internal form (the POSIX ledger is an empty-struct slice in both
// surfaces).
func internalHousekeepingArtifacts(artifacts []HousekeepingArtifact) []live.HousekeepingArtifact {
	if len(artifacts) == 0 {
		return nil
	}
	return make([]live.HousekeepingArtifact, len(artifacts))
}

// internalTransitionResult maps one public transition result back to
// the internal attempt facts (the inverse of publicTransitionResult).
func internalTransitionResult(result LiveTransitionResult) *live.LiveTransitionResult {
	return &live.LiveTransitionResult{
		Operation:               live.LiveTransitionOperation(result.Operation),
		ResetPolicy:             internalResetPolicy(result.ResetPolicy),
		Status:                  live.LiveTransitionStatus(result.Status),
		DatabaseID:              result.DatabaseID,
		TransactionID:           result.TransactionID,
		CommitNonce:             result.CommitNonce,
		DirectoryIdentity:       internalIdentity(result.DirectoryIdentity),
		MainIdentity:            internalIdentity(result.MainIdentity),
		MainBasename:            internalBasename(result.MainBasename),
		ReaderCapacity:          result.ReaderCapacity,
		SidecarID:               result.SidecarID,
		PreviousSidecarIdentity: internalIdentity(result.PreviousSidecarIdentity),
		NewSidecarIdentity:      internalIdentity(result.NewSidecarIdentity),
		NewSidecarLocation:      live.LiveCoordinationLocation(result.NewSidecarLocation),
		ResiduePossible:         result.ResiduePossible,
		Housekeeping:            live.HousekeepingFromValue(uint8(result.Housekeeping)),
		VisibleHousekeeping:     internalHousekeepingArtifacts(result.VisibleHousekeeping),
		Cause:                   result.Cause,
	}
}

// internalResetPolicy maps a public reset-policy pointer back to the
// internal form.
func internalResetPolicy(policy *LiveResetPolicy) *live.LiveResetPolicy {
	if policy == nil {
		return nil
	}
	out := live.LiveResetPolicy(*policy)
	return &out
}
