package validation

// Worker-session unreadable-page tests (Rust validation.rs:310
// validation_mapping parity): with a declared page set through the
// mapping session state, the immutable and live sweeps refuse that
// page deterministically with the io-unreadable class (CodeIO)
// instead of faulting; an empty session list keeps the sweep
// unchanged. The fixtures are real committed databases, so the
// declared page is a page the sweep genuinely probes.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// sessionTag builds one direct value-tag (the writer spec field).
func sessionTag(text string) [16]byte {
	var tag [16]byte
	copy(tag[:], text)
	return tag
}

// immutableSessionFixture builds one real immutable direct database
// with two committed ranges and returns its path and the committed
// meta (the range root is the page the sweep probes first).
func immutableSessionFixture(t *testing.T) (string, format.Meta) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.iprdb")
	builder, err := writer.NewOutputBuilder(path, writer.OutputSpec{
		AddressFamily:  format.AddressFamilyIPv4,
		ValueKind:      format.ValueKindDirect,
		StructureKind:  format.StructureKindNone,
		ValueTag:       sessionTag("first-seen"),
		DatabaseID:     [16]byte{1},
		TxnID:          7,
		CommitNonce:    [16]byte{2},
		FeedIndexLimit: 0,
	}, writer.OutputBudget{MaxOutputPages: 20_000}, 0, nil)
	if err != nil {
		t.Fatalf("NewOutputBuilder: %v", err)
	}
	for _, record := range [][3]uint32{{10, 20, 1}, {100, 110, 2}} {
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
	if meta.RangeRoot == 0 || meta.PageCount <= uint64(meta.RangeRoot) {
		t.Fatalf("fixture meta %+v: want a range root inside the extent", meta)
	}
	return path, meta
}

// wantRefusedFinding fails the test unless the completed sweep
// carried the declared-page IoError finding: the Rust authority
// converts a declared-page refusal inside the graph walk into a
// finding (Rust validation/context.rs load_graph_page), so the
// deterministic refusal surfaces as a completed invalid result (with
// the natural root-count follow-up when the root itself is refused),
// never as a failure and never as a SIGBUS.
func wantRefusedFinding(t *testing.T, failure *ValidationFailure, findings []ValidationFinding, pageNumber uint32) {
	t.Helper()
	if failure != nil {
		t.Fatalf("validation failed: %v, want the completed finding arm", failure.Cause)
	}
	for _, finding := range findings {
		if finding.Reason == ReasonIoError && finding.PageNumber != nil && *finding.PageNumber == pageNumber {
			return
		}
	}
	t.Fatalf("findings = %+v, want an IoError finding at page %d", findings, pageNumber)
}

func TestValidateImmutableDeclaredPageRefusesCodeIO(t *testing.T) {
	path, meta := immutableSessionFixture(t)
	t.Cleanup(func() { _ = mapping.SetSessionUnreadablePages(nil) })

	// Empty session: the fixture sweeps clean.
	result, failure := Validate(path, ValidationModeImmutableCurrent, HeapOnly(1<<20, 2), nil, nil)
	if failure != nil {
		t.Fatalf("clean-session validation failed: %v", failure.Cause)
	}
	if result == nil || !result.Valid {
		t.Fatalf("result = %+v, want a valid clean sweep", result)
	}

	// The declared range root is a real page of the fixture: the sweep
	// must refuse it with the io-unreadable class before the range
	// checks, without any SIGBUS (Rust validation.rs:310 parity): the
	// refusal comes back as the completed invalid result with exactly
	// the declared-page IoError finding.
	if err := mapping.SetSessionUnreadablePages([]uint32{meta.RangeRoot}); err != nil {
		t.Fatal("set session pages:", err)
	}
	var findings []ValidationFinding
	refused, refuseFailure := Validate(path, ValidationModeImmutableCurrent, HeapOnly(1<<20, 2), nil, SinkFunc(func(f *ValidationFinding) (ValidationSinkControl, error) {
		findings = append(findings, *f)
		return SinkContinue, nil
	}))
	wantRefusedFinding(t, refuseFailure, findings, meta.RangeRoot)
	if refused == nil || refused.Valid {
		t.Fatalf("refused result = %+v, want the invalid completed result", refused)
	}
}

func TestValidateLiveDeclaredPageRefusesCodeIO(t *testing.T) {
	main := createLiveValidationPair(t, 1)
	t.Cleanup(func() { _ = mapping.SetSessionUnreadablePages(nil) })
	w := openLiveValidationWriter(t, main)
	if err := w.BeginDirect(); err != nil {
		t.Fatalf("BeginDirect: %v", err)
	}
	if changed, err := w.AssignV4(100, 200, 9); err != nil || !changed {
		t.Fatalf("AssignV4: changed=%v err=%v", changed, err)
	}
	if result, err := w.Commit(nil); err != nil || result.Durability != live.CommitCommitted {
		t.Fatalf("Commit: durability=%v err=%v", result.Durability, err)
	}

	// Empty session: the live sweep is unchanged (clean).
	result, failure, _ := findLiveValidationFindings(t, main)
	if failure != nil {
		t.Fatalf("clean-session live validation failed: %v", failure.Cause)
	}
	if result == nil || !result.Valid {
		t.Fatalf("result = %+v, want a valid live sweep", result)
	}

	// Declare every committed data page (page 2 onward; the meta pair
	// pages 0/1 stay readable so the live open's bootstrap probe is
	// never faulted). The sweep probes the range tree root first and
	// must refuse it with the io-unreadable class (Rust live.rs:298 /
	// validation.rs validation_mapping parity), without any SIGBUS.
	meta, ok := currentSessionMeta(t, main)
	if !ok || meta.PageCount <= 2 {
		t.Fatalf("live fixture meta ok=%v pages=%d, want committed data pages", ok, meta.PageCount)
	}
	pages := make([]uint32, 0, meta.PageCount-2)
	for page := uint32(2); page < uint32(meta.PageCount); page++ {
		pages = append(pages, page)
	}
	if err := mapping.SetSessionUnreadablePages(pages); err != nil {
		t.Fatal("set session pages:", err)
	}
	refused, refuseFailure, refusedFindings := findLiveValidationFindings(t, main)
	wantRefusedFinding(t, refuseFailure, refusedFindings, uint32(meta.RangeRoot))
	if refused == nil || refused.Valid {
		t.Fatalf("refused result = %+v, want the invalid completed result", refused)
	}

	if _, err := w.Close(); err != nil {
		t.Fatalf("writer close: %v", err)
	}
}

// currentSessionMeta reads the committed meta of one live main by
// parsing both pair pages and keeping the higher transaction (the
// bootstrap pair authority: the committed generation is the
// higher-txn page of a live pair). The read goes through a read-only
// mapping, keeping the fixture discipline mmap-only.
func currentSessionMeta(t *testing.T, main string) (format.Meta, bool) {
	t.Helper()
	file, err := os.Open(main)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()
	m, err := mapping.MapFile(file, 2*format.PageSize, false)
	if err != nil {
		t.Fatalf("map meta pair: %v", err)
	}
	defer func() { _ = m.Close() }()
	var best format.Meta
	found := false
	for pageNumber := uint32(0); pageNumber < 2; pageNumber++ {
		page, err := m.Page(pageNumber)
		if err != nil {
			t.Fatalf("meta page %d: %v", pageNumber, err)
		}
		if meta, ok := format.ParseIdentity(page); ok {
			if !found || meta.TxnID > best.TxnID {
				best, found = meta, true
			}
		}
	}
	return best, found
}
