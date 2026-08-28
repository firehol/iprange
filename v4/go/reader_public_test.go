package iprangedb

import (
	"path/filepath"
	"testing"
)

// TestPublicReaderMetadataBufferApis ports the Rust database.rs /
// live_reader.rs metadata_json_len + read_metadata_json surface: both
// the immutable and the live reader report the exact length, fill
// caller storage, refuse an undersized buffer with ErrorBufferTooSmall,
// and report absent metadata without error.
func TestPublicReaderMetadataBufferApis(t *testing.T) {
	requireLiveCreation(t)
	requirePublicationSecurity(t)
	main, _ := createLivePublicPair(t, 2)
	w, err := OpenLiveWriter(main, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := w.BeginDirect(nil)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := tx.SetMetadataJSON([]byte(`{"readers":true}`)); err != nil || !changed {
		t.Fatalf("stage: changed=%v err=%v", changed, err)
	}
	if _, err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}

	live, err := OpenLiveReader(main, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()

	snapshot := filepath.Join(t.TempDir(), "reader-metadata.iprdb")
	if _, err := SnapshotTo(main, SnapshotSourceLive, snapshot, PolicyFailIfExists, snapshotBudget(6), nil); err != nil {
		t.Fatal("snapshot:", err)
	}
	immutable, err := OpenImmutable(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer immutable.Close()

	for name, reader := range map[string]interface {
		MetadataJSONLen() (uint64, bool, error)
		ReadMetadataJSON([]byte) (int, bool, error)
	}{
		"immutable": immutable,
		"live":      live,
	} {
		length, present, err := reader.MetadataJSONLen()
		if err != nil || !present || length != uint64(len(`{"readers":true}`)) {
			t.Fatalf("%s length = (%d, %v, %v), want the exact payload length", name, length, present, err)
		}
		var small [4]byte
		if _, _, err := reader.ReadMetadataJSON(small[:]); !isPubCode(err, ErrorBufferTooSmall) {
			t.Fatalf("%s small-buffer read = %v, want buffer too small", name, err)
		}
		output := make([]byte, 64)
		n, present, err := reader.ReadMetadataJSON(output)
		if err != nil || !present || n != len(`{"readers":true}`) || string(output[:n]) != `{"readers":true}` {
			t.Fatalf("%s caller-buffer read = (%d, %v, %v), want the exact payload", name, n, present, err)
		}
	}

	// A database without metadata reports absent on both methods.
	absentMain, _ := createLivePublicPair(t, 1)
	absentSnapshot := filepath.Join(t.TempDir(), "reader-metadata-absent.iprdb")
	if _, err := SnapshotTo(absentMain, SnapshotSourceLive, absentSnapshot, PolicyFailIfExists, snapshotBudget(6), nil); err != nil {
		t.Fatal("absent snapshot:", err)
	}
	absent, err := OpenImmutable(absentSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer absent.Close()
	if _, present, err := absent.MetadataJSONLen(); err != nil || present {
		t.Fatalf("absent metadata len = (%v, %v), want absent", present, err)
	}
	if n, present, err := absent.ReadMetadataJSON(nil); err != nil || present || n != 0 {
		t.Fatalf("absent metadata read = (%d, %v, %v), want (0, false, nil)", n, present, err)
	}
}
