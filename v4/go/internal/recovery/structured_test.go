package recovery

// Structured recovery construction tests (Rust recovery/structured_tests
// arms): the digest-damaged record rejection, the missing-membership
// rejection, and the out-of-bounds branch pointer best-effort recovery.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/validation"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// structuredTag is the shared value tag of the structured recovery
// tests (Rust source_builder tag "enrichment").
var structuredTag = func() [16]byte {
	var tag [16]byte
	copy(tag[:], "enrichment")
	return tag
}()

// structuredSourceLimit builds one structured source with the given
// structure pushes and returns the committed meta (Rust structured
// source_builder).
func structuredSourceLimit(t *testing.T, path string, feedLimit uint64, feeds [][2]any, pushes []structuredPush) format.Meta {
	t.Helper()
	spec, err := writer.FreshOutputSpec(format.AddressFamilyIPv4, format.ValueKindStructured, format.StructureKindNetworkEnrichmentV1, structuredTag, feedLimit)
	if err != nil {
		t.Fatalf("FreshOutputSpec: %v", err)
	}
	builder := buildFixtureWriter(t, path, spec, writer.OutputBudget{MaxOutputPages: 20_000}, writer.ReferenceBatchEntryLimit)
	for _, pair := range feeds {
		if err := builder.PushFeed(pair[0].(string), pair[1].(uint32)); err != nil {
			t.Fatalf("PushFeed: %v", err)
		}
	}
	for _, push := range pushes {
		if err := builder.PushNetworkEnrichmentV1V4(push.from, push.to, push.value, push.membership); err != nil {
			t.Fatalf("PushNetworkEnrichmentV1V4: %v", err)
		}
	}
	if err := builder.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	meta := builder.Meta()
	if err := builder.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return meta
}

// structuredPush is one structured range push of the tests.
type structuredPush struct {
	from, to   uint32
	value      format.NetworkEnrichmentV1
	membership writer.OutputWords
}

// enrichment builds one NetworkEnrichmentV1 with the given ASN (Rust
// enrichment).
func enrichment(asn uint32) format.NetworkEnrichmentV1 {
	return format.NetworkEnrichmentV1{ASN: asn}
}

// structuredOutputBuilder builds the recovery destination for one
// structured source (Rust output_builder).
func structuredOutputBuilder(t *testing.T, path string, source format.Meta) *writer.OutputBuilder {
	t.Helper()
	spec, err := writer.FreshOutputSpec(source.AddressFamily, format.ValueKindStructured, format.StructureKindNetworkEnrichmentV1, source.ValueTag, source.FeedIndexLimit)
	if err != nil {
		t.Fatalf("FreshOutputSpec: %v", err)
	}
	return buildFixtureWriter(t, path, spec, writer.OutputBudget{MaxOutputPages: 20_000}, writer.ReferenceBatchEntryLimit)
}

// constructStructured runs one structured construction and closes the
// finished output.
func constructStructured(t *testing.T, source *mapping.Mapping, meta format.Meta, outputPath string, budget *RecoveryBudget, sink RecoverySink) (*Construction, *constructionFailure) {
	t.Helper()
	builder := structuredOutputBuilder(t, outputPath, meta)
	construction, failure := structuredConstruct(source, meta, builder, budget, nil, sink)
	if failure == nil {
		if err := construction.finished.Close(); err != nil {
			t.Fatalf("Close finished output: %v", err)
		}
	} else {
		// Rust drops the builder with the failure; the mapping must
		// close here so the destination file stays deletable on
		// Windows.
		_ = builder.Close()
	}
	return construction, failure
}

// TestStructuredRecoveryConstructRefusesInvalidIDLimit proves the
// structure table refuses a source generation whose ID limit lies
// outside the Rust required_level range before any page work (Rust
// table::required_level: Corrupt "structure table ID limit is
// invalid").
func TestStructuredRecoveryConstructRefusesInvalidIDLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.iprdb")
	meta := structuredSourceLimit(t, path, 64, nil, []structuredPush{
		{from: 0, to: 9, value: enrichment(64512)},
	})
	meta.StructureIDLimit = 1<<32 + 1
	source := mapSource(t, path)
	defer source.Close()
	sink := RecoverySinkFunc(func(envelope *RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		return RecoverySinkContinue, nil
	})
	outputPath := filepath.Join(dir, "output.iprdb")
	_, failure := constructStructured(t, source, meta, outputPath, recoveryBudget(1<<23), sink)
	if failure == nil {
		t.Fatal("invalid structure id limit accepted")
	}
	var fe *format.Error
	if !errors.As(failure.cause, &fe) || fe.Code != format.CodeFormatInvalid {
		t.Fatalf("cause %v, want the corrupt id-limit class", failure.cause)
	}
}

// rewriteStructureRecord edits one structure record of the root leaf in
// place and re-seals its checksum (Rust rewrite_structure_record: the
// dense table stores record id at slot id, so the record offset is
// 32 + id*80).
func rewriteStructureRecord(t *testing.T, path string, meta format.Meta, id uint32, edit func(record []byte)) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()
	page := make([]byte, format.PageSize)
	if _, err := file.ReadAt(page, int64(meta.StructureIDRoot)*format.PageSize); err != nil {
		t.Fatalf("read page: %v", err)
	}
	header, problem := format.InspectStructureTableHeader(page, meta.TxnID, uint32(format.StructureKindNetworkEnrichmentV1), nil)
	if problem != format.TreeHeaderProblemNone || header.Level != 0 {
		t.Fatalf("structure root header problem %v level %d", problem, header.Level)
	}
	at := 32 + int(id)*format.StructureRecordSize
	record := page[at : at+format.StructureRecordSize]
	decoded, err := format.DecodeStructureRecord(record)
	if err != nil || decoded.ID != id {
		t.Fatalf("slot record id mismatch: %v", err)
	}
	edit(record)
	if err := format.SealPageChecksum(page); err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := file.WriteAt(page, int64(meta.StructureIDRoot)*format.PageSize); err != nil {
		t.Fatalf("write page: %v", err)
	}
}

// rewriteStructureBranchChild rewrites one directory child of the
// branch root and re-seals its checksum (Rust
// rewrite_structure_branch_child).
func rewriteStructureBranchChild(t *testing.T, path string, meta format.Meta, index int, child uint32) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()
	page := make([]byte, format.PageSize)
	if _, err := file.ReadAt(page, int64(meta.StructureIDRoot)*format.PageSize); err != nil {
		t.Fatalf("read page: %v", err)
	}
	header, problem := format.InspectStructureTableHeader(page, meta.TxnID, uint32(format.StructureKindNetworkEnrichmentV1), nil)
	if problem != format.TreeHeaderProblemNone || header.Level == 0 {
		t.Fatalf("structure root header problem %v level %d", problem, header.Level)
	}
	at := 32 + index*4
	if format.U32(page[at:]) == 0 {
		t.Fatal("the fixture must populate the corrupted branch")
	}
	format.PutU32(page[at:], child)
	if err := format.SealPageChecksum(page); err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := file.WriteAt(page, int64(meta.StructureIDRoot)*format.PageSize); err != nil {
		t.Fatalf("write page: %v", err)
	}
}

// TestStructuredRecoveryConstructDamagedRecord flips the digest of one
// structure record and proves only the dependent range rejects (Rust
// damaged_structure_rejects_only_its_dependent_range).
func TestStructuredRecoveryConstructDamagedRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.iprdb")
	meta := structuredSourceLimit(t, path, 64, nil, []structuredPush{
		{from: 0, to: 9, value: enrichment(64512)},
		{from: 20, to: 29, value: enrichment(64513)},
	})
	source := mapSource(t, path)
	defer source.Close()
	rewriteStructureRecord(t, path, meta, 1, func(record []byte) {
		record[16] ^= 1
	})

	var unknown []RecoveryUnknownEnvelope
	sink := RecoverySinkFunc(func(envelope *RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		unknown = append(unknown, *envelope)
		return RecoverySinkContinue, nil
	})
	outputPath := filepath.Join(dir, "output.iprdb")
	construction, failure := constructStructured(t, source, meta, outputPath, recoveryBudget(1<<23), sink)
	if failure != nil {
		t.Fatalf("construct failure: %v", failure.cause)
	}
	report := construction.report
	if report.StructureEntries.Examined != 2 || report.StructureEntries.Accepted != 1 || report.StructureEntries.Rejected != 1 {
		t.Fatalf("structure counts %+v, want 2/1/1", report.StructureEntries)
	}
	if report.Ranges.Accepted != 1 || report.Ranges.Rejected != 1 {
		t.Fatalf("range counts %+v, want 1/1", report.Ranges)
	}
	hashInvalid := false
	for _, envelope := range unknown {
		if envelope.Reason == validation.ReasonStructureHashInvalid && envelope.Object == validation.ObjectStructureDictionary {
			hashInvalid = true
		}
	}
	if !hashInvalid {
		t.Fatalf("envelopes %+v, want structure hash invalid", unknown)
	}
	validateClean(t, outputPath)
}

// TestStructuredRecoveryConstructSlotIDMismatch rewrites the stored
// id of one structure record so it no longer matches its implied
// slot and proves the record rejects with the structure-invalid
// envelope (Rust structure_index Events::leaf: decode_record, then the
// id and limit proof; the mismatch is not a decode failure).
func TestStructuredRecoveryConstructSlotIDMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.iprdb")
	meta := structuredSourceLimit(t, path, 64, nil, []structuredPush{
		{from: 0, to: 9, value: enrichment(64512)},
		{from: 20, to: 29, value: enrichment(64513)},
	})
	source := mapSource(t, path)
	defer source.Close()
	rewriteStructureRecord(t, path, meta, 1, func(record []byte) {
		format.PutU32(record[4:8], 2)
	})

	var unknown []RecoveryUnknownEnvelope
	sink := RecoverySinkFunc(func(envelope *RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		unknown = append(unknown, *envelope)
		return RecoverySinkContinue, nil
	})
	outputPath := filepath.Join(dir, "output.iprdb")
	construction, failure := constructStructured(t, source, meta, outputPath, recoveryBudget(1<<23), sink)
	if failure != nil {
		t.Fatalf("construct failure: %v", failure.cause)
	}
	report := construction.report
	if report.StructureEntries.Examined != 2 || report.StructureEntries.Accepted != 1 || report.StructureEntries.Rejected != 1 {
		t.Fatalf("structure counts %+v, want 2/1/1", report.StructureEntries)
	}
	structureInvalid := false
	for _, envelope := range unknown {
		if envelope.Reason == validation.ReasonStructureInvalid && envelope.Object == validation.ObjectStructureDictionary {
			structureInvalid = true
		}
	}
	if !structureInvalid {
		t.Fatalf("envelopes %+v, want structure invalid", unknown)
	}
}

// TestStructuredRecoveryConstructMissingMembership corrupts the
// membership ID root and proves the structure depending on it rejects
// (Rust missing_membership_rejects_only_dependent_structure).
func TestStructuredRecoveryConstructMissingMembership(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.iprdb")
	meta := structuredSourceLimit(t, path, 64, [][2]any{{"threat", uint32(1)}}, []structuredPush{
		{from: 0, to: 9, value: enrichment(64512), membership: writer.OutputWords{1 << 1}},
		{from: 20, to: 29, value: enrichment(64513)},
	})
	source := mapSource(t, path)
	defer source.Close()
	corruptCRC(t, path, meta.MembershipIDRoot)

	var unknown []RecoveryUnknownEnvelope
	sink := RecoverySinkFunc(func(envelope *RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		unknown = append(unknown, *envelope)
		return RecoverySinkContinue, nil
	})
	outputPath := filepath.Join(dir, "output.iprdb")
	construction, failure := constructStructured(t, source, meta, outputPath, recoveryBudget(1<<23), sink)
	if failure != nil {
		t.Fatalf("construct failure: %v", failure.cause)
	}
	report := construction.report
	if report.StructureEntries.Accepted != 1 || report.StructureEntries.Rejected != 1 {
		t.Fatalf("structure counts %+v, want 1/1", report.StructureEntries)
	}
	if report.Ranges.Accepted != 1 || report.Ranges.Rejected != 1 {
		t.Fatalf("range counts %+v, want 1/1", report.Ranges)
	}
	membershipInvalid := false
	for _, envelope := range unknown {
		if envelope.Reason == validation.ReasonStructureMembershipInvalid && envelope.Object == validation.ObjectStructureDictionary {
			membershipInvalid = true
		}
	}
	if !membershipInvalid {
		t.Fatalf("envelopes %+v, want structure membership invalid", unknown)
	}
	validateClean(t, outputPath)
}

// TestStructuredRecoveryConstructBranchPointer rewrites one directory
// child out of bounds and proves the other leaf still recovers (Rust
// invalid_structure_branch_pointer_is_reported_and_best_effort_recovers_other_leaves).
func TestStructuredRecoveryConstructBranchPointer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.iprdb")
	pushes := make([]structuredPush, 0, 51)
	for index := uint32(0); index < 51; index++ {
		pushes = append(pushes, structuredPush{from: index * 2, to: index*2 + 1, value: enrichment(64_000 + index)})
	}
	meta := structuredSourceLimit(t, path, 64, nil, pushes)
	source := mapSource(t, path)
	defer source.Close()
	rewriteStructureBranchChild(t, path, meta, 1, uint32(meta.PageCount)+10)

	var unknown []RecoveryUnknownEnvelope
	sink := RecoverySinkFunc(func(envelope *RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		unknown = append(unknown, *envelope)
		return RecoverySinkContinue, nil
	})
	outputPath := filepath.Join(dir, "output.iprdb")
	construction, failure := constructStructured(t, source, meta, outputPath, recoveryBudget(1<<23), sink)
	if failure != nil {
		t.Fatalf("construct failure: %v", failure.cause)
	}
	report := construction.report
	if report.StructureEntries.Accepted != 49 {
		t.Fatalf("structure accepted %d, want 49", report.StructureEntries.Accepted)
	}
	if report.Ranges.Accepted != 49 || report.Ranges.Rejected != 2 {
		t.Fatalf("range counts %+v, want 49/2", report.Ranges)
	}
	outOfBounds := false
	for _, envelope := range unknown {
		if envelope.Reason == validation.ReasonPageOutOfBounds && envelope.Object == validation.ObjectStructureDictionary {
			outOfBounds = true
		}
	}
	if !outOfBounds {
		t.Fatalf("envelopes %+v, want page out of bounds", unknown)
	}
	validateClean(t, outputPath)
}

// structureLevelTwoFile builds the synthetic three-level structure
// generation of the validator regression: a level-2 directory root
// whose children span 25600 ids each (Rust coverage(level-1)), two
// level-1 directories, and record leaves at ids 1 and 25600. The
// count-only scan needs no range, hash, or used roots; the unused
// roots stay zero.
func structureLevelTwoFile(t *testing.T) string {
	t.Helper()
	meta := make([]byte, format.PageSize)
	copy(meta[0:8], format.MainMagic[:])
	format.PutU16(meta[8:10], format.MetaSize)
	meta[10] = format.PageShift
	meta[11] = format.AddressFamilyIPv4
	meta[12] = format.ValueKindStructured
	meta[13] = format.StructureKindNetworkEnrichmentV1
	copy(meta[16:32], "valid-tag")
	format.PutU64(meta[32:40], 1)   // database id
	format.PutU64(meta[48:56], 2)   // transaction
	format.PutU64(meta[72:80], 7)   // page count
	format.PutU64(meta[112:120], 1) // MembershipIDLimit
	format.PutU64(meta[200:208], 2) // StructureEntryCount
	format.PutU64(meta[208:216], 25_601)
	format.PutU32(meta[216:220], 2) // StructureIDRoot
	format.PutU32(meta[252:256], format.MetaCRC32C(meta))

	payloadA := make([]byte, format.NetworkEnrichmentV1PayloadSize)
	format.EncodeNetworkEnrichmentV1(payloadA, format.NetworkEnrichmentV1{ASN: 64_000})
	payloadB := make([]byte, format.NetworkEnrichmentV1PayloadSize)
	format.EncodeNetworkEnrichmentV1(payloadB, format.NetworkEnrichmentV1{ASN: 64_001})
	digestA, err := format.StructurePayloadDigest(format.StructureKindNetworkEnrichmentV1, payloadA)
	if err != nil {
		t.Fatal(err)
	}
	digestB, err := format.StructurePayloadDigest(format.StructureKindNetworkEnrichmentV1, payloadB)
	if err != nil {
		t.Fatal(err)
	}
	root := recoveryStructureDirectory(t, 2, [2]uint32{0, 3}, [2]uint32{1, 4})
	dirA := recoveryStructureDirectory(t, 1, [2]uint32{0, 5})
	dirB := recoveryStructureDirectory(t, 1, [2]uint32{0, 6})
	leafA := recoveryStructureLeaf(t, map[uint64][]byte{1: recoveryStructureRecord(1, payloadA, digestA)})
	leafB := recoveryStructureLeaf(t, map[uint64][]byte{0: recoveryStructureRecord(25_600, payloadB, digestB)})

	path := filepath.Join(t.TempDir(), "source.iprdb")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(meta); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(make([]byte, format.PageSize)); err != nil {
		t.Fatal(err)
	}
	for _, page := range [][]byte{root, dirA, dirB, leafA, leafB} {
		if _, err := file.Write(page); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// recoveryStructureDirectory builds one structure directory page of
// the given level (Rust table::initialize).
func recoveryStructureDirectory(t *testing.T, level uint16, children ...[2]uint32) []byte {
	t.Helper()
	page := make([]byte, format.PageSize)
	copy(page[:4], format.PageMagic[:])
	page[4] = byte(format.PageTypeStructureIDDirectory)
	format.PutU16(page[6:8], 32)
	format.PutU64(page[8:16], 2)
	format.PutU16(page[16:18], uint16(len(children)))
	format.PutU16(page[18:20], level)
	format.PutU16(page[20:22], format.StructureBranchEnd)
	format.PutU16(page[22:24], format.PageSize)
	format.PutU32(page[24:28], uint32(format.StructureKindNetworkEnrichmentV1))
	for _, child := range children {
		format.PutU32(page[32+child[0]*4:36+child[0]*4], child[1])
	}
	if err := format.SealPageChecksum(page); err != nil {
		t.Fatal(err)
	}
	return page
}

// recoveryStructureLeaf builds one level-0 structure record page over
// the given dense slots (Rust table::initialize).
func recoveryStructureLeaf(t *testing.T, slots map[uint64][]byte) []byte {
	t.Helper()
	page := make([]byte, format.PageSize)
	copy(page[:4], format.PageMagic[:])
	page[4] = byte(format.PageTypeStructureIDRecord)
	format.PutU16(page[6:8], 32)
	format.PutU64(page[8:16], 2)
	format.PutU16(page[16:18], uint16(len(slots)))
	format.PutU16(page[18:20], 0) // level
	format.PutU16(page[20:22], format.StructureLeafEnd)
	format.PutU16(page[22:24], format.PageSize)
	format.PutU32(page[24:28], uint32(format.StructureKindNetworkEnrichmentV1))
	for slot, cell := range slots {
		copy(page[32+slot*format.StructureRecordSize:], cell)
	}
	if err := format.SealPageChecksum(page); err != nil {
		t.Fatal(err)
	}
	return page
}

// recoveryStructureRecord builds one fixed 80-byte dictionary record.
func recoveryStructureRecord(id uint32, payload []byte, digest [32]byte) []byte {
	cell := make([]byte, format.StructureRecordSize)
	format.PutU16(cell[0:2], format.StructureRecordSize)
	format.PutU32(cell[4:8], id)
	format.PutU64(cell[8:16], 1)
	copy(cell[16:48], digest[:])
	copy(cell[48:80], payload)
	return cell
}

// TestRecoveryStructureCountLevelTwoRoot proves the recovery table
// scan scales the child base by the full coverage of the level below
// (25600 ids per level-2 child): both records of the three-level
// generation count, and neither implied-slot id is misattributed to a
// 50-id child span.
func TestRecoveryStructureCountLevelTwoRoot(t *testing.T) {
	path := structureLevelTwoFile(t)
	source := mapSource(t, path)
	defer source.Close()
	meta, ok := format.ParseIdentity(mustReadPage(t, path, 0))
	if !ok {
		t.Fatal("meta identity failed")
	}
	pages, err := newPageSet(1<<20, 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	count, err := countStructureRecords(source, meta, pages, nil)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("structure count %d, want 2", count)
	}
}

// mustReadPage reads one page of a synthetic test file.
func mustReadPage(t *testing.T, path string, pageNumber uint64) []byte {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	buf := make([]byte, format.PageSize)
	if _, err := file.ReadAt(buf, int64(pageNumber*format.PageSize)); err != nil {
		t.Fatal(err)
	}
	return buf
}
