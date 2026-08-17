//go:build !windows

package mapping

import (
	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// Shrink truncates the file to exactly newSize and establishes a
// replacement mapping of that extent, mirroring Rust mapping.rs
// shrink_or_retain: unmap first, ftruncate, then remap, fail-closed on
// remap failure exactly like replace_map (map=nil, size=0 -> WrongState).
// It refuses closed or read-only mappings, non-page-aligned requests, and
// any request above the current physical extent (a shrink must never
// extend the file; a file shorter than the requested extent is the Corrupt
// class, FormatInvalid). Same-size requests are a no-op. On success the
// tracked physical extent becomes the truncated size, so later Grow/Remap
// and writer committed-selection observe the real locked extent.
func (m *Mapping) Shrink(newSize uint64) error {
	if m.data == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "mapping closed"}
	}
	if m.prot&unix.PROT_WRITE == 0 {
		return &format.Error{Code: format.CodeWrongState, Detail: "mapping is read-only"}
	}
	if newSize%format.PageSize != 0 {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "new size not page-aligned"}
	}
	// Re-stat the locked file for the physical extent (Rust
	// shrink_file_or_retain): refusing when the file is shorter than the
	// requested extent keeps a shrink from ever extending the file.
	st, err := m.file.Stat()
	if err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
	}
	physical := uint64(st.Size())
	if physical < newSize {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "main file is shorter than its committed generation"}
	}
	if m.size == newSize && physical == newSize {
		return nil
	}
	// Nil the mapping before truncating so the file can shrink while the
	// old extent is unmapped; a partial failure leaves the Mapping
	// fail-closed with size zero and every later access reports
	// WrongState (Rust shrink_or_retain + replace_map).
	old := m.data
	m.data = nil
	var truncateErr error
	if physical != newSize {
		if err := unix.Ftruncate(int(m.file.Fd()), int64(newSize)); err != nil {
			truncateErr = &format.Error{Code: format.CodeIO, Detail: "ftruncate: " + err.Error()}
		}
	}
	// Re-establish the mapping at the requested extent. The pre-stat
	// proved the file still covers newSize even when the truncate failed,
	// so the remap attempt is safe (Rust replaces the map regardless and
	// combines the failure). On remap failure the Mapping is fail-closed:
	// the old mapping is torn down and every later access reports
	// WrongState.
	data, err := remapPages(m.file, old, m.size, newSize, m.prot)
	if err != nil {
		if data != nil {
			unix.Munmap(data)
		}
		m.size = 0
		if truncateErr != nil {
			return truncateErr
		}
		return err
	}
	m.data = data
	m.size = newSize
	if truncateErr == nil {
		m.physical = newSize
	}
	work.MappingRemap(1)
	return truncateErr
}
