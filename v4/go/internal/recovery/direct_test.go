package recovery

// Direct recovery construction tests ported from the Rust
// recovery/direct_tests.rs: the canonical ordered output, the
// crc-damaged leaf skip, the whole overlap-component rejection, the
// bounded in-memory sort of disordered records, the insufficient-heap
// refusal, and the metadata preservation and omission.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/reader"
	"github.com/firehol/iprange/v4/go/internal/validation"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// directTag is the shared value tag of the direct test sources and
// outputs (the Rust ValueTag::FIRST_SEEN peer).
var directTag = tag16("first-seen")

func tag16(text string) [16]byte {
	var tag [16]byte
	copy(tag[:], text)
	return tag
}

func id16(value byte) [16]byte {
	var out [16]byte
	for index := range out {
		out[index] = value
	}
	return out
}

// directSourceBuilder starts one direct source output at path with the
// fixed Rust test identity (txn 7).
func directSourceBuilder(t *testing.T, path string) *writer.OutputBuilder {
	t.Helper()
	spec := writer.OutputSpec{
		AddressFamily:  format.AddressFamilyIPv4,
		ValueKind:      format.ValueKindDirect,
		StructureKind:  format.StructureKindNone,
		ValueTag:       directTag,
		DatabaseID:     id16(1),
		TxnID:          7,
		CommitNonce:    id16(2),
		FeedIndexLimit: 0,
	}
	builder, err := writer.NewOutputBuilder(path, spec, writer.OutputBudget{MaxOutputPages: 20_000}, 0, nil)
	if err != nil {
		t.Fatalf("NewOutputBuilder: %v", err)
	}
	return builder
}

// directOutputBuilder starts one recovery destination output at path
// (Rust output_builder: txn 1, fresh identity).
func directOutputBuilder(t *testing.T, path string) *writer.OutputBuilder {
	t.Helper()
	spec, err := writer.FreshOutputSpec(format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, directTag, 0)
	if err != nil {
		t.Fatalf("FreshOutputSpec: %v", err)
	}
	builder, err := writer.NewOutputBuilder(path, spec, writer.OutputBudget{MaxOutputPages: 20_000}, 0, nil)
	if err != nil {
		t.Fatalf("NewOutputBuilder: %v", err)
	}
	return builder
}

// finishRanges seals one direct source over the given ranges and
// returns the committed meta (Rust finish_ranges).
func finishRanges(t *testing.T, builder *writer.OutputBuilder, ranges [][3]uint32) format.Meta {
	t.Helper()
	for _, record := range ranges {
		if err := builder.PushDirectV4(record[0], record[1], record[2]); err != nil {
			t.Fatalf("PushDirectV4: %v", err)
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

// mapSource opens one finished source file read-only (Rust
// source_mapping).
func mapSource(t *testing.T, path string) *mapping.Mapping {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	m, err := mapping.MapFile(file, uint64(info.Size()), false)
	if err != nil {
		t.Fatalf("MapFile: %v", err)
	}
	return m
}

// recoveryBudget builds the shared test budget (Rust budget).
func recoveryBudget(maxHeapBytes uint64) *RecoveryBudget {
	return HeapOnly(maxHeapBytes, 20_000, 2)
}

// constructDirect runs one direct construction over the source mapping
// and closes the finished output (Rust construct + drop(finished.file)).
func constructDirect(t *testing.T, source *mapping.Mapping, meta format.Meta, outputPath string, budget *RecoveryBudget) (*Construction, *constructionFailure) {
	t.Helper()
	builder := directOutputBuilder(t, outputPath)
	construction, failure := directConstruct(source, meta, builder, budget, nil, nil)
	if failure == nil {
		if err := construction.finished.Close(); err != nil {
			t.Fatalf("Close finished output: %v", err)
		}
	}
	return construction, failure
}

// editRootLeaf edits the root page of the range tree in place and
// re-seals its checksum (Rust edit_root_leaf).
func editRootLeaf(t *testing.T, path string, meta format.Meta, edit func(page []byte, header *format.PageHeader)) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()
	page := make([]byte, format.PageSize)
	if _, err := file.ReadAt(page, int64(meta.RangeRoot)*format.PageSize); err != nil {
		t.Fatalf("read page: %v", err)
	}
	header, problem := format.InspectTreeHeader(page, meta.TxnID,
		byte(format.PageTypeRangeBranch), byte(format.PageTypeRangeLeaf), uint32(meta.AddressFamily), nil)
	if problem != format.TreeHeaderProblemNone {
		t.Fatalf("root header problem %v", problem)
	}
	if header.Level != 0 {
		t.Fatalf("root level %d, want leaf 0", header.Level)
	}
	edit(page, &header)
	if err := format.SealPageChecksum(page); err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := file.WriteAt(page, int64(meta.RangeRoot)*format.PageSize); err != nil {
		t.Fatalf("write page: %v", err)
	}
}

// swapFirstTwoRecords swaps the first two root-leaf records (Rust
// swap_first_two_records).
func swapFirstTwoRecords(t *testing.T, path string, meta format.Meta) {
	t.Helper()
	editRootLeaf(t, path, meta, func(page []byte, header *format.PageHeader) {
		if header.ItemCount < 2 {
			t.Fatalf("item count %d, want at least 2", header.ItemCount)
		}
		slotted, err := format.OpenSlottedHeader(page, *header, format.PageTypeRangeLeaf, uint32(meta.AddressFamily), format.SlotItemsPerPage)
		if err != nil {
			t.Fatalf("slotted: %v", err)
		}
		first, err := slotted.Record(0)
		if err != nil {
			t.Fatalf("record 0: %v", err)
		}
		second, err := slotted.Record(1)
		if err != nil {
			t.Fatalf("record 1: %v", err)
		}
		var saved [12]byte
		copy(saved[:], first)
		copy(first, second)
		copy(second, saved[:])
	})
}

// rewriteSecondStart rewrites the from-key of the second root-leaf
// record (Rust rewrite_second_start).
func rewriteSecondStart(t *testing.T, path string, meta format.Meta, from uint32) {
	t.Helper()
	editRootLeaf(t, path, meta, func(page []byte, header *format.PageHeader) {
		if header.ItemCount < 2 {
			t.Fatalf("item count %d, want at least 2", header.ItemCount)
		}
		start := int(format.U16(page[34:36]))
		format.PutU32(page[start:start+4], from)
	})
}

// firstChild returns the first child page of the range-tree root (Rust
// first_child: the root must be a branch).
func firstChild(t *testing.T, source *mapping.Mapping, meta format.Meta) uint32 {
	t.Helper()
	page, err := source.Page(meta.RangeRoot)
	if err != nil {
		t.Fatalf("root page: %v", err)
	}
	header, problem := format.InspectTreeHeader(page, meta.TxnID,
		byte(format.PageTypeRangeBranch), byte(format.PageTypeRangeLeaf), uint32(meta.AddressFamily), nil)
	if problem != format.TreeHeaderProblemNone {
		t.Fatalf("root header problem %v", problem)
	}
	if header.Level == 0 {
		t.Fatal("root is not a branch")
	}
	slotted, err := format.OpenSlottedHeader(page, header, format.PageTypeRangeBranch, uint32(meta.AddressFamily), format.SlotItemsPerPage)
	if err != nil {
		t.Fatalf("slotted: %v", err)
	}
	cell, err := slotted.Record(0)
	if err != nil {
		t.Fatalf("record 0: %v", err)
	}
	_, child, err := format.DecodeRangeEntryV4(cell)
	if err != nil {
		t.Fatalf("branch cell decode: %v", err)
	}
	return child
}

// corruptCRC flips one payload byte of a page so its checksum no
// longer verifies (Rust corrupt_crc).
func corruptCRC(t *testing.T, path string, pageNumber uint32) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()
	page := make([]byte, format.PageSize)
	if _, err := file.ReadAt(page, int64(pageNumber)*format.PageSize); err != nil {
		t.Fatalf("read page: %v", err)
	}
	page[100] ^= 0x5a
	if _, err := file.WriteAt(page, int64(pageNumber)*format.PageSize); err != nil {
		t.Fatalf("write page: %v", err)
	}
}

// outputRanges reads every direct IPv4 range of one finished output
// (Rust output_ranges).
func outputRanges(t *testing.T, path string) []reader.DirectRange4 {
	t.Helper()
	r, err := reader.OpenImmutable(path)
	if err != nil {
		t.Fatalf("OpenImmutable: %v", err)
	}
	defer r.Close()
	cursor, err := r.NewDirectCursor4(reader.RangeForward)
	if err != nil {
		t.Fatalf("NewDirectCursor4: %v", err)
	}
	var out []reader.DirectRange4
	for {
		record, ok, err := cursor.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !ok {
			return out
		}
		out = append(out, record)
	}
}

// validateClean proves the finished output validates with zero
// findings (Rust validate_clean).
func validateClean(t *testing.T, path string) {
	t.Helper()
	result, failure := validation.Validate(path, validation.ValidationModeImmutableCurrent, validation.HeapOnly(2*1024*1024, 1), nil, nil)
	if failure != nil {
		t.Fatalf("validate failure: %v", failure.Cause)
	}
	if !result.Valid {
		t.Fatalf("validate progress: %+v", result.Progress)
	}
}

// assertRanges compares the output ranges with the expected records.
func assertRanges(t *testing.T, got []reader.DirectRange4, want [][3]uint32) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("range count %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index].From != want[index][0] || got[index].To != want[index][1] || got[index].Value != want[index][2] {
			t.Fatalf("range %d %+v, want %v", index, got[index], want[index])
		}
	}
}

func TestOrderedDirectRecoveryStreamsACanonicalOutput(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.iprdb")
	meta := finishRanges(t, directSourceBuilder(t, sourcePath), [][3]uint32{{0, 9, 1}, {10, 19, 2}, {30, 39, 2}, {100, 199, 3}})
	source := mapSource(t, sourcePath)
	defer source.Close()
	var unknown []RecoveryUnknownEnvelope
	outputPath := filepath.Join(dir, "output.iprdb")
	builder := directOutputBuilder(t, outputPath)
	sink := RecoverySinkFunc(func(envelope *RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		unknown = append(unknown, *envelope)
		return RecoverySinkContinue, nil
	})
	construction, failure := directConstruct(source, meta, builder, recoveryBudget(1024*1024), nil, sink)
	if failure != nil {
		t.Fatalf("construct: %v", failure.cause)
	}
	if err := construction.finished.Close(); err != nil {
		t.Fatalf("Close finished output: %v", err)
	}
	if len(unknown) != 0 {
		t.Fatalf("unknown envelopes %d, want 0", len(unknown))
	}
	report := construction.report
	if report.Ranges.Examined != 4 || report.Ranges.Accepted != 4 || report.Ranges.Rejected != 0 {
		t.Fatalf("range counts %+v, want 4/4/0", report.Ranges)
	}
	if want := format.CardinalityFromUint64(130); report.VerifiedAddresses.Compare(want) != 0 {
		t.Fatalf("verified addresses %s, want %s", report.VerifiedAddresses, want)
	}
	assertRanges(t, outputRanges(t, outputPath), [][3]uint32{{0, 9, 1}, {10, 19, 2}, {30, 39, 2}, {100, 199, 3}})
	validateClean(t, outputPath)
}

func TestCRCDamagedLeafIsSkippedAndReportedAsUnbounded(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.iprdb")
	builder := directSourceBuilder(t, sourcePath)
	for index := uint32(0); index < 2_000; index++ {
		from := index * 3
		if err := builder.PushDirectV4(from, from+1, index); err != nil {
			t.Fatalf("PushDirectV4: %v", err)
		}
	}
	if err := builder.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	meta := builder.Meta()
	if err := builder.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	source := mapSource(t, sourcePath)
	defer source.Close()
	corruptCRC(t, sourcePath, firstChild(t, source, meta))
	var unknown []RecoveryUnknownEnvelope
	outputPath := filepath.Join(dir, "output.iprdb")
	outBuilder := directOutputBuilder(t, outputPath)
	sink := RecoverySinkFunc(func(envelope *RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		unknown = append(unknown, *envelope)
		return RecoverySinkContinue, nil
	})
	construction, failure := directConstruct(source, meta, outBuilder, recoveryBudget(2*1024*1024), nil, sink)
	if failure != nil {
		t.Fatalf("construct: %v", failure.cause)
	}
	if err := construction.finished.Close(); err != nil {
		t.Fatalf("Close finished output: %v", err)
	}
	report := construction.report
	if !report.HasUnboundedUnknown {
		t.Fatal("missing unbounded unknown")
	}
	if report.Pages.Rejected != 1 || report.Pages.IOUnreadable != 0 {
		t.Fatalf("page counts %+v, want 1 rejected and 0 I/O-unreadable", report.Pages)
	}
	if report.Ranges.Accepted == 0 || report.Ranges.Accepted >= 2_000 {
		t.Fatalf("accepted ranges %d, want between 1 and 2000", report.Ranges.Accepted)
	}
	found := false
	for _, envelope := range unknown {
		if envelope.Reason == validation.ReasonPageCrcMismatch {
			found = true
		}
	}
	if !found {
		t.Fatal("missing PageCrcMismatch envelope")
	}
	validateClean(t, outputPath)
}

func TestAnOverlapComponentIsRejectedWhole(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.iprdb")
	meta := finishRanges(t, directSourceBuilder(t, sourcePath), [][3]uint32{{0, 9, 1}, {20, 29, 2}, {40, 49, 3}})
	rewriteSecondStart(t, sourcePath, meta, 5)
	source := mapSource(t, sourcePath)
	defer source.Close()
	var unknown []RecoveryUnknownEnvelope
	outputPath := filepath.Join(dir, "output.iprdb")
	outBuilder := directOutputBuilder(t, outputPath)
	sink := RecoverySinkFunc(func(envelope *RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		unknown = append(unknown, *envelope)
		return RecoverySinkContinue, nil
	})
	construction, failure := directConstruct(source, meta, outBuilder, recoveryBudget(1024*1024), nil, sink)
	if failure != nil {
		t.Fatalf("construct: %v", failure.cause)
	}
	if err := construction.finished.Close(); err != nil {
		t.Fatalf("Close finished output: %v", err)
	}
	report := construction.report
	if report.Ranges.Examined != 3 || report.Ranges.Accepted != 1 || report.Ranges.Rejected != 2 {
		t.Fatalf("range counts %+v, want 3/1/2", report.Ranges)
	}
	if want := format.CardinalityFromUint64(30); report.RejectedAddresses.Compare(want) != 0 {
		t.Fatalf("rejected addresses %s, want %s", report.RejectedAddresses, want)
	}
	found := false
	for _, envelope := range unknown {
		if envelope.Reason == validation.ReasonRangeOverlap {
			found = true
		}
	}
	if !found {
		t.Fatal("missing RangeOverlap envelope")
	}
	assertRanges(t, outputRanges(t, outputPath), [][3]uint32{{40, 49, 3}})
	validateClean(t, outputPath)
}

func TestDisorderedReadableRecordsAreSortedWithBoundedHeap(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.iprdb")
	meta := finishRanges(t, directSourceBuilder(t, sourcePath), [][3]uint32{{0, 9, 1}, {20, 29, 2}, {40, 49, 3}})
	swapFirstTwoRecords(t, sourcePath, meta)
	source := mapSource(t, sourcePath)
	defer source.Close()
	var unknown []RecoveryUnknownEnvelope
	outputPath := filepath.Join(dir, "output.iprdb")
	outBuilder := directOutputBuilder(t, outputPath)
	sink := RecoverySinkFunc(func(envelope *RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		unknown = append(unknown, *envelope)
		return RecoverySinkContinue, nil
	})
	construction, failure := directConstruct(source, meta, outBuilder, recoveryBudget(4096), nil, sink)
	if failure != nil {
		t.Fatalf("construct: %v", failure.cause)
	}
	if err := construction.finished.Close(); err != nil {
		t.Fatalf("Close finished output: %v", err)
	}
	if construction.report.Ranges.Accepted != 3 {
		t.Fatalf("accepted ranges %d, want 3", construction.report.Ranges.Accepted)
	}
	found := false
	for _, envelope := range unknown {
		if envelope.Reason == validation.ReasonTreeOrderInvalid {
			found = true
		}
	}
	if !found {
		t.Fatal("missing TreeOrderInvalid envelope")
	}
	assertRanges(t, outputRanges(t, outputPath), [][3]uint32{{0, 9, 1}, {20, 29, 2}, {40, 49, 3}})
	validateClean(t, outputPath)
}

func TestDisorderedRecoveryRefusesInsufficientHeapBeforeOutputMutation(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.iprdb")
	meta := finishRanges(t, directSourceBuilder(t, sourcePath), [][3]uint32{{0, 9, 1}, {20, 29, 2}, {40, 49, 3}})
	swapFirstTwoRecords(t, sourcePath, meta)
	source := mapSource(t, sourcePath)
	defer source.Close()
	builder := directOutputBuilder(t, filepath.Join(dir, "output.iprdb"))
	_, failure := directConstruct(source, meta, builder, recoveryBudget(80), nil, nil)
	if failure == nil {
		t.Fatal("construct succeeded with an insufficient heap")
	}
	var full *format.Error
	if !errors.As(failure.cause, &full) || full.Code != format.CodeInsufficientResourceBudget || full.Detail != "recovery unordered ranges" {
		t.Fatalf("cause %v, want the unordered-ranges budget class", failure.cause)
	}
	if failure.report.Ranges.Examined != 3 {
		t.Fatalf("examined ranges %d, want 3", failure.report.Ranges.Examined)
	}
	if err := failure.builder.Close(); err != nil {
		t.Fatalf("Close retained builder: %v", err)
	}
}

func TestCompleteMetadataIsPreservedAndDamagedMetadataIsOmitted(t *testing.T) {
	payload := []byte(`{"source":"recovery"}`)
	dir := t.TempDir()

	// Clean source with one range and one metadata payload.
	cleanSource := filepath.Join(dir, "clean.iprdb")
	builder := directSourceBuilder(t, cleanSource)
	if err := builder.PushDirectV4(10, 19, 7); err != nil {
		t.Fatalf("PushDirectV4: %v", err)
	}
	if err := builder.WriteMetadataWithBudget(payload, 2*1024*1024); err != nil {
		t.Fatalf("WriteMetadataWithBudget: %v", err)
	}
	if err := builder.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	meta := builder.Meta()
	if err := builder.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	source := mapSource(t, cleanSource)
	construction, failure := constructDirect(t, source, meta, filepath.Join(dir, "clean-output.iprdb"), recoveryBudget(2*1024*1024))
	source.Close()
	if failure != nil {
		t.Fatalf("construct: %v", failure.cause)
	}
	if construction.report.MetadataChunks.Accepted != 1 {
		t.Fatalf("accepted metadata chunks %d, want 1", construction.report.MetadataChunks.Accepted)
	}
	output := filepath.Join(dir, "clean-output.iprdb")
	r, err := reader.OpenImmutable(output)
	if err != nil {
		t.Fatalf("OpenImmutable: %v", err)
	}
	length, ok := r.MetadataJSONLen()
	r.Close()
	if !ok || length != uint64(len(payload)) {
		t.Fatalf("clean output metadata %d present=%v, want %d bytes", length, ok, len(payload))
	}

	// Damaged source: copy the clean file and corrupt the metadata
	// chain root checksum.
	damagedSource := filepath.Join(dir, "damaged.iprdb")
	data, err := os.ReadFile(cleanSource)
	if err != nil {
		t.Fatalf("read clean source: %v", err)
	}
	if err := os.WriteFile(damagedSource, data, 0o600); err != nil {
		t.Fatalf("copy source: %v", err)
	}
	corruptCRC(t, damagedSource, meta.MetadataRoot)
	source = mapSource(t, damagedSource)
	defer source.Close()
	var unknown []RecoveryUnknownEnvelope
	outputPath := filepath.Join(dir, "damaged-output.iprdb")
	outBuilder := directOutputBuilder(t, outputPath)
	sink := RecoverySinkFunc(func(envelope *RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		unknown = append(unknown, *envelope)
		return RecoverySinkContinue, nil
	})
	construction, failure = directConstruct(source, meta, outBuilder, recoveryBudget(2*1024*1024), nil, sink)
	if failure != nil {
		t.Fatalf("construct: %v", failure.cause)
	}
	if err := construction.finished.Close(); err != nil {
		t.Fatalf("Close finished output: %v", err)
	}
	if construction.report.MetadataChunks.Rejected != 1 {
		t.Fatalf("rejected metadata chunks %d, want 1", construction.report.MetadataChunks.Rejected)
	}
	if construction.report.HasUnboundedUnknown {
		t.Fatal("metadata damage must not be unbounded")
	}
	found := false
	for _, envelope := range unknown {
		if envelope.Object == validation.ObjectMetadata && envelope.Reason == validation.ReasonPageCrcMismatch {
			found = true
		}
	}
	if !found {
		t.Fatal("missing metadata PageCrcMismatch envelope")
	}
	r, err = reader.OpenImmutable(outputPath)
	if err != nil {
		t.Fatalf("OpenImmutable: %v", err)
	}
	_, ok = r.MetadataJSONLen()
	r.Close()
	if ok {
		t.Fatal("damaged output must carry no metadata")
	}
	validateClean(t, outputPath)
}

// TestDamagedMetadataHeaderIsOmitted corrupts the born transaction of
// the metadata chain root (re-sealing the page so the checksum still
// passes) and proves the chain rejects with the metadata-invalid class
// exactly like the Rust require_page_header arm: the common, kind, and
// born identity gates run before the chunk body proof.
func TestDamagedMetadataHeaderIsOmitted(t *testing.T) {
	payload := []byte(`{"source":"recovery"}`)
	dir := t.TempDir()
	path := filepath.Join(dir, "source.iprdb")
	builder := directSourceBuilder(t, path)
	if err := builder.PushDirectV4(10, 19, 7); err != nil {
		t.Fatalf("PushDirectV4: %v", err)
	}
	if err := builder.WriteMetadataWithBudget(payload, 2*1024*1024); err != nil {
		t.Fatalf("WriteMetadataWithBudget: %v", err)
	}
	if err := builder.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	meta := builder.Meta()
	if err := builder.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	corruptPageBorn(t, path, meta.MetadataRoot, meta.TxnID)
	source := mapSource(t, path)
	defer source.Close()
	var unknown []RecoveryUnknownEnvelope
	sink := RecoverySinkFunc(func(envelope *RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		unknown = append(unknown, *envelope)
		return RecoverySinkContinue, nil
	})
	outputPath := filepath.Join(dir, "output.iprdb")
	outBuilder := directOutputBuilder(t, outputPath)
	construction, failure := directConstruct(source, meta, outBuilder, recoveryBudget(2*1024*1024), nil, sink)
	if failure != nil {
		t.Fatalf("construct: %v", failure.cause)
	}
	if err := construction.finished.Close(); err != nil {
		t.Fatalf("Close finished output: %v", err)
	}
	if construction.report.MetadataChunks.Rejected != 1 {
		t.Fatalf("rejected metadata chunks %d, want 1", construction.report.MetadataChunks.Rejected)
	}
	if construction.report.HasUnboundedUnknown {
		t.Fatal("metadata damage must not be unbounded")
	}
	found := false
	for _, envelope := range unknown {
		if envelope.Object == validation.ObjectMetadata && envelope.Reason == validation.ReasonMetadataInvalid {
			found = true
		}
	}
	if !found {
		t.Fatalf("envelopes %+v, want metadata invalid", unknown)
	}
	r, err := reader.OpenImmutable(outputPath)
	if err != nil {
		t.Fatalf("OpenImmutable: %v", err)
	}
	_, ok := r.MetadataJSONLen()
	r.Close()
	if ok {
		t.Fatal("damaged output must carry no metadata")
	}
	validateClean(t, outputPath)
}
