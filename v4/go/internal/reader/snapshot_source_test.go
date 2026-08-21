package reader

// Snapshot source surface (slice A of chunk 3b-3): MetadataJSONLen,
// FileIdentity and ConfirmUnchanged mirror Rust metadata_json_len,
// identity_any_link and BasicSource::final_check over the Current
// selection. The conformance fixtures are frozen, so the metadata
// lengths and identities below are stable.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

func TestMetadataJSONLen(t *testing.T) {
	// Present metadata: the length equals the exact decompressed bytes.
	r := openFixture(t, "structured-ipv4.iprdb")
	n, ok := r.MetadataJSONLen()
	if !ok {
		t.Fatal("expected metadata on structured-ipv4")
	}
	raw, present, err := r.ReadMetadataJSON()
	if err != nil || !present {
		t.Fatalf("read: %v %v", present, err)
	}
	if n != uint64(len(raw)) {
		t.Fatalf("MetadataJSONLen %d, decompressed %d", n, len(raw))
	}
	// The corpus value is frozen: 87 bytes of the asn_names fixture.
	if n != 87 {
		t.Fatalf("metadata length %d want 87", n)
	}

	// Absent metadata: root zero reports false.
	r2 := openFixture(t, "structured-ipv4-nothreat.iprdb")
	if _, ok := r2.MetadataJSONLen(); ok {
		t.Fatal("structured-ipv4-nothreat must not carry metadata")
	}
}

func TestFileIdentity(t *testing.T) {
	r := openFixture(t, "direct-ipv4.iprdb")
	device, inode, err := r.FileIdentity()
	if err != nil {
		t.Fatal(err)
	}
	// The mapping owner is the single identity authority on every
	// platform (Windows included); comparing against its probe keeps the
	// test portable without syscall.Stat_t.
	device2, inode2, err := mapping.StatIdentity(fixture(t, "direct-ipv4.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	if device != device2 || inode != inode2 {
		t.Fatalf("identity (%d,%d) want (%d,%d)", device, inode, device2, inode2)
	}

	// After Close the descriptor is gone: an IO-class error, never a
	// panic (mapping.FileIdentity fstats the closed descriptor).
	r.Close()
	if _, _, err := r.FileIdentity(); mustCode(err) != format.CodeIO {
		t.Fatalf("identity after close: code %v want %v (err %v)", mustCode(err), format.CodeIO, err)
	}
}

func TestConfirmUnchanged(t *testing.T) {
	path := copyFixture(t, "direct-ipv4.iprdb", "confirm.iprdb")
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// Unchanged path and generation pass.
	if err := r.ConfirmUnchanged(path); err != nil {
		t.Fatalf("unchanged: %v", err)
	}

	// A removed path is the changed-candidate class (Rust
	// candidate_changed wraps the bind_current identity failure).
	t.Run("removed path", func(t *testing.T) {
		moved := path + ".moved"
		if err := os.Rename(path, moved); err != nil {
			t.Fatal(err)
		}
		defer os.Rename(moved, path)
		if err := r.ConfirmUnchanged(path); mustCode(err) != format.CodeRecoveryCandidateChanged {
			t.Fatalf("removed: code %v want %v (err %v)", mustCode(err), format.CodeRecoveryCandidateChanged, err)
		}
	})

	// A replaced inode (new file at the same name) is the same class.
	t.Run("replaced inode", func(t *testing.T) {
		backup := path + ".backup"
		if err := os.Rename(path, backup); err != nil {
			t.Fatal(err)
		}
		defer os.Rename(backup, path)
		other := copyFixture(t, "membership-ipv4.iprdb", "other.iprdb")
		raw, err := os.ReadFile(other)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := r.ConfirmUnchanged(path); mustCode(err) != format.CodeRecoveryCandidateChanged {
			t.Fatalf("replaced: code %v want %v (err %v)", mustCode(err), format.CodeRecoveryCandidateChanged, err)
		}
	})

	// A changed generation on the SAME inode (both meta pages rewritten
	// in place with a new transaction id) is detected by the re-bootstrap
	// compare: the path identity passes, the selected meta differs.
	t.Run("changed generation same inode", func(t *testing.T) {
		raw := make([]byte, 2*format.PageSize)
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.ReadAt(raw[:format.PageSize], 0); err != nil {
			f.Close()
			t.Fatal(err)
		}
		if _, err := f.ReadAt(raw[format.PageSize:], int64(format.PageSize)); err != nil {
			f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		for _, p := range [][]byte{raw[:format.PageSize], raw[format.PageSize:]} {
			format.PutU64(p[64:72], 3) // bumped transaction id
			format.PutU32(p[252:256], format.MetaCRC32C(p))
		}
		w, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.WriteAt(raw, 0); err != nil {
			w.Close()
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if err := r.ConfirmUnchanged(path); mustCode(err) != format.CodeRecoveryCandidateChanged {
			t.Fatalf("overwritten: code %v want %v (err %v)", mustCode(err), format.CodeRecoveryCandidateChanged, err)
		}
	})

	// After the mutation sub-tests the original content is gone; the
	// reader itself still mirrors the open-time generation.
	if err := r.ConfirmUnchanged(filepath.Join(t.TempDir(), "missing.iprdb")); mustCode(err) != format.CodeRecoveryCandidateChanged {
		t.Fatalf("missing path: code %v", mustCode(err))
	}
}
