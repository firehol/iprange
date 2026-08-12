package iprangedb

import "testing"

// TestPinVariableReassignmentKeepsViewGuard pins the view-lifetime contract
// against variable reassignment (round-11 sol finding): a view created from
// a value copy of a pin must keep guarding the pin state it was created
// from, even when the variable that held the pin is later reassigned to a
// different pin (cross-reader reassignment). Views retain the immutable
// pinState, never the Pin wrapper, so reassignment cannot retarget the
// guard to another reader's lifetime state and expose the first reader's
// released mapping.
func TestPinVariableReassignmentKeepsViewGuard(t *testing.T) {
	r1 := mustOpen(t, "rust/structured-ipv4.iprdb")
	pin1, err := r1.Pin()
	if err != nil {
		t.Fatal("pin1:", err)
	}
	pinCopy := *pin1

	enrich, found, err := pinCopy.LookupNetworkEnrichmentV1V4(IPv4(0x0a010000))
	if err != nil || !found {
		t.Fatalf("enrichment lookup: %v %v", found, err)
	}
	threat, present, err := enrich.ThreatMembership()
	if err != nil || !present {
		t.Fatalf("threat membership: %v %v", present, err)
	}
	if _, _, err := threat.Word(0); err != nil {
		t.Fatalf("word through live pin: %v", err)
	}

	r2 := mustOpen(t, "rust/membership-ipv4.iprdb")
	pin2, err := r2.Pin()
	if err != nil {
		t.Fatal("pin2:", err)
	}
	pinCopy = *pin2 // reassign the variable to another reader's pin

	if err := pin1.Close(); err != nil {
		t.Fatal("pin1 close:", err)
	}
	if err := r1.Close(); err != nil {
		t.Fatal("r1 close:", err)
	}

	// Every view born from pin1 must report WrongState now, never touch
	// r1's released mapping through the pin2 state the variable holds.
	if _, err := enrich.Value(); errorAsCode(err) != ErrorWrongState {
		t.Fatalf("enrichment view after owner close: %v", err)
	}
	if _, _, err := threat.Word(0); errorAsCode(err) != ErrorWrongState {
		t.Fatalf("threat word after owner close: %v", err)
	}
	if _, err := threat.ContainsIndex(0); errorAsCode(err) != ErrorWrongState {
		t.Fatalf("threat contains after owner close: %v", err)
	}

	if err := pin2.Close(); err != nil {
		t.Fatal("pin2 close:", err)
	}
	if err := r2.Close(); err != nil {
		t.Fatal("r2 close:", err)
	}
}
