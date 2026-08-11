// Package reader implements the immutable reader core: one owner of healthy
// selected-generation access over the file-backed mapping. It performs no
// content I/O, holds no complete page in heap memory, and exposes only
// bounded scalar values and logical handles that re-derive checked mapped
// views on demand.
package reader

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// MetaSelection reports how the selected meta was derived (section 4.2).
type MetaSelection uint8

const (
	MetaSelectionProvenCurrent MetaSelection = iota
	MetaSelectionSoleMeta0
	MetaSelectionSoleMeta1
)

// ImmutableReader is the opened immutable database. It is not safe for
// concurrent use by multiple goroutines; callers that share a database must
// guard it (or open one per worker). The underlying file is shared-locked for
// the reader lifetime.
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

// sidecarAbsentUnderLock runs after the shared lifetime lock is held and
// refuses the immutable open when the canonical external sidecar exists.
// Absence is the only accepted answer: an unreadable sidecar path is a
// refused open, not a silently ignored error.
func sidecarAbsentUnderLock(clean string) error {
	_, err := os.Stat(sidecarPath(clean))
	switch {
	case err == nil:
		return &format.Error{Code: format.CodeLiveCoordinationUnsupported, Detail: "external sidecar present; immutable open of a live database is refused"}
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
	if ok0 && ok1 && isUnsupportedKind(m0.StructureKind) {
		return &format.Error{Code: format.CodeUnsupportedStructure, Detail: "unsupported structure kind"}
	}
	e0 := validateMeta(m0, ok0)
	e1 := validateMeta(m1, ok1)
	valid0 := ok0 && e0 == nil
	valid1 := ok1 && e1 == nil
	if err := r.selectBetween(p0, p1, m0, m1, valid0, valid1, e0, e1); err != nil {
		return err
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
	unsupported := func() error {
		return &format.Error{Code: format.CodeUnsupportedStructure, Detail: "unsupported structure kind"}
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
	if unsupportedKindError(e0) || unsupportedKindError(e1) {
		return unsupported()
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

func isUnsupportedKind(kind uint8) bool {
	return kind != 0 && kind != format.StructureKindNetworkEnrichmentV1
}

func validateMeta(m format.Meta, ok bool) error {
	if !ok {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "meta not identity-readable"}
	}
	if err := m.ValidateKindInvariants(); err != nil {
		if format.UnsupportedKind(err) {
			return &format.Error{Code: format.CodeUnsupportedStructure, Detail: err.Error()}
		}
		return &format.Error{Code: format.CodeFormatInvalid, Detail: err.Error()}
	}
	return nil
}

func unsupportedKindError(err error) bool {
	if err == nil {
		return false
	}
	if ferr, ok := err.(*format.Error); ok {
		return ferr.Code == format.CodeUnsupportedStructure
	}
	return false
}

// Close releases the mapping and the shared lifetime lock.
func (r *ImmutableReader) Close() error { return r.m.Close() }

// Meta returns the selected committed meta page.
func (r *ImmutableReader) Meta() format.Meta { return r.meta }

// Selection returns how the selected meta was derived.
func (r *ImmutableReader) Selection() MetaSelection { return r.selection }

// page returns a checked full-page view after validating that the page is a
// non-meta page below the selected committed count.
func (r *ImmutableReader) page(pgno uint32) ([]byte, error) {
	if !format.PageNumberValid(pgno, r.meta.PageCount) {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "page number out of range"}
	}
	return r.m.Page(pgno)
}
