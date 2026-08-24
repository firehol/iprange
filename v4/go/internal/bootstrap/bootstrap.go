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
	"errors"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// Mode selects the open-mode finish rules (binary-format-v4.md section
// 4.2, Rust bootstrap.rs finish_open).
type Mode uint8

const (
	// ModeImmutableReader requires the exact committed physical length
	// and accepts a sole-meta selection when no pair is provable.
	ModeImmutableReader Mode = iota
	// ModeWriter requires a provable current generation (a sole meta can
	// never prove currency) and tolerates an unpublished physical tail.
	ModeWriter
	// ModeLiveReader has the same finish rules as ModeWriter: a live
	// reader pins one committed generation of a live pair whose writer
	// may carry an unpublished tail, and a sole meta can never prove
	// currency (Rust OpenMode::LiveReader).
	ModeLiveReader
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
	result, err := openMetaPages(p0, p1, physical, mode)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// OpenMeta validates and selects one committed meta pair and returns
// the selected meta by value (Rust open_meta_pages). The value core
// keeps inspect paths allocation-free; the reader-facing Open keeps
// the pointer result for its own surface. Both share one selection
// authority.
func OpenMeta(p0, p1 []byte, physical uint64, mode Mode) (format.Meta, error) {
	result, err := openMetaPages(p0, p1, physical, mode)
	if err != nil {
		return format.Meta{}, err
	}
	return result.Meta, nil
}

// openMetaPages is the value-returning selection core of Open and
// OpenMeta (Rust open_meta_pages + finish_open; every failure except
// an unknown structured-file kind is the FormatInvalid class; an
// unknown kind on a structured file reports UnsupportedStructure
// after pair selection, exactly like the reader's historical open).
func openMetaPages(p0, p1 []byte, physical uint64, mode Mode) (Result, error) {
	if len(p0) != format.PageSize || len(p1) != format.PageSize {
		return Result{}, problemErr(ProblemFileTooShort, "meta page not a complete page")
	}
	if physical < 2*format.PageSize {
		return Result{}, problemErr(ProblemFileTooShort, "file smaller than two pages")
	}
	if physical%format.PageSize != 0 {
		return Result{}, problemErr(ProblemFileUnaligned, "file size not page-aligned")
	}
	m0, ok0 := format.ParseIdentity(p0)
	m1, ok1 := format.ParseIdentity(p1)
	if ok0 && ok1 && !sameIdentity(m0, m1) {
		return Result{}, problemErr(ProblemStaticIdentityMismatch, "conflicting meta identity")
	}
	e0 := validateMeta(m0, ok0, physical)
	e1 := validateMeta(m1, ok1, physical)
	meta, selection, page, err := selectBetween(p0, p1, m0, m1, ok0 && e0 == nil, ok1 && e1 == nil)
	if err != nil {
		if err == errNoBootstrapMeta {
			return Result{}, &ProblemError{
				Format:            formatErr("no bootstrap-valid meta"),
				Kind:              ProblemNoBootstrapMeta,
				Meta0MagicInvalid: metaMagicInvalid(p0),
				Meta1MagicInvalid: metaMagicInvalid(p1),
			}
		}
		return Result{}, err
	}
	// An unknown structure kind on a structured file is reported only
	// after the meta pair is selected (Rust finish_open), never as a
	// validation failure (the count/root checks still ran).
	if meta.ValueKind == format.ValueKindStructured && meta.StructureKind != format.StructureKindNetworkEnrichmentV1 {
		return Result{}, &ProblemError{
			Format:            &format.Error{Code: format.CodeUnsupportedStructure, Detail: "unsupported structure kind"},
			Kind:              ProblemUnsupportedStructure,
			StructureKindCode: meta.StructureKind,
		}
	}
	// A writer or live-reader open must prove the current generation from
	// the meta pair; a sole meta cannot (Rust finish_open
	// CurrentGenerationUnprovable applies to every mode except the
	// immutable reader).
	if mode != ModeImmutableReader && selection != SelectionProvenCurrent {
		return Result{}, problemErr(ProblemCurrentGenerationUnprovable, "current generation not provable")
	}
	committed := meta.PageCount * format.PageSize
	// The immutable reader requires the exact committed physical extent
	// (Rust finish_open ImmutableLengthMismatch); the writer and the live
	// reader may carry an unpublished tail (the live reader remaps to the
	// committed bytes only).
	if mode == ModeImmutableReader && committed != physical {
		return Result{}, problemErr(ProblemImmutableLengthMismatch, "file size does not match meta page count")
	}
	return Result{
		Meta:             meta,
		Selection:        selection,
		SelectedMetaPage: page,
		CommittedBytes:   committed,
		PhysicalBytes:    physical,
	}, nil
}

func formatErr(detail string) *format.Error {
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
// errNoBootstrapMeta is the sentinel of the both-invalid selection
// arm; openMetaPages upgrades it to the classified ProblemError with
// the per-page magic split.
var errNoBootstrapMeta = errors.New("no bootstrap-valid meta")

func selectBetween(p0, p1 []byte, m0, m1 format.Meta, valid0, valid1 bool) (format.Meta, Selection, uint8, error) {
	fail := func(kind ProblemKind, detail string) (format.Meta, Selection, uint8, error) {
		return format.Meta{}, 0, 0, problemErr(kind, detail)
	}
	if valid0 && valid1 {
		switch {
		case m0.TxnID == m1.TxnID:
			if !bytes.Equal(p0[:256], p1[:256]) {
				return fail(ProblemEqualTransactionDisagreement, "equal transactions with different meta images")
			}
			return m0, SelectionProvenCurrent, uint8(m0.TxnID & 1), nil
		case m0.TxnID == m1.TxnID+1:
			if m0.TxnID&1 != 0 {
				return fail(ProblemPhysicalParity, "swapped meta parity")
			}
			return m0, SelectionProvenCurrent, 0, nil
		case m1.TxnID == m0.TxnID+1:
			if m1.TxnID&1 != 1 {
				return fail(ProblemPhysicalParity, "swapped meta parity")
			}
			return m1, SelectionProvenCurrent, 1, nil
		default:
			return fail(ProblemTransactionGap, "transaction gap between metas")
		}
	}
	if valid0 != valid1 {
		if valid0 {
			return m0, SelectionSoleMeta0, 0, nil
		}
		return m1, SelectionSoleMeta1, 1, nil
	}
	return format.Meta{}, 0, 0, errNoBootstrapMeta
}

// DatabaseIDFromPages returns the bound database identity of one raw
// meta pair (Rust database_id_from_meta_pages): both identity-readable
// pages must agree on the full static identity (Rust
// require_same_identity over static_identity_eq: family, value kind,
// structure kind, value tag, and database id), then either page's
// database id binds. A pair with no identity-readable page is the
// NoBootstrapMeta class. The committed-generation rules do not run
// here: this is the "bound" id used by the live validation
// registration when the committed generation cannot be selected.
func DatabaseIDFromPages(p0, p1 []byte) ([16]byte, error) {
	var zero [16]byte
	m0, ok0 := format.ParseIdentity(p0)
	m1, ok1 := format.ParseIdentity(p1)
	if ok0 && ok1 && !sameIdentity(m0, m1) {
		return zero, problemErr(ProblemStaticIdentityMismatch, "conflicting meta identity")
	}
	if ok0 {
		return m0.DatabaseID, nil
	}
	if ok1 {
		return m1.DatabaseID, nil
	}
	return zero, problemErr(ProblemNoBootstrapMeta, "no identity-readable meta page")
}
