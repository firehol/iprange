//go:build linux && amd64

package worker

// Control.ProbeRegion unit tests (Rust worker.rs enter_region + Probe
// drop + NEXT_MAPPING_GENERATION): the previous-registration restore
// on release (also after a failed operation), the unarmed probe
// leaving the control disarmed, the monotonic nonzero generation
// counter, the invalid armed-registration refusal, and the
// ownership-mismatch Conflict. The tests publish the
// handler-ownership seam directly (activeControl is the
// process-global the naked handler reads); no signal handler is
// installed and no fault is raised in this process.

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// withOwnedControl publishes the control base as the active handler
// ownership for one test and restores the previous value on cleanup.
func withOwnedControl(t *testing.T, c *Control) {
	t.Helper()
	previous := atomic.LoadUintptr(&activeControl)
	atomic.StoreUintptr(&activeControl, c.base())
	t.Cleanup(func() { atomic.StoreUintptr(&activeControl, previous) })
}

func TestProbeRegionNestingRestoresOuterRegistration(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	withOwnedControl(t, c)

	const (
		outerGeneration = 17
		outerBase       = 0x7f0000001000
		outerLen        = 0x2000
		innerBase       = 0x7f0000004000
		innerLen        = 0x1000
	)
	if err := c.Arm(outerGeneration, RoleSource, outerBase, outerLen); err != nil {
		t.Fatal("arm outer:", err)
	}
	innerRan := false
	err = c.ProbeRegion(mapping.RoleOutput, innerBase, innerLen, func() error {
		innerRan = true
		reg, armed, err := c.registration()
		if err != nil {
			return err
		}
		if !armed {
			return errors.New("inner probe is not armed")
		}
		if reg.Generation == outerGeneration || reg.Role != RoleOutput || reg.Base != innerBase || reg.Len != innerLen {
			return fmt.Errorf("inner registration = %+v, want output role at %#x/%#x with a fresh generation", reg, innerBase, innerLen)
		}
		return nil
	})
	if err != nil {
		t.Fatal("ProbeRegion:", err)
	}
	if !innerRan {
		t.Fatal("inner operation did not run")
	}
	reg, armed, err := c.registration()
	if err != nil {
		t.Fatal("registration after restore:", err)
	}
	if !armed || reg.Generation != outerGeneration || reg.Role != RoleSource || reg.Base != outerBase || reg.Len != outerLen {
		t.Fatalf("outer registration not restored: armed=%v reg=%+v", armed, reg)
	}
	if mapAtomicLoad32(baseOf(c.data), offArmed) != 1 {
		t.Fatal("the armed flag did not stay set after the nested restore")
	}
}

func TestProbeRegionRestoreRunsAfterOperationFailure(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	withOwnedControl(t, c)

	if err := c.Arm(9, RoleScratch, 0x1000, 0x1000); err != nil {
		t.Fatal("arm outer:", err)
	}
	opErr := errors.New("operation failed")
	if err := c.ProbeRegion(mapping.RoleSource, 0x2000, 0x1000, func() error { return opErr }); !errors.Is(err, opErr) {
		t.Fatalf("ProbeRegion = %v, want the operation error", err)
	}
	// The failed operation still releases the inner probe and restores
	// the outer registration (Rust Probe::drop runs in all paths).
	reg, armed, err := c.registration()
	if err != nil {
		t.Fatal(err)
	}
	if !armed || reg.Generation != 9 || reg.Role != RoleScratch || reg.Base != 0x1000 || reg.Len != 0x1000 {
		t.Fatalf("outer registration not restored after a failed operation: armed=%v reg=%+v", armed, reg)
	}
}

func TestProbeRegionUnarmedLeavesDisarmed(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	withOwnedControl(t, c)

	ran := false
	if err := c.ProbeRegion(mapping.RoleSource, 0x1000, 0x1000, func() error {
		ran = true
		return nil
	}); err != nil {
		t.Fatal("ProbeRegion:", err)
	}
	if !ran {
		t.Fatal("operation did not run")
	}
	if mapAtomicLoad32(baseOf(c.data), offArmed) != 0 {
		t.Fatal("probe left armed without a previous registration")
	}
}

func TestProbeRegionGenerationCounterMonotonicNonzero(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	withOwnedControl(t, c)

	var last uint64
	for index := 0; index < 8; index++ {
		var generation uint64
		if err := c.ProbeRegion(mapping.RoleCoordination, 0x1000, 0x1000, func() error {
			reg, armed, err := c.registration()
			if err != nil {
				return err
			}
			if !armed || reg.Generation == 0 {
				return errors.New("probe has no nonzero generation")
			}
			generation = reg.Generation
			return nil
		}); err != nil {
			t.Fatal("ProbeRegion:", err)
		}
		if generation == 0 {
			t.Fatal("generation is zero")
		}
		if index > 0 && generation <= last {
			t.Fatalf("generation %d is not above the previous %d", generation, last)
		}
		last = generation
	}
}

// TestNextMappingGenerationWrapsToNonzero pins the overflow contract
// of the lock-free generation counter (Rust NEXT_MAPPING_GENERATION
// checked add: the maximum generation is consumable and the next call
// wraps to 1, preserving the never-0 invariant).
func TestNextMappingGenerationWrapsToNonzero(t *testing.T) {
	previous := mappingGeneration.Load()
	t.Cleanup(func() { mappingGeneration.Store(previous) })
	mappingGeneration.Store(^uint64(0))
	if generation := nextMappingGeneration(); generation != ^uint64(0) {
		t.Fatalf("next generation after storing the maximum = %d, want the maximum to be consumed", generation)
	}
	if generation := nextMappingGeneration(); generation != 1 {
		t.Fatalf("next generation after the overflow = %d, want 1 (never-0 wrap)", generation)
	}
}

func TestRegistrationRejectsInvalidArmedState(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Unarmed state reads as absent without validation.
	if _, armed, err := c.registration(); err != nil || armed {
		t.Fatalf("unarmed registration = armed %v err %v, want false nil", armed, err)
	}

	// An armed state with a zero generation is the verbatim Conflict
	// (Rust registration "worker mapping registration is invalid").
	format.PutU64(c.data[offGeneration:offGeneration+8], 0)
	format.PutU32(c.data[offRole:offRole+4], uint32(RoleSource))
	format.PutU64(c.data[offBase:offBase+8], 0x1000)
	format.PutU64(c.data[offLen:offLen+8], 0x1000)
	mapAtomicStore32(baseOf(c.data), offArmed, 1)
	_, _, err = c.registration()
	wantCode(t, err, format.CodeConflict)
	var e *format.Error
	if !errors.As(err, &e) || e.Detail != "worker mapping registration is invalid" {
		t.Fatalf("detail = %v, want the verbatim Rust detail", err)
	}

	// A bad role is refused the same way.
	format.PutU64(c.data[offGeneration:offGeneration+8], 1)
	format.PutU32(c.data[offRole:offRole+4], 5)
	if _, _, err := c.registration(); err == nil {
		t.Fatal("registration accepted an invalid role")
	}
}

func TestProbeRegionOwnershipMismatchRefuses(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	other, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	previous := atomic.LoadUintptr(&activeControl)
	atomic.StoreUintptr(&activeControl, other.base())
	t.Cleanup(func() { atomic.StoreUintptr(&activeControl, previous) })

	ran := false
	err = c.ProbeRegion(mapping.RoleSource, 0x1000, 0x1000, func() error {
		ran = true
		return nil
	})
	wantCode(t, err, format.CodeConflict)
	var e *format.Error
	if !errors.As(err, &e) || e.Detail != "SIGBUS worker handler ownership was lost" {
		t.Fatalf("detail = %v, want the verbatim ownership refusal", err)
	}
	if ran {
		t.Fatal("operation ran without handler ownership")
	}
	if mapAtomicLoad32(baseOf(c.data), offArmed) != 0 {
		t.Fatal("mismatched probe armed the control")
	}
}

// TestArmProbeAndReleaseAllocsZero pins the session-mode arm cost:
// Control.ArmProbe and the release restore must be allocation-free
// (Rust enter_region + Probe::drop are stack values; the previous
// registration and the release are plain values and the control is
// already heap). The session installation and the generation counter
// are covered by the other tests; this pin measures the per-arm cost.
func TestArmProbeAndReleaseAllocsZero(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	withOwnedControl(t, c)
	allocs := testing.AllocsPerRun(200, func() {
		release, err := c.ArmProbe(mapping.RoleOutput, 0x1000, 0x1000)
		if err != nil {
			t.Fatalf("ArmProbe: %v", err)
		}
		release.Release()
	})
	if allocs != 0 {
		t.Fatalf("ArmProbe+Release allocates %v objects per arm, want 0", allocs)
	}
}
