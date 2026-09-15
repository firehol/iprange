//go:build windows

package publication

import (
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// resumePlatform resumes one abandoned retirement through its GC
// envelope (Rust Artifact::resume_windows): an envelope that exists for
// this attempt and ordinal means an earlier removal moved the artifact
// and died before finishing, so the envelope — not a fresh unlink —
// decides the outcome. ok is false when no envelope exists and the
// ordinary owned open must run.
func (a *maintenanceArtifact) resumePlatform(dir *live.Directory, attempt [16]byte, name string, expected live.FileIdentity, ordinal uint32, kind live.ArtifactKind, payload *maintenanceRetirementPayload, sourcePresent bool) (AbandonedArtifactRemoval, bool) {
	retired, err := live.GCResume(dir, &live.GCResumeAuthority{
		AttemptID:     attempt,
		Ordinal:       ordinal,
		Kind:          kind,
		DirectoryRole: DirectoryRoleDestination,
		SourceName:    name,
		Identity:      expected,
		Payload:       maintenanceGCPayloadOf(payload),
	})
	if err != nil {
		return removalResult(sourcePresent, err), true
	}
	if retired == nil {
		return AbandonedArtifactRemoval{}, false
	}
	return retirementResult(sourcePresent, retired.Housekeeping, gcVisibleArtifacts(retired.Visible), retired.Problem), true
}

// retirePlatform retires the artifact through its attempt-bound GC
// envelope (Rust Artifact::retire_windows). Windows cannot unlink a
// name whose handle is retained, so the removal is a rename the GC
// machine drives through the owned handle: the creator-only policy of
// the retained file is proved and committed into the envelope together
// with the retirement authority (attempt, ordinal, kind, destination
// role, source name, identity, and the exact payload evidence), and the
// housekeeping facts the resolver reports become the removal outcome.
func (a *maintenanceArtifact) retirePlatform(dir *live.Directory, name string, regular *live.RegularFile, attempt [16]byte, expected live.FileIdentity, ordinal uint32, kind live.ArtifactKind, payload *maintenanceRetirementPayload) (AbandonedArtifactRemoval, error) {
	commitment, err := security.CreatorOnlyCommitment(regular.File)
	if err != nil {
		return AbandonedArtifactRemoval{}, a.namespaceError(err)
	}
	retirement := live.GCRetire(dir, &live.GCAuthority{
		AttemptID:     attempt,
		Ordinal:       ordinal,
		Kind:          kind,
		DirectoryRole: DirectoryRoleDestination,
		SourceName:    name,
		SourceFile:    regular.File,
		Identity:      expected,
		CreationSecurity: CreationSecurity{
			Kind:       creationSecurityKind,
			Commitment: commitment,
		},
		Payload: maintenanceGCPayloadOf(payload),
	})
	return retirementResult(true, retirement.Housekeeping, gcVisibleArtifacts(retirement.Visible), retirement.Problem), nil
}

// maintenanceGCPayloadOf converts one retirement payload to the GC
// envelope payload; nil means the retirement carries no content
// evidence, exactly like the Rust None arm.
func maintenanceGCPayloadOf(payload *maintenanceRetirementPayload) *live.GCPayload {
	if payload == nil {
		return nil
	}
	return &live.GCPayload{
		ByteLength:    payload.byteLength,
		SHA512:        payload.sha512,
		DatabaseID:    payload.databaseID,
		TransactionID: payload.transactionID,
		CommitNonce:   payload.commitNonce,
	}
}

// gcVisibleArtifacts lifts the optional visible housekeeping artifact of
// one retirement into the ledger slice the removal reports.
func gcVisibleArtifacts(visible *HousekeepingArtifact) []HousekeepingArtifact {
	if visible == nil {
		return nil
	}
	return []HousekeepingArtifact{*visible}
}
