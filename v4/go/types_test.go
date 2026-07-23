package iprangedb

import (
	"math"
	"testing"
)

func TestPublicSemanticFoundation(t *testing.T) {
	if AddressFamilyIPv4 != 4 || AddressFamilyIPv6 != 6 {
		t.Fatal("address-family registry drift")
	}
	if ValueKindDirect != 1 || ValueKindMembership != 2 {
		t.Fatal("value-kind registry drift")
	}
	if got := string(RetentionTag().Bytes()); got != "retention" {
		t.Fatalf("retention tag = %q", got)
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
