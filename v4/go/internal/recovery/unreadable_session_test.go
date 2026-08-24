package recovery

// Worker-session unreadable-page tests for the recovery domain (Rust
// source_guard/basic.rs:165 map_available + inspection.rs:260
// classify_mapped parity): with a declared page set through the
// mapping session state, the basic guard mapping refuses the page
// deterministically and the checked-page authority records the
// I/O-unreadable fact end-to-end through one recovery report path;
// the candidate inspection skips a declared bootstrap page without
// faulting; an empty session list keeps every behavior unchanged.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// sessionRecoverySource builds one real direct recovery source with
// two committed ranges (the Rust fault_fixture shape: meta pair,
// range root, two leaves) and returns its path and committed meta.
func sessionRecoverySource(t *testing.T) (string, format.Meta) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.v4")
	builder := directSourceBuilder(t, path)
	meta := finishRanges(t, builder, [][3]uint32{{10, 20, 1}, {100, 110, 2}})
	if meta.RangeRoot == 0 || meta.PageCount <= uint64(meta.RangeRoot) {
		t.Fatalf("fixture meta %+v: want a range root inside the extent", meta)
	}
	return path, meta
}

func TestRecoverImmutableDeclaredPageRecordsIOUnreadable(t *testing.T) {
	sourcePath, meta := sessionRecoverySource(t)
	t.Cleanup(func() { _ = mapping.SetSessionUnreadablePages(nil) })
	candidate := apiTestCandidate(t, sourcePath)
	directory := t.TempDir()
	cleanOutput := filepath.Join(directory, "clean-output.v4")
	outputPath := filepath.Join(directory, "declared-output.v4")
	// The Go writer coalesces the two ranges into one root leaf, so
	// the single real data page is the range root itself.
	declared := meta.RangeRoot

	// Empty session: the recovery completes with no unreadable pages.
	var cleanEnvelopes []*RecoveryUnknownEnvelope
	clean, failure := RecoverImmutable(sourcePath, candidate, cleanOutput, apiTestBudget(), nil, RecoverySinkFunc(func(envelope *RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		cleanEnvelopes = append(cleanEnvelopes, envelope)
		return RecoverySinkContinue, nil
	}))
	if failure != nil {
		t.Fatalf("clean-session recovery failed: %v", failure.Cause)
	}
	if clean == nil || clean.Report.Pages.IOUnreadable != 0 || len(cleanEnvelopes) != 0 {
		t.Fatalf("clean recovery = %+v envelopes %d, want no unreadable pages", clean, len(cleanEnvelopes))
	}

	// A declared real leaf page refuses deterministically: the
	// checked-page authority maps the mapping refusal to the
	// I/O-unreadable class (page_read.go), the reporter counts it
	// (report.go pageRejected), and the recovery completes without any
	// SIGBUS (Rust client_tests.rs fault_fixture second run:
	// report.pages.io_unreadable == 1).
	if err := mapping.SetSessionUnreadablePages([]uint32{declared}); err != nil {
		t.Fatal("set session pages:", err)
	}
	var envelopes []*RecoveryUnknownEnvelope
	result, failure := RecoverImmutable(sourcePath, candidate, outputPath, apiTestBudget(), nil, RecoverySinkFunc(func(envelope *RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		envelopes = append(envelopes, envelope)
		return RecoverySinkContinue, nil
	}))
	if failure != nil {
		t.Fatalf("declared-page recovery failed: %v", failure.Cause)
	}
	if result == nil || result.Report.Pages.IOUnreadable != 1 {
		t.Fatalf("report = %+v, want exactly 1 I/O-unreadable page", result)
	}
	if result.Report.Ranges.Accepted != 0 {
		t.Fatalf("ranges accepted %d, want none (the refused root carries every record)", result.Report.Ranges.Accepted)
	}
	count := 0
	for _, envelope := range envelopes {
		if envelope.Reason == validation.ReasonIoError && envelope.PageNumber != nil && *envelope.PageNumber == declared {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("envelopes = %+v, want one IoError envelope at page %d", envelopes, declared)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("published output missing after the refused page: %v", err)
	}
}

func TestInspectDeclaredBootstrapPageSkipsWithoutFault(t *testing.T) {
	path := metaDBFile(t, 2)
	t.Cleanup(func() { _ = mapping.SetSessionUnreadablePages(nil) })

	// Empty session: the inspection projects the proven pair.
	clean, err := inspect(t, path, RecoveryInspectionImmutable)
	if err != nil {
		t.Fatalf("clean-session inspect: %v", err)
	}
	if clean.CandidateCount() != 1 || clean.Candidate(0).Label != CandidateNewest || clean.Progress.FindingCount != 0 {
		t.Fatalf("clean inspection %+v, want the proven newest pair", clean)
	}

	// A declared bootstrap page is skipped before any page probe (Rust
	// inspection.rs:260): the state slot stays unset, the order stays
	// unproven, and the classification reports the absent page as the
	// IoError finding - exactly like a short file, without a fault.
	if err := mapping.SetSessionUnreadablePages([]uint32{1}); err != nil {
		t.Fatal("set session pages:", err)
	}
	declared, err := inspect(t, path, RecoveryInspectionImmutable)
	if err != nil {
		t.Fatalf("declared-page inspect: %v", err)
	}
	if declared.CandidateCount() != 1 || declared.Candidate(0).Label != CandidateUnorderedMeta0 {
		t.Fatalf("candidates %+v, want the unordered page-0 candidate", declared.Candidates())
	}
	if found := declared.Progress.FindingsFor(validation.ReasonIoError); found != 1 {
		t.Fatalf("IoError findings %d, want 1 (the skipped page 1)", found)
	}
	if declared.Progress.UntraversableSubgraphs != 1 {
		t.Fatalf("untraversable subgraphs %d, want 1", declared.Progress.UntraversableSubgraphs)
	}
}
