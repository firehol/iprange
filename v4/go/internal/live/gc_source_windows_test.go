//go:build windows

package live

import "testing"

// TestGCPrivateNameFitsEveryPrivatePrefix pins the fixed scratch of
// gcPrivateName to the longest private prefix. The reservation name is four
// characters longer than the output name, so a scratch sized for the output
// prefix silently drops the ".tmp" suffix and then panics on the return
// slice — the shape the maintenance reservation retirement (the first caller
// of gcPrivateName with the reservation prefix) exposed.
func TestGCPrivateNameFitsEveryPrivatePrefix(t *testing.T) {
	var attempt [16]byte
	for index := range attempt {
		attempt[index] = byte(index)
	}
	const attemptHex = "000102030405060708090a0b0c0d0e0f"
	for _, probe := range []struct {
		name   string
		prefix string
	}{
		{"output", gcOutputPrefix},
		{"reservation", gcReservationPrefix},
	} {
		width := len(probe.prefix) + 32 + len(gcPrivateSuffix)
		name, err := gcPrivateName(probe.prefix, attempt)
		if err != nil {
			t.Fatalf("%s: gcPrivateName: %v", probe.name, err)
		}
		if want := probe.prefix + attemptHex + gcPrivateSuffix; name != want {
			t.Fatalf("%s: name = %q (len %d), want %q (len %d)",
				probe.name, name, len(name), want, width)
		}
	}
}

// TestGCNameMatchesAcceptsTheExactPrivateNames pins the source-name binding
// the GC retirement authority relies on for each private kind: the exact
// attempt-derived name of its own family matches, and the other family's
// name never does.
func TestGCNameMatchesAcceptsTheExactPrivateNames(t *testing.T) {
	var attempt [16]byte
	for index := range attempt {
		attempt[index] = byte(index)
	}
	output, err := gcPrivateName(gcOutputPrefix, attempt)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := gcPrivateName(gcReservationPrefix, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if !gcNameMatches(ArtifactPrivateOutput, attempt, 0, output) {
		t.Fatalf("the exact private output name %q must match its kind", output)
	}
	if !gcNameMatches(ArtifactPrivateReservation, attempt, 1, reservation) {
		t.Fatalf("the exact private reservation name %q must match its kind", reservation)
	}
	if gcNameMatches(ArtifactPrivateOutput, attempt, 0, reservation) ||
		gcNameMatches(ArtifactPrivateReservation, attempt, 1, output) {
		t.Fatal("a private name of the other family must never match its kind")
	}
}
