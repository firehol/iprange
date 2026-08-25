//go:build !windows

package mapping

// Session mapping-probe hook tests (Rust worker.rs CURRENT_CONTROL +
// probe_region/enter_region): the no-hook direct path, the hook
// receiving the resolved region and propagating both hook and
// operation results, the clear-to-direct transition, and the
// unmapped-region refusal. The session hook is package-level, so
// every test restores the nil hook in its cleanup exactly like the
// session-unreadable suite.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

func TestProbeWithoutHookRunsDirectly(t *testing.T) {
	t.Cleanup(ClearSessionProbe)
	ran := false
	if err := (&Mapping{}).Probe(RoleSource, func() error {
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
	dir := t.TempDir()
	path := filepath.Join(dir, "page.v4")
	if err := os.WriteFile(path, make([]byte, format.PageSize), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := MapFile(f, format.PageSize, false)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	base, length, err := m.Region()
	if err != nil {
		t.Fatal(err)
	}

	var gotRole ProbeRole
	var gotBase uintptr
	var gotLength uint64
	released := false
	opRan := false
	// The arm receives the role and the resolved region; the release
	// runs when the probe (and the operation inside it) finishes.
	SetSessionProbe(func(role ProbeRole, base uintptr, length uint64) (func(), error) {
		gotRole, gotBase, gotLength = role, base, length
		return func() { released = true }, nil
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
	if !released {
		t.Fatal("release did not run after the operation")
	}
	if gotRole != RoleOutput || gotBase != base || gotLength != length {
		t.Fatalf("arm got role=%d base=%#x length=%d, want role=%d base=%#x length=%d",
			gotRole, gotBase, gotLength, RoleOutput, base, length)
	}

	// An arm failure surfaces before the operation runs (Rust
	// enter_region error propagation).
	wantArmErr := errors.New("arm failure")
	opRan = false
	SetSessionProbe(func(ProbeRole, uintptr, uint64) (func(), error) {
		return nil, wantArmErr
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
	SetSessionProbe(func(_ ProbeRole, _ uintptr, _ uint64) (func(), error) {
		return func() {}, nil
	})
	if err := m.Probe(RoleSource, func() error { return opErr }); !errors.Is(err, opErr) {
		t.Fatalf("Probe = %v, want the operation error", err)
	}
}

func TestEnterProbeGuardDirectAndArmedPaths(t *testing.T) {
	t.Cleanup(ClearSessionProbe)
	// Without a hook the guard is the shared inert instance and the
	// machine runs directly (the pinned library-path shape).
	var m Mapping
	guard, err := m.EnterProbe(RoleOutput)
	if err != nil {
		t.Fatalf("EnterProbe without a hook = %v, want nil", err)
	}
	if guard != inertProbeGuard {
		t.Fatal("without a hook EnterProbe must return the shared inert guard")
	}
	guard.Exit() // no-op, must not panic

	// With a hook the guard arms one region and Exit releases it; the
	// armed window brackets the machine step.
	dir := t.TempDir()
	path := filepath.Join(dir, "page.v4")
	if err := os.WriteFile(path, make([]byte, format.PageSize), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	mapped, err := MapFile(f, format.PageSize, false)
	if err != nil {
		t.Fatal(err)
	}
	defer mapped.Close()
	base, length, err := mapped.Region()
	if err != nil {
		t.Fatal(err)
	}
	released := false
	SetSessionProbe(func(role ProbeRole, gotBase uintptr, gotLength uint64) (func(), error) {
		if role != RoleScratch || gotBase != base || gotLength != length {
			t.Fatalf("arm got role=%d base=%#x length=%d, want role=%d base=%#x length=%d",
				role, gotBase, gotLength, RoleScratch, base, length)
		}
		return func() { released = true }, nil
	})
	guard, err = mapped.EnterProbe(RoleScratch)
	if err != nil {
		t.Fatalf("EnterProbe with a hook = %v, want nil", err)
	}
	if guard == inertProbeGuard {
		t.Fatal("with a hook EnterProbe must return an armed guard")
	}
	if released {
		t.Fatal("release ran before Exit")
	}
	guard.Exit()
	if !released {
		t.Fatal("release did not run on Exit")
	}
}

func TestClearSessionProbeRestoresDirectMode(t *testing.T) {
	t.Cleanup(ClearSessionProbe)
	called := false
	SetSessionProbe(func(ProbeRole, uintptr, uint64) (func(), error) {
		called = true
		return func() {}, nil
	})
	ClearSessionProbe()
	ran := false
	if err := (&Mapping{}).Probe(RoleScratch, func() error {
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

	// With a hook installed, an unmapped Probe refuses before the hook
	// runs (Rust probe_region resolves mapping.region() first).
	t.Cleanup(ClearSessionProbe)
	hooked := false
	SetSessionProbe(func(ProbeRole, uintptr, uint64) (func(), error) {
		hooked = true
		return func() {}, nil
	})
	if err := m.Probe(RoleCoordination, func() error { return nil }); err == nil {
		t.Fatal("unmapped Probe succeeded")
	}
	if hooked {
		t.Fatal("hook ran for an unmapped mapping")
	}
}
