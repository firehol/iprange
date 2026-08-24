//go:build !windows

// Portable private reservation discovery and exact offline removal
// (Rust publication/maintenance/reservation.rs): stable no-follow
// listing with the authenticated policy/phase/output/previous
// evidence of selectable reservations, and exact removal under the
// operation lock.

package publication

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// maintenanceReservationArtifact is the reservation-artifact family
// (Rust reservation.rs ARTIFACT).
var maintenanceReservationArtifact = maintenanceArtifact{
	prefix:              reservationPrefix,
	invalidName:         "invalid reservation artifact name",
	unsupportedIdentity: "unsupported reservation identity kind",
	invalidIdentity:     "invalid reservation identity",
	ownershipMismatch:   "reservation artifact identity or link count changed",
	ownershipChanged:    "reservation artifact ownership changed",
	lostName:            "reservation artifact lost its exact name",
	remainedLinked:      "reservation artifact remained linked after removal",
}

// inspectAbandonedReservation inspects one exact reservation name
// with the authenticated evidence of a selectable bound record (Rust
// reservation.rs inspect: the header must carry the same attempt id
// and the exact inode identity; anything else reports no evidence).
func inspectAbandonedReservation(dir *live.Directory, directoryIdentity LocalFileIdentity, bytes []byte, attempt [16]byte) (*abandonedReservationEntry, error) {
	identity, evidence, ok, err := inspectStable(&maintenanceReservationArtifact, dir, bytes, func(file *os.File, identity live.FileIdentity) (*abandonedReservationEvidence, error) {
		header, found := readReservationHeader(file)
		if !found || header.attemptID != attempt || header.reservationIdentity != reservationIdentityBytes(identity) {
			return nil, nil
		}
		return reservationMaintenanceEvidence(header), nil
	})
	if err != nil || !ok {
		return nil, err
	}
	return &abandonedReservationEntry{
		directoryIdentity: directoryIdentity,
		artifactIdentity:  localIdentityFromEncoded(identity),
		attempt:           attempt,
		evidence:          evidence,
	}, nil
}

// readReservationHeader reads one selectable reservation record of
// an open file with the exact geometry (Rust reservation.rs
// read_header: a wrong size, an unreadable mapping, or an
// unselectable record carries no header).
func readReservationHeader(file *os.File) (reservationHeader, bool) {
	st, err := file.Stat()
	if err != nil || uint64(st.Size()) != reservationFileSize {
		return reservationHeader{}, false
	}
	mapped, err := mapping.MapFile(file, reservationFileSize, false)
	if err != nil {
		return reservationHeader{}, false
	}
	defer mapped.Close()
	bytes, err := mapped.View(0, reservationFileSize)
	if err != nil {
		return reservationHeader{}, false
	}
	selected, err := selectReservation(bytes)
	if err != nil {
		return reservationHeader{}, false
	}
	return selected.header, true
}

// requireReadableReservationBinding refuses removal of a readable
// reservation that is not bound to its exact name and inode (Rust
// reservation.rs require_readable_binding).
func requireReadableReservationBinding(file *os.File, attempt [16]byte, identity live.FileIdentity) error {
	if header, found := readReservationHeader(file); found {
		if header.attemptID != attempt || header.reservationIdentity != reservationIdentityBytes(identity) {
			return cleanupConflictProblem("readable reservation is not bound to its name and inode")
		}
	}
	return nil
}

// reservationMaintenanceEvidence decodes the authenticated evidence
// of one selectable reservation header (Rust reservation.rs
// evidence; the output identity is a written record, so the decode
// is the panic-free expect of the ported codec).
func reservationMaintenanceEvidence(header reservationHeader) *abandonedReservationEvidence {
	output := publicationOutputEvidence{
		identity: identityFromEncodedLocal(header.outputIdentity),
		tuple: residueTuple{
			databaseID:    header.databaseID,
			transactionID: header.transactionID,
			commitNonce:   header.commitNonce,
		},
		digest: residueDigest{byteLength: header.outputByteLength, sha512: header.outputSHA512},
	}
	evidence := &abandonedReservationEvidence{
		policy: reservationMaintenancePolicy(header.policy),
		phase:  reservationMaintenancePhase(header.state),
		output: output,
	}
	if header.previousPresent {
		evidence.previous = &reservationPreviousEvidence{
			identity: identityFromEncodedLocal(header.previous.identity),
			digest:   residueDigest{byteLength: header.previous.byteLength, sha512: header.previous.sha512},
		}
	}
	return evidence
}

// reservationMaintenancePolicy maps one wire policy (Rust
// AbandonedReservationPolicy match; the codec only selects the three
// valid policies).
func reservationMaintenancePolicy(policy reservationPolicy) abandonedReservationPolicy {
	switch policy {
	case reservationPolicyReplaceExisting:
		return abandonedReservationPolicyReplaceExisting
	case reservationPolicyReplaceExistingNoRollback:
		return abandonedReservationPolicyReplaceExistingNoRollback
	default:
		return abandonedReservationPolicyFailIfExists
	}
}

// reservationMaintenancePhase maps one wire state (Rust
// AbandonedReservationPhase match; the codec only selects the two
// valid states).
func reservationMaintenancePhase(state reservationState) abandonedReservationPhase {
	if state == reservationStateMainMayHaveBeenAttempted {
		return abandonedReservationPhaseMainMayHaveBeenAttempted
	}
	return abandonedReservationPhasePrepared
}

// identityFromEncodedLocal decodes one written identity payload to
// the portable identity (Rust Identity::decode expect arms).
func identityFromEncodedLocal(bytes [32]byte) LocalFileIdentity {
	return localIdentityFromEncoded(identityFromEncoded(bytes))
}
