//go:build windows

// Windows correctness cleanup through one authenticated inert name
// (Rust publication/gc.rs + gc_barrier.rs, spec 14.4.1). Once an
// operation decides to retire an exact retained inode, cleanup
// classifies its artifact kind and authoritative basename, exclusively
// creates the attempt-bound 8,192-byte GC authority envelope in the
// retained directory, synchronizes it, and only then moves the payload
// to the inert GC name. The selected envelope is immutable and every
// ordinary open checks it through the barrier (requireAvailable)
// without scanning the directory.

package live

import (
	"errors"
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/random"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// gcAuthority identifies one retired artifact for the GC machine
// (Rust gc::Authority): the attempt identity, ordinal, artifact kind,
// directory role, exact source component and handle, local identity,
// creator-only security evidence, and the optional content payload.
type gcAuthority struct {
	attemptID        [16]byte
	ordinal          uint32
	kind             ArtifactKind
	directoryRole    DirectoryRole
	sourceName       string
	sourceFile       *os.File
	identity         FileIdentity
	creationSecurity CreationSecurity
	payload          *gcPayload
}

// gcRetirement is the factual outcome of one GC retirement (Rust
// gc::Retirement).
type gcRetirement struct {
	problem      *format.Error
	housekeeping Housekeeping
	visible      *HousekeepingArtifact
}

// gcResumeAuthority is the expected identity of one resumed envelope
// (Rust gc::ResumeAuthority; no source handle or security profile: the
// envelope is the authority).
type gcResumeAuthority struct {
	attemptID     [16]byte
	ordinal       uint32
	kind          ArtifactKind
	directoryRole DirectoryRole
	sourceName    string
	identity      FileIdentity
	payload       *gcPayload
}

// gcRetire retires one exact artifact with no housekeeping observer
// (Rust gc::retire).
func gcRetire(directory *Directory, authority gcAuthority) gcRetirement {
	return gcRetireWith(directory, authority, false, func(*HousekeepingArtifact) error { return nil })
}

// gcRetireObserved retires one exact artifact and streams the pending
// housekeeping artifact to the observer before any move (Rust
// gc::retire_observed).
func gcRetireObserved(directory *Directory, authority gcAuthority, observer func(*HousekeepingArtifact) error) gcRetirement {
	return gcRetireWith(directory, authority, true, observer)
}

func gcRetireWith(directory *Directory, authority gcAuthority, observe bool, observer func(*HousekeepingArtifact) error) gcRetirement {
	envelopeName, err := gcEnvelopeName(authority.attemptID, authority.ordinal)
	if err != nil {
		return gcFailed(directory, &authority, "", "", gcNamespaceProblem(err))
	}
	inertName, err := gcInertName(authority.attemptID, authority.ordinal)
	if err != nil {
		return gcFailed(directory, &authority, envelopeName, "", gcNamespaceProblem(err))
	}
	envelope, failure := gcLoadOrCreate(directory, &authority, envelopeName, inertName, observe, observer)
	if failure != nil {
		return gcFailed(directory, &authority, failure.envelopeName, failure.inertName, failure.problem)
	}
	return gcResolve(directory, authority, envelope)
}

// envelopeFailure carries the exact names of a failed envelope load or
// creation (Rust gc::EnvelopeFailure).
type envelopeFailure struct {
	envelopeName string
	inertName    string
	problem      *format.Error
}

// gcResume resumes one abandoned retirement through its envelope (Rust
// gc::resume): nil means no envelope exists.
func gcResume(directory *Directory, expected gcResumeAuthority) (*gcRetirement, *format.Error) {
	envelopeName, err := gcEnvelopeName(expected.attemptID, expected.ordinal)
	if err != nil {
		return nil, gcNamespaceProblem(err)
	}
	envelope, openErr := gcOpenAs(directory, envelopeName, true, expected.kind)
	if openErr != nil {
		return nil, openErr
	}
	if envelope == nil {
		return nil, nil
	}
	header := envelope.header
	if header.attemptID != expected.attemptID ||
		header.ordinal != expected.ordinal ||
		header.kind != expected.kind ||
		header.directoryRole != expected.directoryRole ||
		!gcSourceEncodedMatches(envelope.sourceEncoded, expected.sourceName) ||
		header.artifactIdentity != gcEncodeIdentity(expected.identity) ||
		!gcPayloadEqual(header.payload, expected.payload) {
		return nil, gcCleanupConflict("GC authority does not match the abandoned artifact")
	}
	retired := gcResolveExisting(directory, envelope)
	return &retired, nil
}

// gcRequireSourceAvailable proves one retained source is not owned by
// Windows housekeeping (Rust gc::require_source_available): a matching
// selected envelope means cleanup owns the inode and the ordinary
// operation must fail with CleanupInProgress; a mismatched envelope is
// CleanupConflict; absence permits the normal operation record.
func gcRequireSourceAvailable(directory *Directory, attemptID [16]byte, ordinal uint32, kind ArtifactKind, directoryRole DirectoryRole, sourceName string, identity FileIdentity) *format.Error {
	envelopeName, err := gcEnvelopeName(attemptID, ordinal)
	if err != nil {
		return gcNamespaceProblem(err)
	}
	envelope, openErr := gcOpenAs(directory, envelopeName, false, kind)
	if openErr != nil {
		return openErr
	}
	if envelope == nil {
		return nil
	}
	header := envelope.header
	if header.attemptID != attemptID ||
		header.ordinal != ordinal ||
		header.kind != kind ||
		header.directoryRole != directoryRole ||
		!gcSourceEncodedMatches(envelope.sourceEncoded, sourceName) ||
		header.artifactIdentity != gcEncodeIdentity(identity) {
		return gcCleanupConflict("GC authority conflicts with the retained source")
	}
	return gcCleanupInProgress("retained source is owned by Windows housekeeping")
}

// gcFreshAttempt draws one collision-free attempt identity for an
// exact source (Rust gc::fresh_attempt): the source must be unclaimed
// by any existing envelope, and both the envelope and inert names must
// be absent for the drawn identity.
func gcFreshAttempt(directory *Directory, sourceName string, identity FileIdentity, ordinal uint32, kind ArtifactKind, directoryRole DirectoryRole) ([16]byte, *format.Error) {
	if err := gcRequireUnclaimedSource(directory, sourceName, identity, kind, directoryRole); err != nil {
		return [16]byte{}, err
	}
	for {
		attempt, err := random.Nonzero128()
		if err != nil {
			return [16]byte{}, gcSdkProblem(err)
		}
		envelopeName, err := gcEnvelopeName(attempt, ordinal)
		if err != nil {
			return [16]byte{}, gcNamespaceProblem(err)
		}
		inertName, err := gcInertName(attempt, ordinal)
		if err != nil {
			return [16]byte{}, gcNamespaceProblem(err)
		}
		envelopeErr := directory.RequireAbsent(envelopeName)
		inertErr := directory.RequireAbsent(inertName)
		if envelopeErr == nil && inertErr == nil {
			return attempt, nil
		}
		if isNamespaceExists(envelopeErr) || isNamespaceExists(inertErr) {
			continue
		}
		if envelopeErr != nil {
			return [16]byte{}, gcNamespaceProblem(envelopeErr)
		}
		return [16]byte{}, gcNamespaceProblem(inertErr)
	}
}

// gcRequireUnclaimedSource scans the directory for any envelope
// claiming the exact source (Rust gc::require_unclaimed_source): zero
// claims permits, one claim means cleanup in progress, and a duplicate
// or conflicting claim is CleanupConflict.
func gcRequireUnclaimedSource(directory *Directory, sourceName string, identity FileIdentity, kind ArtifactKind, directoryRole DirectoryRole) *format.Error {
	exactClaims := 0
	scanErr := directory.Scan(func(bytes []byte) error {
		candidate := gcCandidateOf(bytes)
		if !candidate.envelope || !candidate.decoded {
			return nil
		}
		name, err := gcEnvelopeName(candidate.attempt, candidate.ordinal)
		if err != nil {
			return gcNamespaceProblem(err)
		}
		envelope, err := gcOpenAs(directory, name, false, kind)
		if err != nil {
			return err
		}
		if envelope == nil {
			return nil
		}
		if !gcSourceEncodedMatches(envelope.sourceEncoded, sourceName) {
			return nil
		}
		if envelope.header.artifactIdentity != gcEncodeIdentity(identity) ||
			envelope.header.kind != kind ||
			envelope.header.directoryRole != directoryRole {
			return gcCleanupConflict("GC authority conflicts with the retained source")
		}
		exactClaims++
		if exactClaims > 1 {
			return gcCleanupConflict("duplicate GC source authority")
		}
		return nil
	})
	if scanErr != nil {
		var fe *format.Error
		if errors.As(scanErr, &fe) {
			return fe
		}
		return gcSdkProblem(scanErr)
	}
	switch exactClaims {
	case 0:
		return nil
	case 1:
		return gcCleanupInProgress("retained source is owned by Windows housekeeping")
	}
	return gcCleanupConflict("duplicate GC source authority")
}

// gcEnvelope is one loaded GC authority envelope (Rust gc::Envelope).
// sourceEncoded carries the stored basename in the declared encoding
// (raw bytes on unix, UTF-16LE units on Windows); sourceName is its
// ASCII projection for the Go name machine.
type gcEnvelope struct {
	name          string
	sourceEncoded []byte
	sourceName    string
	inertName     string
	identity      FileIdentity
	header        *gcHeader
}

// gcLoadOrCreate opens an existing envelope and proves the authority,
// or creates the pair when no envelope exists (Rust gc::load_or_create:
// every failure returns the exact names for the failed() artifact).
func gcLoadOrCreate(directory *Directory, authority *gcAuthority, envelopeName, inertName string, observe bool, observer func(*HousekeepingArtifact) error) (*gcEnvelope, *envelopeFailure) {
	regular, err := directory.OpenRegular(envelopeName, true)
	if err != nil {
		return nil, &envelopeFailure{envelopeName: envelopeName, inertName: inertName, problem: gcNamespaceProblem(err)}
	}
	if regular != nil {
		if err := gcCheckpointEnvelope(directory, authority, envelopeName, regular.Identity, inertName, observe, observer); err != nil {
			return nil, &envelopeFailure{envelopeName: envelopeName, inertName: inertName, problem: err}
		}
		envelope, err := gcLoad(directory, envelopeName, regular.File, regular.Identity, authority.kind, true)
		if err != nil {
			return nil, &envelopeFailure{envelopeName: envelopeName, inertName: inertName, problem: err}
		}
		if err := gcVerifyAuthority(directory, authority, envelope); err != nil {
			return nil, &envelopeFailure{envelopeName: envelopeName, inertName: inertName, problem: err}
		}
		return envelope, nil
	}
	envelope, createErr := gcCreate(directory, authority, envelopeName, inertName, observe, observer)
	if createErr != nil {
		return nil, &envelopeFailure{envelopeName: envelopeName, inertName: inertName, problem: createErr}
	}
	return envelope, nil
}

// gcCreate exclusively creates the envelope pair, encodes two
// sequence-1 blocks, synchronizes the file and directory, and proves
// the authority before any payload move (Rust gc::create).
func gcCreate(directory *Directory, authority *gcAuthority, envelopeName, inertName string, observe bool, observer func(*HousekeepingArtifact) error) (*gcEnvelope, *format.Error) {
	if err := gcVerifySource(directory, authority); err != nil {
		return nil, err
	}
	if err := directory.RequireAbsent(inertName); err != nil {
		return nil, gcNamespaceProblem(err)
	}
	profile, err := security.Capture()
	if err != nil {
		return nil, gcNamespaceProblem(err)
	}
	if authority.creationSecurity.Kind != gcCreationSecurityKind() ||
		authority.creationSecurity.Commitment != profile.Commitment() {
		return nil, gcCleanupConflict("GC source access policy no longer matches the effective user")
	}
	file, err := directory.CreateSecured(envelopeName, profile)
	if err != nil {
		return nil, gcNamespaceProblem(err)
	}
	identity, err := RegularIdentity(file, directory.Identity())
	if err != nil {
		file.Close()
		return nil, gcNamespaceProblem(err)
	}
	if err := security.SecureCreatorOnly(file, profile); err != nil {
		file.Close()
		return nil, gcNamespaceProblem(err)
	}
	if err := gcCheckpointEnvelope(directory, authority, envelopeName, identity, inertName, observe, observer); err != nil {
		file.Close()
		return nil, err
	}
	header := gcHeaderOf(directory, authority, inertName)
	if err := file.Truncate(gcEnvelopeSize); err != nil {
		file.Close()
		return nil, gcSdkProblem(err)
	}
	m, err := mapping.MapFile(file, gcEnvelopeSize, true)
	if err != nil {
		file.Close()
		return nil, gcSdkProblem(err)
	}
	enterErr := gcProbe(m, authority.kind)
	if enterErr != nil {
		_ = m.Close()
		file.Close()
		return nil, gcSdkProblem(enterErr)
	}
	block0, err := m.Page(0)
	if err != nil {
		_ = m.Close()
		file.Close()
		return nil, gcSdkProblem(err)
	}
	if err := header.gcEncode(block0); err != nil {
		_ = m.Close()
		file.Close()
		return nil, gcSdkProblem(err)
	}
	block1, err := m.Page(1)
	if err != nil {
		_ = m.Close()
		file.Close()
		return nil, gcSdkProblem(err)
	}
	if err := header.gcEncode(block1); err != nil {
		_ = m.Close()
		file.Close()
		return nil, gcSdkProblem(err)
	}
	if err := m.FlushRange(0, gcEnvelopeSize); err != nil {
		_ = m.Close()
		file.Close()
		return nil, gcSdkProblem(err)
	}
	if err := SyncFile(file); err != nil {
		_ = m.Close()
		file.Close()
		return nil, gcSdkProblem(err)
	}
	_ = m.Close()
	if err := directory.Sync(); err != nil {
		file.Close()
		return nil, gcNamespaceProblem(err)
	}
	if err := directory.Verify(); err != nil {
		file.Close()
		return nil, gcNamespaceProblem(err)
	}
	envelope, loadErr := gcLoad(directory, envelopeName, file, identity, authority.kind, true)
	if loadErr != nil {
		file.Close()
		return nil, loadErr
	}
	if err := gcVerifyAuthority(directory, authority, envelope); err != nil {
		file.Close()
		return nil, err
	}
	if envelope.inertName != inertName {
		file.Close()
		return nil, gcCleanupConflict("GC inert name changed during envelope creation")
	}
	return envelope, nil
}

// gcCheckpointEnvelope streams the pending artifact to the observer
// (Rust gc::checkpoint_envelope): the artifact is visible only when
// observation is enabled.
func gcCheckpointEnvelope(directory *Directory, authority *gcAuthority, envelopeName string, envelopeIdentity FileIdentity, inertName string, enabled bool, observer func(*HousekeepingArtifact) error) *format.Error {
	if !enabled {
		return nil
	}
	artifact := gcPendingArtifact(directory, authority, envelopeName, envelopeIdentity, inertName)
	if err := observer(artifact); err != nil {
		return gcObserverProblem(err)
	}
	return nil
}

// gcObserverProblem folds one observer failure (Rust the observer's
// Result<(), Problem> is the direct problem).
func gcObserverProblem(err error) *format.Error {
	var fe *format.Error
	if errors.As(err, &fe) {
		return fe
	}
	return gcSdkProblem(err)
}

// gcOpen opens one envelope without a kind expectation (Rust gc::open).
func gcOpen(directory *Directory, envelopeName string, writable bool) (*gcEnvelope, *format.Error) {
	regular, err := directory.OpenRegular(envelopeName, writable)
	if err != nil {
		return nil, gcNamespaceProblem(err)
	}
	if regular == nil {
		return nil, nil
	}
	return gcLoad(directory, envelopeName, regular.File, regular.Identity, ArtifactKind(0), false)
}

// gcOpenAs opens one envelope and requires it to decode under the
// artifact kind (Rust gc::open_as).
func gcOpenAs(directory *Directory, envelopeName string, writable bool, kind ArtifactKind) (*gcEnvelope, *format.Error) {
	regular, err := directory.OpenRegular(envelopeName, writable)
	if err != nil {
		return nil, gcNamespaceProblem(err)
	}
	if regular == nil {
		return nil, nil
	}
	return gcLoad(directory, envelopeName, regular.File, regular.Identity, kind, true)
}

// gcLoad loads and proves one envelope file (Rust gc::load): the name
// and identity must match the directory, the length must be exact, the
// selected header must decode under the expected kind, and the record
// must be coherent with the names.
func gcLoad(directory *Directory, envelopeName string, file *os.File, identity FileIdentity, kind ArtifactKind, expectKind bool) (*gcEnvelope, *format.Error) {
	if err := directory.VerifyName(envelopeName, identity); err != nil {
		return nil, gcNamespaceProblem(err)
	}
	length, err := gcFileSize(file)
	if err != nil {
		return nil, gcSdkProblem(err)
	}
	if length != gcEnvelopeSize {
		return nil, gcCleanupConflict("GC authority envelope has the wrong length")
	}
	m, err := mapping.MapFile(file, gcEnvelopeSize, false)
	if err != nil {
		return nil, gcSdkProblem(err)
	}
	if expectKind {
		if err := gcProbe(m, kind); err != nil {
			_ = m.Close()
			return nil, gcSdkProblem(err)
		}
	}
	bytes, err := m.View(0, gcEnvelopeSize)
	if err != nil {
		_ = m.Close()
		return nil, gcSdkProblem(err)
	}
	header, err := gcSelect(bytes)
	if err != nil {
		_ = m.Close()
		return nil, gcCleanupConflict("GC authority envelope is not selectable")
	}
	attemptID, ordinal, ok := gcDecodeEnvelope([]byte(envelopeName))
	if !ok {
		_ = m.Close()
		return nil, gcCleanupConflict("GC envelope name is not canonical")
	}
	inertName, err := gcInertName(attemptID, ordinal)
	if err != nil {
		_ = m.Close()
		return nil, gcNamespaceProblem(err)
	}
	sourceName, ok := gcDecodeNameBytes(header.sourceBasename)
	if !ok {
		_ = m.Close()
		return nil, gcNamespaceProblem(nsInvalidNameError())
	}
	if err := gcVerifyRecord(directory, file, attemptID, ordinal, header.sourceBasename, sourceName, inertName, header); err != nil {
		_ = m.Close()
		return nil, err
	}
	_ = m.Close()
	return &gcEnvelope{
		name:          envelopeName,
		sourceEncoded: header.sourceBasename,
		sourceName:    sourceName,
		inertName:     inertName,
		identity:      identity,
		header:        header,
	}, nil
}

// gcHeaderOf builds the sequence-1 header of one new envelope (Rust
// gc::header).
func gcHeaderOf(directory *Directory, authority *gcAuthority, inert string) *gcHeader {
	encoding := gcBasenameEncodingValue()
	return &gcHeader{
		kind:                   authority.kind,
		basenameEncoding:       uint16(encoding),
		attemptID:              authority.attemptID,
		ordinal:                authority.ordinal,
		directoryIdentityKind:  identityKind,
		artifactIdentityKind:   identityKind,
		directoryIdentity:      gcEncodeIdentity(directory.Identity()),
		sourceCommitment:       gcSourceCommitment(uint16(encoding), gcNameBytes(authority.sourceName)),
		inertCommitment:        gcInertCommitment(uint16(encoding), gcNameBytes(inert)),
		artifactIdentity:       gcEncodeIdentity(authority.identity),
		payload:                authority.payload,
		creationSecurityKind:   authority.creationSecurity.Kind,
		directoryRole:          authority.directoryRole,
		creationSecurityCommit: authority.creationSecurity.Commitment,
		sourceBasename:         gcNameBytes(authority.sourceName),
		sequence:               1,
	}
}

// gcVerifyAuthority proves one loaded envelope carries exactly the
// authority that retired the artifact (Rust gc::verify_authority).
func gcVerifyAuthority(directory *Directory, authority *gcAuthority, envelope *gcEnvelope) *format.Error {
	header := envelope.header
	encoding := uint16(gcBasenameEncodingValue())
	if header.kind != authority.kind ||
		header.basenameEncoding != encoding ||
		header.attemptID != authority.attemptID ||
		header.ordinal != authority.ordinal ||
		header.directoryIdentityKind != identityKind ||
		header.artifactIdentityKind != identityKind ||
		header.directoryIdentity != gcEncodeIdentity(directory.Identity()) ||
		header.artifactIdentity != gcEncodeIdentity(authority.identity) ||
		!gcSourceEncodedMatches(envelope.sourceEncoded, authority.sourceName) ||
		header.sourceCommitment != gcSourceCommitment(encoding, gcNameBytes(authority.sourceName)) ||
		header.inertCommitment != gcInertCommitment(encoding, gcNameBytes(envelope.inertName)) ||
		!gcPayloadEqual(header.payload, authority.payload) ||
		header.creationSecurityKind != authority.creationSecurity.Kind ||
		header.directoryRole != authority.directoryRole ||
		header.creationSecurityCommit != authority.creationSecurity.Commitment {
		return gcCleanupConflict("GC authority envelope does not match the retained artifact")
	}
	return nil
}

// gcVerifyRecord proves one selected envelope coherent with its names
// and directory identity and its creator-only access policy (Rust
// gc::verify_record).
func gcVerifyRecord(directory *Directory, envelopeFile *os.File, attemptID [16]byte, ordinal uint32, sourceEncoded []byte, sourceName, inertName string, header *gcHeader) *format.Error {
	encoding := uint16(gcBasenameEncodingValue())
	if header.basenameEncoding != encoding ||
		header.attemptID != attemptID ||
		header.ordinal != ordinal ||
		header.directoryIdentityKind != identityKind ||
		header.artifactIdentityKind != identityKind ||
		header.directoryIdentity != gcEncodeIdentity(directory.Identity()) ||
		!gcIdentityDecodable(header.artifactIdentity) ||
		header.sourceCommitment != gcSourceCommitment(encoding, sourceEncoded) ||
		header.inertCommitment != gcInertCommitment(encoding, gcNameBytes(inertName)) ||
		header.creationSecurityKind != gcCreationSecurityKind() ||
		!gcRoleMatches(header.kind, header.directoryRole) ||
		!gcNameMatches(header.kind, attemptID, ordinal, sourceName) {
		return gcCleanupConflict("GC authority envelope does not match its names or directory")
	}
	commitment, err := security.CreatorOnlyCommitment(envelopeFile)
	if err != nil {
		return gcNamespaceProblem(err)
	}
	if commitment != header.creationSecurityCommit {
		return gcCleanupConflict("GC authority envelope access policy changed")
	}
	return nil
}

// gcVerifySource proves the retained source handle still names the
// authority identity at its committed name (Rust gc::verify_source).
func gcVerifySource(directory *Directory, authority *gcAuthority) *format.Error {
	identity, err := RegularIdentity(authority.sourceFile, directory.Identity())
	if err != nil {
		return gcNamespaceProblem(err)
	}
	if identity != authority.identity {
		return gcCleanupConflict("GC source handle identity changed")
	}
	if err := directory.VerifyName(authority.sourceName, authority.identity); err != nil {
		return gcNamespaceProblem(err)
	}
	return nil
}

// gcEncodeIdentity builds the 32-byte encoded identity payload (Rust
// Identity::encode over local_identity).
func gcEncodeIdentity(identity FileIdentity) [32]byte {
	return LocalFileIdentityFromDeviceInode(identity.device, identity.inode).Bytes
}

// gcIdentityDecodable reports whether the encoded identity parses
// under the platform identity kind (Rust Identity::decode(...).is_some()).
func gcIdentityDecodable(bytes [32]byte) bool {
	identity := LocalFileIdentity{Kind: identityKind, Bytes: bytes}
	_, _, ok := identity.DeviceInode()
	return ok
}

// gcNameBytes is the platform basename encoding of one ASCII name
// (Rust Name::bytes: raw bytes on unix, UTF-16LE units on Windows).
func gcNameBytes(name string) []byte {
	return gcNameBytesPlatform(name)
}

// gcProbe enters the mapped artifact region under the worker session
// (Rust worker::enter_artifact: AuthorizedScratch probes the scratch
// role, every other kind probes the output role).
func gcProbe(m *mapping.Mapping, kind ArtifactKind) error {
	role := mapping.RoleOutput
	if kind == ArtifactAuthorizedScratch {
		role = mapping.RoleScratch
	}
	return m.Probe(role, func() error { return nil })
}

func isNamespaceExists(err error) bool {
	var nerr *NamespaceError
	if !errors.As(err, &nerr) {
		return false
	}
	return nerr.Kind == NamespaceExists
}
