//go:build v4work && linux

// Exported public boundary tests (public.go): the residue
// inspect/remove entry points with the handle consume rule, the
// resolution mode mapping over real crash states, and the
// abandoned-artifact maintenance surfaces with the sink-stop
// control. Every cycle pins the process-fd count.

package publication

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// TestPublicResidueInspectAndRemoveMalformedCanonicalResidue ports
// the malformed-residue removal through the exported boundary: the
// unselectable classification, the removal facts, the consumed
// handle rule, and the fd pin.
func TestPublicResidueInspectAndRemoveMalformedCanonicalResidue(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	coordination := residueCoordinationPath(main)
	if err := os.WriteFile(coordination, []byte("malformed"), 0o600); err != nil {
		t.Fatalf("write coordination: %v", err)
	}
	before := countProcessFds(t)
	inspected, err := InspectPublicationResidue(main, noopCheck)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if inspected.Coordination != PublicationResidueCoordinationUnselectable {
		t.Fatalf("coordination = %v, want unselectable", inspected.Coordination)
	}
	if inspected.Publication != nil {
		t.Fatal("publication present for unselectable residue")
	}
	if inspected.CoordinationIdentity == nil {
		t.Fatal("coordination identity missing")
	}
	if inspected.Handle == nil {
		t.Fatal("handle missing for unselectable residue")
	}

	removal, err := RemovePublicationResidue(inspected.Handle, noopCheck)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if removal.CleanupState() != CleanupStateClean {
		t.Fatalf("cleanup state = %v, want clean", removal.CleanupState())
	}
	if removal.LaterCoordination != PublicationResidueCoordinationAbsent {
		t.Fatalf("later coordination = %v, want absent", removal.LaterCoordination)
	}
	if removal.Main != nil {
		t.Fatal("main evidence present, want none")
	}
	if removal.Handle != nil {
		t.Fatal("residual authority present on the clean removal")
	}
	if _, err := os.Lstat(coordination); err == nil {
		t.Fatal("coordination still exists")
	}
	// The handle was consumed by the removal exactly like the Rust
	// move: a second Remove is the invalid-argument refusal.
	if _, err := RemovePublicationResidue(inspected.Handle, noopCheck); codeOf(err) != format.CodeInvalidArgument {
		t.Fatalf("consumed handle problem = %v, want invalid argument", err)
	}
	if after := countProcessFds(t); after > before {
		t.Fatalf("residue cycle left %d descriptors open", after-before)
	}
}

// TestPublicResolvePublicationCompleteAndRemoveOverCrashStates ports
// the resolver Complete and Remove arms through the exported mode
// mapping: Complete resumes the interrupted publication, Remove
// discards both artifacts, and both terminals carry the exact
// machine facts.
func TestPublicResolvePublicationCompleteAndRemoveOverCrashStates(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	runAttemptCrashChild(t, main, "publish", resolverPreMainPoints[1])
	result, err := ResolvePublication(main, nil, PublicationResolutionComplete, noopCheck)
	if err != nil {
		t.Fatalf("resolve complete: %v", err)
	}
	if result.Publication != PublicationPublished {
		t.Fatalf("publication = %v, want published", result.Publication)
	}
	if result.CleanupState() != CleanupStateClean {
		t.Fatalf("cleanup state = %v, want clean", result.CleanupState())
	}
	assertResolverMainReopens(t, main, "complete")

	dir2 := t.TempDir()
	main2 := filepath.Join(dir2, "result.v4")
	runAttemptCrashChild(t, main2, "publish", resolverPreMainPoints[1])
	before := countProcessFds(t)
	result, err = ResolvePublication(main2, nil, PublicationResolutionRemove, noopCheck)
	if err != nil {
		t.Fatalf("resolve remove: %v", err)
	}
	if result.Publication != PublicationNotPublished {
		t.Fatalf("publication = %v, want not published", result.Publication)
	}
	if result.DestinationContent != DestinationContentAbsent {
		t.Fatalf("destination content = %v, want absent", result.DestinationContent)
	}
	if result.CleanupState() != CleanupStateClean {
		t.Fatalf("cleanup state = %v, want clean", result.CleanupState())
	}
	if _, err := os.Lstat(main2); err == nil {
		t.Fatal("main still present after remove")
	}
	assertResolverClean(t, dir2, main2, "remove")
	if after := countProcessFds(t); after > before {
		t.Fatalf("resolution cycle left %d descriptors open", after-before)
	}
}

// TestPublicMaintenanceTempsAndReservationsExactFactsAndSinkStop
// ports the abandoned-artifact listing and removal through the
// exported boundary: the exact tuple/digest evidence, the complete
// and partial removals, the missing evidence refusal, and the
// sink-stop control.
func TestPublicMaintenanceTempsAndReservationsExactFactsAndSinkStop(t *testing.T) {
	dir := t.TempDir()
	before := countProcessFds(t)
	completePath, completeAttempt := maintenanceTestCompleteOutput(t, dir, "result.v4")
	partialID := [16]byte{4}
	partialPath := filepath.Join(dir, maintenanceTestName(t, outputPrefix, partialID))
	if err := os.WriteFile(partialPath, []byte("partial"), 0o600); err != nil {
		t.Fatalf("write partial: %v", err)
	}

	var listed []*AbandonedPublicationTempEntry
	summary, err := ListAbandonedPublicationTemps(dir, noopCheck, func(entry *AbandonedPublicationTempEntry) error {
		listed = append(listed, entry)
		return nil
	})
	if err != nil {
		t.Fatalf("list temps: %v", err)
	}
	if summary.Entries != uint64(len(listed)) {
		t.Fatalf("summary entries %d, want %d", summary.Entries, len(listed))
	}
	var completeEntry, partialEntry *AbandonedPublicationTempEntry
	for _, entry := range listed {
		switch entry.PublicationAttemptID {
		case completeAttempt:
			completeEntry = entry
		case partialID:
			partialEntry = entry
		}
	}
	if completeEntry == nil || partialEntry == nil {
		t.Fatal("listed entries missing")
	}
	if completeEntry.Tuple == nil || completeEntry.Digest == nil {
		t.Fatal("complete entry lacks the readable evidence")
	}
	if completeEntry.Tuple.DatabaseID != testFixtureDBID || completeEntry.Tuple.TransactionID != 1 || completeEntry.Tuple.CommitNonce != testFixtureNonce {
		t.Fatalf("complete entry tuple = %+v, want fixture", completeEntry.Tuple)
	}
	if partialEntry.Tuple != nil || partialEntry.Digest != nil {
		t.Fatal("partial entry carries evidence")
	}

	// Returning the exported stop value ends the scan with the
	// StoppedBySink class; foreign sink errors are SinkFailed.
	if _, err := ListAbandonedPublicationTemps(dir, noopCheck, func(*AbandonedPublicationTempEntry) error {
		return ErrMaintenanceSinkStop
	}); codeOf(err) != format.CodeStoppedBySink {
		t.Fatalf("stop problem = %v, want stopped by sink", err)
	}
	if _, err := ListAbandonedPublicationTemps(dir, noopCheck, func(*AbandonedPublicationTempEntry) error {
		return os.ErrClosed
	}); codeOf(err) != format.CodeSinkFailed {
		t.Fatalf("sink problem = %v, want sink failed", err)
	}

	removal, err := RemoveAbandonedPublicationTemp(dir, completeEntry.DirectoryIdentity, completeEntry.PublicationAttemptID, completeEntry.ArtifactIdentity, completeEntry.Tuple, completeEntry.Digest, noopCheck)
	if err != nil {
		t.Fatalf("remove complete: %v", err)
	}
	if !removal.SourcePresent {
		t.Fatal("complete removal reports source absent")
	}
	if removal.CleanupState != CleanupStateClean {
		t.Fatalf("complete cleanup state = %v, want clean", removal.CleanupState)
	}
	if _, err := os.Lstat(completePath); err == nil {
		t.Fatal("complete output still exists")
	}
	if _, err := RemoveAbandonedPublicationTemp(dir, partialEntry.DirectoryIdentity, partialEntry.PublicationAttemptID, partialEntry.ArtifactIdentity, nil, nil, noopCheck); err != nil {
		t.Fatalf("remove partial: %v", err)
	}
	if _, err := os.Lstat(partialPath); err == nil {
		t.Fatal("partial output still exists")
	}
	if after := countProcessFds(t); after > before {
		t.Fatalf("maintenance cycle left %d descriptors open", after-before)
	}
}

// TestPublicMaintenanceReservationListingMapsExactEvidence ports the
// reservation listing evidence through the exported boundary: the
// fail-if-exists policy/phase/output tuple/digest facts convert
// exactly, and the replacement previous evidence survives the
// mapping.
func TestPublicMaintenanceReservationListingMapsExactEvidence(t *testing.T) {
	failDir := t.TempDir()
	failMain := filepath.Join(failDir, "fail.v4")
	runAttemptCrashChild(t, failMain, "publish", resolverPreMainPoints[0])
	var failEntries []*AbandonedReservationEntry
	failSummary, err := ListAbandonedReservationArtifacts(failDir, noopCheck, func(entry *AbandonedReservationEntry) error {
		failEntries = append(failEntries, entry)
		return nil
	})
	if err != nil {
		t.Fatalf("list fail reservations: %v", err)
	}
	if failSummary.Entries != 1 || len(failEntries) != 1 {
		t.Fatalf("fail summary = %d, listed = %d, want 1", failSummary.Entries, len(failEntries))
	}
	evidence := failEntries[0].Evidence
	if evidence == nil {
		t.Fatal("fail evidence missing")
	}
	if evidence.Policy != AbandonedReservationPolicyFailIfExists {
		t.Fatalf("fail policy = %v, want fail-if-exists", evidence.Policy)
	}
	if evidence.Phase != AbandonedReservationPhasePrepared {
		t.Fatalf("fail phase = %v, want prepared", evidence.Phase)
	}
	if evidence.Output.Tuple.DatabaseID != testFixtureDBID || evidence.Output.Tuple.TransactionID != 1 || evidence.Output.Tuple.CommitNonce != testFixtureNonce {
		t.Fatalf("fail output tuple = %+v, want the fixture identity", evidence.Output.Tuple)
	}
	if evidence.Output.Digest.ByteLength != 2*uint64(format.PageSize) {
		t.Fatalf("fail output digest length = %d, want the two fixture pages", evidence.Output.Digest.ByteLength)
	}
	if evidence.Previous != nil {
		t.Fatal("fail previous evidence present")
	}

	replaceDir := t.TempDir()
	replaceMain := filepath.Join(replaceDir, "replace.v4")
	runAttemptCrashChild(t, replaceMain, "replace", resolverPreMainPoints[0])
	var replaceEntries []*AbandonedReservationEntry
	replaceSummary, err := ListAbandonedReservationArtifacts(replaceDir, noopCheck, func(entry *AbandonedReservationEntry) error {
		replaceEntries = append(replaceEntries, entry)
		return nil
	})
	if err != nil {
		t.Fatalf("list replace reservations: %v", err)
	}
	if replaceSummary.Entries != 1 || len(replaceEntries) != 1 {
		t.Fatalf("replace summary = %d, listed = %d, want 1", replaceSummary.Entries, len(replaceEntries))
	}
	replaceEvidence := replaceEntries[0].Evidence
	if replaceEvidence == nil {
		t.Fatal("replace evidence missing")
	}
	if replaceEvidence.Policy != AbandonedReservationPolicyReplaceExisting {
		t.Fatalf("replace policy = %v, want replace-existing", replaceEvidence.Policy)
	}
	if replaceEvidence.Previous == nil {
		t.Fatal("replace previous evidence missing")
	}
	if replaceEvidence.Previous.Digest.ByteLength != uint64(len("previous bytes")) {
		t.Fatalf("replace previous digest length = %d, want %d", replaceEvidence.Previous.Digest.ByteLength, len("previous bytes"))
	}
}
