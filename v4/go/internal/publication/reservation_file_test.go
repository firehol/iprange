//go:build linux

// Reservation lifecycle tests (Rust reservation_file_tests.rs port):
// the draft -> private -> canonical -> armed transitions, the exact
// header and operation lock, the conflict and tampering refusals with
// their failure owners, and the resume-armed evidence gate.

package publication

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

func TestInitializeReservationHasExactHeaderSecurityAndLock(t *testing.T) {
	dir := t.TempDir()
	prepared, _, _ := prepareTestOutput(t, dir)
	draft, err := createReservationDraft(prepared)
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	privatePath := filepath.Join(dir, draft.name)
	reservation, failure := draft.initialize(prepared)
	if failure != nil {
		t.Fatalf("initialize: %v", failure)
	}
	defer reservation.Close()

	st, err := reservation.file.Stat()
	if err != nil {
		t.Fatalf("stat reservation: %v", err)
	}
	if st.Size() != reservationFileSize {
		t.Fatalf("reservation size %d, want %d", st.Size(), reservationFileSize)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("reservation mode %o, want 600", st.Mode().Perm())
	}
	if reservation.header.reservationIdentity != reservationIdentityBytes(reservation.identity) {
		t.Fatal("header reservation identity differs from the inode identity")
	}
	if reservation.header.outputIdentity != reservationIdentityBytes(prepared.attempt.identityOf()) {
		t.Fatal("header output identity differs from the attempt identity")
	}
	if reservation.header.outputSHA512 != prepared.sha512 {
		t.Fatal("header output sha512 differs from the prepared digest")
	}
	if reservation.header.basenameLen != uint32(len("result.v4")) {
		t.Fatalf("header basename length %d, want %d", reservation.header.basenameLen, len("result.v4"))
	}
	if err := selectExact(reservation.mapping, reservation.header, 0); err != nil {
		t.Fatalf("select state 1: %v", err)
	}

	contender, err := os.OpenFile(privatePath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open contender: %v", err)
	}
	defer contender.Close()
	acquired, err := live.TryLockFile(contender, reservationOperationLock, live.LockExclusive)
	if err != nil {
		t.Fatalf("contender lock: %v", err)
	}
	if acquired {
		t.Fatal("contender must not acquire the operation lock while initialized")
	}
}

func TestAcquireAndArmKeepOneInodeAndSelectState2(t *testing.T) {
	dir := t.TempDir()
	prepared, _, _ := prepareTestOutput(t, dir)
	draft, err := createReservationDraft(prepared)
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	private, initFailure := draft.initialize(prepared)
	if initFailure != nil {
		t.Fatalf("initialize: %v", initFailure)
	}
	expectedIdentity := private.identity
	privatePath := filepath.Join(dir, private.name)
	canonicalPath := filepath.Join(dir, "result.v4.readers")

	canonical, acquireFailure := private.acquire(prepared)
	if acquireFailure != nil {
		t.Fatalf("acquire: %v", acquireFailure)
	}
	defer canonical.Close()
	if _, err := os.Lstat(privatePath); !os.IsNotExist(err) {
		t.Fatalf("private reservation still exists after acquire: %v", err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(canonical.file.Fd()), &st); err != nil {
		t.Fatalf("stat canonical: %v", err)
	}
	if live.IdentityFromDeviceInode(uint64(st.Dev), uint64(st.Ino)) != expectedIdentity {
		t.Fatal("canonical reservation identity differs from the acquired inode")
	}
	if err := selectExact(canonical.mapping, canonical.header, 0); err != nil {
		t.Fatalf("select state 1 at canonical: %v", err)
	}

	armed, armFailure := canonical.arm(prepared)
	if armFailure != nil {
		t.Fatalf("arm: %v", armFailure)
	}
	defer armed.Close()
	if armed.identity != expectedIdentity {
		t.Fatal("armed identity differs from the acquired inode")
	}
	if armed.header.state != reservationStateMainMayHaveBeenAttempted {
		t.Fatalf("armed state %d, want MainMayHaveBeenAttempted", armed.header.state)
	}
	if armed.header.sequence != 2 {
		t.Fatalf("armed sequence %d, want 2", armed.header.sequence)
	}
	if err := selectExact(armed.mapping, armed.header, 1); err != nil {
		t.Fatalf("select state 2: %v", err)
	}
	if err := prepared.verifyPrivate(); err != nil {
		t.Fatalf("verifyPrivate after arming: %v", err)
	}

	contender, err := os.OpenFile(canonicalPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open contender: %v", err)
	}
	defer contender.Close()
	acquired, err := live.TryLockFile(contender, reservationOperationLock, live.LockExclusive)
	if err != nil {
		t.Fatalf("contender lock: %v", err)
	}
	if acquired {
		t.Fatal("contender must not acquire the operation lock once armed")
	}
}

func TestResumeArmedRequiresState2(t *testing.T) {
	// A Prepared,1 canonical reservation is not resumable: resume_armed
	// demands the durable state-2 selection.
	dir := t.TempDir()
	prepared, _, _ := prepareTestOutput(t, dir)
	draft, err := createReservationDraft(prepared)
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	private, initFailure := draft.initialize(prepared)
	if initFailure != nil {
		t.Fatalf("initialize: %v", initFailure)
	}
	canonical, acquireFailure := private.acquire(prepared)
	if acquireFailure != nil {
		t.Fatalf("acquire: %v", acquireFailure)
	}
	resumed, resumeFailure := canonical.resumeArmed(prepared)
	if resumeFailure == nil {
		t.Fatal("resumeArmed on a Prepared reservation must fail")
	}
	fe, ok := resumeFailure.cause.(*format.Error)
	if !ok || fe.Code != format.CodeFormatInvalid || fe.Detail != "reservation state is inconsistent" {
		t.Fatalf("resumeArmed refusal = %v, want header invariant", resumeFailure.cause)
	}
	if resumed.file != nil || resumed.mapping != nil {
		t.Fatal("resumeArmed returned a reservation on refusal")
	}
	resumeFailure.owner.Close()

	// Reconstructing the canonical reservation from the on-disk state
	// after arm (the future discover path) makes it resumable.
	dir2 := t.TempDir()
	prepared2, _, _ := prepareTestOutput(t, dir2)
	draft2, err := createReservationDraft(prepared2)
	if err != nil {
		t.Fatalf("create second draft: %v", err)
	}
	private2, initFailure2 := draft2.initialize(prepared2)
	if initFailure2 != nil {
		t.Fatalf("initialize second: %v", initFailure2)
	}
	canonical2, acquireFailure2 := private2.acquire(prepared2)
	if acquireFailure2 != nil {
		t.Fatalf("acquire second: %v", acquireFailure2)
	}
	armed2, armFailure := canonical2.arm(prepared2)
	if armFailure != nil {
		t.Fatalf("arm second: %v", armFailure)
	}
	canonical3 := reopenCanonicalReservation(t, filepath.Join(dir2, "result.v4.readers"), private2.name)
	resumed2, resumeFailure2 := canonical3.resumeArmed(prepared2)
	if resumeFailure2 != nil {
		t.Fatalf("resumeArmed after state 2: %v", resumeFailure2)
	}
	if err := selectExact(resumed2.mapping, resumed2.header, 1); err != nil {
		t.Fatalf("resumed reservation does not select state 2: %v", err)
	}
	resumed2.Close()
	armed2.Close()
}

// reopenCanonicalReservation rebuilds one canonical reservation owner
// from the on-disk coordination artifact (the slice-G discover path
// composes the same codec and identity facts).
func reopenCanonicalReservation(t *testing.T, canonicalPath, privateName string) *canonicalReservation {
	t.Helper()
	f, err := os.OpenFile(canonicalPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open canonical reservation: %v", err)
	}
	mapped, err := mapping.MapFile(f, reservationFileSize, true)
	if err != nil {
		_ = f.Close()
		t.Fatalf("map canonical reservation: %v", err)
	}
	view, err := mapped.View(0, reservationFileSize)
	if err != nil {
		t.Fatalf("view canonical reservation: %v", err)
	}
	selected, err := selectReservation(view)
	if err != nil {
		t.Fatalf("select canonical reservation: %v", err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		t.Fatalf("stat canonical reservation: %v", err)
	}
	return &canonicalReservation{
		name:     privateName,
		file:     f,
		mapping:  mapped,
		identity: live.IdentityFromDeviceInode(uint64(st.Dev), uint64(st.Ino)),
		header:   selected.header,
	}
}

func TestCanonicalConflictNeverOverwritesAndReturnsPrivateOwner(t *testing.T) {
	dir := t.TempDir()
	prepared, _, _ := prepareTestOutput(t, dir)
	draft, err := createReservationDraft(prepared)
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	private, initFailure := draft.initialize(prepared)
	if initFailure != nil {
		t.Fatalf("initialize: %v", initFailure)
	}
	privatePath := filepath.Join(dir, private.name)
	canonicalPath := filepath.Join(dir, "result.v4.readers")
	if err := os.WriteFile(canonicalPath, []byte("foreign"), 0o600); err != nil {
		t.Fatalf("write foreign canonical: %v", err)
	}

	_, acquireFailure := private.acquire(prepared)
	if acquireFailure == nil {
		t.Fatal("acquire over an existing canonical name must fail")
	}
	if !acquireFailure.owner.namespaceCallStarted {
		t.Fatal("acquire failure owner must record the namespace call start")
	}
	nerr, ok := live.AsNamespaceError(acquireFailure.cause)
	if !ok || nerr.Kind != live.NamespaceExists {
		t.Fatalf("acquire refusal = %v, want Exists", acquireFailure.cause)
	}
	if _, err := os.Lstat(privatePath); err != nil {
		t.Fatalf("private reservation must survive the conflict: %v", err)
	}
	content, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatalf("read foreign canonical: %v", err)
	}
	if string(content) != "foreign" {
		t.Fatalf("foreign canonical content changed to %q", content)
	}
	if err := selectExact(acquireFailure.owner.reservation.mapping, acquireFailure.owner.reservation.header, 0); err != nil {
		t.Fatalf("conflict owner reservation no longer selects state 1: %v", err)
	}
	acquireFailure.owner.reservation.Close()
}

func TestInitializationFailureReturnsCreatedReservation(t *testing.T) {
	dir := t.TempDir()
	prepared, privatePath, _ := prepareTestOutput(t, dir)
	draft, err := createReservationDraft(prepared)
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	reservationPath := filepath.Join(dir, draft.name)
	if err := os.Chmod(privatePath, 0o640); err != nil {
		t.Fatalf("chmod private output: %v", err)
	}

	failure := func() *reservationDraftFailure {
		_, failure := draft.initialize(prepared)
		return failure
	}()
	if failure == nil {
		t.Fatal("initialize over a re-permissioned output must fail")
	}
	nerr, ok := live.AsNamespaceError(failure.cause)
	if !ok || nerr.Kind != live.NamespaceAccessPolicy {
		t.Fatalf("initialize refusal = %v, want access policy", failure.cause)
	}
	if _, err := os.Lstat(reservationPath); err != nil {
		t.Fatalf("created reservation must survive the failure: %v", err)
	}
	st, err := failure.owner.file.Stat()
	if err != nil {
		t.Fatalf("stat failure-owner reservation: %v", err)
	}
	if st.Size() != 0 {
		t.Fatalf("failure-owner reservation size %d, want 0 (never truncated)", st.Size())
	}
	if failure.owner.state1Selected {
		t.Fatal("failure owner must not record a state-1 selection")
	}
	failure.owner.Close()
}

func TestHardLinkedPrivateReservationFailsClosed(t *testing.T) {
	dir := t.TempDir()
	prepared, _, _ := prepareTestOutput(t, dir)
	draft, err := createReservationDraft(prepared)
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	privatePath := filepath.Join(dir, draft.name)
	if err := os.Link(privatePath, filepath.Join(dir, "extra-link")); err != nil {
		t.Fatalf("hard link: %v", err)
	}

	failure := func() *reservationDraftFailure {
		_, failure := draft.initialize(prepared)
		return failure
	}()
	if failure == nil {
		t.Fatal("initialize over a hard-linked reservation must fail")
	}
	nerr, ok := live.AsNamespaceError(failure.cause)
	if !ok || nerr.Kind != live.NamespaceLinkCount || nerr.Links != 2 {
		t.Fatalf("initialize refusal = %v, want link count 2", failure.cause)
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(failure.owner.file.Fd()), &st); err != nil {
		t.Fatalf("stat failure-owner reservation: %v", err)
	}
	if st.Nlink != 2 {
		t.Fatalf("failure-owner reservation links = %d, want 2", st.Nlink)
	}
	failure.owner.Close()
}

func TestCanonicalTamperingFailsBeforeState2AndRetainsPhase(t *testing.T) {
	dir := t.TempDir()
	prepared, _, _ := prepareTestOutput(t, dir)
	draft, err := createReservationDraft(prepared)
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	private, initFailure := draft.initialize(prepared)
	if initFailure != nil {
		t.Fatalf("initialize: %v", initFailure)
	}
	canonical, acquireFailure := private.acquire(prepared)
	if acquireFailure != nil {
		t.Fatalf("acquire: %v", acquireFailure)
	}
	canonicalPath := filepath.Join(dir, "result.v4.readers")
	if err := os.Link(canonicalPath, filepath.Join(dir, "extra-link")); err != nil {
		t.Fatalf("hard link canonical: %v", err)
	}

	_, armFailure := canonical.arm(prepared)
	if armFailure == nil {
		t.Fatal("arm over a hard-linked canonical reservation must fail")
	}
	if armFailure.owner.state2Selected {
		t.Fatal("arm failure owner must not record a state-2 selection")
	}
	nerr, ok := live.AsNamespaceError(armFailure.cause)
	if !ok || nerr.Kind != live.NamespaceLinkCount || nerr.Links != 2 {
		t.Fatalf("arm refusal = %v, want link count 2", armFailure.cause)
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(armFailure.owner.reservation.file.Fd()), &st); err != nil {
		t.Fatalf("stat failure-owner reservation: %v", err)
	}
	if st.Nlink != 2 {
		t.Fatalf("failure-owner reservation links = %d, want 2", st.Nlink)
	}
	armFailure.owner.reservation.Close()
}

func TestExistingMainRejectedBeforeState2(t *testing.T) {
	dir := t.TempDir()
	prepared, _, _ := prepareTestOutput(t, dir)
	draft, err := createReservationDraft(prepared)
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	private, initFailure := draft.initialize(prepared)
	if initFailure != nil {
		t.Fatalf("initialize: %v", initFailure)
	}
	canonical, acquireFailure := private.acquire(prepared)
	if acquireFailure != nil {
		t.Fatalf("acquire: %v", acquireFailure)
	}
	mainPath := filepath.Join(dir, "result.v4")
	if err := os.WriteFile(mainPath, []byte("existing"), 0o600); err != nil {
		t.Fatalf("write existing main: %v", err)
	}

	_, armFailure := canonical.arm(prepared)
	if armFailure == nil {
		t.Fatal("arm with an existing main must fail")
	}
	if armFailure.owner.state2Selected {
		t.Fatal("arm failure owner must not record a state-2 selection")
	}
	nerr, ok := live.AsNamespaceError(armFailure.cause)
	if !ok || nerr.Kind != live.NamespaceExists {
		t.Fatalf("arm refusal = %v, want Exists", armFailure.cause)
	}
	if armFailure.owner.reservation.header.state != reservationStatePrepared {
		t.Fatalf("failure-owner header state %d, want Prepared", armFailure.owner.reservation.header.state)
	}
	content, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("read main: %v", err)
	}
	if string(content) != "existing" {
		t.Fatalf("existing main content changed to %q", content)
	}
	armFailure.owner.reservation.Close()
}
