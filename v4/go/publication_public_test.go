//go:build v4work && linux

// Public publication surface tests (Rust publication.rs + residue.rs
// + maintenance.rs parity): the resolution refusal on an empty
// destination, the residue inspection/removal through the SDK
// boundary, the abandoned-publication-temp listing/removal over one
// exact private name, the sink-stop control, and the leading
// cancellation refusal of every entry point.

package iprangedb

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPublicationSurfaceResidueAndMaintenanceEndToEnd drives the
// residue inspector/remover and the abandoned-temp maintenance
// through the public entry points over hand-built namespace state:
// the coordination twin is unselectable junk, the private output is
// one exact-pattern temp with partial content.
func TestPublicationSurfaceResidueAndMaintenanceEndToEnd(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	coordination := main + ".readers"
	if err := os.WriteFile(coordination, []byte("malformed"), 0o600); err != nil {
		t.Fatalf("write coordination: %v", err)
	}
	attempt := [16]byte{9}
	privatePath := filepath.Join(dir, publicationTempName(attempt))
	if err := os.WriteFile(privatePath, []byte("partial"), 0o600); err != nil {
		t.Fatalf("write private temp: %v", err)
	}

	inspected, err := InspectPublicationResidue(main, nil)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if inspected.Coordination != PublicationResidueCoordinationUnselectable {
		t.Fatalf("coordination = %v, want unselectable", inspected.Coordination)
	}
	if inspected.Handle == nil {
		t.Fatal("handle missing")
	}
	removal, err := RemovePublicationResidue(inspected.Handle, nil)
	if err != nil {
		t.Fatalf("remove residue: %v", err)
	}
	if removal.CleanupState() != CleanupStateClean {
		t.Fatalf("cleanup state = %v, want clean", removal.CleanupState())
	}
	if _, err := os.Lstat(coordination); err == nil {
		t.Fatal("coordination still exists")
	}

	// A fresh inspection reports the absent class and no handle.
	inspected, err = InspectPublicationResidue(main, nil)
	if err != nil {
		t.Fatalf("re-inspect: %v", err)
	}
	if inspected.Coordination != PublicationResidueCoordinationAbsent {
		t.Fatalf("coordination = %v, want absent", inspected.Coordination)
	}
	if inspected.Handle != nil {
		t.Fatal("handle present on absence")
	}

	var listed []*AbandonedPublicationTempEntry
	summary, err := ListAbandonedPublicationTemps(dir, nil, func(entry *AbandonedPublicationTempEntry) error {
		listed = append(listed, entry)
		return nil
	})
	if err != nil {
		t.Fatalf("list temps: %v", err)
	}
	if summary.Entries != 1 || len(listed) != 1 {
		t.Fatalf("summary = %d, listed = %d, want 1", summary.Entries, len(listed))
	}
	entry := listed[0]
	if entry.PublicationAttemptID != attempt {
		t.Fatalf("attempt id %x, want %x", entry.PublicationAttemptID, attempt)
	}
	if entry.Tuple != nil || entry.Digest != nil {
		t.Fatal("partial entry carries evidence")
	}
	// The exported stop value ends the scan with the stopped class
	// (checked while entries still exist: an empty scan never calls
	// the sink).
	if _, err := ListAbandonedPublicationTemps(dir, nil, func(*AbandonedPublicationTempEntry) error {
		return ErrMaintenanceSinkStop
	}); codeOfPublic(err) != ErrorStoppedBySink {
		t.Fatalf("stop problem = %v, want stopped by sink", err)
	}

	removed, err := RemoveAbandonedPublicationTemp(dir, entry.DirectoryIdentity, entry.PublicationAttemptID, entry.ArtifactIdentity, nil, nil, nil)
	if err != nil {
		t.Fatalf("remove temp: %v", err)
	}
	if !removed.SourcePresent {
		t.Fatal("removal reports source absent")
	}
	if _, err := os.Lstat(privatePath); err == nil {
		t.Fatal("private temp still exists")
	}
}

// TestPublicationSurfaceResolutionRefusalAndCancellation pins the
// unresolvable refusal on an empty destination and the leading
// cancellation of the residue inspector.
func TestPublicationSurfaceResolutionRefusalAndCancellation(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	if _, err := ResolvePublication(main, nil, PublicationResolutionRemove, nil); codeOfPublic(err) != ErrorUnresolvable {
		t.Fatalf("resolve problem = %v, want unresolvable", err)
	}
	if _, err := os.Lstat(main); err == nil {
		t.Fatal("main was created by the refusal")
	}

	token := NewCancellationToken()
	token.Cancel()
	if _, err := InspectPublicationResidue(main, token); codeOfPublic(err) != ErrorCancelled {
		t.Fatalf("cancelled inspect problem = %v, want cancelled", err)
	}
	if _, err := ListAbandonedPublicationTemps(dir, token, func(*AbandonedPublicationTempEntry) error { return nil }); codeOfPublic(err) != ErrorCancelled {
		t.Fatalf("cancelled list problem = %v, want cancelled", err)
	}
}

// publicationTempName builds one exact private publication-output
// name for attempt (the machine's private-name rule; the boundary
// scan decodes it back).
func publicationTempName(attempt [16]byte) string {
	const hexDigits = "0123456789abcdef"
	name := make([]byte, 0, 33)
	name = append(name, ".iprange-publish-"...)
	for _, b := range attempt {
		name = append(name, hexDigits[b>>4], hexDigits[b&0xf])
	}
	return string(append(name, ".tmp"...))
}

// codeOfPublic reports the public error code of one boundary error.
func codeOfPublic(err error) ErrorCode {
	if err == nil {
		return 0
	}
	if public, ok := err.(*Error); ok {
		return public.Code
	}
	var typed *Error
	if asError(err, &typed) {
		return typed.Code
	}
	return 0
}

// asError unwraps one public Error through the cause chain.
func asError(err error, target **Error) bool {
	for err != nil {
		if typed, ok := err.(*Error); ok {
			*target = typed
			return true
		}
		unwrapped, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapped.Unwrap()
	}
	return false
}
