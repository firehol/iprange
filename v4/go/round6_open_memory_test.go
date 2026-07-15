//go:build unix

package iprangedb

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func round6SparseEmptyFile(t *testing.T, pages uint64) string {
	t.Helper()
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	image, ok := w.IntoImage()
	if !ok {
		t.Fatal("missing empty image")
	}
	for p := 0; p < 2; p++ {
		metaPage := image[p*PageSize : (p+1)*PageSize]
		putU64(metaPage, MetaTotalPages, pages)
		finalizeChecksum(metaPage)
	}
	path := filepath.Join(t.TempDir(), "sparse.iprdb")
	if err := os.WriteFile(path, image, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, int64(pages)*PageSize); err != nil {
		t.Fatal(err)
	}
	return path
}

func round6OpenFileAllocatedBytes(t *testing.T, path string) uint64 {
	t.Helper()
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	w, err := OpenFile[Ipv4Key](path)
	if err != nil {
		t.Fatal(err)
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return after.TotalAlloc - before.TotalAlloc
}

func TestRound6FileWriterOpenHeapDoesNotScaleWithCommittedFileSize(t *testing.T) {
	const largePages = 16384 // 64 MiB sparse committed image.
	small := round6OpenFileAllocatedBytes(t, round6SparseEmptyFile(t, 2))
	large := round6OpenFileAllocatedBytes(t, round6SparseEmptyFile(t, largePages))
	const tolerance = 32 << 10
	if large > small+tolerance {
		t.Fatalf("file-backed writable open allocates with committed file size: small=%d large=%d", small, large)
	}
}

type round6VirtualPageStore struct {
	image []byte
	pages uint32
}

func (s *round6VirtualPageStore) page(pgno uint32) []byte {
	base := int(pgno) * PageSize
	return s.image[base : base+PageSize]
}
func (s *round6VirtualPageStore) pageMut(uint32) []byte       { panic("unexpected mutation") }
func (s *round6VirtualPageStore) copyPage(uint32, uint32)     { panic("unexpected copy") }
func (s *round6VirtualPageStore) allocPage() (uint32, error)  { panic("unexpected allocation") }
func (s *round6VirtualPageStore) totalPages() uint32          { return s.pages }
func (s *round6VirtualPageStore) committedPages() uint32      { return s.pages }
func (s *round6VirtualPageStore) setCommittedPages(uint32)    { panic("unexpected commit") }
func (s *round6VirtualPageStore) committedBytes() []byte      { return s.image }
func (s *round6VirtualPageStore) ensureCapacity(uint32) error { panic("unexpected growth") }
func (s *round6VirtualPageStore) sync() error                 { panic("unexpected sync") }
func (s *round6VirtualPageStore) truncate(uint32) error       { panic("unexpected truncate") }

func round6MalformedCoreOpenAllocatedBytes(t *testing.T, pages uint32) (uint64, error) {
	t.Helper()
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	image, ok := w.IntoImage()
	if !ok {
		t.Fatal("missing empty image")
	}
	for p := 0; p < 2; p++ {
		metaPage := image[p*PageSize : (p+1)*PageSize]
		putU64(metaPage, MetaTotalPages, uint64(pages))
		finalizeChecksum(metaPage)
	}
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	opened, err := openWriter[Ipv4Key](&round6VirtualPageStore{image: image, pages: pages})
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(opened)
	return after.TotalAlloc - before.TotalAlloc, err
}

func TestRound6CoreWriterRejectsShortStoreWithoutPageCountSizedAllocation(t *testing.T) {
	const largePages = 8 << 20 // A 32 GiB logical database.
	allocated, err := round6MalformedCoreOpenAllocatedBytes(t, largePages)
	const tolerance = 4 << 20
	if allocated > tolerance {
		t.Fatalf("core writable open reserved heap from untrusted page count before rejecting it: allocated=%d", allocated)
	}
	if err == nil {
		t.Fatal("core writable open accepted metadata whose committed image is shorter than total_pages")
	}
}

func round6TreeWithFreeList(t *testing.T, records uint32) []byte {
	t.Helper()
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < records; i++ {
		ip := Ipv4Key(i * 2)
		if err := w.Append(ip, ip, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Delete(0, 0); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(2, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	image, ok := w.IntoImage()
	if !ok {
		t.Fatal("missing committed tree image")
	}
	_, m := round5ActiveMetaPage(t, image)
	if m.freeListHead == 0 {
		t.Fatal("fixture did not persist a free list")
	}
	return image
}

func round6CoreImageOpenAllocatedBytes(t *testing.T, image []byte) uint64 {
	t.Helper()
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	w, err := openWriter[Ipv4Key](newVecPageStore(image))
	if err != nil {
		t.Fatal(err)
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(w)
	return after.TotalAlloc - before.TotalAlloc
}

func TestRound6FreeListValidationHeapDoesNotScaleWithLiveTreePages(t *testing.T) {
	smallImage := round6TreeWithFreeList(t, 1000)
	largeImage := round6TreeWithFreeList(t, 500000)
	small := round6CoreImageOpenAllocatedBytes(t, smallImage)
	large := round6CoreImageOpenAllocatedBytes(t, largeImage)
	const tolerance = 16 << 10
	if large > small+tolerance {
		t.Fatalf("free-list validation allocates per reachable tree page: small=%d large=%d", small, large)
	}
}
