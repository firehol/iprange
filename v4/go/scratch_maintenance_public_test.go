package iprangedb

// Public abandoned-scratch maintenance surface (Rust
// recovery::list_abandoned_scratch / remove_abandoned_scratch):
// independent root-package consumer tests — the exact artifact names
// and 128-byte ownership headers are built here from the documented
// wire format, listed through the public API, read through the
// exported entry fields, stopped with the exported sink sentinel,
// and removed with the listed identities.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// scratchPublicHeader builds the exact 128-byte ownership header of
// one recovery scratch artifact (spec binary-format-v4 scratch
// format): magic, fixed fields, meta facts, attempt, ordinal, the
// creator-only security kind and commitment, and the zeroed-field
// CRC-32C.
func scratchPublicHeader(attempt [16]byte, ordinal uint32) [128]byte {
	var out [128]byte
	copy(out[0:8], "IPR4SCR1")
	format.PutU16(out[8:10], 1)    // version
	format.PutU16(out[10:12], 128) // header size
	format.PutU16(out[12:14], 2)   // owner kind: recovery
	copy(out[16:32], []byte{1})    // database id
	format.PutU64(out[32:40], 7)   // txn id
	copy(out[40:56], []byte{2})    // commit nonce
	copy(out[56:72], attempt[:])
	format.PutU32(out[72:76], ordinal)
	format.PutU16(out[76:78], 1) // creator-only security kind
	copy(out[80:112], []byte{9}) // security commitment (nonzero)
	checksum, ok := format.CRC32CWithZeroed(out[:], 124, 4)
	if !ok {
		panic("fixed header CRC range")
	}
	format.PutU32(out[124:128], checksum)
	return out
}

// scratchPublicName builds the exact 62-byte artifact basename of one
// recovery scratch artifact (the 8-hex ordinal field is the fixed
// little-endian decimal ordinal, zero-padded).
func scratchPublicName(attempt [16]byte, ordinal uint32) string {
	const hex = "0123456789abcdef"
	name := ".iprange-scratch-"
	for _, b := range attempt {
		name += string([]byte{hex[b>>4], hex[b&0x0f]})
	}
	return name + "-" + fmt.Sprintf("%08x", ordinal) + ".tmp"
}

// writeScratchArtifact creates one exact scratch artifact in the
// directory.
func writeScratchArtifact(t *testing.T, directory string, attempt [16]byte, ordinal uint32) {
	t.Helper()
	header := scratchPublicHeader(attempt, ordinal)
	name := scratchPublicName(attempt, ordinal)
	if err := os.WriteFile(filepath.Join(directory, name), header[:], 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestPublicScratchMaintenanceListReadsFieldsAndRemovesExact mirrors
// the Rust maintenance surface end to end from the root package: two
// artifacts authenticate as recovery, every exported entry field is
// readable, and removal with the listed identities leaves the
// directory empty.
func TestPublicScratchMaintenanceListReadsFieldsAndRemovesExact(t *testing.T) {
	directory := t.TempDir()
	attempt := [16]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x01}
	writeScratchArtifact(t, directory, attempt, 0)
	writeScratchArtifact(t, directory, attempt, 1)

	var entries []AbandonedScratchEntry
	list, err := ListAbandonedScratch(directory, nil, func(entry *AbandonedScratchEntry) error {
		entries = append(entries, *entry)
		return nil
	})
	if err != nil {
		t.Fatalf("ListAbandonedScratch: %v", err)
	}
	if list.Entries != 2 {
		t.Fatalf("listed entries = %d, want 2", list.Entries)
	}
	if list.DirectoryIdentity == (FileIdentity{}) {
		t.Fatal("directory identity is zero")
	}
	if len(entries) != 2 {
		t.Fatalf("sink entries = %d, want 2", len(entries))
	}
	if entries[0].Ordinal > entries[1].Ordinal {
		entries[0], entries[1] = entries[1], entries[0]
	}
	for index, wantOrdinal := range []uint32{0, 1} {
		entry := entries[index]
		if !entry.Authentication.Authenticated || entry.Authentication.Owner != ScratchOwnerRecovery {
			t.Fatalf("entry %d authentication = %+v, want authenticated recovery", index, entry.Authentication)
		}
		if entry.AttemptID != attempt {
			t.Fatalf("entry %d attempt = %x, want %x", index, entry.AttemptID, attempt)
		}
		if entry.Ordinal != wantOrdinal {
			t.Fatalf("entry %d ordinal = %d, want %d", index, entry.Ordinal, wantOrdinal)
		}
		if entry.DirectoryIdentity != list.DirectoryIdentity {
			t.Fatalf("entry %d directory identity mismatch", index)
		}
		if entry.ArtifactIdentity == (FileIdentity{}) {
			t.Fatalf("entry %d artifact identity is zero", index)
		}
	}
	for _, entry := range entries {
		removal, err := RemoveAbandonedScratch(directory, list.DirectoryIdentity, attempt, entry.Ordinal, entry.ArtifactIdentity, nil)
		if err != nil {
			t.Fatalf("RemoveAbandonedScratch(%d): %v", entry.Ordinal, err)
		}
		if removal.Cause != nil {
			t.Fatalf("removal %d problem = %+v", entry.Ordinal, removal.Cause)
		}
	}
	left, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("directory left %d entries after removal", len(left))
	}
}

// TestPublicScratchMaintenanceSinkStopMapsToStoppedBySink pins the
// documented public control: returning ErrMaintenanceSinkStop from
// the sink stops the scan with the StoppedBySink class.
func TestPublicScratchMaintenanceSinkStopMapsToStoppedBySink(t *testing.T) {
	directory := t.TempDir()
	attempt := [16]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10}
	writeScratchArtifact(t, directory, attempt, 0)
	_, err := ListAbandonedScratch(directory, nil, func(*AbandonedScratchEntry) error {
		return ErrMaintenanceSinkStop
	})
	if err == nil {
		t.Fatal("list succeeded despite sink stop")
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != ErrorStoppedBySink {
		t.Fatalf("err = %v, want StoppedBySink", err)
	}
}
