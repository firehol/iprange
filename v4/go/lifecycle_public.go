// Public live-coordination lifecycle facade: CreateLive and
// InitializeLive with the full Rust result surfaces (Rust
// live_writer::create_live and live_lifecycle::initialize_live). The
// public types mirror the Rust library API exactly: CreationState,
// LiveTransitionOperation/LiveResetPolicy/LiveTransitionStatus/
// LiveCoordinationLocation, Housekeeping, LocalBasename, and
// FileIdentity. Reset (live_lifecycle::reset_live_coordination) is a
// chunk 4-6 item and is not exposed yet; its enum values exist because
// the transition result carries the operation and reset policy.

package iprangedb

import (
	"encoding/binary"

	"github.com/firehol/iprange/v4/go/internal/live"
)

// FileIdentity is the exact local identity of one retained inode (Rust
// validation::LocalFileIdentity): the platform kind tag (1 = POSIX)
// and the encoded identity bytes (device little-endian, inode
// little-endian, zero padding).
type FileIdentity struct {
	Kind  uint16
	Bytes [32]byte
}

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
// publication::Housekeeping).
type Housekeeping uint8

const (
	HousekeepingNone Housekeeping = iota
	HousekeepingCrashReappearancePossible
	HousekeepingVisible
)

// HousekeepingArtifact is one ledger entry of the Windows retirement
// machinery (Rust publication::HousekeepingArtifact). The POSIX live
// lifecycle never produces entries; the full field surface lands with
// the publication resolver slice.
type HousekeepingArtifact struct{}

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
		return CreateResult{}, err
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
		return LiveTransitionResult{}, err
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
		Cause:               created.Cause,
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
		Cause:                   result.Cause,
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

// publicHousekeeping maps the empty POSIX ledger.
func publicHousekeeping(artifacts []live.HousekeepingArtifact) []HousekeepingArtifact {
	if len(artifacts) == 0 {
		return nil
	}
	out := make([]HousekeepingArtifact, len(artifacts))
	for i := range artifacts {
		out[i] = HousekeepingArtifact{}
	}
	return out
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
