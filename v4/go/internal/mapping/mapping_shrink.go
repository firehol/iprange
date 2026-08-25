package mapping

import (
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
// class, FormatInvalid). Same-size requests are a no-op. No lower bound
// is enforced below the two-page meta extent: the committed generation is
// never smaller than two pages (meta validation refuses PageCount < 2), so
// a smaller request is caller misuse, and Rust shrink_or_retain has no such
// check either (accepted parity, recorded). On success the
// tracked physical extent becomes the truncated size, so later Grow/Remap
// and writer committed-selection observe the real locked extent.
func (m *Mapping) Shrink(newSize uint64) error {
	if m.data == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "mapping closed"}
	}
	if m.prot&protWrite == 0 {
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
	// Rust shrink_or_retain drops the old map before changing the
	// extent: unmap first, then ftruncate, then re-establish (unix
	// keeps no old view for an mremap because the authority unmaps;
	// Windows forbids truncation while a view is mapped with
	// ERROR_USER_MAPPED_FILE). A partial failure leaves the Mapping
	// fail-closed with size zero and every later access reports
	// WrongState.
	if err := munmapShared(m.data); err != nil {
		m.size = 0
		return err
	}
	m.data = nil
	var truncateErr error
	if physical != newSize {
		if err := truncateFile(m.file, int64(newSize)); err != nil {
			truncateErr = err
		}
	}
	// Re-establish the mapping at the requested extent. The pre-stat
	// proved the file still covers newSize even when the truncate failed,
	// so the remap attempt is safe (Rust replaces the map regardless and
	// combines the failure). On remap failure the Mapping is fail-closed:
	// every later access reports WrongState.
	data, err := remapPages(m.file, nil, m.size, newSize, m.prot)
	if err != nil {
		if data != nil {
			munmapShared(data)
		}
		m.size = 0
		// Both-failure reporting (Rust combines the two errors via
		// combine_errors): the truncate error is the more specific one and
		// the remap error is dropped; the observable state is identical
		// either way (fail-closed, size zero).
		if truncateErr != nil {
			return truncateErr
		}
		return err
	}
	m.data = data
	m.size = newSize
	if truncateErr != nil {
		// Rust shrink_file_or_retain retains the physical extent when
		// another process's mapped view prevents truncation: the
		// retention is a success, the file keeps its length, and the
		// mapping covers the committed extent.
		if isMappedViewRetained(truncateErr) {
			work.MappingRemap(1)
			return nil
		}
		return truncateErr
	}
	m.physical = newSize
	work.MappingRemap(1)
	return nil
}
