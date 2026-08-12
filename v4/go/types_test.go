package iprangedb

import (
	"math"
	"testing"
)

func TestPublicSemanticFoundation(t *testing.T) {
	if AddressFamilyIPv4 != 4 || AddressFamilyIPv6 != 6 {
		t.Fatal("address-family registry drift")
	}
	if ValueKindDirect != 1 || ValueKindMembership != 2 || ValueKindStructured != 3 {
		t.Fatal("value-kind registry drift")
	}
	// Engine-defined direct semantics share the Rust numeric registry
	// (Generic=1, FirstSeen=2, LastSeen=3); zero is not a valid public value.
	if DirectSemanticGeneric != 1 || DirectSemanticFirstSeen != 2 || DirectSemanticLastSeen != 3 {
		t.Fatal("direct-semantic registry drift")
	}
	count, err := IPv6Inclusive(0, 0, math.MaxUint64, math.MaxUint64)
	if err != nil {
		t.Fatal(err)
	}
	if count != FullIPv6Space() {
		t.Fatalf("full IPv6 cardinality = %#v", count)
	}
	if ErrorInvalidArgument != 1 || ErrorCleanupInProgress != 64 {
		t.Fatalf("error registry endpoints = %d/%d", ErrorInvalidArgument, ErrorCleanupInProgress)
	}
}
