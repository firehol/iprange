//go:build windows

// Idempotent Windows GC move classification and completion (Rust
// publication/gc/resolver.rs). The resolver observes the source and
// inert slots, moves the payload when the move is pending, proves the
// inert state, and best-effort unlinks the pair. Only the pure inert
// state is clean housekeeping; every other state reports the visible
// artifact with the exact problem class.

package live

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// gcObservation is one slot observation (Rust resolver::Observation).
type gcObservation struct {
	presence ArtifactPresence
	identity *FileIdentity
	exact    bool
}

// gcPairObservation is the classified source/inert pair (Rust
// resolver::PairObservation).
type gcPairObservation struct {
	source gcObservation
	inert  gcObservation
	state  HousekeepingState
}

// gcResolve resolves one retirement with the retained source handle
// (Rust gc::resolve).
func gcResolve(directory *Directory, authority gcAuthority, envelope *gcEnvelope) gcRetirement {
	return gcResolveEnvelope(directory, envelope, authority.sourceFile)
}

// gcResolveExisting resolves one retirement from the envelope alone
// (Rust gc::resolve_existing).
func gcResolveExisting(directory *Directory, envelope *gcEnvelope) gcRetirement {
	return gcResolveEnvelope(directory, envelope, nil)
}

func gcResolveEnvelope(directory *Directory, envelope *gcEnvelope, retainedSource *os.File) gcRetirement {
	observed := gcObservePair(directory, envelope)
	var moveProblem *format.Error
	if observed.state == HousekeepingMovePending {
		if err := gcMovePayload(directory, envelope, retainedSource); err != nil {
			moveProblem = gcProblemOf(err)
		}
		observed = gcObservePair(directory, envelope)
	}
	if observed.state != HousekeepingInert {
		problem := moveProblem
		if problem == nil {
			switch observed.state {
			case HousekeepingMovePending, HousekeepingMoveAmbiguous:
				problem = gcCleanupInProgress("GC payload move remains unresolved")
			case HousekeepingConflict, HousekeepingInert:
				problem = gcCleanupConflict("GC payload names or identities conflict")
			}
		}
		return gcVisibleRetirement(directory, envelope, observed, problem)
	}
	return gcFinishHousekeeping(directory, envelope)
}

// gcMovePayload renames the exact payload from the envelope-committed
// source name to the inert name (Rust resolver::move_payload): with a
// retained source handle the rename goes through the handle; without
// one the payload is re-opened and its identity and creator-only
// policy are re-proved first.
func gcMovePayload(directory *Directory, envelope *gcEnvelope, retainedSource *os.File) error {
	if retainedSource != nil {
		if err := directory.RenameNoReplace(envelope.sourceName, retainedSource, envelope.inertName); err != nil {
			return gcNamespaceProblem(err)
		}
		return nil
	}
	regular, err := directory.OpenRegular(envelope.sourceName, true)
	if err != nil {
		return gcNamespaceProblem(err)
	}
	if regular == nil {
		return gcCleanupConflict("GC source disappeared before its move")
	}
	expected, ok := gcEnvelopeIdentity(envelope)
	if !ok {
		regular.File.Close()
		return gcCleanupConflict("GC artifact identity is malformed")
	}
	commitment, err := security.CreatorOnlyCommitment(regular.File)
	if err != nil {
		regular.File.Close()
		return gcNamespaceProblem(err)
	}
	if regular.Identity != expected || commitment != envelope.header.creationSecurityCommit {
		regular.File.Close()
		return gcCleanupConflict("GC source identity or access policy changed")
	}
	if err := directory.RenameNoReplace(envelope.sourceName, regular.File, envelope.inertName); err != nil {
		regular.File.Close()
		return gcNamespaceProblem(err)
	}
	return nil
}

// gcFinishHousekeeping unlinks the inert payload and the envelope best
// effort and classifies the aftermath (Rust resolver::finish_housekeeping):
// the removal of the pair is not power-loss durable, so a fully absent
// result reports CrashReappearancePossible.
func gcFinishHousekeeping(directory *Directory, envelope *gcEnvelope) gcRetirement {
	identity, ok := gcEnvelopeIdentity(envelope)
	if !ok {
		observed := gcObservePair(directory, envelope)
		return gcVisibleRetirement(directory, envelope, observed, gcCleanupConflict("GC artifact identity is malformed"))
	}
	_, _ = directory.UnlinkExact(envelope.inertName, identity)
	afterPayload := gcObservePair(directory, envelope)
	if afterPayload.inert.presence == ArtifactAbsent {
		_, _ = directory.UnlinkExact(envelope.name, envelope.identity)
	}
	observed := gcObservePair(directory, envelope)
	envelopeAbsent := directory.RequireAbsent(envelope.name) == nil
	if observed.source.presence == ArtifactAbsent &&
		observed.inert.presence == ArtifactAbsent &&
		envelopeAbsent {
		return gcRetirement{
			problem:      nil,
			housekeeping: HousekeepingCrashReappearancePossible,
			visible:      nil,
		}
	}
	return gcVisibleRetirement(directory, envelope, observed, nil)
}

// gcObservePair classifies the source and inert slots against the
// envelope-committed artifact identity and security commitment (Rust
// resolver::observe_pair).
func gcObservePair(directory *Directory, envelope *gcEnvelope) gcPairObservation {
	expected, ok := gcDecodedIdentity(envelope.header.artifactIdentity)
	if !ok {
		return gcPairObservation{
			source: gcObservation{presence: ArtifactUnclassified},
			inert:  gcObservation{presence: ArtifactUnclassified},
			state:  HousekeepingConflict,
		}
	}
	securityValue := envelope.header.creationSecurityCommit
	source := gcObserve(directory, envelope.sourceName, expected, securityValue)
	inert := gcObserve(directory, envelope.inertName, expected, securityValue)
	return gcClassify(source, inert)
}

// gcClassify maps the pair to the housekeeping state (Rust
// resolver::classify): exact source with absent inert is MovePending,
// absent source with exact inert is Inert, both absent is
// MoveAmbiguous, everything else is Conflict.
func gcClassify(source, inert gcObservation) gcPairObservation {
	var state HousekeepingState
	switch {
	case source.exact && inert.presence == ArtifactAbsent:
		state = HousekeepingMovePending
	case source.presence == ArtifactAbsent && inert.exact:
		state = HousekeepingInert
	case source.presence == ArtifactAbsent && inert.presence == ArtifactAbsent:
		state = HousekeepingMoveAmbiguous
	default:
		state = HousekeepingConflict
	}
	return gcPairObservation{source: source, inert: inert, state: state}
}

// gcObserve classifies one slot (Rust resolver::observe): the entry
// must exist, be a regular single-link file with the expected identity,
// and prove its creator-only policy.
func gcObserve(directory *Directory, name string, expected FileIdentity, expectedSecurity [32]byte) gcObservation {
	entry, present, err := directory.Entry(name)
	if err != nil {
		return gcObservation{presence: ArtifactUnclassified}
	}
	if !present {
		return gcObservation{presence: ArtifactAbsent}
	}
	exact := entry.Regular && entry.Links == 1 && entry.Identity == expected
	if exact {
		regular, openErr := directory.OpenRegular(name, false)
		if openErr != nil || regular == nil {
			exact = false
		} else {
			commitment, commitErr := security.CreatorOnlyCommitment(regular.File)
			regular.File.Close()
			if commitErr != nil {
				exact = false
			} else {
				exact = regular.Identity == expected && commitment == expectedSecurity
			}
		}
	}
	identity := entry.Identity
	return gcObservation{
		presence: ArtifactPresent,
		identity: &identity,
		exact:    exact,
	}
}

// gcVisibleRetirement builds the visible retirement facts (Rust
// resolver::visible_retirement).
func gcVisibleRetirement(directory *Directory, envelope *gcEnvelope, observed gcPairObservation, problem *format.Error) gcRetirement {
	artifact := gcArtifact(directory.Identity(), envelope, observed)
	return gcRetirement{
		problem:      problem,
		housekeeping: HousekeepingVisible,
		visible:      &artifact,
	}
}

// gcFailed builds the failure retirement with the optional pending
// artifact (Rust resolver::failed).
func gcFailed(directory *Directory, authority *gcAuthority, envelopeName, inertName string, problem *format.Error) gcRetirement {
	if envelopeName == "" {
		return gcRetirement{problem: problem, housekeeping: HousekeepingNone}
	}
	entry, present, err := directory.Entry(envelopeName)
	if err != nil || !present {
		return gcRetirement{problem: problem, housekeeping: HousekeepingNone}
	}
	if inertName == "" {
		inertName = envelopeName
	}
	return gcRetirement{
		problem:      problem,
		housekeeping: HousekeepingVisible,
		visible:      gcPendingArtifact(directory, authority, envelopeName, entry.Identity, inertName),
	}
}

// gcPendingArtifact builds the visible artifact of one checkpoint or
// failure (Rust resolver::pending_artifact).
func gcPendingArtifact(directory *Directory, authority *gcAuthority, envelopeName string, envelopeIdentity FileIdentity, inertName string) *HousekeepingArtifact {
	source := gcObserve(directory, authority.sourceName, authority.identity, authority.creationSecurity.Commitment)
	inert := gcObserve(directory, inertName, authority.identity, authority.creationSecurity.Commitment)
	artifact := gcFailedArtifact(directory.Identity(), authority, envelopeName, envelopeIdentity, inertName, gcClassify(source, inert))
	return &artifact
}

// gcArtifact builds the ledger artifact of one classified pair (Rust
// resolver::artifact).
func gcArtifact(directoryIdentity FileIdentity, envelope *gcEnvelope, observed gcPairObservation) HousekeepingArtifact {
	dirIdentity := LocalFileIdentityFromDeviceInode(directoryIdentity.device, directoryIdentity.inode)
	return HousekeepingArtifact{
		State:             observed.state,
		DirectoryRole:     envelope.header.directoryRole,
		DirectoryIdentity: dirIdentity,
		BasenameEncoding:  envelope.header.basenameEncoding,
		AttemptID:         envelope.header.attemptID,
		Ordinal:           envelope.header.ordinal,
		EnvelopeBasename:  gcNameBytes(envelope.name),
		EnvelopeIdentity:  LocalFileIdentityFromDeviceInode(envelope.identity.device, envelope.identity.inode),
		SourceBasename:    gcNameBytes(envelope.sourceName),
		InertBasename:     gcNameBytes(envelope.inertName),
		SourcePresence:    observed.source.presence,
		SourceIdentity:    gcLocalIdentityOptional(observed.source.identity),
		InertPresence:     observed.inert.presence,
		InertIdentity:     gcLocalIdentityOptional(observed.inert.identity),
		Kind:              envelope.header.kind,
		CreationSecurity: CreationSecurity{
			Kind:       envelope.header.creationSecurityKind,
			Commitment: envelope.header.creationSecurityCommit,
		},
		SelectedEnvelopeSequence: envelope.header.sequence,
	}
}

// gcFailedArtifact builds the ledger artifact of one pending or failed
// authority (Rust resolver::failed_artifact; the sequence is zero
// because no block was selected).
func gcFailedArtifact(directoryIdentity FileIdentity, authority *gcAuthority, envelopeName string, envelopeIdentity FileIdentity, inertName string, observed gcPairObservation) HousekeepingArtifact {
	dirIdentity := LocalFileIdentityFromDeviceInode(directoryIdentity.device, directoryIdentity.inode)
	return HousekeepingArtifact{
		State:                    observed.state,
		DirectoryRole:            authority.directoryRole,
		DirectoryIdentity:        dirIdentity,
		BasenameEncoding:         uint16(gcBasenameEncodingValue()),
		AttemptID:                authority.attemptID,
		Ordinal:                  authority.ordinal,
		EnvelopeBasename:         gcNameBytes(envelopeName),
		EnvelopeIdentity:         LocalFileIdentityFromDeviceInode(envelopeIdentity.device, envelopeIdentity.inode),
		SourceBasename:           gcNameBytes(authority.sourceName),
		InertBasename:            gcNameBytes(inertName),
		SourcePresence:           observed.source.presence,
		SourceIdentity:           gcLocalIdentityOptional(observed.source.identity),
		InertPresence:            observed.inert.presence,
		InertIdentity:            gcLocalIdentityOptional(observed.inert.identity),
		Kind:                     authority.kind,
		CreationSecurity:         authority.creationSecurity,
		SelectedEnvelopeSequence: 0,
	}
}

// gcEnvelopeIdentity decodes the envelope-committed artifact identity
// (Rust envelope_identity).
func gcEnvelopeIdentity(envelope *gcEnvelope) (FileIdentity, bool) {
	return gcDecodedIdentity(envelope.header.artifactIdentity)
}

// gcDecodedIdentity decodes the committed identity bytes to the live
// pair (Rust Identity::decode).
func gcDecodedIdentity(bytes [32]byte) (FileIdentity, bool) {
	portable := LocalFileIdentity{Kind: identityKind, Bytes: bytes}
	device, inode, ok := portable.DeviceInode()
	if !ok {
		return FileIdentity{}, false
	}
	return FileIdentity{device: device, inode: inode}, true
}

// gcLocalIdentityOptional projects an optional live identity to the
// portable form (Rust local identity projection).
func gcLocalIdentityOptional(identity *FileIdentity) *LocalFileIdentity {
	if identity == nil {
		return nil
	}
	value := LocalFileIdentityFromDeviceInode(identity.device, identity.inode)
	return &value
}
