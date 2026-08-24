//go:build linux && amd64

// Publication-fact wire codecs (Rust worker/wire_publication.rs): the
// private output attempt, the full publication attempt/result, the
// cleanup ledgers, housekeeping artifacts, publication problems, and
// the fixed enum tags. Every field order and ledger bound mirrors the
// Rust authority; the enum tag tables are the wire constants of the
// Rust macro-generated codecs. The Go publication package drops
// os_code from its problem type (design decision 6), so the wire
// problem keeps the pair explicitly on this boundary.

package worker

import (
	"errors"
	"unicode/utf8"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// maxHousekeeping is the fixed visible-housekeeping ledger bound (Rust
// wire_publication.rs MAX_HOUSEKEEPING = 8).
const maxHousekeeping = 8

// WireProblem is the wire form of the Rust PublicationProblem: the
// stable code class, the optional errno, and the exact UTF-8 detail.
// The Go publication machine reports its problems as format.Errors; the
// worker boundary is the only place the pair survives.
type WireProblem struct {
	Code   format.ErrorCode
	OSCode *int32
	Detail string
}

// Err converts the wire problem back to the Go problem surface (Rust
// into_error parity; the errno is dropped like every Go arm).
func (p WireProblem) Err() error {
	return &format.Error{Code: p.Code, Detail: p.Detail}
}

// wireProblemOf folds one Go error into the wire problem shape: a
// format.Error keeps its class and detail, an errno chain reports the
// Io class with the raw errno, and any other error is the fixed
// Conflict class of an unknown failure.
// WireProblemOf folds one Go error into the wire problem shape (Rust
// publication problem folding): a format.Error keeps its class and
// detail, an errno chain reports the Io class with the raw errno, and
// any other error is the fixed Conflict class of an unknown failure.
// The worker binary boundary composes this one authoritative mapping
// instead of repeating it.
func WireProblemOf(err error) WireProblem {
	var formatted *format.Error
	if errors.As(err, &formatted) {
		return WireProblem{Code: formatted.Code, Detail: formatted.Detail}
	}
	if osCode, ok := errnoOf(err); ok {
		return WireProblem{Code: format.CodeIO, OSCode: osCode, Detail: err.Error()}
	}
	return WireProblem{Code: format.CodeConflict, Detail: err.Error()}
}

// writeProblem writes one publication problem (Rust
// wire_publication::problem: code, optional errno, sized UTF-8
// detail).
func writeProblem(w *WireWriter, value *WireProblem) error {
	if err := w.U32(uint32(value.Code)); err != nil {
		return err
	}
	if err := w.Bool(value.OSCode != nil); err != nil {
		return err
	}
	if value.OSCode != nil {
		if err := w.I32(*value.OSCode); err != nil {
			return err
		}
	}
	return w.SizedBytes([]byte(value.Detail))
}

// readProblem decodes one publication problem (Rust
// wire_publication::read_problem: an unknown code or a non-UTF-8
// detail is corruption).
func readProblem(r *WireReader) (*WireProblem, error) {
	rawCode, err := r.U32()
	if err != nil {
		return nil, err
	}
	code, ok := errorCodeFromWire(rawCode)
	if !ok {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker publication error code is invalid"}
	}
	hasOS, err := r.Bool()
	if err != nil {
		return nil, err
	}
	var osCode *int32
	if hasOS {
		value, err := r.I32()
		if err != nil {
			return nil, err
		}
		osCode = &value
	}
	detail, err := r.BoxedBytes()
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(detail) {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker publication error detail is not UTF-8"}
	}
	return &WireProblem{Code: code, OSCode: osCode, Detail: string(detail)}, nil
}

// writeOptionalProblem writes one optional publication problem (Rust
// wire_publication::optional_problem).
func writeOptionalProblem(w *WireWriter, value *WireProblem) error {
	if value == nil {
		return w.Bool(false)
	}
	if err := w.Bool(true); err != nil {
		return err
	}
	return writeProblem(w, value)
}

// readOptionalProblem decodes one optional publication problem (Rust
// wire_publication::read_optional_problem).
func readOptionalProblem(r *WireReader) (*WireProblem, error) {
	present, err := r.Bool()
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	return readProblem(r)
}

// writeCreationSecurity writes one creator-only commitment (Rust
// wire_publication::creation_security: kind u16 then the 32-byte
// commitment).
func writeCreationSecurity(w *WireWriter, value *publication.CreationSecurity) error {
	if err := w.U16(value.Kind); err != nil {
		return err
	}
	return w.Bytes(value.Commitment[:])
}

// readCreationSecurity decodes one creator-only commitment (Rust
// wire_publication::read_creation_security).
func readCreationSecurity(r *WireReader) (publication.CreationSecurity, error) {
	kind, err := r.U16()
	if err != nil {
		return publication.CreationSecurity{}, err
	}
	commitment, err := r.Array32()
	if err != nil {
		return publication.CreationSecurity{}, err
	}
	return publication.CreationSecurity{Kind: kind, Commitment: commitment}, nil
}

// writeOptionalIdentity writes one optional portable identity (Rust
// wire_publication::optional_identity).
func writeOptionalIdentity(w *WireWriter, value *publication.LocalFileIdentity) error {
	if value == nil {
		return w.Bool(false)
	}
	if err := w.Bool(true); err != nil {
		return err
	}
	return writeIdentity(w, *value)
}

// readOptionalIdentity decodes one optional portable identity (Rust
// wire_publication::read_optional_identity).
func readOptionalIdentity(r *WireReader) (*publication.LocalFileIdentity, error) {
	present, err := r.Bool()
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	value, err := readIdentity(r)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

// writeOptionalByte writes one optional byte (Rust
// wire_publication::optional_byte).
func writeOptionalByte(w *WireWriter, value *uint8) error {
	if value == nil {
		return w.Bool(false)
	}
	if err := w.Bool(true); err != nil {
		return err
	}
	return w.Byte(*value)
}

// readOptionalByte decodes one optional byte (Rust
// wire_publication::read_optional_byte).
func readOptionalByte(r *WireReader) (*uint8, error) {
	present, err := r.Bool()
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	value, err := r.Byte()
	if err != nil {
		return nil, err
	}
	return &value, nil
}

// writeOptionalU64 writes one optional u64 (Rust
// wire_publication::optional_u64).
func writeOptionalU64(w *WireWriter, value *uint64) error {
	if value == nil {
		return w.Bool(false)
	}
	if err := w.Bool(true); err != nil {
		return err
	}
	return w.U64(*value)
}

// readOptionalU64 decodes one optional u64 (Rust
// wire_publication::read_optional_u64).
func readOptionalU64(r *WireReader) (*uint64, error) {
	present, err := r.Bool()
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	value, err := r.U64()
	if err != nil {
		return nil, err
	}
	return &value, nil
}

// writeOptionalI32 writes one optional i32 (Rust
// wire_publication::optional_i32).
func writeOptionalI32(w *WireWriter, value *int32) error {
	if value == nil {
		return w.Bool(false)
	}
	if err := w.Bool(true); err != nil {
		return err
	}
	return w.I32(*value)
}

// readOptionalI32 decodes one optional i32 (Rust
// wire_publication::read_optional_i32).
func readOptionalI32(r *WireReader) (*int32, error) {
	present, err := r.Bool()
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	value, err := r.I32()
	if err != nil {
		return nil, err
	}
	return &value, nil
}

// writeOptionalArray16 writes one optional 16-byte array (Rust
// wire_publication::optional_array).
func writeOptionalArray16(w *WireWriter, value *[16]byte) error {
	if value == nil {
		return w.Bool(false)
	}
	if err := w.Bool(true); err != nil {
		return err
	}
	return w.Bytes(value[:])
}

// readOptionalArray16 decodes one optional 16-byte array (Rust
// wire_publication::read_optional_array).
func readOptionalArray16(r *WireReader) (*[16]byte, error) {
	present, err := r.Bool()
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	value, err := r.Array16()
	if err != nil {
		return nil, err
	}
	return &value, nil
}

// writePrivateOutput writes one private output attempt (Rust
// wire_publication::private_output).
func writePrivateOutput(w *WireWriter, value *publication.PrivateOutputAttempt) error {
	if err := w.Bytes(value.PublicationAttemptID[:]); err != nil {
		return err
	}
	if err := writeIdentity(w, value.DirectoryIdentity); err != nil {
		return err
	}
	if err := w.U16(value.BasenameEncoding); err != nil {
		return err
	}
	if err := w.SizedBytes(value.Basename); err != nil {
		return err
	}
	var identity *publication.LocalFileIdentity
	if value.IdentityPresent {
		identity = &value.Identity
	}
	if err := writeOptionalIdentity(w, identity); err != nil {
		return err
	}
	return writeCreationSecurity(w, &value.CreationSecurity)
}

// readPrivateOutput decodes one private output attempt (Rust
// wire_publication::read_private_output).
func readPrivateOutput(r *WireReader) (publication.PrivateOutputAttempt, error) {
	value := publication.PrivateOutputAttempt{}
	var err error
	if value.PublicationAttemptID, err = r.Array16(); err != nil {
		return value, err
	}
	if value.DirectoryIdentity, err = readIdentity(r); err != nil {
		return value, err
	}
	if value.BasenameEncoding, err = r.U16(); err != nil {
		return value, err
	}
	if value.Basename, err = r.BoxedBytes(); err != nil {
		return value, err
	}
	identity, err := readOptionalIdentity(r)
	if err != nil {
		return value, err
	}
	if identity != nil {
		value.Identity = *identity
		value.IdentityPresent = true
	}
	if value.CreationSecurity, err = readCreationSecurity(r); err != nil {
		return value, err
	}
	return value, nil
}

// writePreviousDestination writes one previous-destination evidence
// record (Rust wire_publication::previous_destination).
func writePreviousDestination(w *WireWriter, value *publication.PreviousDestination) error {
	if err := writeIdentity(w, value.Identity); err != nil {
		return err
	}
	if err := w.U64(value.ByteLength); err != nil {
		return err
	}
	return w.Bytes(value.SHA512[:])
}

// readPreviousDestination decodes one previous-destination evidence
// record (Rust wire_publication::read_previous_destination).
func readPreviousDestination(r *WireReader) (publication.PreviousDestination, error) {
	value := publication.PreviousDestination{}
	var err error
	if value.Identity, err = readIdentity(r); err != nil {
		return value, err
	}
	if value.ByteLength, err = r.U64(); err != nil {
		return value, err
	}
	sha, err := r.Array64()
	if err != nil {
		return value, err
	}
	value.SHA512 = sha
	return value, nil
}

// writePublicationAttempt writes one full publication attempt (Rust
// wire_publication::attempt).
func writePublicationAttempt(w *WireWriter, value *publication.PublicationAttempt) error {
	if err := w.Bytes(value.DatabaseID[:]); err != nil {
		return err
	}
	if err := w.U64(value.TransactionID); err != nil {
		return err
	}
	if err := w.Bytes(value.CommitNonce[:]); err != nil {
		return err
	}
	if err := w.Bytes(value.PublicationAttemptID[:]); err != nil {
		return err
	}
	if err := writeIdentity(w, value.DirectoryIdentity); err != nil {
		return err
	}
	if err := w.U16(value.DestinationBasenameEncoding); err != nil {
		return err
	}
	if err := w.SizedBytes(value.DestinationBasename); err != nil {
		return err
	}
	if err := writeIdentity(w, value.OutputIdentity); err != nil {
		return err
	}
	if err := w.U64(value.OutputByteLength); err != nil {
		return err
	}
	if err := w.Bytes(value.OutputSHA512[:]); err != nil {
		return err
	}
	if err := w.Byte(publicationPolicyTag(value.PublicationPolicy)); err != nil {
		return err
	}
	if err := w.Bool(value.PreviousDestination != nil); err != nil {
		return err
	}
	if value.PreviousDestination != nil {
		if err := writePreviousDestination(w, value.PreviousDestination); err != nil {
			return err
		}
	}
	if err := writeIdentity(w, value.ReservationIdentity); err != nil {
		return err
	}
	return writeCreationSecurity(w, &value.CreationSecurity)
}

// readPublicationAttempt decodes one full publication attempt (Rust
// wire_publication::read_attempt).
func readPublicationAttempt(r *WireReader) (publication.PublicationAttempt, error) {
	value := publication.PublicationAttempt{}
	var err error
	if value.DatabaseID, err = r.Array16(); err != nil {
		return value, err
	}
	if value.TransactionID, err = r.U64(); err != nil {
		return value, err
	}
	if value.CommitNonce, err = r.Array16(); err != nil {
		return value, err
	}
	if value.PublicationAttemptID, err = r.Array16(); err != nil {
		return value, err
	}
	if value.DirectoryIdentity, err = readIdentity(r); err != nil {
		return value, err
	}
	if value.DestinationBasenameEncoding, err = r.U16(); err != nil {
		return value, err
	}
	if value.DestinationBasename, err = r.BoxedBytes(); err != nil {
		return value, err
	}
	if value.OutputIdentity, err = readIdentity(r); err != nil {
		return value, err
	}
	if value.OutputByteLength, err = r.U64(); err != nil {
		return value, err
	}
	outputSHA, err := r.Array64()
	if err != nil {
		return value, err
	}
	value.OutputSHA512 = outputSHA
	tag, err := r.Byte()
	if err != nil {
		return value, err
	}
	policy, err := readPublicationPolicy(tag)
	if err != nil {
		return value, err
	}
	value.PublicationPolicy = policy
	hasPrevious, err := r.Bool()
	if err != nil {
		return value, err
	}
	if hasPrevious {
		previous, err := readPreviousDestination(r)
		if err != nil {
			return value, err
		}
		value.PreviousDestination = &previous
	}
	if value.ReservationIdentity, err = readIdentity(r); err != nil {
		return value, err
	}
	if value.CreationSecurity, err = readCreationSecurity(r); err != nil {
		return value, err
	}
	return value, nil
}

// writeCleanupArtifacts writes one cleanup ledger (Rust
// wire_publication::cleanup: a byte count followed by the artifacts).
func writeCleanupArtifacts(w *WireWriter, value *publication.CleanupArtifacts) error {
	if err := w.Byte(uint8(value.Len())); err != nil {
		return err
	}
	for index := 0; index < value.Len(); index++ {
		if err := writeCleanupArtifact(w, value.At(index)); err != nil {
			return err
		}
	}
	return nil
}

// readCleanupArtifacts decodes one cleanup ledger (Rust
// wire_publication::read_cleanup: more than four artifacts is
// corruption).
func readCleanupArtifacts(r *WireReader) (publication.CleanupArtifacts, error) {
	count, err := r.Byte()
	if err != nil {
		return publication.CleanupArtifacts{}, err
	}
	if count > 4 {
		return publication.CleanupArtifacts{}, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker cleanup ledger is too large"}
	}
	cleanup := publication.NewCleanupArtifacts()
	for index := 0; index < int(count); index++ {
		artifact, err := readCleanupArtifact(r)
		if err != nil {
			return publication.CleanupArtifacts{}, err
		}
		cleanup.Push(artifact)
	}
	return cleanup, nil
}

// writeCleanupArtifact writes one exact cleanup artifact (Rust
// wire_publication::cleanup_artifact).
func writeCleanupArtifact(w *WireWriter, value *publication.CleanupArtifact) error {
	if err := w.Byte(artifactKindTag(value.Kind)); err != nil {
		return err
	}
	if err := w.Byte(directoryRoleTag(value.DirectoryRole)); err != nil {
		return err
	}
	if err := writeIdentity(w, value.DirectoryIdentity); err != nil {
		return err
	}
	if err := w.U16(value.BasenameEncoding); err != nil {
		return err
	}
	if err := w.SizedBytes(value.Basename); err != nil {
		return err
	}
	if err := writeOptionalIdentity(w, value.Identity); err != nil {
		return err
	}
	if err := w.Bool(value.CreationSecurity != nil); err != nil {
		return err
	}
	if value.CreationSecurity != nil {
		if err := writeCreationSecurity(w, value.CreationSecurity); err != nil {
			return err
		}
	}
	if err := w.Bool(value.UnpublishedTail != nil); err != nil {
		return err
	}
	if value.UnpublishedTail != nil {
		if err := writeUnpublishedTail(w, value.UnpublishedTail); err != nil {
			return err
		}
	}
	problem := WireProblemOf(value.Error)
	return writeProblem(w, &problem)
}

// readCleanupArtifact decodes one exact cleanup artifact (Rust
// wire_publication::read_cleanup_artifact).
func readCleanupArtifact(r *WireReader) (publication.CleanupArtifact, error) {
	value := publication.CleanupArtifact{}
	var err error
	kindTag, err := r.Byte()
	if err != nil {
		return value, err
	}
	kind, err := readArtifactKind(kindTag)
	if err != nil {
		return value, err
	}
	value.Kind = kind
	roleTag, err := r.Byte()
	if err != nil {
		return value, err
	}
	role, err := readDirectoryRole(roleTag)
	if err != nil {
		return value, err
	}
	value.DirectoryRole = role
	if value.DirectoryIdentity, err = readIdentity(r); err != nil {
		return value, err
	}
	if value.BasenameEncoding, err = r.U16(); err != nil {
		return value, err
	}
	if value.Basename, err = r.BoxedBytes(); err != nil {
		return value, err
	}
	if value.Identity, err = readOptionalIdentity(r); err != nil {
		return value, err
	}
	hasSecurity, err := r.Bool()
	if err != nil {
		return value, err
	}
	if hasSecurity {
		security, err := readCreationSecurity(r)
		if err != nil {
			return value, err
		}
		value.CreationSecurity = &security
	}
	hasTail, err := r.Bool()
	if err != nil {
		return value, err
	}
	if hasTail {
		tail, err := readUnpublishedTail(r)
		if err != nil {
			return value, err
		}
		value.UnpublishedTail = &tail
	}
	problem, err := readProblem(r)
	if err != nil {
		return value, err
	}
	value.Error = problem.Err()
	return value, nil
}

// writeUnpublishedTail writes one unpublished main-tail evidence record
// (Rust wire_publication::unpublished_tail).
func writeUnpublishedTail(w *WireWriter, value *publication.UnpublishedTailFacts) error {
	if err := w.Bytes(value.ExpectedDatabaseID[:]); err != nil {
		return err
	}
	if err := w.U64(value.CommittedTargetTransactionID); err != nil {
		return err
	}
	if err := w.Bytes(value.CommittedTargetNonce[:]); err != nil {
		return err
	}
	if err := w.U64(value.CommittedTargetLength); err != nil {
		return err
	}
	return w.U64(value.ObservedTailEndExclusive)
}

// readUnpublishedTail decodes one unpublished main-tail evidence
// record (Rust wire_publication::read_unpublished_tail).
func readUnpublishedTail(r *WireReader) (publication.UnpublishedTailFacts, error) {
	value := publication.UnpublishedTailFacts{}
	var err error
	if value.ExpectedDatabaseID, err = r.Array16(); err != nil {
		return value, err
	}
	if value.CommittedTargetTransactionID, err = r.U64(); err != nil {
		return value, err
	}
	if value.CommittedTargetNonce, err = r.Array16(); err != nil {
		return value, err
	}
	if value.CommittedTargetLength, err = r.U64(); err != nil {
		return value, err
	}
	if value.ObservedTailEndExclusive, err = r.U64(); err != nil {
		return value, err
	}
	return value, nil
}

// writeHousekeepingList writes one visible-housekeeping ledger (Rust
// wire_publication::housekeeping_list: the bounded 8-entry ledger).
func writeHousekeepingList(w *WireWriter, values []publication.HousekeepingArtifact) error {
	if len(values) > maxHousekeeping {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "worker housekeeping ledger"}
	}
	if err := w.Byte(uint8(len(values))); err != nil {
		return err
	}
	for index := range values {
		if err := writeHousekeepingArtifact(w, &values[index]); err != nil {
			return err
		}
	}
	return nil
}

// readHousekeepingList decodes one visible-housekeeping ledger (Rust
// wire_publication::read_housekeeping_list: beyond 8 entries is
// corruption).
func readHousekeepingList(r *WireReader) ([]publication.HousekeepingArtifact, error) {
	count, err := r.Byte()
	if err != nil {
		return nil, err
	}
	if count > maxHousekeeping {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker housekeeping ledger is too large"}
	}
	values := make([]publication.HousekeepingArtifact, 0, int(count))
	for index := 0; index < int(count); index++ {
		value, err := readHousekeepingArtifact(r)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

// readHousekeepingArtifacts decodes a visible-housekeeping ledger at
// the current read position (Rust wire_publication::
// read_housekeeping_artifacts).
func readHousekeepingArtifacts(r *WireReader) ([]publication.HousekeepingArtifact, error) {
	return readHousekeepingList(r)
}

// writeHousekeepingArtifact writes one visible housekeeping artifact
// (Rust wire_publication::housekeeping_artifact).
func writeHousekeepingArtifact(w *WireWriter, value *publication.HousekeepingArtifact) error {
	if err := w.Byte(housekeepingStateTag(value.State)); err != nil {
		return err
	}
	if err := w.Byte(directoryRoleTag(value.DirectoryRole)); err != nil {
		return err
	}
	if err := writeIdentity(w, value.DirectoryIdentity); err != nil {
		return err
	}
	if err := w.U16(value.BasenameEncoding); err != nil {
		return err
	}
	if err := w.Bytes(value.AttemptID[:]); err != nil {
		return err
	}
	if err := w.U32(value.Ordinal); err != nil {
		return err
	}
	if err := w.SizedBytes(value.EnvelopeBasename); err != nil {
		return err
	}
	if err := writeIdentity(w, value.EnvelopeIdentity); err != nil {
		return err
	}
	if err := w.SizedBytes(value.SourceBasename); err != nil {
		return err
	}
	if err := w.SizedBytes(value.InertBasename); err != nil {
		return err
	}
	if err := w.Byte(artifactPresenceTag(value.SourcePresence)); err != nil {
		return err
	}
	if err := writeOptionalIdentity(w, value.SourceIdentity); err != nil {
		return err
	}
	if err := w.Byte(artifactPresenceTag(value.InertPresence)); err != nil {
		return err
	}
	if err := writeOptionalIdentity(w, value.InertIdentity); err != nil {
		return err
	}
	if err := w.Byte(artifactKindTag(value.Kind)); err != nil {
		return err
	}
	if err := writeCreationSecurity(w, &value.CreationSecurity); err != nil {
		return err
	}
	return w.U64(value.SelectedEnvelopeSequence)
}

// readHousekeepingArtifact decodes one visible housekeeping artifact
// (Rust wire_publication::read_housekeeping_artifact).
func readHousekeepingArtifact(r *WireReader) (publication.HousekeepingArtifact, error) {
	value := publication.HousekeepingArtifact{}
	var err error
	stateTag, err := r.Byte()
	if err != nil {
		return value, err
	}
	state, err := readHousekeepingState(stateTag)
	if err != nil {
		return value, err
	}
	value.State = state
	roleTag, err := r.Byte()
	if err != nil {
		return value, err
	}
	role, err := readDirectoryRole(roleTag)
	if err != nil {
		return value, err
	}
	value.DirectoryRole = role
	if value.DirectoryIdentity, err = readIdentity(r); err != nil {
		return value, err
	}
	if value.BasenameEncoding, err = r.U16(); err != nil {
		return value, err
	}
	if value.AttemptID, err = r.Array16(); err != nil {
		return value, err
	}
	if value.Ordinal, err = r.U32(); err != nil {
		return value, err
	}
	if value.EnvelopeBasename, err = r.BoxedBytes(); err != nil {
		return value, err
	}
	if value.EnvelopeIdentity, err = readIdentity(r); err != nil {
		return value, err
	}
	if value.SourceBasename, err = r.BoxedBytes(); err != nil {
		return value, err
	}
	if value.InertBasename, err = r.BoxedBytes(); err != nil {
		return value, err
	}
	sourceTag, err := r.Byte()
	if err != nil {
		return value, err
	}
	sourcePresence, err := readArtifactPresence(sourceTag)
	if err != nil {
		return value, err
	}
	value.SourcePresence = sourcePresence
	if value.SourceIdentity, err = readOptionalIdentity(r); err != nil {
		return value, err
	}
	inertTag, err := r.Byte()
	if err != nil {
		return value, err
	}
	inertPresence, err := readArtifactPresence(inertTag)
	if err != nil {
		return value, err
	}
	value.InertPresence = inertPresence
	if value.InertIdentity, err = readOptionalIdentity(r); err != nil {
		return value, err
	}
	kindTag, err := r.Byte()
	if err != nil {
		return value, err
	}
	kind, err := readArtifactKind(kindTag)
	if err != nil {
		return value, err
	}
	value.Kind = kind
	if value.CreationSecurity, err = readCreationSecurity(r); err != nil {
		return value, err
	}
	if value.SelectedEnvelopeSequence, err = r.U64(); err != nil {
		return value, err
	}
	return value, nil
}

// writePublicationResult writes one publication result (Rust
// wire_publication::result).
func writePublicationResult(w *WireWriter, value *publication.PublicationResult) error {
	if err := writePublicationAttempt(w, &value.Attempt); err != nil {
		return err
	}
	if err := w.Bool(value.MainNamespaceMayHaveBeenAttempted); err != nil {
		return err
	}
	if err := w.Byte(publicationStatusTag(value.Publication)); err != nil {
		return err
	}
	if err := w.Byte(destinationContentTag(value.DestinationContent)); err != nil {
		return err
	}
	if err := w.Byte(laterCanonicalTag(value.LaterCanonical)); err != nil {
		return err
	}
	if err := writeOptionalLineage(w, value.LiveLineage); err != nil {
		return err
	}
	if err := writeOptionalArray16(w, value.LaterAttemptOrSidecarID); err != nil {
		return err
	}
	if err := writeOptionalU64(w, value.LaterSelectedTransactionID); err != nil {
		return err
	}
	if err := writeOptionalArray16(w, value.LaterSelectedCommitNonce); err != nil {
		return err
	}
	if err := w.Byte(accessPolicyTag(value.MainAccessPolicy)); err != nil {
		return err
	}
	if err := w.Byte(accessPolicyTag(value.CoordinationAccessPolicy)); err != nil {
		return err
	}
	if err := writeCleanupArtifacts(w, &value.Cleanup); err != nil {
		return err
	}
	if err := w.Byte(coordinationCleanupTag(value.CoordinationCleanup)); err != nil {
		return err
	}
	if err := w.Byte(housekeepingTag(value.Housekeeping)); err != nil {
		return err
	}
	if err := writeHousekeepingList(w, value.VisibleHousekeeping); err != nil {
		return err
	}
	if value.Cause == nil {
		return writeOptionalProblem(w, nil)
	}
	problem := WireProblemOf(value.Cause)
	return writeOptionalProblem(w, &problem)
}

// writeOptionalLineage writes one optional live-lineage class (Rust
// wire_publication::optional_byte over the live_lineage tag).
func writeOptionalLineage(w *WireWriter, value *publication.LiveLineage) error {
	if value == nil {
		return w.Bool(false)
	}
	if err := w.Bool(true); err != nil {
		return err
	}
	return w.Byte(liveLineageTag(*value))
}

// readPublicationResult decodes one publication result (Rust
// wire_publication::read_result).
func readPublicationResult(r *WireReader) (publication.PublicationResult, error) {
	value := publication.PublicationResult{}
	var err error
	if value.Attempt, err = readPublicationAttempt(r); err != nil {
		return value, err
	}
	if value.MainNamespaceMayHaveBeenAttempted, err = r.Bool(); err != nil {
		return value, err
	}
	publicationTag, err := r.Byte()
	if err != nil {
		return value, err
	}
	status, err := readPublicationStatus(publicationTag)
	if err != nil {
		return value, err
	}
	value.Publication = status
	contentTag, err := r.Byte()
	if err != nil {
		return value, err
	}
	content, err := readDestinationContent(contentTag)
	if err != nil {
		return value, err
	}
	value.DestinationContent = content
	canonicalTag, err := r.Byte()
	if err != nil {
		return value, err
	}
	canonical, err := readLaterCanonical(canonicalTag)
	if err != nil {
		return value, err
	}
	value.LaterCanonical = canonical
	lineageTag, err := readOptionalByte(r)
	if err != nil {
		return value, err
	}
	if lineageTag != nil {
		lineage, err := readLiveLineage(*lineageTag)
		if err != nil {
			return value, err
		}
		value.LiveLineage = &lineage
	}
	if value.LaterAttemptOrSidecarID, err = readOptionalArray16(r); err != nil {
		return value, err
	}
	if value.LaterSelectedTransactionID, err = readOptionalU64(r); err != nil {
		return value, err
	}
	if value.LaterSelectedCommitNonce, err = readOptionalArray16(r); err != nil {
		return value, err
	}
	mainTag, err := r.Byte()
	if err != nil {
		return value, err
	}
	mainPolicy, err := readAccessPolicy(mainTag)
	if err != nil {
		return value, err
	}
	value.MainAccessPolicy = mainPolicy
	coordinationTag, err := r.Byte()
	if err != nil {
		return value, err
	}
	coordinationPolicy, err := readAccessPolicy(coordinationTag)
	if err != nil {
		return value, err
	}
	value.CoordinationAccessPolicy = coordinationPolicy
	if value.Cleanup, err = readCleanupArtifacts(r); err != nil {
		return value, err
	}
	coordCleanupTag, err := r.Byte()
	if err != nil {
		return value, err
	}
	value.CoordinationCleanup, err = readCoordinationCleanup(coordCleanupTag)
	if err != nil {
		return value, err
	}
	housekeepingTag, err := r.Byte()
	if err != nil {
		return value, err
	}
	value.Housekeeping, err = readHousekeeping(housekeepingTag)
	if err != nil {
		return value, err
	}
	if value.VisibleHousekeeping, err = readHousekeepingList(r); err != nil {
		return value, err
	}
	problem, err := readOptionalProblem(r)
	if err != nil {
		return value, err
	}
	if problem != nil {
		value.Cause = problem.Err()
	}
	return value, nil
}

// readPublicationResultFromControl decodes a sealed publication result
// through the raw codec after Finish (the recovery/validation compose
// point of the session payload).
func readPublicationResultFromControl(control *Control) (publication.PublicationResult, error) {
	r, err := NewWireReader(control)
	if err != nil {
		return publication.PublicationResult{}, err
	}
	value, err := readPublicationResult(r)
	if err != nil {
		return publication.PublicationResult{}, err
	}
	if err := r.Finish(); err != nil {
		return publication.PublicationResult{}, err
	}
	return value, nil
}

// writeCoordinationCleanup writes one coordination residue class byte
// (Rust wire_publication::coordination).
func writeCoordinationCleanup(w *WireWriter, value publication.CoordinationCleanup) error {
	return w.Byte(coordinationCleanupTag(value))
}

// readCoordinationCleanupByte decodes one coordination residue class
// byte (Rust wire_publication::read_coordination).
func readCoordinationCleanupByte(value byte) (publication.CoordinationCleanup, error) {
	return readCoordinationCleanup(value)
}

// writeHousekeepingValue writes one housekeeping class byte (Rust
// wire_publication::housekeeping_value).
func writeHousekeepingValue(w *WireWriter, value publication.Housekeeping) error {
	return w.Byte(housekeepingTag(value))
}

// readHousekeepingValueByte decodes one housekeeping class byte (Rust
// wire_publication::read_housekeeping_value).
func readHousekeepingValueByte(value byte) (publication.Housekeeping, error) {
	return readHousekeeping(value)
}
