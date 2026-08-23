//go:build linux && amd64

// Control-page unit tests (no signals, no subprocesses): header layout,
// exact-extent open, arm/disarm, and the fault-record rejection state.
// The subprocess signal matrix lives in sigbus_linux_amd64_test.go and
// sigbus_matrix_v4work_test.go.

package worker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// wantCode fails the test unless err carries the given typed code.
func wantCode(t *testing.T, err error, code format.ErrorCode) {
	t.Helper()
	var e *format.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("expected error code %d, got %v", code, err)
	}
}

func TestCreateParentWritesHeader(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal("create parent:", err)
	}
	path := c.path
	defer c.Close()
	data := c.data
	if string(data[offMagic:offMagic+8]) != string(controlMagic[:]) {
		t.Fatalf("magic = %q, want %q", data[offMagic:offMagic+8], controlMagic[:])
	}
	if format.U32(data[offProtocol:offProtocol+4]) != protocol {
		t.Fatalf("protocol = %d, want %d", format.U32(data[offProtocol:offProtocol+4]), protocol)
	}
	if c.state() != stateRequest {
		t.Fatalf("state = %d, want %d", c.state(), stateRequest)
	}
	if mapAtomicLoad32(baseOf(data), offArmed) != 0 {
		t.Fatal("new control is armed")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("control file %s missing: %v", path, err)
	}
}

// TestCreateParentModeIndependentOfUmask proves the secure_creator_only
// core: the control file is exactly 0600 even under a restrictive umask
// (Rust control.rs create_file + security::secure_creator_only), so the
// worker can always reopen it read-write.
func TestCreateParentModeIndependentOfUmask(t *testing.T) {
	previous := unix.Umask(0o077)
	defer unix.Umask(previous)
	c, err := CreateParent()
	if err != nil {
		t.Fatal("create parent:", err)
	}
	defer c.Close()
	st, err := os.Stat(c.path)
	if err != nil {
		t.Fatal("stat control:", err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Fatalf("control mode = %#o, want 0600", mode)
	}
}

func TestCreateParentUniquePaths(t *testing.T) {
	a, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if a.path == b.path {
		t.Fatalf("two controls share the path %s", a.path)
	}
}

func TestCloseRemovesPath(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	path := c.path
	c.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("control file %s survived Close: %v", path, err)
	}
}

func TestRemovePath(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	path := c.path
	defer c.Close()
	if err := c.RemovePath(); err != nil {
		t.Fatal("remove path:", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("control file %s survived RemovePath: %v", path, err)
	}
	if err := c.RemovePath(); err != nil {
		t.Fatalf("second RemovePath must be a no-op, got %v", err)
	}
}

func TestOpenWorkerExactExtent(t *testing.T) {
	parent, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	worker, err := OpenWorker(parent.path)
	if err != nil {
		t.Fatal("open worker:", err)
	}
	defer worker.Close()
	if string(worker.data[offMagic:offMagic+8]) != string(controlMagic[:]) {
		t.Fatal("worker sees a different control header")
	}
}

func TestOpenWorkerRejectsWrongExtent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wrong-size.ctl")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(controlLen - 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = OpenWorker(path)
	wantCode(t, err, format.CodeFormatInvalid)
}

func TestOpenWorkerRejectsMissing(t *testing.T) {
	_, err := OpenWorker(filepath.Join(t.TempDir(), "missing.ctl"))
	wantCode(t, err, format.CodeIO)
}

func TestArmDisarm(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	const (
		generation = 23
		base       = 0x7f0000001000
		length     = 8192
	)
	if err := c.Arm(generation, RoleScratch, base, length); err != nil {
		t.Fatal("arm:", err)
	}
	data := c.data
	if mapAtomicLoad32(baseOf(data), offArmed) != 1 {
		t.Fatal("armed flag not set")
	}
	if format.U64(data[offGeneration:offGeneration+8]) != generation {
		t.Fatal("generation mismatch")
	}
	if format.U32(data[offRole:offRole+4]) != uint32(RoleScratch) {
		t.Fatal("role mismatch")
	}
	if format.U64(data[offBase:offBase+8]) != base {
		t.Fatal("base mismatch")
	}
	if format.U64(data[offLen:offLen+8]) != length {
		t.Fatal("length mismatch")
	}
	c.Disarm()
	if mapAtomicLoad32(baseOf(data), offArmed) != 0 {
		t.Fatal("armed flag not cleared")
	}
}

func TestArmRejectsEmptyProbe(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Arm(0, RoleSource, 0x1000, 4096); err == nil {
		t.Fatal("zero generation accepted")
	}
	if err := c.Arm(1, RoleSource, 0, 4096); err == nil {
		t.Fatal("zero base accepted")
	}
	if err := c.Arm(1, RoleSource, 0x1000, 0); err == nil {
		t.Fatal("zero length accepted")
	}
}

func TestArmRejectsInvalidRole(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Arm(1, MappingRole(0), 0x1000, 4096); err == nil {
		t.Fatal("role 0 accepted")
	}
	if err := c.Arm(1, MappingRole(5), 0x1000, 4096); err == nil {
		t.Fatal("role 5 accepted")
	}
	wantCode(t, c.Arm(1, MappingRole(5), 0x1000, 4096), format.CodeInvalidArgument)
}

func TestFaultRecordRejectsBeforeFault(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Arm(9, RoleSource, 0x1000, 4096); err != nil {
		t.Fatal("arm:", err)
	}
	_, err = c.FaultRecord()
	wantCode(t, err, format.CodeConflict)
}

func TestWorkerSharesArmedState(t *testing.T) {
	parent, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	worker, err := OpenWorker(parent.path)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	if err := parent.Arm(5, RoleCoordination, 0x2000, 4096); err != nil {
		t.Fatal("arm:", err)
	}
	if mapAtomicLoad32(baseOf(worker.data), offArmed) != 1 {
		t.Fatal("worker does not see the armed state")
	}
	worker.Disarm()
	if mapAtomicLoad32(baseOf(parent.data), offArmed) != 0 {
		t.Fatal("parent does not see the disarmed state")
	}
}
