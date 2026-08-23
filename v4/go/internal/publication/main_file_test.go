//go:build linux

// Main-file publication and retirement tests (Rust
// publication/main_file_tests.rs arms): the publish steps per policy,
// the failure-after-rename ambiguous main, and the exact retirement
// link-count conflicts with their cleanup facts. A hard alias before
// retirement trips the single-link custody rule of the verify stage
// exactly like Rust (regular_identity / verify_name return
// NamespaceError::LinkCount before any unlink); the post-unlink
// PreviousLinkCount/ReservationLinkCount classes remain the race
// guards of both implementations and are pinned here through their
// problem mapping.

package publication

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

// testArmedAttempt builds one fully prepared output and its armed
// reservation inside dir (Rust armed_attempt fixture).
func testArmedAttempt(t *testing.T, dir string) (*preparedOutput, armedReservation) {
	t.Helper()
	prepared := cleanupTestPrepared(t, dir, "result.v4")
	draft, err := createReservationDraft(prepared)
	if err != nil {
		prepared.Close()
		t.Fatalf("create reservation draft: %v", err)
	}
	private, initFailure := draft.initialize(prepared)
	if initFailure != nil {
		prepared.Close()
		t.Fatalf("initialize reservation: %v", initFailure)
	}
	canonical, acquireFailure := private.acquire(prepared)
	if acquireFailure != nil {
		prepared.Close()
		_ = private.Close()
		t.Fatalf("acquire reservation: %v", acquireFailure)
	}
	armed, armFailure := canonical.arm(prepared)
	if armFailure != nil {
		prepared.Close()
		_ = canonical.Close()
		t.Fatalf("arm reservation: %v", armFailure)
	}
	return prepared, armed
}

func TestMainPublishesAndRetiresClean(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	prepared, armed := testArmedAttempt(t, dir)
	defer armed.Close()

	mainName := d.mainName()
	if _, present, err := d.directory().Entry(mainName); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("main name exists before publication")
	}

	published, failure := publishProved(prepared, armed)
	if failure != nil {
		t.Fatalf("publish: %v", failure)
	}
	if published.output != prepared || published.reservation != armed {
		t.Fatal("published main does not carry the published owners")
	}
	// The main name now names the output inode.
	entry, present, err := d.directory().Entry(mainName)
	if err != nil {
		t.Fatalf("entry: %v", err)
	}
	if !present {
		t.Fatal("main name missing after publication")
	}
	if entry.Identity != prepared.attempt.identityOf() {
		t.Fatal("main name does not name the output inode")
	}

	retired, retireFailure := published.retire()
	if retireFailure != nil {
		t.Fatalf("retire: %v", retireFailure)
	}
	if retired.reservationIdentity != armed.identity {
		t.Fatal("retired reservation identity differs from the armed reservation")
	}
	if retired.housekeeping != HousekeepingNone || len(retired.visibleHousekeeping) != 0 {
		t.Fatalf("retirement housekeeping = (%v, %v), want none", retired.housekeeping, retired.visibleHousekeeping)
	}
	// The coordination name is gone; the main is intact.
	if _, present, err := d.directory().Entry(d.coordinationName()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("coordination name still present after retirement")
	}
	if _, present, err := d.directory().Entry(mainName); err != nil {
		t.Fatalf("entry: %v", err)
	} else if !present {
		t.Fatal("main name removed by retirement")
	}
}

func TestMainFailureAfterRenameRetainsAmbiguousCompleteMain(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	prepared, armed := testArmedAttempt(t, dir)
	defer armed.Close()

	// The checkpoint fails after the main rename: the main is in
	// place but the desired proof never ran.
	injected := problem(format.CodeIO, "injected main checkpoint failure")
	published, failure := publishObserved(prepared, armed, func(point mainPoint) error {
		if point == mainPointMainSynced {
			return injected
		}
		return nil
	})
	if failure == nil {
		t.Fatal("publish succeeded despite the injected checkpoint failure")
	}
	if published.output != nil || published.reservation.file != nil {
		t.Fatal("failed publish returned a published main")
	}
	if failure.owner.desiredProven {
		t.Fatal("desired proof ran despite the checkpoint failure")
	}
	entry, present, err := d.directory().Entry(d.mainName())
	if err != nil {
		t.Fatalf("entry: %v", err)
	}
	if !present {
		t.Fatal("main name missing after the post-rename failure")
	}
	if entry.Identity != prepared.attempt.identityOf() {
		t.Fatal("post-rename main does not carry the output identity")
	}
}

func TestMainReservationLinkCountConflict(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	prepared, armed := testArmedAttempt(t, dir)
	defer armed.Close()
	published, failure := publishProved(prepared, armed)
	if failure != nil {
		t.Fatalf("publish: %v", failure)
	}
	// A hard alias on the coordination name increases the reservation
	// inode link count, tripping the single-link custody rule of the
	// verify stage before any unlink (Rust regular_identity returns
	// NamespaceError::LinkCount at verify_published).
	coordination := d.coordinationName()
	if err := os.Link(filepath.Join(dir, coordination), filepath.Join(dir, "alias.tmp")); err != nil {
		t.Fatalf("link coordination alias: %v", err)
	}
	_, retireFailure := published.retire()
	if retireFailure == nil {
		t.Fatal("retire succeeded despite the reservation alias")
	}
	var nerr *live.NamespaceError
	if !errors.As(retireFailure.cause, &nerr) || nerr.Kind != live.NamespaceLinkCount || nerr.Links != 2 {
		t.Fatalf("retire cause = %v, want namespace link-count(2)", retireFailure.cause)
	}
	if retireFailure.owner.reservationUnlinked {
		t.Fatal("reservation name was unlinked despite the verify-stage conflict")
	}
	if retireFailure.owner.previousRetiredProven {
		t.Fatal("previous retirement proven despite the verify-stage conflict")
	}
	// Nothing was unlinked: the coordination name, alias, and main are
	// all intact and the reservation still carries both links.
	if _, present, err := d.directory().Entry(coordination); err != nil {
		t.Fatalf("entry: %v", err)
	} else if !present {
		t.Fatal("coordination name removed by the conflicted retire")
	}
	if _, present, err := d.directory().Entry(filepath.Join(dir, "alias.tmp")); err != nil {
		t.Fatalf("entry: %v", err)
	} else if !present {
		t.Fatal("reservation alias removed by the conflicted retire")
	}
	if _, present, err := d.directory().Entry(d.mainName()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if !present {
		t.Fatal("main name removed by the conflicted retire")
	}
	if count, err := live.RegularLinkCount(armed.file); err != nil {
		t.Fatalf("link count: %v", err)
	} else if count != 2 {
		t.Fatalf("reservation links %d after conflict, want 2", count)
	}
}

// TestCleanupConflictProblemMapping pins the problem surface of the
// post-unlink retirement race arms (Rust Error::PreviousLinkCount /
// Error::ReservationLinkCount). Both implementations reach them only
// when an alias races in between the verified single-link state and
// the post-unlink proof, so the arm text is pinned here directly.
func TestCleanupConflictProblemMapping(t *testing.T) {
	assertProblemCodeDetail(t, cleanupConflictProblem("retired previous destination still has a link"), format.CodeCleanupConflict, "retired previous destination still has a link")
	assertProblemCodeDetail(t, cleanupConflictProblem("retired reservation still has a link"), format.CodeCleanupConflict, "retired reservation still has a link")
}

// testArmedReplacementAttempt builds one fully prepared replacement
// output bound to the destination main and arms its reservation (Rust
// armed_replacement_attempt fixture).
func testArmedReplacementAttempt(t *testing.T, dir string) (*preparedOutput, armedReservation) {
	t.Helper()
	prepared := cleanupTestPrepared(t, dir, "result.v4")
	bound, bindFailure := bindPrevious(prepared, func() error { return nil })
	if bindFailure != nil {
		prepared.Close()
		t.Fatalf("bind previous: %v", bindFailure)
	}
	draft, err := createReservationDraft(bound)
	if err != nil {
		bound.Close()
		t.Fatalf("create reservation draft: %v", err)
	}
	private, initFailure := draft.initialize(bound)
	if initFailure != nil {
		bound.Close()
		t.Fatalf("initialize reservation: %v", initFailure)
	}
	canonical, acquireFailure := private.acquire(bound)
	if acquireFailure != nil {
		bound.Close()
		_ = private.Close()
		t.Fatalf("acquire reservation: %v", acquireFailure)
	}
	armed, armFailure := canonical.arm(bound)
	if armFailure != nil {
		bound.Close()
		_ = canonical.Close()
		t.Fatalf("arm reservation: %v", armFailure)
	}
	return bound, armed
}

func TestMainPublishExchangeRetiresPrevious(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	// v1: the fail-if-exists publish makes the previous main.
	first := cleanupTestPrepared(t, dir, "result.v4")
	result, prepareFailure := failIfExistsCancellable(first, func() error { return nil })
	if prepareFailure != nil {
		t.Fatalf("v1 publish preparation: %v", prepareFailure)
	}
	if result.Publication != PublicationPublished {
		t.Fatalf("v1 publication = %v, want published", result.Publication)
	}
	firstIdentity := first.attempt.identityOf()
	// Close the v1 owner so the v2 previous binding can take the
	// exclusive lifetime lock on the v1 inode (Rust drops the v1
	// PublishedOutput guard when the caller releases its result).
	if err := first.Close(); err != nil {
		t.Fatalf("close v1: %v", err)
	}

	// v2: the replacement exchange moves v1 under v2's private name.
	second, armed := testArmedReplacementAttempt(t, dir)
	defer armed.Close()
	if second.policy != reservationPolicyReplaceExisting {
		t.Fatalf("v2 policy = %v, want replace existing", second.policy)
	}
	published, failure := publishProved(second, armed)
	if failure != nil {
		t.Fatalf("v2 publish: %v", failure)
	}
	// v2's private name now names the v1 inode.
	entry, present, err := d.directory().Entry(second.attempt.nameOf())
	if err != nil {
		t.Fatalf("entry: %v", err)
	}
	if !present {
		t.Fatal("v2 private name missing after the exchange")
	}
	if entry.Identity != firstIdentity {
		t.Fatal("v2 private name does not carry the v1 inode")
	}

	retired, retireFailure := published.retire()
	if retireFailure != nil {
		t.Fatalf("retire: %v", retireFailure)
	}
	if retired.reservationIdentity != armed.identity {
		t.Fatal("retired reservation identity differs from the armed reservation")
	}
	// The previous private name is gone; the main carries v2.
	if _, present, err := d.directory().Entry(second.attempt.nameOf()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("previous private name still present after retirement")
	}
	mainEntry, present, err := d.directory().Entry(d.mainName())
	if err != nil {
		t.Fatalf("entry: %v", err)
	}
	if !present {
		t.Fatal("main name missing after replacement retirement")
	}
	if mainEntry.Identity != second.attempt.identityOf() {
		t.Fatal("main does not carry the v2 output inode")
	}
}

func TestMainRetirePreviousLinkCountConflict(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	first := cleanupTestPrepared(t, dir, "result.v4")
	result, prepareFailure := failIfExistsCancellable(first, func() error { return nil })
	if prepareFailure != nil || result.Publication != PublicationPublished {
		t.Fatalf("v1 publish: %v %v", result.Publication, prepareFailure)
	}
	// Close the v1 owner so the v2 previous binding can take the
	// exclusive lifetime lock on the v1 inode.
	if err := first.Close(); err != nil {
		t.Fatalf("close v1: %v", err)
	}
	second, armed := testArmedReplacementAttempt(t, dir)
	defer armed.Close()
	published, failure := publishProved(second, armed)
	if failure != nil {
		t.Fatalf("v2 publish: %v", failure)
	}
	// A hard alias on the previous private name raises the previous
	// inode link count, tripping the single-link custody rule inside
	// the verify stage before any unlink (Rust
	// PreviousMain::verify_private_or_retired returns
	// NamespaceError::LinkCount at verify_published).
	privateName := second.attempt.nameOf()
	if err := os.Link(filepath.Join(dir, privateName), filepath.Join(dir, "alias.tmp")); err != nil {
		t.Fatalf("link previous alias: %v", err)
	}
	_, retireFailure := published.retire()
	if retireFailure == nil {
		t.Fatal("retire succeeded despite the previous alias")
	}
	var nerr *live.NamespaceError
	if !errors.As(retireFailure.cause, &nerr) || nerr.Kind != live.NamespaceLinkCount || nerr.Links != 2 {
		t.Fatalf("retire cause = %v, want namespace link-count(2)", retireFailure.cause)
	}
	if retireFailure.owner.previousUnlinked {
		t.Fatal("previous name was unlinked despite the verify-stage conflict")
	}
	if retireFailure.owner.previousRetiredProven {
		t.Fatal("previous retirement proven despite the verify-stage conflict")
	}
	if retireFailure.owner.reservationUnlinked {
		t.Fatal("reservation name unlinked despite the verify-stage conflict")
	}
	// Nothing was unlinked: the previous private name, alias, main
	// (v2), and coordination are all intact.
	if _, present, err := d.directory().Entry(privateName); err != nil {
		t.Fatalf("entry: %v", err)
	} else if !present {
		t.Fatal("previous private name removed by the conflicted retire")
	}
	if _, present, err := d.directory().Entry(filepath.Join(dir, "alias.tmp")); err != nil {
		t.Fatalf("entry: %v", err)
	} else if !present {
		t.Fatal("previous alias removed by the conflicted retire")
	}
	if _, present, err := d.directory().Entry(d.mainName()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if !present {
		t.Fatal("main name removed by the conflicted retire")
	}
	if _, present, err := d.directory().Entry(d.coordinationName()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if !present {
		t.Fatal("coordination name removed by the conflicted retire")
	}
}
