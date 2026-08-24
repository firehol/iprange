package recovery

// Structured recovery construction tests (Rust recovery/structured_tests
// arms): the digest-damaged record rejection, the missing-membership
// rejection, and the out-of-bounds branch pointer best-effort recovery.

import (
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
	builder, err := writer.NewOutputBuilder(path, spec, writer.OutputBudget{MaxOutputPages: 20_000}, writer.ReferenceBatchEntryLimit, nil)
	if err != nil {
		t.Fatalf("NewOutputBuilder: %v", err)
	}
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
	builder, err := writer.NewOutputBuilder(path, spec, writer.OutputBudget{MaxOutputPages: 20_000}, writer.ReferenceBatchEntryLimit, nil)
	if err != nil {
		t.Fatalf("NewOutputBuilder: %v", err)
	}
	return builder
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
	}
	return construction, failure
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
	decoded, err := format.DecodeStructureRecord(record, uint64(id))
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
