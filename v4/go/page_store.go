package iprangedb

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// pageStore is the page-level storage abstraction. All methods are zero-alloc
// in the hot path — page storage lives in the mmap, not in heap buffers.
type pageStore interface {
	page(pgno uint32) []byte    // read a page
	pageMut(pgno uint32) []byte // mutable page (caller ensures COW discipline)
	copyPage(src, dst uint32)   // copy PAGE_SIZE bytes from src to dst
	allocPage() (uint32, error) // allocate a new page in the growth region
	totalPages() uint32         // committed + growth region
	committedPages() uint32     // stable prefix
	setCommittedPages(n uint32) // advance the committed boundary (at commit)
	committedBytes() []byte     // for Reader construction
	ensureCapacity(minPages uint32) error
	sync() error
	truncate(newTotalPages uint32) error // shrink the store (Rule 5)
}

// --- vecPageStore (tests / pure-API) ---

type vecPageStore struct {
	image     []byte
	committed uint32
}

func newVecPageStore(image []byte) *vecPageStore {
	cp := uint32(len(image) / PageSize)
	return &vecPageStore{image: image, committed: cp}
}

func (s *vecPageStore) page(pgno uint32) []byte {
	base := int(pgno) * PageSize
	return s.image[base : base+PageSize]
}

func (s *vecPageStore) pageMut(pgno uint32) []byte {
	base := int(pgno) * PageSize
	return s.image[base : base+PageSize]
}

func (s *vecPageStore) copyPage(src, dst uint32) {
	sb := int(src) * PageSize
	db := int(dst) * PageSize
	copy(s.image[db:db+PageSize], s.image[sb:sb+PageSize])
}

func (s *vecPageStore) allocPage() (uint32, error) {
	p := uint32(len(s.image) / PageSize)
	s.image = append(s.image, make([]byte, PageSize)...)
	return p, nil
}

func (s *vecPageStore) totalPages() uint32         { return uint32(len(s.image) / PageSize) }
func (s *vecPageStore) committedPages() uint32     { return s.committed }
func (s *vecPageStore) setCommittedPages(n uint32) { s.committed = n }
func (s *vecPageStore) committedBytes() []byte     { return s.image[:int(s.committed)*PageSize] }

func (s *vecPageStore) ensureCapacity(minPages uint32) error {
	needed := int(minPages) * PageSize
	if len(s.image) < needed {
		s.image = append(s.image, make([]byte, needed-len(s.image))...)
	}
	return nil
}

func (s *vecPageStore) sync() error { return nil }

func (s *vecPageStore) truncate(newTotalPages uint32) error {
	newLen := int(newTotalPages) * PageSize
	if newLen < len(s.image) {
		s.image = s.image[:newLen]
	}
	return nil
}

// --- mmapStore (writable MAP_SHARED, zero-heap) ---

type mmapStore struct {
	data        []byte // writable mmap
	file        *os.File
	committed   uint32
	logical     uint32
	growthChunk uint32
}

func newMmapStore(file *os.File, committedPages uint32) (*mmapStore, error) {
	growthChunk := uint32(64)
	mapPages := committedPages + growthChunk
	mapLen := int(mapPages) * PageSize

	// Extend the file to the mapping size.
	if err := file.Truncate(int64(mapLen)); err != nil {
		return nil, fmt.Errorf("mmap truncate: %w", err)
	}

	data, err := syscall.Mmap(int(file.Fd()), 0, mapLen,
		syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("mmap: %w", err)
	}

	return &mmapStore{
		data:        data,
		file:        file,
		committed:   committedPages,
		logical:     committedPages,
		growthChunk: growthChunk,
	}, nil
}

func (s *mmapStore) page(pgno uint32) []byte {
	base := int(pgno) * PageSize
	return s.data[base : base+PageSize]
}

func (s *mmapStore) pageMut(pgno uint32) []byte {
	base := int(pgno) * PageSize
	return s.data[base : base+PageSize]
}

func (s *mmapStore) copyPage(src, dst uint32) {
	sb := int(src) * PageSize
	db := int(dst) * PageSize
	copy(s.data[db:db+PageSize], s.data[sb:sb+PageSize])
}

func (s *mmapStore) allocPage() (uint32, error) {
	p := s.logical
	s.logical++
	needed := s.logical
	mapped := uint32(len(s.data) / PageSize)
	if needed > mapped {
		if err := s.remap(needed); err != nil {
			return 0, err
		}
	}
	return p, nil
}

func (s *mmapStore) remap(minPages uint32) error {
	// Unmap old.
	if err := syscall.Munmap(s.data); err != nil {
		return fmt.Errorf("munmap: %w", err)
	}
	// Grow the file.
	mapLen := int(minPages+s.growthChunk) * PageSize
	if err := s.file.Truncate(int64(mapLen)); err != nil {
		return fmt.Errorf("ftruncate: %w", err)
	}
	// New writable mapping.
	data, err := syscall.Mmap(int(s.file.Fd()), 0, mapLen,
		syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return fmt.Errorf("mmap remap: %w", err)
	}
	s.data = data
	if s.growthChunk < 1024 {
		s.growthChunk *= 2
	}
	return nil
}

func (s *mmapStore) totalPages() uint32         { return s.logical }
func (s *mmapStore) committedPages() uint32     { return s.committed }
func (s *mmapStore) setCommittedPages(n uint32) { s.committed = n }

func (s *mmapStore) committedBytes() []byte {
	return s.data[:int(s.committed)*PageSize]
}

func (s *mmapStore) ensureCapacity(minPages uint32) error {
	mapped := uint32(len(s.data) / PageSize)
	if minPages > mapped {
		return s.remap(minPages)
	}
	return nil
}

func (s *mmapStore) sync() error {
	// msync(MS_SYNC) flushes dirty pages to the page cache.
	_, _, errno := syscall.Syscall(syscall.SYS_MSYNC,
		uintptr(unsafe.Pointer(&s.data[0])),
		uintptr(len(s.data)),
		syscall.MS_SYNC)
	if errno != 0 {
		return fmt.Errorf("msync: %v", errno)
	}
	// fdatasync ensures the page cache reaches stable storage.
	// Without this, a system crash (not just process crash) can lose data.
	if s.file != nil {
		if err := s.file.Sync(); err != nil {
			return fmt.Errorf("fdatasync: %w", err)
		}
	}
	return nil
}

// truncate mirrors the Rust MmapStore::truncate: shrink the backing file to
// exactly newTotalPages pages, remap to that size (no growth over-allocation),
// and reset the growth chunk. Called only when no readers are active.
func (s *mmapStore) truncate(newTotalPages uint32) error {
	newLen := int(newTotalPages) * PageSize
	if newLen >= len(s.data) {
		return nil
	}
	if err := syscall.Munmap(s.data); err != nil {
		return fmt.Errorf("munmap: %w", err)
	}
	if err := s.file.Truncate(int64(newLen)); err != nil {
		return fmt.Errorf("ftruncate: %w", err)
	}
	data, err := syscall.Mmap(int(s.file.Fd()), 0, newLen,
		syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return fmt.Errorf("mmap truncate: %w", err)
	}
	s.data = data
	s.logical = newTotalPages
	if s.committed > newTotalPages {
		s.committed = newTotalPages
	}
	s.growthChunk = 64
	return nil
}

func (s *mmapStore) close() {
	if s.data != nil {
		syscall.Munmap(s.data)
		s.data = nil
	}
}
