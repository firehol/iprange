package exactv4

import (
	"errors"
	"math"
	"testing"
)

func TestFullIPv6SpaceIsExact(t *testing.T) {
	count, err := IPv6Inclusive(0, 0, math.MaxUint64, math.MaxUint64)
	if err != nil {
		t.Fatal(err)
	}
	if count != FullIPv6Space() {
		t.Fatalf("count = %#v", count)
	}
	if got, want := count.String(), "340282366920938463463374607431768211456"; got != want {
		t.Fatalf("decimal = %q, want %q", got, want)
	}
	if _, _, err := count.Uint128(); !errors.Is(err, ErrCardinalityOverflow) {
		t.Fatalf("u128 conversion error = %v", err)
	}
}

func TestCardinalityAdditionAndSubtractionAreChecked(t *testing.T) {
	maximum, err := NewCardinality129(1, math.MaxUint64, math.MaxUint64)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := maximum.Add(CardinalityFromUint64(1)); !errors.Is(err, ErrCardinalityOverflow) {
		t.Fatalf("maximum + 1 error = %v", err)
	}
	if got, want := maximum.String(), "680564733841876926926749214863536422911"; got != want {
		t.Fatalf("maximum decimal = %q, want %q", got, want)
	}

	below, err := FullIPv6Space().Sub(CardinalityFromUint64(1))
	if err != nil {
		t.Fatal(err)
	}
	if below != CardinalityFromUint128(math.MaxUint64, math.MaxUint64) {
		t.Fatalf("2^128 - 1 = %#v", below)
	}
	if _, err := CardinalityZero().Sub(CardinalityFromUint64(1)); !errors.Is(err, ErrCardinalityOverflow) {
		t.Fatalf("zero - 1 error = %v", err)
	}
}

func TestInclusiveBoundaries(t *testing.T) {
	v4, err := IPv4Inclusive(0, math.MaxUint32)
	if err != nil {
		t.Fatal(err)
	}
	if v4 != CardinalityFromUint64(1<<32) {
		t.Fatalf("full IPv4 = %#v", v4)
	}
	v6, err := IPv6Inclusive(5, math.MaxUint64, 6, 0)
	if err != nil {
		t.Fatal(err)
	}
	if v6 != CardinalityFromUint64(2) {
		t.Fatalf("cross-boundary IPv6 = %#v", v6)
	}
	if _, err := IPv4Inclusive(2, 1); !errors.Is(err, ErrCardinalityOverflow) {
		t.Fatalf("reversed IPv4 error = %v", err)
	}
	if _, err := IPv6Inclusive(1, 0, 0, math.MaxUint64); !errors.Is(err, ErrCardinalityOverflow) {
		t.Fatalf("reversed IPv6 error = %v", err)
	}
}

func TestCardinalityConstructionAndConversions(t *testing.T) {
	if _, err := NewCardinality129(2, 0, 0); !errors.Is(err, ErrCardinalityOverflow) {
		t.Fatalf("bit128=2 error = %v", err)
	}
	values := []struct {
		hi   uint64
		lo   uint64
		text string
	}{
		{0, 0, "0"},
		{0, 1, "1"},
		{0, math.MaxUint64, "18446744073709551615"},
		{1, 0, "18446744073709551616"},
		{math.MaxUint64, math.MaxUint64, "340282366920938463463374607431768211455"},
	}
	for _, tc := range values {
		value := CardinalityFromUint128(tc.hi, tc.lo)
		if got := value.String(); got != tc.text {
			t.Errorf("%#x/%#x decimal = %q, want %q", tc.hi, tc.lo, got, tc.text)
		}
		hi, lo, err := value.Uint128()
		if err != nil || hi != tc.hi || lo != tc.lo {
			t.Errorf("u128 round trip = %#x/%#x/%v", hi, lo, err)
		}
	}
}
