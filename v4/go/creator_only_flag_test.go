package iprangedb

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// The create flag is recorded in the sidecar and honored on a later
// open. These cases close the writer before reopening, so the check
// is not an in-process flag.

func TestCreatorOnlyFlagCreateCloseReopen(t *testing.T) {
	requireLiveCreation(t)
	dir := t.TempDir()
	main := filepath.Join(dir, "protected.iprdb")
	tag, err := NewValueTag([]byte("asn"))
	if err != nil {
		t.Fatal(err)
	}
	created, err := CreateLive(main, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, tag, 2, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if created.State != CreationStateCreated {
		t.Fatalf("state = %v, want Created", created.State)
	}
	info, err := os.Stat(main)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("protected mode = %o, want 0600", info.Mode().Perm())
	}
	reader, err := OpenLiveReader(main, nil)
	if err != nil {
		t.Fatal("protected reopen:", err)
	}
	reader.Close()

	if err := os.Chmod(main, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLiveReader(main, nil); err == nil {
		t.Fatal("widened protected file reopened")
	}
}

func TestUnprotectedCreateSkipsProofEvenAtMode0600(t *testing.T) {
	requireLiveCreation(t)
	old := unix.Umask(0)
	defer unix.Umask(old)
	dir := t.TempDir()
	main := filepath.Join(dir, "plain.iprdb")
	tag, err := NewValueTag([]byte("asn"))
	if err != nil {
		t.Fatal(err)
	}
	created, err := CreateLive(main, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, tag, 2, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if created.State != CreationStateCreated {
		t.Fatalf("state = %v, want Created", created.State)
	}
	info, err := os.Stat(main)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() == 0o600 {
		t.Fatal("unprotected create forced mode 0600; umask 0 must not be overridden")
	}
	reader, err := OpenLiveReader(main, nil)
	if err != nil {
		t.Fatal("unprotected reopen checked the proof:", err)
	}
	reader.Close()
	if _, err := OpenLiveReaderPolicy(main, nil, true); err == nil {
		t.Fatal("require_creator_only accepted an unprotected file")
	}
}
