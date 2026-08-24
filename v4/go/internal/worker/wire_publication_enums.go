//go:build linux && amd64

// Fixed wire enum tags of the publication facts (Rust
// wire_publication.rs enum_codec section): every tag table of the
// authority, split into its own file so the codec file stays focused.
// The tag functions panic only when the closed Go enum carries an
// impossible discriminant, exactly like the exhaustive Rust match; the
// readers reject unknown tags with the fixed corrupt class.

package worker

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// Enum tag tables (Rust wire_publication.rs enum_codec): the wire tag
// of every fixed enum; the tag functions panic only when the closed Go
// enum carries an impossible discriminant, exactly like the exhaustive
// Rust match.

func publicationPolicyTag(value publication.PublicationPolicy) byte {
	switch value {
	case publication.PolicyFailIfExists:
		return 1
	case publication.PolicyReplaceExisting:
		return 2
	case publication.PolicyReplaceExistingNoRollback:
		return 3
	}
	panic("invalid publication policy")
}

func readPublicationPolicy(value byte) (publication.PublicationPolicy, error) {
	switch value {
	case 1:
		return publication.PolicyFailIfExists, nil
	case 2:
		return publication.PolicyReplaceExisting, nil
	case 3:
		return publication.PolicyReplaceExistingNoRollback, nil
	}
	return 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker publication enum is invalid"}
}

func publicationStatusTag(value publication.PublicationStatus) byte {
	switch value {
	case publication.PublicationNotPublished:
		return 1
	case publication.PublicationPublished:
		return 2
	case publication.PublicationOutcomeUnknown:
		return 3
	}
	panic("invalid publication status")
}

func readPublicationStatus(value byte) (publication.PublicationStatus, error) {
	switch value {
	case 1:
		return publication.PublicationNotPublished, nil
	case 2:
		return publication.PublicationPublished, nil
	case 3:
		return publication.PublicationOutcomeUnknown, nil
	}
	return 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker publication enum is invalid"}
}

func destinationContentTag(value publication.DestinationContent) byte {
	switch value {
	case publication.DestinationContentDesired:
		return 1
	case publication.DestinationContentPrevious:
		return 2
	case publication.DestinationContentAbsent:
		return 3
	case publication.DestinationContentOther:
		return 4
	case publication.DestinationContentUnclassified:
		return 5
	}
	panic("invalid destination content")
}

func readDestinationContent(value byte) (publication.DestinationContent, error) {
	switch value {
	case 1:
		return publication.DestinationContentDesired, nil
	case 2:
		return publication.DestinationContentPrevious, nil
	case 3:
		return publication.DestinationContentAbsent, nil
	case 4:
		return publication.DestinationContentOther, nil
	case 5:
		return publication.DestinationContentUnclassified, nil
	}
	return 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker publication enum is invalid"}
}

func laterCanonicalTag(value publication.LaterCanonical) byte {
	switch value {
	case publication.LaterCanonicalNone:
		return 1
	case publication.LaterCanonicalReservationOrTransition:
		return 2
	case publication.LaterCanonicalReadyLiveSidecar:
		return 3
	}
	panic("invalid later canonical")
}

func readLaterCanonical(value byte) (publication.LaterCanonical, error) {
	switch value {
	case 1:
		return publication.LaterCanonicalNone, nil
	case 2:
		return publication.LaterCanonicalReservationOrTransition, nil
	case 3:
		return publication.LaterCanonicalReadyLiveSidecar, nil
	}
	return 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker publication enum is invalid"}
}

func liveLineageTag(value publication.LiveLineage) byte {
	switch value {
	case publication.LiveLineageSameGenerationExactBytes:
		return 1
	case publication.LiveLineageSameGenerationPhysicalBytesChanged:
		return 2
	case publication.LiveLineageAdvancedGeneration:
		return 3
	}
	panic("invalid live lineage")
}

func readLiveLineage(value byte) (publication.LiveLineage, error) {
	switch value {
	case 1:
		return publication.LiveLineageSameGenerationExactBytes, nil
	case 2:
		return publication.LiveLineageSameGenerationPhysicalBytesChanged, nil
	case 3:
		return publication.LiveLineageAdvancedGeneration, nil
	}
	return 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker publication enum is invalid"}
}

func accessPolicyTag(value publication.AccessPolicy) byte {
	switch value {
	case publication.AccessPolicyAbsent:
		return 1
	case publication.AccessPolicyCreatorOnly:
		return 2
	case publication.AccessPolicyChangedOrUnproven:
		return 3
	case publication.AccessPolicyUnclassified:
		return 4
	}
	panic("invalid access policy")
}

func readAccessPolicy(value byte) (publication.AccessPolicy, error) {
	switch value {
	case 1:
		return publication.AccessPolicyAbsent, nil
	case 2:
		return publication.AccessPolicyCreatorOnly, nil
	case 3:
		return publication.AccessPolicyChangedOrUnproven, nil
	case 4:
		return publication.AccessPolicyUnclassified, nil
	}
	return 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker publication enum is invalid"}
}

func coordinationCleanupTag(value publication.CoordinationCleanup) byte {
	switch value {
	case publication.CoordinationCleanupNone:
		return 1
	case publication.CoordinationCleanupCleanupGuard:
		return 2
	case publication.CoordinationCleanupRetainedReaderCloseRequired:
		return 3
	case publication.CoordinationCleanupRetainedWriterCloseRequired:
		return 4
	}
	panic("invalid coordination cleanup")
}

func readCoordinationCleanup(value byte) (publication.CoordinationCleanup, error) {
	switch value {
	case 1:
		return publication.CoordinationCleanupNone, nil
	case 2:
		return publication.CoordinationCleanupCleanupGuard, nil
	case 3:
		return publication.CoordinationCleanupRetainedReaderCloseRequired, nil
	case 4:
		return publication.CoordinationCleanupRetainedWriterCloseRequired, nil
	}
	return 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker publication enum is invalid"}
}

func housekeepingTag(value publication.Housekeeping) byte {
	switch value {
	case publication.HousekeepingNone:
		return 1
	case publication.HousekeepingCrashReappearancePossible:
		return 2
	case publication.HousekeepingVisible:
		return 3
	}
	panic("invalid housekeeping")
}

func readHousekeeping(value byte) (publication.Housekeeping, error) {
	switch value {
	case 1:
		return publication.HousekeepingNone, nil
	case 2:
		return publication.HousekeepingCrashReappearancePossible, nil
	case 3:
		return publication.HousekeepingVisible, nil
	}
	return 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker publication enum is invalid"}
}

func artifactKindTag(value publication.ArtifactKind) byte {
	switch value {
	case publication.ArtifactPrivateOutput:
		return 1
	case publication.ArtifactPrivateReservation:
		return 2
	case publication.ArtifactOwnedCoordination:
		return 3
	case publication.ArtifactAuthorizedScratch:
		return 4
	case publication.ArtifactOwnedMain:
		return 5
	case publication.ArtifactUnpublishedMainTail:
		return 6
	}
	panic("invalid artifact kind")
}

func readArtifactKind(value byte) (publication.ArtifactKind, error) {
	switch value {
	case 1:
		return publication.ArtifactPrivateOutput, nil
	case 2:
		return publication.ArtifactPrivateReservation, nil
	case 3:
		return publication.ArtifactOwnedCoordination, nil
	case 4:
		return publication.ArtifactAuthorizedScratch, nil
	case 5:
		return publication.ArtifactOwnedMain, nil
	case 6:
		return publication.ArtifactUnpublishedMainTail, nil
	}
	return 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker publication enum is invalid"}
}

func directoryRoleTag(value publication.DirectoryRole) byte {
	switch value {
	case publication.DirectoryRoleDestination:
		return 1
	case publication.DirectoryRoleScratchDirectory:
		return 2
	case publication.DirectoryRoleMainFile:
		return 3
	}
	panic("invalid directory role")
}

func readDirectoryRole(value byte) (publication.DirectoryRole, error) {
	switch value {
	case 1:
		return publication.DirectoryRoleDestination, nil
	case 2:
		return publication.DirectoryRoleScratchDirectory, nil
	case 3:
		return publication.DirectoryRoleMainFile, nil
	}
	return 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker publication enum is invalid"}
}

func housekeepingStateTag(value publication.HousekeepingState) byte {
	switch value {
	case publication.HousekeepingMovePending:
		return 1
	case publication.HousekeepingMoveAmbiguous:
		return 2
	case publication.HousekeepingInert:
		return 3
	case publication.HousekeepingConflict:
		return 4
	}
	panic("invalid housekeeping state")
}

func readHousekeepingState(value byte) (publication.HousekeepingState, error) {
	switch value {
	case 1:
		return publication.HousekeepingMovePending, nil
	case 2:
		return publication.HousekeepingMoveAmbiguous, nil
	case 3:
		return publication.HousekeepingInert, nil
	case 4:
		return publication.HousekeepingConflict, nil
	}
	return 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker publication enum is invalid"}
}

func artifactPresenceTag(value publication.ArtifactPresence) byte {
	switch value {
	case publication.ArtifactAbsent:
		return 1
	case publication.ArtifactPresent:
		return 2
	case publication.ArtifactUnclassified:
		return 3
	}
	panic("invalid artifact presence")
}

func readArtifactPresence(value byte) (publication.ArtifactPresence, error) {
	switch value {
	case 1:
		return publication.ArtifactAbsent, nil
	case 2:
		return publication.ArtifactPresent, nil
	case 3:
		return publication.ArtifactUnclassified, nil
	}
	return 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker publication enum is invalid"}
}
