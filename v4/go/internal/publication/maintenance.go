// Explicit offline maintenance for publication-private artifacts
// (Rust publication/maintenance.rs + maintenance/{common,output,
// reservation}.rs): constant-memory listing of abandoned private
// publication temps and reservation artifacts with exact evidence,
// and exact removal of one retained artifact after the caller
// certified its quiescence. The Windows housekeeping machines live in
// maintenance_gc_windows.go (posix refuses).

package publication

import (
	"errors"
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

// abandonedReservationPolicy is the publication policy recorded in
// one authenticated private reservation (Rust
// AbandonedReservationPolicy).
type abandonedReservationPolicy uint8

const (
	abandonedReservationPolicyFailIfExists abandonedReservationPolicy = iota
	abandonedReservationPolicyReplaceExisting
	abandonedReservationPolicyReplaceExistingNoRollback
)

// abandonedReservationPhase is the durable namespace phase of one
// private reservation (Rust AbandonedReservationPhase).
type abandonedReservationPhase uint8

const (
	abandonedReservationPhasePrepared abandonedReservationPhase = iota
	abandonedReservationPhaseMainMayHaveBeenAttempted
)

// publicationOutputEvidence is the exact attempted-output identity
// and content evidence of one reservation (Rust
// PublicationOutputEvidence; the tuple and digest are the same
// portable facts as residueTuple/residueDigest).
type publicationOutputEvidence struct {
	identity LocalFileIdentity
	tuple    residueTuple
	digest   residueDigest
}

// reservationPreviousEvidence is the exact previous-destination
// evidence of one replacement reservation (Rust evidence.previous).
type reservationPreviousEvidence struct {
	identity LocalFileIdentity
	digest   residueDigest
}

// abandonedReservationEvidence is the authenticated fields of one
// selectable private reservation (Rust
// AbandonedReservationEvidence).
type abandonedReservationEvidence struct {
	policy   abandonedReservationPolicy
	phase    abandonedReservationPhase
	output   publicationOutputEvidence
	previous *reservationPreviousEvidence
}

// abandonedReservationEntry is one stable exact-pattern private
// reservation (Rust AbandonedReservationEntry).
type abandonedReservationEntry struct {
	directoryIdentity LocalFileIdentity
	artifactIdentity  LocalFileIdentity
	attempt           [16]byte
	evidence          *abandonedReservationEvidence
}

// abandonedReservationList is one completed constant-memory private
// reservation scan (Rust AbandonedReservationList).
type abandonedReservationList struct {
	directoryIdentity LocalFileIdentity
	entries           uint64
}

// abandonedPublicationTempEntry is one stable exact-pattern private
// publication output (Rust AbandonedPublicationTempEntry).
type abandonedPublicationTempEntry struct {
	directoryIdentity LocalFileIdentity
	artifactIdentity  LocalFileIdentity
	attempt           [16]byte
	tuple             *residueTuple
	digest            *residueDigest
}

// abandonedPublicationTempList is one completed constant-memory
// private-output scan (Rust AbandonedPublicationTempList).
type abandonedPublicationTempList struct {
	directoryIdentity LocalFileIdentity
	entries           uint64
}

// errMaintenanceSinkStop is the Go control value of the Rust
// *SinkControl::Stop; deliverMaintenance maps it to the
// StoppedBySink class at the boundary.
var errMaintenanceSinkStop = errors.New("publication maintenance sink stopped")

// deliverMaintenance runs one sink call and maps the control surface
// (Rust deliver: Continue passes, Stop becomes StoppedBySink, any
// sink error becomes SinkFailed).
func deliverMaintenance(sink func() error) error {
	if err := sink(); err != nil {
		if errors.Is(err, errMaintenanceSinkStop) {
			return problem(format.CodeStoppedBySink, "publication maintenance sink stopped")
		}
		return problem(format.CodeSinkFailed, err.Error())
	}
	return nil
}

// listAbandonedPublicationTemps lists the stable no-follow regular
// private publication outputs of one directory in constant memory
// (Rust list_abandoned_publication_temps). The sink receives every
// entry; returning errMaintenanceSinkStop stops the scan.
func listAbandonedPublicationTemps(path string, check func() error, sink func(entry *abandonedPublicationTempEntry) error) (abandonedPublicationTempList, error) {
	directoryIdentity, entries, err := maintenancePublicationTemp.scan(path, check, "publication temp entries", func(dir *live.Directory, directoryIdentity LocalFileIdentity, bytes []byte, attempt [16]byte) (bool, error) {
		entry, err := inspectAbandonedPublicationTemp(dir, directoryIdentity, bytes, attempt, check)
		if err != nil {
			return false, err
		}
		if entry == nil {
			return false, nil
		}
		if err := deliverMaintenance(func() error { return sink(entry) }); err != nil {
			return false, err
		}
		return true, nil
	})
	if err != nil {
		return abandonedPublicationTempList{}, err
	}
	return abandonedPublicationTempList{directoryIdentity: directoryIdentity, entries: entries}, nil
}

// removeAbandonedPublicationTemp removes one exact private output
// after caller-certified quiescence (Rust
// remove_abandoned_publication_temp: readable content requires exact
// tuple and digest evidence; partial content requires both absent).
func removeAbandonedPublicationTemp(path string, expectedDirectory LocalFileIdentity, attempt [16]byte, expectedArtifact LocalFileIdentity, expectedTuple *residueTuple, expectedDigest *residueDigest, check func() error) (AbandonedArtifactRemoval, error) {
	if err := live.Checkpoint(check); err != nil {
		return AbandonedArtifactRemoval{}, sdkProblem(err)
	}
	if (expectedTuple != nil) != (expectedDigest != nil) {
		return AbandonedArtifactRemoval{}, problem(format.CodeInvalidArgument, "publication tuple and digest evidence must both be present or absent")
	}
	var expected *publicationOutputEvidence
	if expectedTuple != nil {
		expected = &publicationOutputEvidence{tuple: *expectedTuple, digest: *expectedDigest}
	}
	return maintenancePublicationTemp.remove(path, expectedDirectory, attempt, expectedArtifact, live.MainLifetimeOffset, check, func(file *os.File, _ live.FileIdentity) error {
		if evidence, err := contentEvidence(file, check); err != nil {
			return err
		} else if !samePublicationOutputEvidence(evidence, expected) {
			return cleanupConflictProblem("publication temp content evidence changed")
		}
		return nil
	})
}

// listAbandonedReservationArtifacts lists the stable no-follow
// regular private publication reservations of one directory in
// constant memory (Rust list_abandoned_reservation_artifacts). The
// sink receives every entry; returning errMaintenanceSinkStop stops
// the scan.
func listAbandonedReservationArtifacts(path string, check func() error, sink func(entry *abandonedReservationEntry) error) (abandonedReservationList, error) {
	directoryIdentity, entries, err := maintenanceReservationArtifact.scan(path, check, "reservation artifact entries", func(dir *live.Directory, directoryIdentity LocalFileIdentity, bytes []byte, attempt [16]byte) (bool, error) {
		entry, err := inspectAbandonedReservation(dir, directoryIdentity, bytes, attempt)
		if err != nil {
			return false, err
		}
		if entry == nil {
			return false, nil
		}
		if err := deliverMaintenance(func() error { return sink(entry) }); err != nil {
			return false, err
		}
		return true, nil
	})
	if err != nil {
		return abandonedReservationList{}, err
	}
	return abandonedReservationList{directoryIdentity: directoryIdentity, entries: entries}, nil
}

// removeAbandonedReservationArtifact removes one exact private
// reservation after caller-certified quiescence (Rust
// remove_abandoned_reservation_artifact).
func removeAbandonedReservationArtifact(path string, expectedDirectory LocalFileIdentity, attempt [16]byte, expectedArtifact LocalFileIdentity, check func() error) (AbandonedArtifactRemoval, error) {
	if err := live.Checkpoint(check); err != nil {
		return AbandonedArtifactRemoval{}, sdkProblem(err)
	}
	return maintenanceReservationArtifact.remove(path, expectedDirectory, attempt, expectedArtifact, reservationOperationLock, check, func(file *os.File, identity live.FileIdentity) error {
		return requireReadableReservationBinding(file, attempt, identity)
	})
}

// residuePayloadIdentity is the optional exact content evidence of
// one Windows housekeeping removal (Rust HousekeepingPayloadIdentity;
// present only so the refused Go arm keeps the Rust surface shape for
// the later public wire).
type residuePayloadIdentity struct {
	tuple  *residueTuple
	digest residueDigest
}

// samePublicationOutputEvidence compares one readable evidence pair
// with the caller expectation (Rust expected.zip(expected) equality;
// a nil evidence equals a nil expectation).
func samePublicationOutputEvidence(evidence, expected *publicationOutputEvidence) bool {
	if evidence == nil || expected == nil {
		return evidence == nil && expected == nil
	}
	return evidence.tuple == expected.tuple && evidence.digest == expected.digest
}
