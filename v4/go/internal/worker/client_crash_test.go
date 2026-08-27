//go:build linux || darwin || freebsd || windows

// Real-worker source-fault crash matrix (Rust worker/client_tests.rs
// source_sigbus_is_classified_cleaned_and_restartable): session 1 runs
// the hand-built 5-page fixture with the CRC-damaged page 3, the
// parent sink truncates the source to four pages, the worker's next
// source scan SIGBUSes inside the armed Source probe, and the parent
// reads the exact fault record (role Source, relative 4*PAGE, mapping
// 5*PAGE) after the child exited 197. Session 2 restores the file and
// reruns with page 4 declared unreadable: the machine completes
// deterministically with one IoError envelope at page 4,
// report.pages.io_unreadable == 1, publication Published, and an
// output with zero range records.

package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/reader"
	"github.com/firehol/iprange/v4/go/internal/recovery"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// crashSourceFixture builds the hand-encoded 5-page source of the Rust
// fault_fixture (client_tests.rs:213-260): the dual meta pair (pages
// 0/1), the range branch root (page 2 with cells (10, child 3) and
// (100, child 4)), the CRC-damaged leaf (page 3, record 10-20,
// byte 100 XOR 0x5a after the seal), and the valid leaf (page 4,
// record 100-110). It returns the path and the intact last page for
// the session-2 restore.
//
// The fixture builder is TEST-ONLY: it constructs pages in owned
// memory, which the production format primitives never do.
func crashSourceFixture(t *testing.T) (string, []byte) {
	t.Helper()
	meta := format.Meta{
		AddressFamily:    format.AddressFamilyIPv4,
		ValueKind:        format.ValueKindDirect,
		StructureKind:    format.StructureKindNone,
		ValueTag:         crashValueTag(),
		DatabaseID:       [16]byte{1},
		TxnID:            1,
		CommitNonce:      [16]byte{2},
		PageCount:        5,
		RangeRoot:        2,
		RangeRecordCount: 2,
	}
	root := make([]byte, format.PageSize)
	builder := format.NewSlottedBuilder(root, format.PageTypeRangeBranch, meta.TxnID, 1, uint32(format.AddressFamilyIPv4))
	if err := builder.Push(root, crashBranchCell(10, 3)); err != nil {
		t.Fatal(err)
	}
	if err := builder.Push(root, crashBranchCell(100, 4)); err != nil {
		t.Fatal(err)
	}
	if err := builder.Finish(root); err != nil {
		t.Fatal(err)
	}
	if err := format.SealPageChecksum(root); err != nil {
		t.Fatal(err)
	}

	damaged := crashLeaf(t, meta.TxnID, 10, 20, 1)
	damaged[100] ^= 0x5a
	lastPage := crashLeaf(t, meta.TxnID, 100, 110, 2)

	image := make([]byte, 5*format.PageSize)
	if err := meta.EncodeMapped(image[:format.PageSize]); err != nil {
		t.Fatal(err)
	}
	if err := meta.EncodeMapped(image[format.PageSize : 2*format.PageSize]); err != nil {
		t.Fatal(err)
	}
	copy(image[2*format.PageSize:3*format.PageSize], root)
	copy(image[3*format.PageSize:4*format.PageSize], damaged)
	copy(image[4*format.PageSize:], lastPage)
	path := filepath.Join(t.TempDir(), "source.v4")
	if err := os.WriteFile(path, image, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, lastPage
}

// crashValueTag is the Rust ValueTag::FIRST_SEEN wire bytes
// (b"first_seen\0\0\0\0\0\0").
func crashValueTag() [16]byte {
	var tag [16]byte
	copy(tag[:], "first_seen")
	return tag
}

// crashBranchCell encodes one 8-byte IPv4 branch cell: first u32 LE,
// child page u32 LE (Rust range_tree RANGE_BRANCH cell layout).
func crashBranchCell(first, child uint32) []byte {
	cell := make([]byte, format.RangeEntryV4Size)
	format.PutU32(cell[0:4], first)
	format.PutU32(cell[4:8], child)
	return cell
}

// crashLeaf builds one sealed IPv4 range leaf page with one record
// (Rust fault_fixture leaf(): RANGE_LEAF, born txn, one 12-byte
// from/to/value cell, checksum sealed).
func crashLeaf(t *testing.T, txn uint64, from, to, value uint32) []byte {
	t.Helper()
	page := make([]byte, format.PageSize)
	builder := format.NewSlottedBuilder(page, format.PageTypeRangeLeaf, txn, 0, uint32(format.AddressFamilyIPv4))
	cell := make([]byte, format.RangeRecordV4Size)
	format.PutU32(cell[0:4], from)
	format.PutU32(cell[4:8], to)
	format.PutU32(cell[8:12], value)
	if err := builder.Push(page, cell); err != nil {
		t.Fatal(err)
	}
	if err := builder.Finish(page); err != nil {
		t.Fatal(err)
	}
	if err := format.SealPageChecksum(page); err != nil {
		t.Fatal(err)
	}
	return page
}

// TestRecoverWithWorkerRealBinarySourceFaultRestartable mirrors the
// Rust two-session source-fault fixture with the real worker binary:
// the session-1 SIGBUS produces the owned Source fault record with
// the exact relative offset and mapping length (the DriveLoop already
// proved the child exited 197 before the record was read back), and
// the session-2 rerun with page 4 declared unreadable completes
// deterministically.
func TestRecoverWithWorkerRealBinarySourceFaultRestartable(t *testing.T) {
	binary := buildRealWorker(t)
	workerCandidatesHook = func() ([]string, error) { return []string{binary}, nil }
	t.Cleanup(func() { workerCandidatesHook = nil })

	sourcePath, lastPage := crashSourceFixture(t)
	candidate := realFixtureCandidate(t, sourcePath)
	budget := &recovery.RecoveryBudget{MaxHeapBytes: 1 << 30, MaxOutputPages: 1 << 16, MaxOpenFiles: 8}
	destination := filepath.Join(t.TempDir(), "out.v4")

	// Session 1: the CRC-damaged page is classified through the sink,
	// the source is truncated to 4 pages inside the sink, and the next
	// source scan faults on the now-unbacked page 4 with the Source
	// probe armed.
	var delivered uint64
	truncated := false
	attempt := recoverOnceWorker(sourcePath, destination, candidate, WorkerModeImmutable, budget, nil, recovery.RecoverySinkFunc(func(envelope *recovery.RecoveryUnknownEnvelope) (recovery.RecoverySinkControl, error) {
		if envelope.Reason != validation.ReasonPageCrcMismatch || envelope.PageNumber == nil || *envelope.PageNumber != 3 {
			t.Fatalf("envelope = %+v, want PageCrcMismatch at page 3", envelope)
		}
		if !truncated {
			if err := os.Truncate(sourcePath, 4*format.PageSize); err != nil {
				return 0, err
			}
			truncated = true
		}
		return recovery.RecoverySinkContinue, nil
	}), nil, &delivered)
	if attempt.kind != attemptInterrupted {
		t.Fatalf("attempt kind = %d (%+v), want the interrupted fault terminal", attempt.kind, attempt)
	}
	if !truncated {
		t.Fatal("the sink did not truncate the source")
	}
	if delivered != 1 {
		t.Fatalf("delivered = %d, want 1", delivered)
	}
	if attempt.fault.Role != RoleSource {
		t.Fatalf("fault role = %d, want %d", attempt.fault.Role, RoleSource)
	}
	if attempt.fault.Relative != 4*format.PageSize {
		t.Fatalf("fault relative = %#x, want %#x", attempt.fault.Relative, 4*format.PageSize)
	}
	if attempt.fault.MappingLen != 5*format.PageSize {
		t.Fatalf("fault mapping_len = %#x, want %#x", attempt.fault.MappingLen, 5*format.PageSize)
	}
	// The DriveLoop enforced the child's exit code 197 before reading
	// the fault record; the validated record above is the second proof
	// of the owned-fault exit path.

	// The interrupted session carries the parent-owned attempt facts
	// (Rust recover_once facts), and the parent discards the owned
	// attempt with the exact identity before any retry (Rust
	// client_tests.rs discard + discard_clean / scratch_clean arms):
	// the worker died before its own failing-terminal arms could run,
	// so this discard is the only thing that removes the private
	// attempt.
	if attempt.output == nil || attempt.output.PublicationAttemptID == [16]byte{} {
		t.Fatalf("interrupted attempt output = %+v, want the parent-created facts", attempt.output)
	}
	discarded, scratch := discardRecoveryAttemptComposed(destination, attempt.output, scratchDirectoryOf(budget), attempt.scratch)
	if discarded == nil || !discarded.Clean() {
		t.Fatalf("discarded = %+v, want the clean discard of the interrupted attempt", discarded)
	}
	if !scratchClean(scratch) {
		t.Fatalf("scratch cleanup = %+v, want clean", scratch)
	}
	if residue := publishResiduePaths(t, filepath.Dir(destination)); len(residue) != 0 {
		t.Fatalf("destination residue after session 1: %v", residue)
	}

	// Restore the source to its full extent (Rust client_tests.rs
	// lines 186-192: set_len, rewrite the last page, sync).
	f, err := os.OpenFile(sourcePath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(5 * format.PageSize); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(lastPage, 4*format.PageSize); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// Session 2: page 4 is declared unreadable; the machine refuses it
	// deterministically (no second SIGBUS) and completes.
	var observed []*recovery.RecoveryUnknownEnvelope
	second := recoverOnceWorker(sourcePath, destination, candidate, WorkerModeImmutable, budget, nil, recovery.RecoverySinkFunc(func(envelope *recovery.RecoveryUnknownEnvelope) (recovery.RecoverySinkControl, error) {
		observed = append(observed, envelope)
		return recovery.RecoverySinkContinue, nil
	}), []uint32{4}, &delivered)
	if second.kind != attemptComplete {
		t.Fatalf("attempt kind = %d (%+v), want completion after the declared page", second.kind, second)
	}
	if second.outcome == nil || second.outcome.Result == nil || second.outcome.Failure != nil {
		t.Fatalf("outcome = %+v, want the completed result arm", second.outcome)
	}
	if delivered != 2 {
		t.Fatalf("delivered = %d, want 2 across both sessions", delivered)
	}
	if len(observed) != 1 {
		t.Fatalf("observed %d envelopes, want 1", len(observed))
	}
	if observed[0].Reason != validation.ReasonIoError || observed[0].PageNumber == nil || *observed[0].PageNumber != 4 {
		t.Fatalf("envelope = %+v, want IoError at page 4", observed[0])
	}
	if second.outcome.Result.Report.Pages.IOUnreadable != 1 {
		t.Fatalf("io_unreadable = %d, want 1", second.outcome.Result.Report.Pages.IOUnreadable)
	}
	if second.outcome.Result.Publication.Publication != publication.PublicationPublished {
		t.Fatalf("publication = %v, want Published", second.outcome.Result.Publication.Publication)
	}
	r, err := reader.OpenImmutable(destination)
	if err != nil {
		t.Fatalf("open the produced output: %v", err)
	}
	defer r.Close()
	if got := r.Meta().RangeRecordCount; got != 0 {
		t.Fatalf("output range_record_count = %d, want 0", got)
	}
	// The published terminal leaves exactly the coordinated main in
	// the destination directory: the parent-created attempt of session
	// 2 was consumed by the machine's own publication terminal, and no
	// private attempt survives either session (Rust
	// client_tests.rs source_sigbus_is_classified_cleaned_and_restartable
	// asserts the same cleanup evidence).
	entries, err := os.ReadDir(filepath.Dir(destination))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(destination) {
		t.Fatalf("destination directory = %v, want exactly the published output %s", entries, filepath.Base(destination))
	}
	if residue := publishResiduePaths(t, filepath.Dir(destination)); len(residue) != 0 {
		t.Fatalf("destination residue after session 2: %v", residue)
	}
}

// publishResiduePaths lists the private publish-attempt artifacts of
// one destination directory (the `.iprange-publish-*` names; the
// coordinated main never matches). The crash fixture asserts the list
// is empty after the session-1 discard and after the session-2
// publication.
func publishResiduePaths(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	var residue []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".iprange-publish-") {
			residue = append(residue, entry.Name())
		}
	}
	return residue
}
