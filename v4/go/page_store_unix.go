//go:build unix

package iprangedb

import "golang.org/x/sys/unix"

func (s *mmapPageStore) remap(fd uintptr, newSize int64) error {
	newData, err := unix.Mmap(int(fd), 0, int(newSize), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return errf("Io", "mmap remap: "+err.Error())
	}
	// Unmap the old mapping.
	_ = unix.Munmap(s.data)
	s.data = newData
	s.committedPages = uint32(newSize / int64(pageSize))
	return nil
}

func (s *mmapPageStore) close() {
	if s.closed {
		return
	}
	s.closed = true
	if s.data != nil {
		_ = unix.Munmap(s.data)
		s.data = nil
	}
}
