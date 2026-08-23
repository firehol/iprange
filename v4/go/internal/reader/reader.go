// Package reader implements the immutable reader core: one owner of healthy
// selected-generation access over the file-backed mapping. It performs no
// content I/O, holds no complete page in heap memory, and exposes only
// bounded scalar values and checked handles whose records are validated and
// decoded during the owning lookup (membership and structure), with mapped
// reads performed on demand inside the operation.
package reader

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// MetaSelection reports how the selected meta was derived (section 4.2).
// It aliases the shared bootstrap authority so the reader and the writer
// can never diverge on selection semantics.
type MetaSelection = bootstrap.Selection

const (
	MetaSelectionProvenCurrent = bootstrap.SelectionProvenCurrent
	MetaSelectionSoleMeta0     = bootstrap.SelectionSoleMeta0
	MetaSelectionSoleMeta1     = bootstrap.SelectionSoleMeta1
)

// ImmutableReader is the opened immutable database. Its state (mapping,
// committed meta, selection) is write-once at open; direct lookups read
// mapped bytes, membership records are validated and decoded during lookup
// with word reads slicing the retained checked bitmap, and structured
// lookups decode during lookup with the scalar retained in a lightweight
// view (no implicit semantic validation, per the normal-operation rule).
// Lookups and independent scans are therefore safe for concurrent use by
// multiple goroutines without a per-call lock (design-iprange-engine.md);
// Close must never race reader work.
type ImmutableReader struct {
	m         *mapping.Mapping
	meta      format.Meta
	selection MetaSelection
}

// OpenImmutable opens path as an immutable v4 database with the exact
// bootstrap rules of sections 3 and 4.
func OpenImmutable(path string) (*ImmutableReader, error) {
	if err := namespaceChecks(path); err != nil {
		return nil, err
	}
	// Pre-open sidecar refusal before the main file is even stat'ed,
	// mirroring Rust open_immutable (require_sidecar_absent before
	// open_read_only): a live database whose main file is
	// missing/renamed but whose .readers sidecar remains must refuse
	// with the WrongState class, not an IO stat failure. The
	// under-lock sidecar check inside the mapping open stays
	// authoritative.
	if err := sidecarAbsentUnderLock(filepath.Clean(path)); err != nil {
		return nil, err
	}
	m, err := mapping.OpenImmutable(path, sidecarAbsentUnderLock)
	if err != nil {
		if ferr, ok := err.(*format.Error); ok {
			return nil, ferr
		}
		return nil, &format.Error{Code: format.CodeIO, Detail: err.Error()}
	}
	r := &ImmutableReader{m: m}
	if err := r.bootstrap(); err != nil {
		m.Close()
		return nil, err
	}
	// Bootstrap proved the meta pair on the 2-page mapping; now grow to
	// the exact committed extent. For immutable readers committed ==
	// physical, so the remap covers the whole file — but a huge corrupt
	// file never got mapped because bootstrap ran on 2 pages only.
	if err := m.Remap(r.meta.PageCount * format.PageSize); err != nil {
		m.Close()
		return nil, err
	}
	// Post-remap identity and sidecar recheck, mirroring Rust
	// open_immutable (verify_path_any_link + require_sidecar_absent
	// after map_reader): the path must still name the same inode and
	// no live sidecar may have appeared during the remap window.
	if err := m.VerifyIdentity(path); err != nil {
		m.Close()
		return nil, err
	}
	if err := sidecarAbsentUnderLock(filepath.Clean(path)); err != nil {
		m.Close()
		return nil, err
	}
	return r, nil
}

// sidecarAbsentUnderLock refuses the immutable open when the canonical
// external sidecar exists. It runs both before the main file is touched
// (Rust's pre-open require_sidecar_absent error class) and again after the
// shared lifetime lock is held (the authoritative re-check).
// Absence is the only accepted answer: an unreadable sidecar path is a
// refused open, not a silently ignored error. The check is symlink-aware
// (os.Lstat), mirroring Rust's fs::symlink_metadata: a dangling .readers
// symlink still exists as a namespace entry and therefore refuses the open.
// A present sidecar is the WrongState class (Rust WrongMode maps to code
// 11), not a coordination error.
func sidecarAbsentUnderLock(clean string) error {
	_, err := os.Lstat(sidecarPath(clean))
	switch {
	case err == nil:
		return &format.Error{Code: format.CodeWrongState, Detail: "external sidecar present; immutable open of a live database is refused"}
	case os.IsNotExist(err):
		return nil
	default:
		return &format.Error{Code: format.CodeIO, Detail: "sidecar stat: " + err.Error()}
	}
}

// sidecarPath returns the canonical external sidecar component: the accepted
// main basename plus lowercase ".readers".
func sidecarPath(clean string) string {
	return filepath.Join(filepath.Dir(clean), filepath.Base(clean)+format.CoordinationSuffix)
}

// namespaceChecks applies the section-3 basename rules and the sidecar
// component-limit rule before opening; the authoritative absence check runs
// again under the lifetime lock (sidecarAbsentUnderLock).
func namespaceChecks(path string) error {
	clean := filepath.Clean(path)
	base := filepath.Base(clean)
	if base == "." || base == string(filepath.Separator) {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "no basename"}
	}
	if strings.IndexByte(base, 0) >= 0 {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "basename contains NUL"}
	}
	// The reserved matches are byte-wise ASCII-case-insensitive (Rust
	// eq_ignore_ascii_case, path.rs validate_posix_name); Unicode folding
	// is not applied, so spellings Rust accepts are accepted here too.
	if format.AsciiFoldHasPrefix(base, format.ReservedBasenamePrefix) ||
		format.AsciiFoldHasSuffix(base, format.CoordinationSuffix) {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "reserved basename"}
	}
	// The canonical sidecar (main + ".readers") must fit the target
	// filesystem component limit (POSIX NAME_MAX).
	if len(base)+len(format.CoordinationSuffix) > 255 {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "canonical sidecar name exceeds component limit"}
	}
	return nil
}

// bootstrap implements the O(1) two-meta selection of section 4.2 through
// the shared bootstrap authority (internal/bootstrap), so the reader and
// writer share one selection implementation. Every bootstrap failure is
// the typed FormatInvalid error (the conformance corpus requires
// FormatInvalid for the wrong-magic, short, and unaligned mutations); only
// an unknown structure kind reports the typed UnsupportedStructure.
func (r *ImmutableReader) bootstrap() error {
	return r.bootstrapMode(bootstrap.ModeImmutableReader)
}

// bootstrapMode is the mode-parameterized selection (Rust
// bootstrap_mapping) over the tracked open physical extent: the immutable
// reader requires the exact committed physical length, the live reader
// requires a proven current generation and tolerates the writer's
// unpublished tail.
func (r *ImmutableReader) bootstrapMode(mode bootstrap.Mode) error {
	return r.bootstrapModeWith(mode, r.m.PhysicalSize())
}

// bootstrapModeWith runs the selection over an explicit physical extent
// (Rust open_meta_pages + finish_open). The live reader re-samples the
// file size under the gate and re-selects through this entry point; the
// open-time stat is never reused for the registered-generation selection.
func (r *ImmutableReader) bootstrapModeWith(mode bootstrap.Mode, physical uint64) error {
	p0, err := r.m.Page(0)
	if err != nil {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: err.Error()}
	}
	p1, err := r.m.Page(1)
	if err != nil {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: err.Error()}
	}
	res, err := bootstrap.Open(p0, p1, physical, mode)
	if err != nil {
		return err
	}
	r.meta = res.Meta
	r.selection = res.Selection
	return nil
}

// SelectRegisteredGeneration re-runs the live-mode selection with a
// freshly sampled physical extent (Rust select_registered_generation):
// the mapping-open stat is never reused; the fresh stat is sampled under
// the exclusive reader-table gate, so it observes the writer's latest
// published extent. The mapping itself is not resized here; the caller
// remaps to the newly selected committed bytes (Rust register).
func (r *ImmutableReader) SelectRegisteredGeneration(physical uint64) error {
	return r.bootstrapModeWith(bootstrap.ModeLiveReader, physical)
}

// OpenLiveMapped builds the logical reader core over an already-open live
// mapping (Rust database_file::map_reader with OpenMode::LiveReader): the
// mapping is bootstrapped in ModeLiveReader over a freshly sampled
// physical extent and remapped to the selected committed extent, giving
// the caller the database identity needed before the sidecar is opened.
// The fresh stat matches Rust bootstrap_file's file.metadata().len() at
// the bootstrap moment: the open-time stat may already be stale because
// the writer commits without holding the shared lifetime lock. The
// gate-side fresh-stat re-selection runs later through
// SelectRegisteredGeneration. On error the mapping is left open and owned
// by the caller, which runs the live open unwind.
func OpenLiveMapped(m *mapping.Mapping) (*ImmutableReader, error) {
	physical, err := m.FileSize()
	if err != nil {
		return nil, err
	}
	r := &ImmutableReader{m: m}
	if err := r.bootstrapModeWith(bootstrap.ModeLiveReader, physical); err != nil {
		return nil, err
	}
	if err := m.Remap(r.meta.PageCount * format.PageSize); err != nil {
		return nil, err
	}
	return r, nil
}

// Close releases the mapping and the shared lifetime lock.
func (r *ImmutableReader) Close() error { return r.m.Close() }

// Unmap releases the mapping without the descriptor or the lifetime lock
// (Rust Mapping::unmap); the live reader close drives the exact unmap
// step itself.
func (r *ImmutableReader) Unmap() error { return r.m.Unmap() }

// Meta returns the selected committed meta page.
func (r *ImmutableReader) Meta() format.Meta { return r.meta }

// Selection returns how the selected meta was derived.
func (r *ImmutableReader) Selection() MetaSelection { return r.selection }

// page returns a checked full-page view after validating that the page is a
// non-meta page below the selected committed count. Every reader hot path
// visits a page through this owner and decodes exactly one page header
// (OpenSlottedHeader never re-decodes), so the page-visit and page-parse
// counters move together; the tests pin both.
func (r *ImmutableReader) page(pgno uint32) ([]byte, error) {
	if !format.PageNumberValid(pgno, r.meta.PageCount) {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "page number out of range"}
	}
	work.PageVisit(1)
	work.PageParse(1)
	return r.m.Page(pgno)
}
