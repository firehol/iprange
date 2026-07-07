package iprangedb

// pageStore is the page-level storage abstraction for the v4 writer.
//
// Two implementations:
//   - vecPageStore: wraps a []byte (current in-memory behavior). Used by tests
//     and the pure-API path (createWriter, openImage from a buffer).
//   - mmapPageStore: wraps a read-only mmap + dirty-page map. Used by the
//     file-backed writer to avoid loading the whole file into heap memory.
type pageStore interface {
	// page reads a page. Checks the dirty map first (hit → return dirty data),
	// then falls back to the committed source (mmap or Vec).
	page(pgno uint32) []byte

	// writePageMut returns a mutable reference to a page's storage.
	writePageMut(pgno uint32) []byte

	// allocPage extends the store by one page (called only when the Writer's free list
	// is empty). Returns the new page number.
	allocPage() uint32

	// totalPages returns the total logical pages in the store.
	totalPages() uint64

	// committedBytes returns the committed bytes as a contiguous slice.
	committedBytes() []byte

	// pageData returns the bytes for a specific dirty page.
	// Used by the OS layer at commit time to obtain page data for pwrite.
	pageData(pgno uint32) []byte

	// clearDirty clears all dirty pages. Called after a successful commit.
	clearDirty()

	// remap remaps the mmap to a new size (mmapPageStore only). vecPageStore no-ops.
	remap(fd uintptr, newSize int64) error

	// close releases resources (mmap munmap for mmapPageStore). vecPageStore no-ops.
	// Must be idempotent.
	close()
}

// vecPageStore is an in-memory page store backed by a []byte.
type vecPageStore struct {
	image []byte
}

func newVecPageStore(image []byte) *vecPageStore {
	return &vecPageStore{image: image}
}

func (s *vecPageStore) page(pgno uint32) []byte {
	base := int(pgno) * pageSize
	return s.image[base : base+pageSize]
}

func (s *vecPageStore) writePageMut(pgno uint32) []byte {
	base := int(pgno) * pageSize
	return s.image[base : base+pageSize]
}

func (s *vecPageStore) allocPage() uint32 {
	p := uint32(len(s.image) / pageSize)
	// Use a stack-local zero page instead of make([]byte, pageSize) — avoids a
	// temporary heap allocation per page (profiled as contributing to memmove overhead).
	var zero [pageSize]byte
	s.image = append(s.image, zero[:]...)
	return p
}

func (s *vecPageStore) totalPages() uint64 {
	return uint64(len(s.image) / pageSize)
}

func (s *vecPageStore) committedBytes() []byte {
	return s.image
}

func (s *vecPageStore) pageData(pgno uint32) []byte {
	base := int(pgno) * pageSize
	return s.image[base : base+pageSize]
}

func (s *vecPageStore) clearDirty() {}

func (s *vecPageStore) remap(_ uintptr, _ int64) error { return nil }

func (s *vecPageStore) close() {}

// mmapPageStore is an mmap-backed page store. Reads committed pages from a read-only mmap;
// stores dirty/new pages in a private map[uint32][]byte.
type mmapPageStore struct {
	data           []byte
	committedPages uint32
	logicalPages   uint32
	dirty          map[uint32][]byte
	pool           [][]byte
	closed         bool
}

func newMmapPageStore(data []byte, committedPages uint32) *mmapPageStore {
	return &mmapPageStore{
		data:           data,
		committedPages: committedPages,
		logicalPages:   committedPages,
		dirty:          make(map[uint32][]byte),
	}
}

// zeroPage is a read-only shared buffer returned for pages beyond the committed range.
// Callers must NOT write through the returned slice.
var zeroPage [pageSize]byte

func (s *mmapPageStore) page(pgno uint32) []byte {
	// Fast path: no pages dirty this txn → skip the map lookup entirely
	// (common in append-only txns that only read committed pages during descent).
	if len(s.dirty) > 0 {
		if buf, ok := s.dirty[pgno]; ok {
			return buf
		}
	}
	// Fall back to mmap for committed pages (nil after close — return zero page).
	if pgno < s.committedPages && s.data != nil {
		base := int(pgno) * pageSize
		return s.data[base : base+pageSize]
	}
	// Pages allocated but not yet written this txn, pages beyond the
	// committed file size, or after close — return a static zero page.
	return zeroPage[:]
}

func (s *mmapPageStore) writePageMut(pgno uint32) []byte {
	buf, ok := s.dirty[pgno]
	if !ok {
		// Try to pop a recycled buffer from the pool first.
		if n := len(s.pool); n > 0 {
			buf = s.pool[n-1]
			s.pool = s.pool[:n-1]
		} else {
			buf = make([]byte, pageSize)
		}
		s.dirty[pgno] = buf
	}
	return buf
}

func (s *mmapPageStore) allocPage() uint32 {
	p := s.logicalPages
	// Saturating add: prevent u32 wrap-around at the theoretical 2^32-page limit.
	// The Writer checks total_pages >= 2^32 before calling, but guard defensively.
	if s.logicalPages != maxUint32 {
		s.logicalPages++
	}
	return p
}

func (s *mmapPageStore) totalPages() uint64 {
	return uint64(s.logicalPages)
}

func (s *mmapPageStore) committedBytes() []byte {
	return s.data[:int(s.committedPages)*pageSize]
}

func (s *mmapPageStore) pageData(pgno uint32) []byte {
	buf, ok := s.dirty[pgno]
	if !ok {
		panic("mmapPageStore.pageData: pgnot found in dirty map — OS layer must only call this for dirty pages")
	}
	return buf
}

func (s *mmapPageStore) clearDirty() {
	txnDirtyCount := len(s.dirty)
	// Move all dirty buffers into the recycled pool.
	for _, buf := range s.dirty {
		s.pool = append(s.pool, buf)
	}
	// Clear the dirty map.
	s.dirty = make(map[uint32][]byte)
	// Trim the pool if it is more than 2x larger than the current txn's
	// dirty page count. Nil the trimmed entries so their backing arrays
	// can be GC'd.
	if len(s.pool) > txnDirtyCount*2 {
		for i := txnDirtyCount; i < len(s.pool); i++ {
			s.pool[i] = nil
		}
		s.pool = s.pool[:txnDirtyCount]
	}
}

// remap and close are implemented in page_store_unix.go (unix build tag).
// On non-unix platforms, mmapPageStore is not used (the os.go file has the unix build tag).
