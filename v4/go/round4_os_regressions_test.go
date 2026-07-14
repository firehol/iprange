//go:build unix

package iprangedb

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestCreateFileValidatesBeforeReplacingExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.iprdb")
	want := []byte("preserve existing data")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	writer, createErr := CreateFile[Ipv4Key](path, 99, 0)
	accepted := createErr == nil
	if writer != nil {
		_ = writer.Close()
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if accepted || !bytes.Equal(got, want) {
		t.Fatalf("CreateFile invalid mode: accepted=%v preserved=%v resulting_size=%d want_size=%d", accepted, bytes.Equal(got, want), len(got), len(want))
	}
}

func TestWritableOpenRejectsInvalidGeometryWithoutChangingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "small-total-pages.iprdb")
	fw, err := CreateFile[Ipv4Key](path, ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < 40000; i++ {
		if err := fw.Set(Ipv4Key(i*3), Ipv4Key(i*3), i); err != nil {
			t.Fatal(err)
		}
	}
	if err := fw.Commit(1); err != nil {
		t.Fatal(err)
	}
	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) <= 66*PageSize {
		t.Fatalf("fixture is too small: %d bytes", len(before))
	}
	forged := append([]byte(nil), before...)
	active := activeMetaPage(forged)
	putU64(forged, active*PageSize+MetaTotalPages, 2)
	restamp(forged, active)
	if err := os.WriteFile(path, forged, 0o600); err != nil {
		t.Fatal(err)
	}

	var opened *FileWriter[Ipv4Key]
	var openErr error
	var panicValue any
	func() {
		defer func() { panicValue = recover() }()
		opened, openErr = OpenFile[Ipv4Key](path)
	}()
	if opened != nil {
		_ = opened.Close()
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if panicValue != nil {
		t.Fatalf("OpenFile panicked on invalid geometry: %v", panicValue)
	}
	if openErr == nil {
		t.Fatal("OpenFile accepted checksum-valid impossible total_pages")
	}
	if string(after) != string(forged) {
		t.Fatalf("OpenFile changed a corrupt file while rejecting it: %d bytes became %d", len(forged), len(after))
	}
}

func TestMmapOpenRejectsInvalidPinnedMetadata(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]byte, int)
	}{
		{
			name: "scope-mode",
			mutate: func(raw []byte, active int) {
				raw[active*PageSize+MetaScopeMode] = 99
			},
		},
		{
			name: "empty-root-with-height",
			mutate: func(raw []byte, active int) {
				putU32(raw, active*PageSize+MetaRootPgno, 0)
				putU32(raw, active*PageSize+MetaTreeHeight, 1)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "db.iprdb")
			fw, err := CreateFile[Ipv4Key](path, ScopeModeScalar, 0)
			if err != nil {
				t.Fatal(err)
			}
			if err := fw.Close(); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			active := activeMetaPage(raw)
			tt.mutate(raw, active)
			restamp(raw, active)
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			reader, err := OpenMmap(path)
			if err == nil {
				_ = reader.Close()
				t.Fatalf("OpenMmap accepted invalid %s metadata", tt.name)
			}
		})
	}
}

func TestExternalMergeUsesBoundedFileDescriptors(t *testing.T) {
	if os.Getenv("IPRANGE_ROUND4_FD_HELPER") == "1" {
		var limit syscall.Rlimit
		if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
			t.Fatal(err)
		}
		if limit.Cur > 64 {
			limit.Cur = 64
		}
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
			t.Fatal(err)
		}
		round4RunBoundedFDSort(t)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestExternalMergeUsesBoundedFileDescriptors$", "-test.v")
	cmd.Env = append(os.Environ(), "IPRANGE_ROUND4_FD_HELPER=1")
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("bounded-FD child did not finish: %v\n%s", ctx.Err(), output)
	}
	if err != nil {
		t.Fatalf("external merge failed under RLIMIT_NOFILE=64: %v\n%s", err, output)
	}
}

func round4RunBoundedFDSort(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	sorter := NewExtSorter[Ipv4Key](&ExtSortConfig{ChunkSize: 1, TempDir: dir})
	for i := 0; i < 100; i++ {
		if err := sorter.Add(Ipv4Key(i*2), Ipv4Key(i*2), 1); err != nil {
			t.Fatalf("Add(%d): %v", i, err)
		}
	}
	stream, err := sorter.Finish()
	if err != nil {
		t.Fatal(err)
	}
	for stream.Next() != nil {
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("spill cleanup: entries=%v err=%v", entries, err)
	}
}

func TestExternalSorterLifecycleIsTerminalAndCleansUp(t *testing.T) {
	t.Run("finish", func(t *testing.T) {
		dir := t.TempDir()
		sorter := NewExtSorter[Ipv4Key](&ExtSortConfig{ChunkSize: 1, TempDir: dir})
		if err := sorter.Add(1, 1, 1); err != nil {
			t.Fatal(err)
		}
		stream, err := sorter.Finish()
		if err != nil {
			t.Fatal(err)
		}
		for stream.Next() != nil {
		}
		if err := stream.Err(); err != nil {
			t.Fatal(err)
		}
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
			t.Fatalf("Finish cleanup: entries=%v err=%v", entries, err)
		}
		if err := sorter.Add(2, 2, 2); err == nil {
			t.Fatal("Add succeeded after Finish")
		}
		if _, err := sorter.Finish(); err == nil {
			t.Fatal("a second Finish succeeded")
		}
	})

	t.Run("abort", func(t *testing.T) {
		dir := t.TempDir()
		sorter := NewExtSorter[Ipv4Key](&ExtSortConfig{ChunkSize: 1, TempDir: dir})
		if err := sorter.Add(1, 1, 1); err != nil {
			t.Fatal(err)
		}
		sorter.Abort()
		if err := sorter.Add(2, 2, 2); err == nil {
			t.Fatal("Add succeeded after Abort")
		}
		if _, err := sorter.Finish(); err == nil {
			t.Fatal("Finish succeeded after Abort")
		}
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
			t.Fatalf("Abort cleanup: entries=%v err=%v", entries, err)
		}
	})
}

func TestExternalSorterCopiesCallerConfiguration(t *testing.T) {
	dir := t.TempDir()
	config := &ExtSortConfig{ChunkSize: 1, TempDir: dir}
	sorter := NewExtSorter[Ipv4Key](config)
	config.ChunkSize = 2
	config.TempDir = filepath.Join(dir, "missing")
	if err := sorter.Add(1, 1, 1); err != nil {
		t.Fatalf("caller mutation changed live sorter configuration: %v", err)
	}
	stream, err := sorter.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if got := stream.Next(); got == nil || got.From != 1 || got.To != 1 || got.ScopeID != 1 {
		t.Fatalf("stream record = %#v", got)
	}
}

func TestExternalSorterRejectsInvalidRange(t *testing.T) {
	sorter := NewExtSorter[Ipv4Key](&ExtSortConfig{ChunkSize: 10, TempDir: t.TempDir()})
	if err := sorter.Add(20, 10, 1); err == nil {
		t.Fatal("ExtSorter.Add accepted from > to")
	}
}

func TestExternalMergeStreamCanBeClosedEarly(t *testing.T) {
	dir := t.TempDir()
	sorter := NewExtSorter[Ipv4Key](&ExtSortConfig{ChunkSize: 1, TempDir: dir})
	for i := 0; i < 4; i++ {
		if err := sorter.Add(Ipv4Key(i*2), Ipv4Key(i*2), 1); err != nil {
			t.Fatal(err)
		}
	}
	stream, err := sorter.Finish()
	if err != nil {
		t.Fatal(err)
	}
	closer, ok := stream.(io.Closer)
	if !ok {
		t.Fatal("external merge stream has no Close method for early abandonment")
	}
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("early Close cleanup: entries=%v err=%v", entries, err)
	}
}
