package recovery

// Recovery api tests ported from the Rust recovery/api_tests.rs: the
// sink-failure partial facts with the private output removed, the
// final source-identity recheck refusal, the post-damage cancellation
// refusal, the destination race terminal result, and the happy-path
// recover_immutable with the rejected range verified in the published
// output. The incomplete source fixture declares one dangling range
// root, so every arm streams exactly one I/O-unreadable envelope.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/reader"
	"github.com/firehol/iprange/v4/go/internal/validation"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// apiTestBudget is the shared recovery budget (Rust api_tests budget:
// heap_only(1<<20, 100, 2)).
func apiTestBudget() *RecoveryBudget {
	return HeapOnly(1024*1024, 100, 2)
}

// apiTestSource builds one incomplete direct source (Rust
// incomplete_source): a finished direct output whose dual meta is
// rewritten to declare one dangling range root, so the recovery read
// reports exactly one I/O-unreadable envelope.
func apiTestSource(t *testing.T, path string) {
	t.Helper()
	builder, err := writer.NewOutputBuilder(path, writer.OutputSpec{
		AddressFamily:  format.AddressFamilyIPv4,
		ValueKind:      format.ValueKindDirect,
		StructureKind:  format.StructureKindNone,
		ValueTag:       tag16("first-seen"),
		DatabaseID:     id16(0x11),
		TxnID:          1,
		CommitNonce:    id16(0x22),
		FeedIndexLimit: 0,
	}, writer.OutputBudget{MaxOutputPages: 100}, 0, nil)
	if err != nil {
		t.Fatalf("NewOutputBuilder: %v", err)
	}
	if err := builder.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	meta := builder.Meta()
	meta.PageCount = 3
	meta.RangeRoot = 2
	meta.RangeRecordCount = 1
	if err := builder.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The dual meta rewrite goes through a writable mapping (the
	// mmap-only fixture discipline): the recovery trace leg proves the
	// machine never streams the source or the output through file I/O,
	// so the fixture must not either.
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()
	mapping, err := mapping.MapFile(file, 2*format.PageSize, true)
	if err != nil {
		t.Fatalf("MapFile: %v", err)
	}
	for _, pageNumber := range []uint32{0, 1} {
		page, err := mapping.Page(pageNumber)
		if err != nil {
			t.Fatalf("Page(%d): %v", pageNumber, err)
		}
		if err := meta.EncodeMapped(page); err != nil {
			t.Fatalf("EncodeMapped: %v", err)
		}
	}
	if err := mapping.FlushRange(0, 2*format.PageSize); err != nil {
		t.Fatalf("FlushRange: %v", err)
	}
	if err := mapping.SyncFile(); err != nil {
		t.Fatalf("SyncFile: %v", err)
	}
	if err := mapping.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// apiTestCandidate inspects the incomplete source and returns its
// newest candidate (Rust api_tests candidate()).
func apiTestCandidate(t *testing.T, path string) *RecoveryCandidate {
	t.Helper()
	inspection, err := InspectRecoveryCandidates(path, RecoveryInspectionImmutable, validation.HeapOnly(0, 1), nil)
	if err != nil {
		t.Fatalf("InspectRecoveryCandidates: %v", err)
	}
	candidate := inspection.Candidate(0)
	if candidate == nil {
		t.Fatal("no candidate projected")
	}
	return candidate
}

// assertNoPrivateNames proves no private publication artifact remains
// in one test directory (Rust api_tests assert_no_private_names).
func assertNoPrivateNames(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if len(entry.Name()) >= len(".iprange-") && entry.Name()[:len(".iprange-")] == ".iprange-" {
			t.Fatalf("private recovery artifact remained: %s", entry.Name())
		}
	}
}

// rewriteDualMeta rewrites both meta pages of one test file through a
// writable mapping (the mmap-only fixture discipline: the recovery
// trace leg proves the machine never streams the source through file
// I/O, so the fixture must not either).
func rewriteDualMeta(t *testing.T, path string, change func(*format.Meta)) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()
	mapping, err := mapping.MapFile(file, 2*format.PageSize, true)
	if err != nil {
		t.Fatalf("MapFile: %v", err)
	}
	defer mapping.Close()
	for _, pageNumber := range []uint32{0, 1} {
		page, err := mapping.Page(pageNumber)
		if err != nil {
			t.Fatalf("Page(%d): %v", pageNumber, err)
		}
		meta, ok := format.ParseIdentity(page)
		if !ok {
			t.Fatalf("page %d not identity-readable", pageNumber)
		}
		change(&meta)
		if err := meta.EncodeMapped(page); err != nil {
			t.Fatalf("EncodeMapped: %v", err)
		}
	}
	if err := mapping.FlushRange(0, 2*format.PageSize); err != nil {
		t.Fatalf("FlushRange: %v", err)
	}
	if err := mapping.SyncFile(); err != nil {
		t.Fatalf("SyncFile: %v", err)
	}
}

// TestRecoverImmutableRefusesUnknownStructureKind proves the
// output-spec structure-kind gate (Rust api.rs output_spec): a
// structured source whose meta pair declares an unknown structure
// kind refuses with the UnsupportedStructure class, the default
// report, and no destination artifact — the builder never exists, so
// no sink traffic or analysis facts are produced.
func TestRecoverImmutableRefusesUnknownStructureKind(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.v4")
	structuredSourceLimit(t, sourcePath, 64, nil, []structuredPush{
		{from: 0, to: 9, value: enrichment(64512)},
	})
	rewriteDualMeta(t, sourcePath, func(meta *format.Meta) {
		meta.StructureKind = 7
	})
	candidate := apiTestCandidate(t, sourcePath)
	sinkTraffic := 0
	sink := RecoverySinkFunc(func(*RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		sinkTraffic++
		return RecoverySinkContinue, nil
	})
	outputPath := filepath.Join(dir, "output.v4")
	_, failure := RecoverImmutable(sourcePath, candidate, outputPath, apiTestBudget(), nil, sink)
	if failure == nil {
		t.Fatal("unknown structure kind accepted")
	}
	var fe *format.Error
	if !errors.As(failure.Cause, &fe) || fe.Code != format.CodeUnsupportedStructure {
		t.Fatalf("cause %v, want UnsupportedStructure", failure.Cause)
	}
	if failure.Report.UnknownEnvelopes != 0 {
		t.Fatalf("unknown envelopes %d, want the default report", failure.Report.UnknownEnvelopes)
	}
	if sinkTraffic != 0 {
		t.Fatalf("sink traffic %d, want none before the builder", sinkTraffic)
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("output still present: %v", err)
	}
	assertNoPrivateNames(t, dir)
}

// TestRecoverImmutableSinkFailureReturnsPartialFactsAndRemovesThe
// PrivateOutput ports the Rust sink_failure arm: the failing sink
// stops the build with the partial report and the private output is
// removed with a clean ledger.
func TestRecoverImmutableSinkFailureReturnsPartialFactsAndRemovesThePrivateOutput(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.v4")
	outputPath := filepath.Join(dir, "output.v4")
	apiTestSource(t, sourcePath)
	candidate := apiTestCandidate(t, sourcePath)

	sink := RecoverySinkFunc(func(*RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		return RecoverySinkContinue, errors.New("injected recovery sink failure")
	})
	_, failure := RecoverImmutable(sourcePath, candidate, outputPath, apiTestBudget(), nil, sink)
	if failure == nil {
		t.Fatal("recovery succeeded despite the sink failure")
	}
	var fe *format.Error
	if !errors.As(failure.Cause, &fe) || fe.Code != format.CodeSinkFailed {
		t.Fatalf("cause %v, want SinkFailed", failure.Cause)
	}
	if failure.Report.UnknownEnvelopes != 1 {
		t.Fatalf("unknown envelopes %d, want 1", failure.Report.UnknownEnvelopes)
	}
	if !failure.Cleanup.Empty() {
		t.Fatalf("cleanup %+v, want empty", failure.Cleanup)
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("output still present after the sink failure: %v", err)
	}
	assertNoPrivateNames(t, dir)
}

// TestRecoverImmutableFinalSourceIdentityRecheckBlocksPublicationAnd
// CleansOutput ports the Rust final_source_identity_recheck arm: the
// source path is replaced during the build, so the final check refuses
// with the candidate-changed class and the private output is removed.
func TestRecoverImmutableFinalSourceIdentityRecheckBlocksPublicationAndCleansOutput(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.v4")
	displacedPath := filepath.Join(dir, "displaced.v4")
	outputPath := filepath.Join(dir, "output.v4")
	apiTestSource(t, sourcePath)
	candidate := apiTestCandidate(t, sourcePath)
	replaced := false

	sink := RecoverySinkFunc(func(*RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		if !replaced {
			if err := os.Rename(sourcePath, displacedPath); err != nil {
				return RecoverySinkStop, err
			}
			copied, err := os.ReadFile(displacedPath)
			if err != nil {
				return RecoverySinkStop, err
			}
			if err := os.WriteFile(sourcePath, copied, 0o600); err != nil {
				return RecoverySinkStop, err
			}
			replaced = true
		}
		return RecoverySinkContinue, nil
	})
	_, failure := RecoverImmutable(sourcePath, candidate, outputPath, apiTestBudget(), nil, sink)
	if failure == nil {
		t.Fatal("recovery succeeded despite the source replacement")
	}
	var fe *format.Error
	if !errors.As(failure.Cause, &fe) || fe.Code != format.CodeRecoveryCandidateChanged {
		t.Fatalf("cause %v, want RecoveryCandidateChanged", failure.Cause)
	}
	if !failure.Cleanup.Empty() {
		t.Fatalf("cleanup %+v, want empty", failure.Cleanup)
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("output still present after the candidate-changed refusal: %v", err)
	}
	assertNoPrivateNames(t, dir)
}

// TestRecoverImmutableCancellationAfterDamageDeliveryAbortsBefore
// Publication ports the Rust cancellation_after_damage_delivery arm:
// the sink cancels during the first envelope and the next checkpoint
// refuses with the cancelled class before any publication.
func TestRecoverImmutableCancellationAfterDamageDeliveryAbortsBeforePublication(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.v4")
	outputPath := filepath.Join(dir, "output.v4")
	apiTestSource(t, sourcePath)
	candidate := apiTestCandidate(t, sourcePath)
	cancelled := false

	check := func() error {
		if cancelled {
			return &format.Error{Code: format.CodeCancelled, Detail: "operation was cancelled"}
		}
		return nil
	}
	sink := RecoverySinkFunc(func(*RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		cancelled = true
		return RecoverySinkContinue, nil
	})
	_, failure := RecoverImmutable(sourcePath, candidate, outputPath, apiTestBudget(), check, sink)
	if failure == nil {
		t.Fatal("recovery succeeded despite the cancellation")
	}
	var fe *format.Error
	if !errors.As(failure.Cause, &fe) || fe.Code != format.CodeCancelled {
		t.Fatalf("cause %v, want Cancelled", failure.Cause)
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("output still present after the cancellation: %v", err)
	}
	assertNoPrivateNames(t, dir)
}

// TestRecoverImmutableDestinationRaceReturnsATerminalNonpublication
// Result ports the Rust destination_race arm: a foreign destination
// appearing during the build yields a completed non-publication result
// with the foreign content preserved.
func TestRecoverImmutableDestinationRaceReturnsATerminalNonpublicationResult(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.v4")
	outputPath := filepath.Join(dir, "output.v4")
	apiTestSource(t, sourcePath)
	candidate := apiTestCandidate(t, sourcePath)
	created := false

	sink := RecoverySinkFunc(func(*RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		if !created {
			if err := os.WriteFile(outputPath, []byte("foreign"), 0o600); err != nil {
				return RecoverySinkStop, err
			}
			created = true
		}
		return RecoverySinkContinue, nil
	})
	result, failure := RecoverImmutable(sourcePath, candidate, outputPath, apiTestBudget(), nil, sink)
	if failure != nil {
		t.Fatalf("recovery failed instead of returning the terminal result: %v", failure)
	}
	if result.Publication.Publication == publication.PublicationPublished {
		t.Fatal("destination race published the output")
	}
	if result.Publication.Cause == nil {
		t.Fatal("destination race result carries no cause")
	}
	content, err := os.ReadFile(outputPath)
	if err != nil || string(content) != "foreign" {
		t.Fatalf("destination content %q err=%v, want foreign", content, err)
	}
}

// TestRecoverImmutableConstructsThePublishedRejectedRange ports the
// happy recover_immutable arm over the incomplete source: the dangling
// range is rejected, the output is a published two-page direct
// database with no ranges.
func TestRecoverImmutableConstructsThePublishedRejectedRange(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.v4")
	outputPath := filepath.Join(dir, "output.v4")
	apiTestSource(t, sourcePath)
	candidate := apiTestCandidate(t, sourcePath)

	result, failure := RecoverImmutable(sourcePath, candidate, outputPath, apiTestBudget(), nil, nil)
	if failure != nil {
		t.Fatalf("recovery: %v", failure)
	}
	if result.Publication.Publication != publication.PublicationPublished {
		t.Fatalf("publication %v, want published (cause %v)", result.Publication.Publication, result.Publication.Cause)
	}
	if result.Publication.Cause != nil {
		t.Fatalf("published result carries cause %v", result.Publication.Cause)
	}
	if result.Report.UnknownEnvelopes != 1 || result.Report.Pages.Rejected != 1 || result.Report.Pages.IOUnreadable != 1 {
		t.Fatalf("report %+v, want 1 unknown 1 rejected unreadable page", result.Report)
	}
	r, err := reader.OpenImmutable(outputPath)
	if err != nil {
		t.Fatalf("OpenImmutable: %v", err)
	}
	defer r.Close()
	meta := r.Meta()
	if meta.PageCount != 2 || meta.RangeRecordCount != 0 || meta.RangeRoot != 0 {
		t.Fatalf("meta %+v, want 2 pages 0 ranges", meta)
	}
}
