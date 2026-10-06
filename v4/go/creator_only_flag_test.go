package iprangedb

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/security"
)

// The create flag is recorded in the sidecar and honored on a later
// open. These cases close the writer before reopening, so the check
// is not an in-process flag.

func TestCreatorOnlySwitchDefaultsOff(t *testing.T) {
	t.Setenv("IPRANGE_CREATOR_ONLY", "")
	if security.CreatorOnlyRequested() {
		t.Fatal("empty switch requested creator-only")
	}
	t.Setenv("IPRANGE_CREATOR_ONLY", "0")
	if security.CreatorOnlyRequested() {
		t.Fatal("zero switch requested creator-only")
	}
	t.Setenv("IPRANGE_CREATOR_ONLY", "1")
	if !security.CreatorOnlyRequested() {
		t.Fatal("IPRANGE_CREATOR_ONLY=1 did not request creator-only")
	}
}

func TestSnapshotFollowsUnprotectedDatabase(t *testing.T) {
	requireLiveCreation(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "plain.iprdb")
	tag, err := NewValueTag([]byte("asn"))
	if err != nil {
		t.Fatal(err)
	}
	old := setUmask(0)
	created, err := CreateLive(source, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, tag, 2, nil, false)
	setUmask(old)
	if err != nil {
		t.Fatal(err)
	}
	if created.State != CreationStateCreated {
		t.Fatalf("state = %v", created.State)
	}
	destination := filepath.Join(dir, "snap.iprdb")
	result, err := SnapshotTo(source, SnapshotSourceLive, destination, PolicyFailIfExists, &SnapshotBudget{
		MaxHeapBytes: 16 << 20, MaxOutputPages: 100_000, MaxOpenFiles: 4,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Publication.Publication != PublicationPublished {
		t.Fatalf("publication = %v", result.Publication.Publication)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() == 0o600 {
		t.Fatal("snapshot of an unprotected database was forced to mode 0600")
	}
}

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
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("protected mode = %o, want 0600", info.Mode().Perm())
	}
	reader, err := OpenLiveReader(main, nil)
	if err != nil {
		t.Fatal("protected reopen:", err)
	}
	reader.Close()

	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Chmod(main, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLiveReader(main, nil); err == nil {
		t.Fatal("widened protected file reopened")
	}
}

func TestUnprotectedCreateSkipsProofEvenAtMode0600(t *testing.T) {
	requireLiveCreation(t)
	old := setUmask(0)
	defer setUmask(old)
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
