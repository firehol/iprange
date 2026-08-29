//go:build (linux || darwin || freebsd || windows) && (amd64 || arm64)

package worker

import "testing"

// TestAsmRoleGateRangeMatchesRoleSet pins the naked-SIGBUS asm role
// gate to the current role set (sigbus_linux_amd64.s: the fault-record
// role is accepted only when it lies in 1..4, mirroring the Rust
// control.rs MappingRole closed set). Adding a fifth MappingRole must
// update roleFromWire, the asm gate, and this list; the test fails on
// the out-of-range value so the synchronized edit cannot be skipped.
func TestAsmRoleGateRangeMatchesRoleSet(t *testing.T) {
	for _, role := range []MappingRole{RoleSource, RoleScratch, RoleOutput, RoleCoordination} {
		if decoded, ok := roleFromWire(uint32(role)); !ok || decoded != role {
			t.Fatalf("roleFromWire(%d) = (%d, %v), want the role itself", role, decoded, ok)
		}
	}
	if _, ok := roleFromWire(0); ok {
		t.Fatal("roleFromWire(0) decoded, want refusal (the asm gate accepts only 1..4)")
	}
	// Role 5 is the first value outside the asm gate. When a fifth
	// role is added this assert fails until the gate
	// (sigbus_linux_amd64.s `CMPL R11, $4`) is extended together with
	// roleFromWire and this list.
	if _, ok := roleFromWire(5); ok {
		t.Fatal("roleFromWire(5) decoded: the asm role gate accepts only roles 1..4; extend sigbus_linux_amd64.s, roleFromWire, and this test together")
	}
}
