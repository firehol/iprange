// Input-guard tests for the public facades (SOW-0027 milestone-5 slice
// A): nil scope entries, nil join partners, and undefined recovery
// certifications return typed errors instead of panicking or silently
// accepting an invalid enum value.

package iprangedb

import (
	"os"
	"testing"
)

func TestNewMembershipAlgebraNilScopeRefused(t *testing.T) {
	if _, err := NewMembershipAlgebra(nil, MembershipAlgebraBudget{}, nil); errorAsCode(err) != ErrorInvalidArgument {
		t.Fatalf("nil scope slice: got error %v (code %v), want ErrorInvalidArgument", err, errorAsCode(err))
	}
	if _, err := NewMembershipAlgebra([]*MembershipScope{nil}, MembershipAlgebraBudget{}, nil); errorAsCode(err) != ErrorInvalidArgument {
		t.Fatalf("nil scope entry: got error %v (code %v), want ErrorInvalidArgument", err, errorAsCode(err))
	}
	// A valid scope mixed with a nil entry must refuse at the nil entry
	// (the loop order names the first failure), never dereference it.
	if _, err := NewMembershipAlgebra([]*MembershipScope{nil, nil}, MembershipAlgebraBudget{}, nil); errorAsCode(err) != ErrorInvalidArgument {
		t.Fatalf("nil scope entries: got error %v (code %v), want ErrorInvalidArgument", err, errorAsCode(err))
	}
}

func TestJoinMembershipNilRightRefused(t *testing.T) {
	// The receiver is never touched when the partner is nil, so even a
	// zero receiver must not panic.
	var left *MembershipScope
	if _, err := left.JoinMembership(nil, nil, nil, nil); errorAsCode(err) != ErrorInvalidArgument {
		t.Fatalf("nil right scope: got error %v (code %v), want ErrorInvalidArgument", err, errorAsCode(err))
	}
}

func TestRecoverOfflineUndefinedCertificationRefused(t *testing.T) {
	destination := t.TempDir() + "/recovery-test.iprdb"
	result, failure := RecoverOffline("no-such-source", nil, destination, OfflineQuiescenceCertification(255), nil, nil, nil)
	if result != nil {
		t.Fatalf("undefined certification: got a result %+v, want nil", result)
	}
	if failure == nil || errorAsCode(failure.Cause) != ErrorInvalidArgument {
		t.Fatalf("undefined certification: got failure %+v (code %v), want ErrorInvalidArgument", failure, errorAsCode(failure.Cause))
	}
	if _, err := os.Stat(destination); err == nil {
		t.Fatalf("undefined certification: destination %s was created before the certification guard", destination)
	}
}

func TestRecoverOfflineNilBudgetStillRefusedAfterValidCertification(t *testing.T) {
	// The certification guard must not break the existing nil-budget
	// refusal for the defined certification value.
	result, failure := RecoverOffline("no-such-source", nil, t.TempDir()+"/recovery-test.iprdb", CallerCertified, nil, nil, nil)
	if result != nil {
		t.Fatalf("nil budget: got a result %+v, want nil", result)
	}
	if failure == nil || errorAsCode(failure.Cause) != ErrorInvalidArgument {
		t.Fatalf("nil budget: got failure %+v (code %v), want ErrorInvalidArgument", failure, errorAsCode(failure.Cause))
	}
}
