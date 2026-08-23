//go:build linux || darwin

// Port of the Rust live_sidecar_tests.rs: sidecar creation, open,
// writer lease, reader slots, stale-slot clearing, fail-closed scan,
// cancellation cadence, replacement detection, symlink refusal, and
// parent-dir durability sync.

package live

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

const (
	testDatabaseID = 1
	testSidecarID  = 2
)

func testDatabaseIdentity() [16]byte { return [16]byte{1} }
func testSidecarIdentity() [16]byte  { return [16]byte{2} }

// createTestMain creates one private main-file stand-in with an OwnedMain
// authority (Rust create_private_for_test).
func createTestMain(t *testing.T, path string) {
	t.Helper()
	created, failure := createPrivate(path, cleanupAuthority{
		attemptID:     [16]byte{9},
		ordinal:       0,
		kind:          cleanupKindOwnedMain,
		directoryRole: cleanupRoleMainFile,
	})
	if failure != nil {
		t.Fatalf("create main: %v", failure.cause)
	}
	file := created.file
	file.Close()
}

// createTestReady creates a ready sidecar bound to the test identities
// (Rust create_ready).
func createTestReady(t *testing.T, main string, capacity uint32) *Sidecar {
	t.Helper()
	createTestMain(t, main)
	sidecar, failure := reserve(main, testDatabaseIdentity(), testSidecarIdentity(), capacity)
	if failure != nil {
		t.Fatalf("reserve: %v", failure.cause)
	}
	if err := sidecar.initializeCreating(); err != nil {
		t.Fatalf("initializeCreating: %v", err)
	}
	if err := sidecar.publishReady(); err != nil {
		t.Fatalf("publishReady: %v", err)
	}
	return sidecar
}

func releaseTestReader(t *testing.T, sidecar *Sidecar, slot uint32) {
	t.Helper()
	if err := sidecar.clearReader(slot); err != nil {
		t.Fatalf("clearReader: %v", err)
	}
	if err := sidecar.unlockReader(slot); err != nil {
		t.Fatalf("unlockReader: %v", err)
	}
}

// writeStaleSlot scribbles foreign bytes into one slot through a second
// mapping, simulating a crashed writer's stale bytes (Rust test
// file_io::write_exact_at).
func writeStaleSlot(t *testing.T, sidecar *Sidecar, slot uint32, value byte) {
	t.Helper()
	offset, err := slotOffset(slot)
	if err != nil {
		t.Fatalf("slotOffset: %v", err)
	}
	m, err := mapping.MapFile(sidecar.file, uint64(format.PageSize)+uint64(sidecar.header.capacity)*slotSize, true)
	if err != nil {
		t.Fatalf("MapFile: %v", err)
	}
	defer m.Close()
	bytes, err := m.View(offset, slotSize)
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	for i := range bytes {
		bytes[i] = value
	}
}

func readSlotBytes(t *testing.T, sidecar *Sidecar, slot uint32) [slotSize]byte {
	t.Helper()
	offset, err := slotOffset(slot)
	if err != nil {
		t.Fatalf("slotOffset: %v", err)
	}
	m, err := mapping.MapFile(sidecar.file, uint64(format.PageSize)+uint64(sidecar.header.capacity)*slotSize, true)
	if err != nil {
		t.Fatalf("MapFile: %v", err)
	}
	defer m.Close()
	bytes, err := m.View(offset, slotSize)
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	var out [slotSize]byte
	copy(out[:], bytes)
	return out
}

func expectCode(t *testing.T, err error, code format.ErrorCode) {
	t.Helper()
	var e *format.Error
	if !errors.As(err, &e) {
		t.Fatalf("expected code %d, got %v", code, err)
	}
	if e.Code != code {
		t.Fatalf("expected code %d, got %d (%v)", code, e.Code, err)
	}
}

func TestReadySidecarReopensWithExactBinding(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")
	sidecar := createTestReady(t, main, 7)

	reopened, err := open(main, testDatabaseIdentity())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.header.capacity != 7 {
		t.Fatalf("capacity = %d, want 7", reopened.header.capacity)
	}
	if reopened.header.sidecarID != testSidecarIdentity() {
		t.Fatalf("sidecar id mismatch")
	}
	if err := reopened.verifyPath(); err != nil {
		t.Fatalf("verifyPath: %v", err)
	}
	sidecar.close()
	reopened.close()

	_, err = open(main, [16]byte{3})
	expectCode(t, err, format.CodeWrongState)
}

func TestCreatingAndMalformedSidecarsAreRejected(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")
	createTestMain(t, main)
	sidecar, failure := reserve(main, testDatabaseIdentity(), testSidecarIdentity(), 2)
	if failure != nil {
		t.Fatalf("reserve: %v", failure.cause)
	}
	if err := sidecar.initializeCreating(); err != nil {
		t.Fatalf("initializeCreating: %v", err)
	}
	_, err := open(main, testDatabaseIdentity())
	expectCode(t, err, format.CodeWrongState)

	if err := sidecar.publishReady(); err != nil {
		t.Fatalf("publishReady: %v", err)
	}
	if err := sidecar.file.Truncate(format.PageSize); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	_, err = open(main, testDatabaseIdentity())
	if err == nil {
		t.Fatal("open: want format error after truncate")
	}
	expectCode(t, err, format.CodeFormatInvalid)

	// A crash-left residue between create and sizing leaves a shorter
	// file; the header read must fail typed before any mapped access
	// (Rust require_file_extent), never SIGBUS.
	if err := sidecar.file.Truncate(0); err != nil {
		t.Fatalf("truncate zero: %v", err)
	}
	_, err = open(main, testDatabaseIdentity())
	expectCode(t, err, format.CodeFormatInvalid)
}

func TestOneWriterOwnsTheDatabase(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")
	first := createTestReady(t, main, 2)
	second, err := open(main, testDatabaseIdentity())
	if err != nil {
		t.Fatalf("second open: %v", err)
	}

	if err := first.claimWriter(); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	expectCode(t, second.claimWriter(), format.CodeWriterBusy)
	if err := first.releaseWriter(); err != nil {
		t.Fatalf("release: %v", err)
	}
	first.close()
	if err := second.claimWriter(); err != nil {
		t.Fatalf("second claim after release: %v", err)
	}
	second.releaseWriter()
	second.close()
}

func TestReaderSlotsReportCapacityScanAndReuse(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")
	scanner := createTestReady(t, main, 2)
	first, err := open(main, testDatabaseIdentity())
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	second, err := open(main, testDatabaseIdentity())
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	exhausted, err := open(main, testDatabaseIdentity())
	if err != nil {
		t.Fatalf("third open: %v", err)
	}

	firstSlot, err := first.claimReaderCancellable(7, nil)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	secondSlot, err := second.claimReaderCancellable(11, nil)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if firstSlot == secondSlot {
		t.Fatalf("slots must differ")
	}
	_, err = exhausted.claimReaderCancellable(13, nil)
	expectCode(t, err, format.CodeReaderCapacityExhausted)

	var active []uint64
	if err := scanner.scanReadersCancellable(nil, func(txn uint64) error {
		active = append(active, txn)
		return nil
	}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(active) != 2 || active[0] == active[1] {
		t.Fatalf("active = %v, want two distinct transactions", active)
	}
	want := map[uint64]bool{7: true, 11: true}
	for _, txn := range active {
		if !want[txn] {
			t.Fatalf("unexpected active transaction %d", txn)
		}
	}

	releaseTestReader(t, first, firstSlot)
	reused, err := exhausted.claimReaderCancellable(13, nil)
	if err != nil {
		t.Fatalf("reuse claim: %v", err)
	}
	if reused != firstSlot {
		t.Fatalf("reused slot %d, want %d", reused, firstSlot)
	}
	releaseTestReader(t, exhausted, reused)
	releaseTestReader(t, second, secondSlot)
	first.close()
	second.close()
	exhausted.close()
	scanner.close()
}

func TestStaleSlotBytesAreClearedBeforeReuse(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")
	sidecar := createTestReady(t, main, 1)
	writeStaleSlot(t, sidecar, 0, 0x5a)

	if err := sidecar.scanReadersCancellable(nil, func(uint64) error {
		t.Fatal("stale slot must not be observed")
		return nil
	}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	slot := readSlotBytes(t, sidecar, 0)
	for i, b := range slot {
		if b != 0 {
			t.Fatalf("slot byte %d = 0x%02x, want zero", i, b)
		}
	}
	sidecar.close()
}

func TestMalformedOrFutureActiveSlotsFailClosed(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")
	scanner := createTestReady(t, main, 1)
	owner, err := open(main, testDatabaseIdentity())
	if err != nil {
		t.Fatalf("owner open: %v", err)
	}
	slot, err := owner.claimReaderCancellable(7, nil)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	writeStaleSlot(t, owner, slot, 0x5a)
	expectCode(t, scanner.scanAtMostCancellable(7, nil), format.CodeFormatInvalid)

	releaseTestReader(t, owner, slot)
	slot, err = owner.claimReaderCancellable(8, nil)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	expectCode(t, scanner.scanAtMostCancellable(7, nil), format.CodeFormatInvalid)
	releaseTestReader(t, owner, slot)
	owner.close()
	scanner.close()
}

func TestReadOnlyCapacityInspectionChecksCancellationPerSlot(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")
	sidecar := createTestReady(t, main, 64)
	polls := 0
	check := func() error {
		polls++
		if polls > 8 {
			return &format.Error{Code: format.CodeCancelled, Detail: "test cancellation"}
		}
		return nil
	}
	err := sidecar.inspectAtMostCancellable(1, check)
	expectCode(t, err, format.CodeCancelled)
	if polls >= 64 {
		t.Fatalf("inspection polled %d times, must cancel before capacity", polls)
	}
	sidecar.close()
}

func TestReplacementAtTheCanonicalPathIsDetected(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")
	sidecar := createTestReady(t, main, 1)
	sidecarPath, err := canonicalSidecarPath(main)
	if err != nil {
		t.Fatalf("canonicalSidecarPath: %v", err)
	}
	old := sidecarPath + ".old"
	if err := os.Rename(sidecarPath, old); err != nil {
		t.Fatalf("rename: %v", err)
	}
	createTestMain(t, sidecarPath)

	expectCode(t, sidecar.verifyPath(), format.CodeWrongState)
	os.Remove(old)
	sidecar.close()
}

func TestSymlinksAreNotFollowed(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")
	createTestMain(t, main)
	target := filepath.Join(dir, "target")
	createTestMain(t, target)
	sidecarPath, err := canonicalSidecarPath(main)
	if err != nil {
		t.Fatalf("canonicalSidecarPath: %v", err)
	}
	if err := os.Symlink(target, sidecarPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	_, err = open(main, testDatabaseIdentity())
	expectCode(t, err, format.CodeWrongState)
}

func TestSidecarPathHasAParentForDurabilitySync(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "db.iprdb")
	sidecar := createTestReady(t, main, 1)
	if err := syncParent(sidecar.path); err != nil {
		t.Fatalf("syncParent: %v", err)
	}
	sidecar.close()
}

func TestSidecarLengthGeometry(t *testing.T) {
	length, err := sidecarLength(1)
	if err != nil {
		t.Fatalf("sidecarLength(1): %v", err)
	}
	if length != format.PageSize+16 {
		t.Fatalf("sidecarLength(1) = %d, want %d", length, format.PageSize+16)
	}
	// Rust sidecar_length(0) is Ok(4096): the header page with no
	// slots. The zero-capacity rejection lives in Sidecar::create,
	// ported here as reserveAt.
	length, err = sidecarLength(0)
	if err != nil {
		t.Fatalf("sidecarLength(0): %v", err)
	}
	if length != format.PageSize {
		t.Fatalf("sidecarLength(0) = %d, want %d", length, format.PageSize)
	}
	_, failure := reserveAt(filepath.Join(t.TempDir(), "zero.iprdb"), testDatabaseIdentity(), testSidecarIdentity(), 0)
	expectCode(t, failure.cause, format.CodeInvalidArgument)
}

func TestHeaderCodecRoundTrip(t *testing.T) {
	page := make([]byte, format.PageSize)
	h := header{capacity: 3, databaseID: [16]byte{1, 2, 3}, sidecarID: [16]byte{4, 5, 6}}
	if err := writeHeaderMapping(page, h, stateReady); err != nil {
		t.Fatalf("writeHeaderMapping: %v", err)
	}
	state, decoded, err := readHeaderMapping(page)
	if err != nil {
		t.Fatalf("readHeaderMapping: %v", err)
	}
	if state != stateReady || decoded != h {
		t.Fatalf("round trip mismatch: %+v %+v", state, decoded)
	}
	page[headerCRCOff] ^= 0xff
	if _, _, err := readHeaderMapping(page); err == nil {
		t.Fatal("corrupted checksum must fail")
	}
}
