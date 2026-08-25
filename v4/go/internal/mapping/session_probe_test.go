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
	opRan := false
	wantHookErr := errors.New("hook failure")
	SetSessionProbe(func(role ProbeRole, base uintptr, length uint64, operation func() error) error {
		gotRole, gotBase, gotLength = role, base, length
		if err := operation(); err != nil {
			return err
		}
		return wantHookErr
	})
	err = m.Probe(RoleOutput, func() error {
		opRan = true
		return nil
	})
	if !errors.Is(err, wantHookErr) {
		t.Fatalf("Probe = %v, want the hook error", err)
	}
	if !opRan {
		t.Fatal("operation did not run inside the hook")
	}
	if gotRole != RoleOutput || gotBase != base || gotLength != length {
		t.Fatalf("hook got role=%d base=%#x length=%d, want role=%d base=%#x length=%d",
			gotRole, gotBase, gotLength, RoleOutput, base, length)
	}

	// A pass-through hook propagates the operation error unchanged.
	opErr := errors.New("operation failure")
	SetSessionProbe(func(_ ProbeRole, _ uintptr, _ uint64, operation func() error) error {
		return operation()
	})
	if err := m.Probe(RoleSource, func() error { return opErr }); !errors.Is(err, opErr) {
		t.Fatalf("Probe = %v, want the operation error", err)
	}
}

func TestClearSessionProbeRestoresDirectMode(t *testing.T) {
	t.Cleanup(ClearSessionProbe)
	called := false
	SetSessionProbe(func(ProbeRole, uintptr, uint64, func() error) error {
		called = true
		return nil
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
	SetSessionProbe(func(ProbeRole, uintptr, uint64, func() error) error {
		hooked = true
		return nil
	})
	if err := m.Probe(RoleCoordination, func() error { return nil }); err == nil {
		t.Fatal("unmapped Probe succeeded")
	}
	if hooked {
		t.Fatal("hook ran for an unmapped mapping")
	}
}
