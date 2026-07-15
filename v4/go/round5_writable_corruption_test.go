package iprangedb

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func round5CommittedScalarImage(t *testing.T, records uint32) []byte {
	t.Helper()
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < records; i++ {
		if err := w.Set(Ipv4Key(i*2+10), Ipv4Key(i*2+10), 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	image, ok := w.IntoImage()
	if !ok {
		t.Fatal("missing committed image")
	}
	return image
}

func TestRound5WritableOpenRejectsEveryCommittedDataCorruptionWithoutChangingFile(t *testing.T) {
	oneRecord := round5CommittedScalarImage(t, 1)
	branchTree := round5CommittedScalarImage(t, 800)
	tests := []struct {
		name   string
		base   []byte
		mutate func([]byte)
	}{
		{
			name: "leaf-crc",
			base: oneRecord,
			mutate: func(image []byte) {
				_, m := round5ActiveMetaPage(t, image)
				leaf := image[int(m.rootPgno)*PageSize : int(m.rootPgno+1)*PageSize]
				leaf[PageHeaderSize] ^= 0x80
			},
		},
		{
			name: "reversed-record",
			base: oneRecord,
			mutate: func(image []byte) {
				_, m := round5ActiveMetaPage(t, image)
				leaf := image[int(m.rootPgno)*PageSize : int(m.rootPgno+1)*PageSize]
				putU32(leaf, PageHeaderSize, 30)
				finalizeChecksum(leaf)
			},
		},
		{
			name: "record-count-mismatch",
			base: oneRecord,
			mutate: func(image []byte) {
				active, _ := round5ActiveMetaPage(t, image)
				putU64(active, MetaRecordCount, 2)
				finalizeChecksum(active)
			},
		},
		{
			name: "branch-separator-mismatch",
			base: branchTree,
			mutate: func(image []byte) {
				_, m := round5ActiveMetaPage(t, image)
				root := image[int(m.rootPgno)*PageSize : int(m.rootPgno+1)*PageSize]
				h := decodeHeader(root)
				if h.pageType != PageTypeBranch || h.entryCount == 0 {
					t.Fatal("fixture root is not a populated branch")
				}
				branch := newBranchView(root, int(h.entryCount), int(m.keyWidth))
				separator := u32le(branch.sep(0), 0)
				putU32(branch.sep(0), 0, separator+1)
				finalizeChecksum(root)
			},
		},
		{
			name: "empty-leaf-nonzero-tail",
			base: branchTree,
			mutate: func(image []byte) {
				active, m := round5ActiveMetaPage(t, image)
				root := image[int(m.rootPgno)*PageSize : int(m.rootPgno+1)*PageSize]
				h := decodeHeader(root)
				if h.pageType != PageTypeBranch || h.entryCount == 0 {
					t.Fatal("fixture root is not a populated branch")
				}
				branch := newBranchView(root, int(h.entryCount), int(m.keyWidth))
				leafPgno := branch.child(0)
				leaf := image[int(leafPgno)*PageSize : int(leafPgno+1)*PageSize]
				leafHeader := decodeHeader(leaf)
				if leafHeader.pageType != PageTypeLeaf || leafHeader.entryCount == 0 {
					t.Fatal("fixture child is not a populated leaf")
				}
				putU16(leaf, PHEntryCount, 0)
				finalizeChecksum(leaf)
				putU64(active, MetaRecordCount, m.recordCount-uint64(leafHeader.entryCount))
				finalizeChecksum(active)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			image := append([]byte(nil), tt.base...)
			tt.mutate(image)
			r, err := Open(image)
			if err != nil {
				t.Fatalf("cheap read open should defer tree validation: %v", err)
			}
			if err := r.Validate(); err == nil {
				t.Fatal("Reader.Validate accepted committed data corruption")
			}
			if w, err := openWriter[Ipv4Key](newVecPageStore(append([]byte(nil), image...))); err == nil {
				_ = w
				t.Error("core writable open accepted committed data corruption")
			}

			path := filepath.Join(t.TempDir(), "corrupt.iprdb")
			if err := os.WriteFile(path, image, 0o600); err != nil {
				t.Fatal(err)
			}
			before := append([]byte(nil), image...)
			fileAccepted := false
			if fw, err := OpenFile[Ipv4Key](path); err == nil {
				fileAccepted = true
				_ = fw.Close()
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, before) {
				t.Fatalf("writable open changed rejected file: %d bytes became %d", len(before), len(after))
			}
			if fileAccepted {
				t.Error("file-backed writable open accepted committed data corruption")
			}
		})
	}
}
