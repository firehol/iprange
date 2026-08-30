// The OutputBuilder page window (Rust with_output_protection across
// update_page/copy_page): Update and CopyPage arm the output region
// before the page fetch and RestoreDirty releases it after the caller's
// mutation, so a worker-session fault inside the mutation window is
// recorded with the Output role instead of chaining. The stub session
// probe observes the arm/release pairing; the real worker control path
// is exercised by the worker recovery sessions.

package writer_test

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// probeRecorder is the stub session-probe arm (Rust enter_region / Probe
// drop projection): it records every armed role and region and counts
// every release handed to the owner.
type probeRecorder struct {
	roles    []mapping.ProbeRole
	bases    []uintptr
	lengths  []uint64
	releases int
}

func (r *probeRecorder) RestoreProbe(_ mapping.ProbeRegistration, _ bool) { r.releases++ }

// installProbeRecorder publishes one recording session-probe hook and
// clears it when the test finishes (the writer suite keeps the hook
// cleared between tests).
func installProbeRecorder(t *testing.T, r *probeRecorder) {
	t.Helper()
	mapping.SetSessionProbe(func(role mapping.ProbeRole, base uintptr, length uint64) (mapping.ProbeRelease, error) {
		r.roles = append(r.roles, role)
		r.bases = append(r.bases, base)
		r.lengths = append(r.lengths, length)
		return mapping.ProbeRelease{Owner: r, Armed: false}, nil
	})
	t.Cleanup(mapping.ClearSessionProbe)
}

// wantOutputRegion asserts every armed region is the whole output
// mapping region with the Output role (Rust protected_region is the
// mapping region and the role is RoleOutput for every store op).
func wantOutputRegion(t *testing.T, b *writer.OutputBuilder, rec *probeRecorder) {
	t.Helper()
	wantBase, wantLen, err := b.Mapping().Region()
	if err != nil {
		t.Fatalf("Region: %v", err)
	}
	if len(rec.roles) == 0 {
		t.Fatalf("no store operation armed the output region")
	}
	for index := range rec.roles {
		if rec.roles[index] != mapping.RoleOutput {
			t.Fatalf("arm %d used role %v, want RoleOutput", index, rec.roles[index])
		}
		if rec.bases[index] != wantBase || rec.lengths[index] != wantLen {
			t.Fatalf("arm %d used region %#x+%d, want the whole output mapping %#x+%d",
				index, rec.bases[index], rec.lengths[index], wantBase, wantLen)
		}
	}
}

// stampOutputPage marks one fresh output data page owned by the output
// transaction (the same bytes the tree codecs write when they create a
// page header), so RestoreDirty's ownership re-check accepts it.
func stampOutputPage(page []byte, txn uint64) {
	copy(page[0:4], format.PageMagic[:])
	format.PutU64(page[format.HeaderBorn:], txn)
}

// TestOutputPageWindowSpansTheMutation pins the closed window: Update
// arms the output region before the fetch, the arm is still held while
// the caller mutates, and RestoreDirty releases it after the ownership
// re-check (Rust update_page under with_output_protection).
func TestOutputPageWindowSpansTheMutation(t *testing.T) {
	b, _ := newOutput(t, directSpec(format.AddressFamilyIPv4), generousBudget())
	rec := &probeRecorder{}
	installProbeRecorder(t, rec)
	defer b.Close()

	pageNumber, err := b.Allocate()
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	page, tag, err := b.Update(pageNumber)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if rec.releases != 0 {
		t.Fatalf("Update released the page window before the mutation: releases=%d", rec.releases)
	}
	stampOutputPage(page, b.Meta().TxnID)
	if rec.releases != 0 {
		t.Fatalf("the page window released during the mutation: releases=%d", rec.releases)
	}
	if err := b.FinishEdit(page, tag); err != nil {
		t.Fatalf("RestoreDirty: %v", err)
	}
	if rec.releases != 1 {
		t.Fatalf("RestoreDirty did not release exactly the one armed window: releases=%d", rec.releases)
	}
	wantOutputRegion(t, b, rec)
}

// TestOutputPageWindowCopyPageSpansTheCopy pins the copy window: one
// arm covers both page fetches, the caller's copy, and the RestoreDirty
// re-check (Rust copy_page under with_output_protection).
func TestOutputPageWindowCopyPageSpansTheCopy(t *testing.T) {
	b, _ := newOutput(t, directSpec(format.AddressFamilyIPv4), generousBudget())
	rec := &probeRecorder{}
	installProbeRecorder(t, rec)
	defer b.Close()

	source, err := b.Allocate()
	if err != nil {
		t.Fatalf("Allocate source: %v", err)
	}
	destination, err := b.Allocate()
	if err != nil {
		t.Fatalf("Allocate destination: %v", err)
	}
	src, dst, tag, err := b.CopyPage(source, destination)
	if err != nil {
		t.Fatalf("CopyPage: %v", err)
	}
	if rec.releases != 0 {
		t.Fatalf("CopyPage released the page window before the copy: releases=%d", rec.releases)
	}
	copy(dst[64:], src[64:128])
	stampOutputPage(dst, b.Meta().TxnID)
	if rec.releases != 0 {
		t.Fatalf("the page window released during the copy: releases=%d", rec.releases)
	}
	if err := b.FinishEdit(dst, tag); err != nil {
		t.Fatalf("RestoreDirty: %v", err)
	}
	if rec.releases != 1 {
		t.Fatalf("RestoreDirty did not release exactly the one armed window: releases=%d", rec.releases)
	}
	if len(rec.roles) != 1 {
		t.Fatalf("CopyPage armed %d probes, want exactly one armed window", len(rec.roles))
	}
	wantOutputRegion(t, b, rec)
}

// TestOutputPageWindowAbortedMutationReleasesAtTheNextStoreOperation
// pins the recorded Go deviation: without RAII, an aborted mutation
// (no RestoreDirty) releases the armed window at the next store entry
// point, so no session can keep a stale armed region across builder use.
func TestOutputPageWindowAbortedMutationReleasesAtTheNextStoreOperation(t *testing.T) {
	b, _ := newOutput(t, directSpec(format.AddressFamilyIPv4), generousBudget())
	rec := &probeRecorder{}
	installProbeRecorder(t, rec)
	defer b.Close()

	pageNumber, err := b.Allocate()
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if _, _, err := b.Update(pageNumber); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if rec.releases != 0 {
		t.Fatalf("Update released before the abort: releases=%d", rec.releases)
	}
	// Abort: no RestoreDirty; the next store operation consumes the
	// stale window (Allocate arms nothing itself).
	if _, err := b.Allocate(); err != nil {
		t.Fatalf("Allocate after abort: %v", err)
	}
	if rec.releases != 1 {
		t.Fatalf("the aborted window was not released at the next store operation: releases=%d", rec.releases)
	}
	wantOutputRegion(t, b, rec)
}

// TestOutputPageWindowInspectSpansTheRead pins the inspection window:
// Inspect arms the output region before the fetch and holds it while
// the caller decodes the returned page; the next store operation
// releases it (Rust inspect_page under with_output_protection).
func TestOutputPageWindowInspectSpansTheRead(t *testing.T) {
	b, _ := newOutput(t, directSpec(format.AddressFamilyIPv4), generousBudget())
	rec := &probeRecorder{}
	installProbeRecorder(t, rec)
	defer b.Close()

	pageNumber, err := b.Allocate()
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	page, err := b.Inspect(pageNumber)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if rec.releases != 0 {
		t.Fatalf("Inspect released the window before the read: releases=%d", rec.releases)
	}
	// Decode a value out of the returned page while the window is held.
	_ = format.U64(page[format.HeaderBorn : format.HeaderBorn+8])
	if rec.releases != 0 {
		t.Fatalf("the window released during the read: releases=%d", rec.releases)
	}
	if _, err := b.Allocate(); err != nil {
		t.Fatalf("Allocate after inspect: %v", err)
	}
	if rec.releases != 1 {
		t.Fatalf("the next store operation did not release the armed window: releases=%d", rec.releases)
	}
	wantOutputRegion(t, b, rec)
}

// TestOutputPageWindowAbortedMutationReleasesAtClose pins the other
// recorded release point of an aborted mutation: the builder Close
// consumes the stale window before unmapping, so the worker control
// never holds a dangling armed region.
func TestOutputPageWindowAbortedMutationReleasesAtClose(t *testing.T) {
	b, _ := newOutput(t, directSpec(format.AddressFamilyIPv4), generousBudget())
	rec := &probeRecorder{}
	installProbeRecorder(t, rec)

	pageNumber, err := b.Allocate()
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if _, _, err := b.Update(pageNumber); err != nil {
		t.Fatalf("Update: %v", err)
	}
	wantOutputRegion(t, b, rec)
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if rec.releases != 1 {
		t.Fatalf("Close did not release the aborted window before unmapping: releases=%d", rec.releases)
	}
}
