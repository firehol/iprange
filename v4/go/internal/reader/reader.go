// Package reader implements the immutable reader core: one owner of healthy
// selected-generation access over the file-backed mapping. It performs no
// content I/O, holds no complete page in heap memory, and exposes only
// bounded scalar values and checked handles whose records are validated and
// decoded during the owning lookup (membership and structure), with mapped
// reads performed on demand inside the operation.
package reader

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// MetaSelection reports how the selected meta was derived (section 4.2).
type MetaSelection uint8

const (
	MetaSelectionProvenCurrent MetaSelection = iota
	MetaSelectionSoleMeta0
	MetaSelectionSoleMeta1
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
	return filepath.Join(filepath.Dir(clean), filepath.Base(clean)+".readers")
}

// namespaceChecks applies the section-3 basename rules and the sidecar
// component-limit rule before opening; the authoritative absence check runs
// again under the lifetime lock (sidecarAbsentUnderLock).
func namespaceChecks(path string) error {
	clean := filepath.Clean(path)
	base := filepath.Base(clean)
	if base == "." || base == string(filepath.Separator) {
		return &format.Error{Code: format.CodeNameInvalid, Detail: "no basename"}
	}
	lower := strings.ToLower(base)
	if strings.HasPrefix(lower, ".iprange-") || strings.HasSuffix(lower, ".readers") {
		return &format.Error{Code: format.CodeNameInvalid, Detail: "reserved basename"}
	}
	// The canonical sidecar (main + ".readers") must fit the target
	// filesystem component limit (POSIX NAME_MAX).
	if len(base)+len(".readers") > 255 {
		return &format.Error{Code: format.CodeNameInvalid, Detail: "canonical sidecar name exceeds component limit"}
	}
	return nil
}

// bootstrap implements the O(1) two-meta selection of section 4.2. Every
// bootstrap failure is the typed FormatInvalid error (the conformance corpus
// requires FormatInvalid for the wrong-magic, short, and unaligned
// mutations); only an unknown structure kind reports the typed
// UnsupportedStructure.
func (r *ImmutableReader) bootstrap() error {
	p0, err := r.m.Page(0)
	if err != nil {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: err.Error()}
	}
	p1, err := r.m.Page(1)
	if err != nil {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: err.Error()}
	}
	m0, ok0 := format.ParseIdentity(p0)
	m1, ok1 := format.ParseIdentity(p1)
	if ok0 && ok1 && !sameIdentity(m0, m1) {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "conflicting meta identity"}
	}
	physical := r.m.Size()
	e0 := validateMeta(m0, ok0, physical)
	e1 := validateMeta(m1, ok1, physical)
	valid0 := ok0 && e0 == nil
	valid1 := ok1 && e1 == nil
	if err := r.selectBetween(p0, p1, m0, m1, valid0, valid1, e0, e1); err != nil {
		return err
	}
	// An unknown structure kind on a structured file is reported only
	// after the meta pair selected (bootstrap.rs finish_open), never as a
	// validation failure.
	if r.meta.ValueKind == format.ValueKindStructured && r.meta.StructureKind != format.StructureKindNetworkEnrichmentV1 {
		return &format.Error{Code: format.CodeUnsupportedStructure, Detail: "unsupported structure kind"}
	}
	// Immutable open requires the exact physical size (section 3).
	if r.meta.PageCount*format.PageSize != r.m.Size() {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "file size does not match meta page count"}
	}
	return nil
}

func (r *ImmutableReader) selectBetween(p0, p1 []byte, m0, m1 format.Meta, valid0, valid1 bool, e0, e1 error) error {
	bootstrapFail := func(detail string) error {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: detail}
	}
	if valid0 && valid1 {
		switch {
		case m0.TxnID == m1.TxnID:
			if !bytes.Equal(p0[:256], p1[:256]) {
				return bootstrapFail("equal transactions with different meta images")
			}
			if m0.TxnID&1 == 0 {
				r.meta, r.selection = m0, MetaSelectionProvenCurrent
			} else {
				r.meta, r.selection = m1, MetaSelectionProvenCurrent
			}
			return nil
		case m0.TxnID == m1.TxnID+1:
			if m0.TxnID&1 != 0 {
				return bootstrapFail("swapped meta parity")
			}
			r.meta, r.selection = m0, MetaSelectionProvenCurrent
			return nil
		case m1.TxnID == m0.TxnID+1:
			if m1.TxnID&1 != 1 {
				return bootstrapFail("swapped meta parity")
			}
			r.meta, r.selection = m1, MetaSelectionProvenCurrent
			return nil
		default:
			return bootstrapFail("transaction gap between metas")
		}
	}
	if valid0 != valid1 {
		if valid0 {
			r.meta, r.selection = m0, MetaSelectionSoleMeta0
		} else {
			r.meta, r.selection = m1, MetaSelectionSoleMeta1
		}
		return nil
	}
	return bootstrapFail("no bootstrap-valid meta")
}

func sameIdentity(a, b format.Meta) bool {
	return a.AddressFamily == b.AddressFamily &&
		a.ValueKind == b.ValueKind &&
		a.StructureKind == b.StructureKind &&
		a.ValueTag == b.ValueTag &&
		a.DatabaseID == b.DatabaseID
}

func validateMeta(m format.Meta, ok bool, physical uint64) error {
	if !ok {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "meta not identity-readable"}
	}
	if err := m.ValidateKindInvariants(); err != nil {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: err.Error()}
	}
	if m.PageCount*format.PageSize > physical {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "meta page count exceeds physical size"}
	}
	return nil
}

// Close releases the mapping and the shared lifetime lock.
func (r *ImmutableReader) Close() error { return r.m.Close() }

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
