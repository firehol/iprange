//go:build windows

// Windows stubs of the public publication boundary (Rust
// publication.rs SOW-0026 tracked surface): every entry point refuses with
// the same OS-unsupported class as the destination bind of the POSIX
// arms, following the mapping-owner platform-stub pattern. The types
// exist only so the SDK facade compiles on Windows; nothing is ever
// constructed.

package publication

import "errors"

// PublicationResolutionMode is the requested terminal action (Rust
// PublicationResolutionMode); unreachable on Windows in milestone 1.
type PublicationResolutionMode uint8

const (
	PublicationResolutionComplete PublicationResolutionMode = iota
	PublicationResolutionRemove
)

// ResolvePublication refuses on Windows (SOW-0026).
func ResolvePublication(string, *PublicationResult, PublicationResolutionMode, func() error) (PublicationResult, error) {
	return PublicationResult{}, windowsCreateRefusal()
}

// PublicationTuple satisfies the common surface; unreachable on
// Windows.
type PublicationTuple struct{}

// PublicationDigest satisfies the common surface.
type PublicationDigest struct{}

// PublicationResidueCoordination satisfies the common surface.
type PublicationResidueCoordination uint8

const (
	PublicationResidueCoordinationAbsent PublicationResidueCoordination = iota
	PublicationResidueCoordinationPublicationReservation
	PublicationResidueCoordinationLiveSidecar
	PublicationResidueCoordinationUnselectable
)

// PublicationResidueMainContent satisfies the common surface.
type PublicationResidueMainContent uint8

const (
	PublicationResidueMainContentV4 PublicationResidueMainContent = iota
	PublicationResidueMainContentOther
)

// PublicationResidueMain satisfies the common surface.
type PublicationResidueMain struct{}

// PublicationResidueHandle satisfies the common surface; every method
// refuses or is a no-op.
type PublicationResidueHandle struct{}

// Close is a no-op on the Windows stub.
func (h *PublicationResidueHandle) Close() {}

// Remove refuses on Windows (SOW-0026).
func (h *PublicationResidueHandle) Remove(func() error) (*PublicationResidueRemoval, error) {
	return nil, windowsCreateRefusal()
}

// PublicationResidueInspection satisfies the common surface.
type PublicationResidueInspection struct{}

// PublicationResidueRemoval satisfies the common surface.
type PublicationResidueRemoval struct{}

// CleanupState reports clean on the Windows stub.
func (r *PublicationResidueRemoval) CleanupState() CleanupState { return CleanupStateClean }

// InspectPublicationResidue refuses on Windows (SOW-0026).
func InspectPublicationResidue(string, func() error) (*PublicationResidueInspection, error) {
	return nil, windowsCreateRefusal()
}

// RemovePublicationResidue refuses on Windows (SOW-0026).
func RemovePublicationResidue(*PublicationResidueHandle, func() error) (*PublicationResidueRemoval, error) {
	return nil, windowsCreateRefusal()
}

// AbandonedReservationPolicy satisfies the common surface.
type AbandonedReservationPolicy uint8

const (
	AbandonedReservationPolicyFailIfExists AbandonedReservationPolicy = iota
	AbandonedReservationPolicyReplaceExisting
	AbandonedReservationPolicyReplaceExistingNoRollback
)

// AbandonedReservationPhase satisfies the common surface.
type AbandonedReservationPhase uint8

const (
	AbandonedReservationPhasePrepared AbandonedReservationPhase = iota
	AbandonedReservationPhaseMainMayHaveBeenAttempted
)

// PublicationOutputEvidence satisfies the common surface.
type PublicationOutputEvidence struct{}

// AbandonedReservationPrevious satisfies the common surface.
type AbandonedReservationPrevious struct{}

// AbandonedReservationEvidence satisfies the common surface.
type AbandonedReservationEvidence struct{}

// AbandonedReservationEntry satisfies the common surface.
type AbandonedReservationEntry struct{}

// AbandonedReservationList satisfies the common surface.
type AbandonedReservationList struct{}

// AbandonedPublicationTempEntry satisfies the common surface.
type AbandonedPublicationTempEntry struct{}

// AbandonedPublicationTempList satisfies the common surface.
type AbandonedPublicationTempList struct{}

// ErrMaintenanceSinkStop satisfies the common surface (the POSIX
// sentinel lives in the !windows maintenance machine).
var ErrMaintenanceSinkStop = errors.New("publication maintenance sink stopped")

// ListAbandonedPublicationTemps refuses on Windows (SOW-0026).
func ListAbandonedPublicationTemps(string, func() error, func(*AbandonedPublicationTempEntry) error) (AbandonedPublicationTempList, error) {
	return AbandonedPublicationTempList{}, windowsCreateRefusal()
}

// RemoveAbandonedPublicationTemp refuses on Windows (SOW-0026).
func RemoveAbandonedPublicationTemp(string, LocalFileIdentity, [16]byte, LocalFileIdentity, *PublicationTuple, *PublicationDigest, func() error) (AbandonedArtifactRemoval, error) {
	return AbandonedArtifactRemoval{}, windowsCreateRefusal()
}

// ListAbandonedReservationArtifacts refuses on Windows (SOW-0026).
func ListAbandonedReservationArtifacts(string, func() error, func(*AbandonedReservationEntry) error) (AbandonedReservationList, error) {
	return AbandonedReservationList{}, windowsCreateRefusal()
}

// RemoveAbandonedReservationArtifact refuses on Windows (SOW-0026).
func RemoveAbandonedReservationArtifact(string, LocalFileIdentity, [16]byte, LocalFileIdentity, func() error) (AbandonedArtifactRemoval, error) {
	return AbandonedArtifactRemoval{}, windowsCreateRefusal()
}
