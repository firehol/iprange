//go:build !windows

package mapping

// Session mapping-probe hook tests (Rust worker.rs CURRENT_CONTROL +
// probe_region/enter_region): the no-hook direct path, the hook
// receiving the resolved region and propagating both hook and
// operation results, the clear-to-direct transition, the
// unmapped-region refusal, and the zero-allocation invariants of the
// value-shaped guard on both the library and session paths. The
// session hook is package-level, so every test restores the nil hook
// in its cleanup exactly like the session-unreadable suite.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// releaseRecorder implements ProbeOwner and records every restore the
// way the worker control would act (Rust Probe::drop arm/disarm).
type releaseRecorder struct {
	calls int
	prev  ProbeRegistration
	armed bool
}

func (r *releaseRecorder) RestoreProbe(previous ProbeRegistration, armed bool) {
	r.calls++
	r.prev = previous
	r.armed = armed
}

// mapTestFile maps one page-sized temp file (the shared fixture of
// the probe tests).
func mapTestFile(t *testing.T) *Mapping {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "page.v4")
	if err := os.WriteFile(path, make([]byte, format.PageSize), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	m, err := MapFile(f, format.PageSize, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func TestProbeWithoutHookRunsDirectly(t *testing.T) {
	t.Cleanup(ClearSessionProbe)
	m := mapTestFile(t)
	ran := false
	if err := m.Probe(RoleSource, func() error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("Probe = %v, want nil", err)
	}
	if !ran {
		t.Fatal("operation did not run without a hook")
	}
}

func TestProbeWithHookReceivesRegionAndPropagatesResults(t *testing.T) {
	t.Cleanup(ClearSessionProbe)
	m := mapTestFile(t)
	base, length, err := m.Region()
	if err != nil {
		t.Fatal(err)
	}

	var gotRole ProbeRole
	var gotBase uintptr
	var gotLength uint64
	recorder := &releaseRecorder{}
	opRan := false
	// The arm receives the role and the resolved region; the release
	// runs when the probe (and the operation inside it) finishes.
	SetSessionProbe(func(role ProbeRole, base uintptr, length uint64) (ProbeRelease, error) {
		gotRole, gotBase, gotLength = role, base, length
		return ProbeRelease{Owner: recorder, Armed: true}, nil
	})
	err = m.Probe(RoleOutput, func() error {
		opRan = true
		return nil
	})
	if err != nil {
		t.Fatalf("Probe = %v, want nil", err)
	}
	if !opRan {
		t.Fatal("operation did not run inside the armed probe")
	}
	if recorder.calls != 1 {
		t.Fatalf("release ran %d times, want 1", recorder.calls)
	}
	if gotRole != RoleOutput || gotBase != base || gotLength != length {
		t.Fatalf("arm got role=%d base=%#x length=%d, want role=%d base=%#x length=%d",
			gotRole, gotBase, gotLength, RoleOutput, base, length)
	}

	// An arm failure surfaces before the operation runs (Rust
	// enter_region error propagation).
	wantArmErr := errors.New("arm failure")
	opRan = false
	SetSessionProbe(func(ProbeRole, uintptr, uint64) (ProbeRelease, error) {
		return ProbeRelease{}, wantArmErr
	})
	if err := m.Probe(RoleSource, func() error {
		opRan = true
		return nil
	}); !errors.Is(err, wantArmErr) {
		t.Fatalf("Probe = %v, want the arm error", err)
	}
	if opRan {
		t.Fatal("operation ran despite the arm failure")
	}

	// A pass-through arm propagates the operation error unchanged.
	opErr := errors.New("operation failure")
	SetSessionProbe(func(_ ProbeRole, _ uintptr, _ uint64) (ProbeRelease, error) {
		return ProbeRelease{Owner: &releaseRecorder{}, Armed: true}, nil
	})
	if err := m.Probe(RoleSource, func() error { return opErr }); !errors.Is(err, opErr) {
		t.Fatalf("Probe = %v, want the operation error", err)
	}
}

func TestEnterProbeGuardDirectAndArmedPaths(t *testing.T) {
	t.Cleanup(ClearSessionProbe)
	// Without a hook the guard is the zero value and the machine runs
	// directly (the pinned library-path shape; the region resolves
	// first, Rust probe_mapping order).
	var m Mapping
	guard, err := m.EnterProbe(RoleOutput)
	if err == nil {
		// Unmapped: the region resolution must refuse before the
		// no-session short-circuit (Rust probe_mapping order).
		t.Fatal("EnterProbe on an unmapped mapping succeeded")
	}
	guard.Exit() // zero guard no-op, must not panic

	mapped := mapTestFile(t)
	guard, err = mapped.EnterProbe(RoleOutput)
	if err != nil {
		t.Fatalf("EnterProbe without a hook = %v, want nil", err)
	}
	if guard.release.Owner != nil {
		t.Fatal("without a hook EnterProbe must return the inert zero guard")
	}
	guard.Exit() // no-op, must not panic

	// With a hook the guard arms one region and Exit releases it; the
	// armed window brackets the machine step.
	base, length, err := mapped.Region()
	if err != nil {
		t.Fatal(err)
	}
	recorder := &releaseRecorder{}
	SetSessionProbe(func(role ProbeRole, gotBase uintptr, gotLength uint64) (ProbeRelease, error) {
		if role != RoleScratch || gotBase != base || gotLength != length {
			t.Fatalf("arm got role=%d base=%#x length=%d, want role=%d base=%#x length=%d",
				role, gotBase, gotLength, RoleScratch, base, length)
		}
		return ProbeRelease{Owner: recorder, Armed: true}, nil
	})
	guard, err = mapped.EnterProbe(RoleScratch)
	if err != nil {
		t.Fatalf("EnterProbe with a hook = %v, want nil", err)
	}
	if guard.release.Owner == nil {
		t.Fatal("with a hook EnterProbe must return an armed guard")
	}
	if recorder.calls != 0 {
		t.Fatal("release ran before Exit")
	}
	guard.Exit()
	if recorder.calls != 1 {
		t.Fatalf("release ran %d times, want 1", recorder.calls)
	}
}

func TestClearSessionProbeRestoresDirectMode(t *testing.T) {
	t.Cleanup(ClearSessionProbe)
	called := false
	SetSessionProbe(func(ProbeRole, uintptr, uint64) (ProbeRelease, error) {
		called = true
		return ProbeRelease{Owner: &releaseRecorder{}, Armed: true}, nil
	})
	ClearSessionProbe()
	m := mapTestFile(t)
	ran := false
	if err := m.Probe(RoleScratch, func() error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("Probe = %v, want nil", err)
	}
	if called || !ran {
		t.Fatalf("hook called=%v ran=%v, want the direct path", called, ran)
	}
}

func TestRegionRefusesUnmappedMapping(t *testing.T) {
	var m Mapping
	_, _, err := m.Region()
	var e *format.Error
	if !errors.As(err, &e) || e.Code != format.CodeWrongState {
		t.Fatalf("Region = %v, want the WrongState class", err)
	}
	if e.Detail != "mapping unavailable" {
		t.Fatalf("detail = %q, want the View refusal detail", e.Detail)
	}

	// The region resolves before the hook runs (Rust probe_region
	// resolves mapping.region() first): an unmapped Probe refuses
	// with the hook installed and the hook never runs.
	t.Cleanup(ClearSessionProbe)
	hooked := false
	SetSessionProbe(func(ProbeRole, uintptr, uint64) (ProbeRelease, error) {
		hooked = true
		return ProbeRelease{Owner: &releaseRecorder{}, Armed: true}, nil
	})
	if err := m.Probe(RoleCoordination, func() error { return nil }); err == nil {
		t.Fatal("unmapped Probe succeeded")
	}
	if hooked {
		t.Fatal("hook ran for an unmapped mapping")
	}

	// The same refusal holds without a hook: the region resolves
	// before the no-session short-circuit (Rust probe_mapping order).
	ClearSessionProbe()
	ran := false
	if err := m.Probe(RoleCoordination, func() error { ran = true; return nil }); err == nil {
		t.Fatal("unmapped Probe succeeded without a hook")
	}
	if ran {
		t.Fatal("operation ran on an unmapped mapping without a hook")
	}
}

func TestEnterProbeSessionPathAllocsZero(t *testing.T) {
	t.Cleanup(ClearSessionProbe)
	m := mapTestFile(t)
	recorder := &releaseRecorder{}
	// A real arm-shaped hook: it returns an armed release referencing
	// the owner, so the measured path builds and releases the value
	// exactly like the worker session does. The arm itself must not
	// allocate.
	SetSessionProbe(func(role ProbeRole, base uintptr, length uint64) (ProbeRelease, error) {
		return ProbeRelease{
			Owner: recorder,
			Previous: ProbeRegistration{
				Generation: 7,
				Role:       uint32(role),
				Base:       base,
				Length:     length,
			},
			Armed: true,
		}, nil
	})
	allocs := testing.AllocsPerRun(200, func() {
		guard, err := m.EnterProbe(RoleOutput)
		if err != nil {
			t.Fatalf("EnterProbe: %v", err)
		}
		guard.Exit()
	})
	if allocs != 0 {
		t.Fatalf("session-active EnterProbe+Exit allocates %v objects per op, want 0 (Rust Probe is a stack value)", allocs)
	}
	if recorder.calls != 201 {
		t.Fatalf("release ran %d times, want 201 (one AllocsPerRun warmup plus the 200 measured runs)", recorder.calls)
	}
}

func TestEnterProbeLibraryPathAllocsZero(t *testing.T) {
	t.Cleanup(ClearSessionProbe)
	m := mapTestFile(t)
	allocs := testing.AllocsPerRun(200, func() {
		guard, err := m.EnterProbe(RoleOutput)
		if err != nil {
			t.Fatalf("EnterProbe: %v", err)
		}
		guard.Exit()
	})
	if allocs != 0 {
		t.Fatalf("library EnterProbe+Exit allocates %v objects per op, want 0", allocs)
	}
}
