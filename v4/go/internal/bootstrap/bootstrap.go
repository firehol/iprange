// Package bootstrap implements the O(1) two-meta-page classification and
// selection of binary-format-v4.md section 4, mirroring the Rust bootstrap
// module. It is the single selection authority: the immutable reader and
// the live writer both open through Open, so a selection-rule change can
// never diverge between open modes. The package is pure (no I/O, no
// mappings): the mapping owner supplies the two page views and the locked
// physical extent; the caller applies the resulting committed generation.
package bootstrap

import (
	"bytes"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// Mode selects the open-mode finish rules (binary-format-v4.md section
// 4.2, Rust bootstrap.rs finish_open). Only the modes this milestone uses
// exist; the Rust LiveReader mode joins when the live-reader milestone
// needs it.
type Mode uint8

const (
	// ModeImmutableReader requires the exact committed physical length
	// and accepts a sole-meta selection when no pair is provable.
	ModeImmutableReader Mode = iota
	// ModeWriter requires a provable current generation (a sole meta can
	// never prove currency) and tolerates an unpublished physical tail.
	ModeWriter
)

// Selection reports how the selected meta was derived (section 4.2).
type Selection uint8

const (
	SelectionProvenCurrent Selection = iota
	SelectionSoleMeta0
	SelectionSoleMeta1
)

// Result is the selected committed generation, mirroring Rust Bootstrap.
type Result struct {
	Meta             format.Meta
	Selection        Selection
	SelectedMetaPage uint8
	CommittedBytes   uint64
	PhysicalBytes    uint64
}

// Open classifies both meta pages, validates each independently, selects
// the committed generation, and applies the mode finish rules (Rust
// open_meta_pages + finish_open). Every failure except an unknown
// structured-file kind is the FormatInvalid class; an unknown kind on a
// structured file reports UnsupportedStructure after pair selection (Rust
// finish_open), exactly like the reader's historical open.
func Open(p0, p1 []byte, physical uint64, mode Mode) (*Result, error) {
	if len(p0) != format.PageSize || len(p1) != format.PageSize {
		return nil, formatErr("meta page not a complete page")
	}
	if physical < 2*format.PageSize {
		return nil, formatErr("file smaller than two pages")
	}
	if physical%format.PageSize != 0 {
		return nil, formatErr("file size not page-aligned")
	}
	m0, ok0 := format.ParseIdentity(p0)
	m1, ok1 := format.ParseIdentity(p1)
	if ok0 && ok1 && !sameIdentity(m0, m1) {
		return nil, formatErr("conflicting meta identity")
	}
	e0 := validateMeta(m0, ok0, physical)
	e1 := validateMeta(m1, ok1, physical)
	meta, selection, page, err := selectBetween(p0, p1, m0, m1, ok0 && e0 == nil, ok1 && e1 == nil)
	if err != nil {
		return nil, err
	}
	// An unknown structure kind on a structured file is reported only
	// after the meta pair is selected (Rust finish_open), never as a
	// validation failure (the count/root checks still ran).
	if meta.ValueKind == format.ValueKindStructured && meta.StructureKind != format.StructureKindNetworkEnrichmentV1 {
		return nil, &format.Error{Code: format.CodeUnsupportedStructure, Detail: "unsupported structure kind"}
	}
	// A writer open must prove the current generation from the meta pair;
	// a sole meta cannot (Rust finish_open CurrentGenerationUnprovable).
	if mode == ModeWriter && selection != SelectionProvenCurrent {
		return nil, formatErr("current generation not provable")
	}
	committed := meta.PageCount * format.PageSize
	// The immutable reader requires the exact committed physical extent
	// (Rust finish_open ImmutableLengthMismatch); the writer may carry an
	// unpublished tail that the caller trims after open.
	if mode == ModeImmutableReader && committed != physical {
		return nil, formatErr("file size does not match meta page count")
	}
	return &Result{
		Meta:             meta,
		Selection:        selection,
		SelectedMetaPage: page,
		CommittedBytes:   committed,
		PhysicalBytes:    physical,
	}, nil
}

func formatErr(detail string) error {
	return &format.Error{Code: format.CodeFormatInvalid, Detail: detail}
}

// sameIdentity compares the static identity fields that must agree between
// the two meta pages of one database (Rust require_same_identity).
func sameIdentity(a, b format.Meta) bool {
	return a.AddressFamily == b.AddressFamily &&
		a.ValueKind == b.ValueKind &&
		a.StructureKind == b.StructureKind &&
		a.ValueTag == b.ValueTag &&
		a.DatabaseID == b.DatabaseID
}

// validateMeta applies the per-meta bootstrap-validity checks (Rust
// bootstrap_valid): generation identity, declared page count, physical
// geometry, roots, counts, metadata lengths, and the value-kind
// invariants, all on the decoded meta.
func validateMeta(m format.Meta, ok bool, physical uint64) error {
	if !ok {
		return formatErr("meta not identity-readable")
	}
	if err := m.ValidateKindInvariants(); err != nil {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: err.Error()}
	}
	if m.PageCount*format.PageSize > physical {
		return formatErr("meta page count exceeds physical size")
	}
	return nil
}

// selectBetween picks the committed generation from the two candidates
// (Rust select_candidates + select_pair): equal transactions require
// byte-identical images and pick the parity page; adjacent transactions
// require correct physical parity; otherwise the pair cannot prove the
// current generation.
func selectBetween(p0, p1 []byte, m0, m1 format.Meta, valid0, valid1 bool) (format.Meta, Selection, uint8, error) {
	fail := func(detail string) (format.Meta, Selection, uint8, error) {
		return format.Meta{}, 0, 0, formatErr(detail)
	}
	if valid0 && valid1 {
		switch {
		case m0.TxnID == m1.TxnID:
			if !bytes.Equal(p0[:256], p1[:256]) {
				return fail("equal transactions with different meta images")
			}
			return m0, SelectionProvenCurrent, uint8(m0.TxnID & 1), nil
		case m0.TxnID == m1.TxnID+1:
			if m0.TxnID&1 != 0 {
				return fail("swapped meta parity")
			}
			return m0, SelectionProvenCurrent, 0, nil
		case m1.TxnID == m0.TxnID+1:
			if m1.TxnID&1 != 1 {
				return fail("swapped meta parity")
			}
			return m1, SelectionProvenCurrent, 1, nil
		default:
			return fail("transaction gap between metas")
		}
	}
	if valid0 != valid1 {
		if valid0 {
			return m0, SelectionSoleMeta0, 0, nil
		}
		return m1, SelectionSoleMeta1, 1, nil
	}
	return fail("no bootstrap-valid meta")
}
