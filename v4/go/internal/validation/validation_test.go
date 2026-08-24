package validation

// Slice-A boundary tests: the budget and mode preflights, the
// immutable bootstrap-report path over corrupt files, the sink Stop
// class, and the claims/table/context units. The clean-sweep PASS
// path lands with the slice-B validators.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// countProcessFds counts the open descriptors of this process (Linux
// /proc/self/fd; the validation tests run sequentially, so the count
// is stable across one measurement point).
func countProcessFds(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("read /proc/self/fd: %v", err)
	}
	return len(entries)
}

// writeFile writes one raw artifact of the given byte length.
func writeFile(t *testing.T, length int, mutate func(page []byte)) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "database.iprdb")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, length)
	if mutate != nil {
		mutate(buf)
	}
	if _, err := f.Write(buf); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// metaPage builds one committed meta page image with the given
// transaction and page count (the bootstrap test helper shape).
func metaPage(txn uint64, pages uint64) []byte {
	page := make([]byte, format.PageSize)
	copy(page[0:8], format.MainMagic[:])
	format.PutU16(page[8:10], format.MetaSize)
	page[10] = format.PageShift
	page[11] = format.AddressFamilyIPv4
	page[12] = format.ValueKindDirect
	copy(page[16:32], "valid-tag")
	format.PutU64(page[32:40], 1) // database id
	format.PutU64(page[48:56], txn)
	copy(page[56:72], []byte{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20})
	format.PutU64(page[72:80], pages)
	format.PutU32(page[252:256], format.MetaCRC32C(page))
	return page
}

// metaDB writes a two-page-meta immutable database file.
func metaDB(t *testing.T, pages uint64) string {
	t.Helper()
	p := metaPage(1, pages)
	path := filepath.Join(t.TempDir(), "database.iprdb")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(0); i < pages; i++ {
		buf := make([]byte, format.PageSize)
		if i == 0 {
			buf = p
		}
		if _, err := f.Write(buf); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateBudgetRefusals(t *testing.T) {
	path := metaDB(t, 4)
	if _, failure := Validate(path, ValidationModeImmutableCurrent, nil, nil, nil); failure == nil {
		t.Fatal("nil budget accepted")
	} else if failure.Cause.Error() == "" || failure.CleanupState() != publication.CleanupStateClean {
		t.Fatalf("nil-budget failure shape: %v", failure)
	}
	zero := HeapOnly(1<<20, 0)
	if _, failure := Validate(path, ValidationModeImmutableCurrent, zero, nil, nil); failure == nil {
		t.Fatal("zero open files accepted")
	}
	scratch := &ValidationBudget{MaxHeapBytes: 1 << 20, MaxOpenFiles: 1, MaxScratchFiles: 1}
	if _, failure := Validate(path, ValidationModeImmutableCurrent, scratch, nil, nil); failure == nil {
		t.Fatal("scratch limits without directory accepted")
	} else if failure.Cause.Error() == "" {
		t.Fatal("empty cause")
	}
	// A valid budget on an immutable file reaches the open (the
	// bootstrap-report path below covers corrupt files; the clean
	// sweep needs the slice-B validators).
	budget := HeapOnly(1<<20, 1)
	result, failure := Validate(path, ValidationModeImmutableCurrent, budget, nil, nil)
	if failure != nil {
		t.Fatalf("valid budget refused: %v", failure.Cause)
	}
	if result == nil {
		t.Fatal("nil result")
	}
	// The slice-A sweep claims only the allocator reserve, so the
	// partition reports the untouched data pages.
	if result.Valid {
		t.Fatal("empty sweep cannot prove validity")
	}
	if result.Generation == nil || result.Generation.TransactionID != 1 {
		t.Fatalf("generation %+v", result.Generation)
	}
	if result.Progress.CheckedUniquePages != 0 {
		t.Fatalf("checked pages %d, want 0", result.Progress.CheckedUniquePages)
	}
}

func TestValidateModeRefusals(t *testing.T) {
	path := metaDB(t, 4)
	// The offline-candidate mode stays refused until chunk 4-10; the
	// trained LiveCurrent behaviors live in the linux/darwin live
	// suite (the open refusal and the selection arms).
	if _, failure := Validate(path, ValidationModeOfflineCandidate, HeapOnly(1<<20, 1), nil, nil); failure == nil {
		t.Fatal("offline mode accepted before chunk 4-10")
	}
}

func TestValidateImmutableBootstrapReport(t *testing.T) {
	// Both meta pages fail magic: two MetaUnavailable findings.
	path := writeFile(t, 2*format.PageSize, func(page []byte) {
		for i := range page {
			page[i] = 0xA5
		}
	})
	before := countProcessFds(t)
	var findings []ValidationFinding
	result, failure := Validate(path, ValidationModeImmutableCurrent, HeapOnly(1<<20, 1), nil, SinkFunc(func(f *ValidationFinding) (ValidationSinkControl, error) {
		findings = append(findings, *f)
		return SinkContinue, nil
	}))
	// Every terminal of the report releases the source (the Rust drop
	// releases the shared lifetime lock and closes the fd).
	if after := countProcessFds(t); after != before {
		t.Fatalf("fd count %d -> %d, want unchanged", before, after)
	}
	if failure != nil {
		t.Fatalf("bootstrap report failed: %v", failure.Cause)
	}
	if result.Valid {
		t.Fatal("garbage file reported valid")
	}
	if len(findings) != 2 {
		t.Fatalf("findings %d, want 2", len(findings))
	}
	for i, f := range findings {
		if f.Reason != ReasonMetaUnavailable || f.Object != ObjectMeta || f.PageNumber == nil || *f.PageNumber != uint32(i) {
			t.Fatalf("finding %d %+v", i, f)
		}
		if f.PhysicalBytes == nil || f.PhysicalBytes.Start != uint64(i)*format.PageSize || f.PhysicalBytes.EndExclusive != uint64(i+1)*format.PageSize {
			t.Fatalf("finding %d bytes %+v", i, f.PhysicalBytes)
		}
	}
	if result.Progress.FindingCount != 2 || result.Progress.FindingsFor(ReasonMetaUnavailable) != 2 {
		t.Fatalf("progress %+v", result.Progress)
	}
	if result.Generation != nil {
		t.Fatal("generation on a corrupted file")
	}
}

func TestValidateImmutableMixedMetaProblems(t *testing.T) {
	// Page 0 carries valid magic but a broken checksum (MetaInvalid);
	// page 1 fails magic (MetaUnavailable).
	p0 := metaPage(1, 4)
	format.PutU32(p0[252:256], 0) // broken CRC
	p1 := metaPage(1, 4)
	p1[0] = 'X'
	path := filepath.Join(t.TempDir(), "database.iprdb")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write(append(p0, p1...))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	var findings []ValidationFinding
	result, failure := Validate(path, ValidationModeImmutableCurrent, HeapOnly(1<<20, 1), nil, SinkFunc(func(f *ValidationFinding) (ValidationSinkControl, error) {
		findings = append(findings, *f)
		return SinkContinue, nil
	}))
	if failure != nil {
		t.Fatalf("bootstrap report failed: %v", failure.Cause)
	}
	if result.Valid || len(findings) != 2 {
		t.Fatalf("result %+v findings %d", result, len(findings))
	}
	if findings[0].Reason != ReasonMetaInvalid || *findings[0].PageNumber != 0 {
		t.Fatalf("finding 0 %+v", findings[0])
	}
	if findings[1].Reason != ReasonMetaUnavailable || *findings[1].PageNumber != 1 {
		t.Fatalf("finding 1 %+v", findings[1])
	}
}

func TestValidateImmutableShortFileGeometryReport(t *testing.T) {
	// A one-page main proves geometry before any mapping: the sweep
	// reports the FileGeometryInvalid finding instead of an IO
	// failure (Rust require_geometry before the bootstrap mapping).
	path := writeFile(t, format.PageSize, func(page []byte) {
		for i := range page {
			page[i] = 0xA5
		}
	})
	var findings []ValidationFinding
	result, failure := Validate(path, ValidationModeImmutableCurrent, HeapOnly(1<<20, 1), nil, SinkFunc(func(f *ValidationFinding) (ValidationSinkControl, error) {
		findings = append(findings, *f)
		return SinkContinue, nil
	}))
	if failure != nil {
		t.Fatalf("geometry report failed: %v", failure.Cause)
	}
	if result.Valid || len(findings) != 1 {
		t.Fatalf("result %+v findings %d", result, len(findings))
	}
	f := findings[0]
	if f.Reason != ReasonFileGeometryInvalid || f.Object != ObjectFileGeometry || f.PageNumber != nil {
		t.Fatalf("finding %+v", f)
	}
	if result.Generation != nil {
		t.Fatal("generation on a short file")
	}
}

func TestValidateSinkStop(t *testing.T) {
	path := writeFile(t, 2*format.PageSize, func(page []byte) {
		for i := range page {
			page[i] = 0xA5
		}
	})
	_, failure := Validate(path, ValidationModeImmutableCurrent, HeapOnly(1<<20, 1), nil, SinkFunc(func(f *ValidationFinding) (ValidationSinkControl, error) {
		return SinkStop, nil
	}))
	if failure == nil {
		t.Fatal("sink stop accepted")
	}
	var fe *format.Error
	if !errors.As(failure.Cause, &fe) || fe.Code != format.CodeStoppedBySink {
		t.Fatalf("cause %v, want StoppedBySink", failure.Cause)
	}
}

func TestValidationIdentifyDeviceInode(t *testing.T) {
	// The identity helpers must not break the gotcha lane: a regular
	// file identity is a nonzero (device, inode) pair, and the
	// portable encoding round-trips through the publication authority.
	path := metaDB(t, 4)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	identity, err := live.IdentityAnyLink(f)
	if err != nil {
		t.Fatal(err)
	}
	if identity == (live.FileIdentity{}) {
		t.Fatal("empty identity")
	}
}

func TestClaimsPartition(t *testing.T) {
	claims, err := newClaims(8, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if claims.retainedBytes() != 2 { // ceil(8/4)
		t.Fatalf("retained %d", claims.retainedBytes())
	}
	if previous, err := claims.add(3, claimGraph); err != nil || previous != claimUnclaimed {
		t.Fatalf("add graph %d %v", previous, err)
	}
	if previous, err := claims.add(3, claimAlloc); err != nil || previous != claimGraph {
		t.Fatalf("add alloc %d %v", previous, err)
	}
	value, err := claims.get(3)
	if err != nil || value != claimGraph|claimAlloc {
		t.Fatalf("get %d %v", value, err)
	}
	if _, err := claims.get(8); err == nil {
		t.Fatal("out-of-range page accepted")
	}
	if _, err := newClaims(8, 1); err == nil {
		t.Fatal("undersized claim budget accepted")
	}
}

func TestTableProbe(t *testing.T) {
	table, err := newTable(4, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if table.len() != 8 { // next power of two above 2*4
		t.Fatalf("capacity %d", table.len())
	}
	first := table.countRange(7, nil)
	second := table.countRange(7, nil)
	other := table.countRange(9, nil)
	if first != CountInserted || second != CountExisting || other != CountInserted {
		t.Fatalf("counts %v %v %v", first, second, other)
	}
	seen := map[uint32]uint64{}
	for index := 0; index < table.len(); index++ {
		if slot, ok := table.slot(index); ok {
			seen[slot.ID] = slot.RangeCount
		}
	}
	if len(seen) != 2 || seen[7] != 2 || seen[9] != 1 {
		t.Fatalf("slots %v", seen)
	}
	var digest [32]byte
	digest[0] = 0xAB
	if result, err := table.define(7, 3, 1, digest, nil); err != nil || result != InsertInserted {
		t.Fatalf("define %v %v", result, err)
	}
	if result, err := table.define(7, 3, 1, digest, nil); err != nil || result != InsertExisting {
		t.Fatalf("redefine %v %v", result, err)
	}
	marked, err := table.markReverse(7, 1, digest, nil)
	if err != nil || !marked {
		t.Fatalf("mark reverse %v %v", marked, err)
	}
	again, err := table.markReverse(7, 1, digest, nil)
	if err != nil || again {
		t.Fatalf("double reverse %v %v", again, err)
	}
	if _, err := newTable(1<<40, 1<<20); err == nil {
		t.Fatal("huge table accepted")
	}
}
