// Exact global-name algebra over pinned membership scopes (Rust
// membership_query/algebra.rs parity). MembershipAlgebra resolves
// same-named feeds across every source scope into one sorted global
// catalog; Count and Compare run one ordered N-way event sweep per
// selection with the Rust corruption classes, budget labels, and
// 4096-unit cancellation cadence. Feed names are validated and copied
// at this package boundary; the core stays zero-alloc per result.

package iprangedb

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/reader"
)

// MembershipAlgebraBudget bounds one pinned algebra (Rust
// MembershipAlgebraBudget: max_heap_bytes, max_sources).
type MembershipAlgebraBudget struct {
	MaxHeapBytes uint64
	MaxSources   uint32
}

// AlgebraCountReport is the exact union cardinality of one selection
// (Rust AlgebraCountReport).
type AlgebraCountReport = reader.AlgebraCountReport

// AlgebraComparisonReport is the exact comparison of two selections
// (Rust AlgebraComparisonReport).
type AlgebraComparisonReport = reader.AlgebraComparisonReport

// FeedSelection selects the global feeds of one algebra. Construct with
// AlgebraFeedSelectionAll or AlgebraFeedSelectionNamed.
type FeedSelection struct {
	kind  feedSelectionKind
	names []string
}

// feedSelectionKind is the unexported FeedSelection discriminant.
type feedSelectionKind uint8

const (
	feedSelectionAll feedSelectionKind = iota
	feedSelectionNamed
)

// AlgebraFeedSelectionAll selects every global feed (Rust
// FeedSelection::All).
func AlgebraFeedSelectionAll() FeedSelection {
	return FeedSelection{kind: feedSelectionAll}
}

// AlgebraFeedSelectionNamed selects exactly the named global feeds
// (Rust FeedSelection::Named). The names are copied at construction, so
// later caller mutation is harmless. Invalid names report
// ErrorNameInvalid at the operation; missing names report
// ErrorNameNotFound; empty and duplicate lists are refused by the
// algebra core with the Rust messages.
func AlgebraFeedSelectionNamed(names []string) FeedSelection {
	copied := make([]string, len(names))
	copy(copied, names)
	return FeedSelection{kind: feedSelectionNamed, names: copied}
}

// MembershipAlgebra is the pinned, reusable virtual catalog over one or
// more membership databases (Rust MembershipAlgebra). Every source scope
// keeps its reader pinned for the algebra's lifetime; operations refuse
// a closed reader with the standard "reader closed" shape.
type MembershipAlgebra struct {
	inner  *reader.MembershipAlgebra
	scopes []*MembershipScope
}

// NewMembershipAlgebra resolves same-named feeds across every supplied
// scope into one global identity (Rust MembershipAlgebra::new). The
// source-count rules and the modeled source-heap admission charge run
// first, then the state construction (family agreement, global catalog
// with sort/dedup, per-source local-to-global maps) charges the same
// bytes as Rust.
func NewMembershipAlgebra(scopes []*MembershipScope, budget MembershipAlgebraBudget, cancellation *CancellationToken) (*MembershipAlgebra, error) {
	sources := make([]reader.AlgebraSource, len(scopes))
	for i, sc := range scopes {
		if err := sc.r.checkOpen(); err != nil {
			return nil, err
		}
		sources[i] = reader.AlgebraSource{Reader: sc.r.inner, Scope: sc.data}
	}
	inner, err := reader.NewMembershipAlgebra(sources, reader.MembershipAlgebraBudget{
		MaxHeapBytes: budget.MaxHeapBytes,
		MaxSources:   budget.MaxSources,
	}, cancellation.check)
	if err != nil {
		return nil, publicError(err)
	}
	return &MembershipAlgebra{inner: inner, scopes: scopes}, nil
}

// AddressFamily returns the family shared by every source.
func (a *MembershipAlgebra) AddressFamily() AddressFamily {
	return AddressFamily(a.inner.AddressFamily())
}

// FeedCount returns the unique global feed name count (Rust
// MembershipAlgebra::feed_count).
func (a *MembershipAlgebra) FeedCount() int {
	return a.inner.FeedCount()
}

// Feeds returns the sorted global catalog in ascending global position
// order (Rust MembershipAlgebra::feeds; the Index is the unique global
// catalog position, not a stored feed index). The returned slice is
// owned by the caller; each name string is a copy of the catalog entry
// name (the root boundary never hands out views that alias the
// mapping), so Feeds allocates once per entry.
func (a *MembershipAlgebra) Feeds() []FeedEntry {
	state := a.inner.State()
	entries := state.Names()
	output := make([]FeedEntry, len(entries))
	for i, entry := range entries {
		output[i] = FeedEntry{Index: entry.FeedIndex, Name: string(entry.Name)}
	}
	return output
}

// openOK refuses any closed source reader (Rust GenerationReader pins
// its mapping; Go readers close eagerly, so each operation re-checks the
// public closed state first).
func (a *MembershipAlgebra) openOK() error {
	for _, sc := range a.scopes {
		if err := sc.r.checkOpen(); err != nil {
			return err
		}
	}
	return nil
}

// selection converts one public selection into the reader selection,
// validating every named feed at the boundary (Rust FeedName::new
// parity: invalid names fail before resolution).
func (a *MembershipAlgebra) selection(feeds FeedSelection) (reader.FeedSelection, error) {
	switch feeds.kind {
	case feedSelectionAll:
		return reader.AlgebraFeedSelectionAll(), nil
	case feedSelectionNamed:
		names := make([]string, len(feeds.names))
		for i, name := range feeds.names {
			if !format.FeedNameValidString(name) {
				return reader.FeedSelection{}, &Error{Code: ErrorNameInvalid, Detail: "invalid feed name"}
			}
			names[i] = name
		}
		return reader.AlgebraFeedSelectionNamed(names), nil
	default:
		return reader.FeedSelection{}, &Error{Code: ErrorInvalidArgument, Detail: "unknown membership algebra feed selection"}
	}
}

// Count computes the exact address union of one selection in one
// ordered pass (Rust MembershipAlgebra::count).
func (a *MembershipAlgebra) Count(feeds FeedSelection, cancellation *CancellationToken) (AlgebraCountReport, error) {
	if err := a.openOK(); err != nil {
		return AlgebraCountReport{}, err
	}
	selection, err := a.selection(feeds)
	if err != nil {
		return AlgebraCountReport{}, err
	}
	report, err := a.inner.Count(selection, cancellation.check)
	if err != nil {
		return AlgebraCountReport{}, publicError(err)
	}
	return report, nil
}

// Compare computes the exact comparison of two selections in one
// ordered pass (Rust MembershipAlgebra::compare): the four-case overlap
// cardinalities and the exact equality of the two selected address
// sets.
func (a *MembershipAlgebra) Compare(left, right FeedSelection, cancellation *CancellationToken) (AlgebraComparisonReport, error) {
	if err := a.openOK(); err != nil {
		return AlgebraComparisonReport{}, err
	}
	leftSelection, err := a.selection(left)
	if err != nil {
		return AlgebraComparisonReport{}, err
	}
	rightSelection, err := a.selection(right)
	if err != nil {
		return AlgebraComparisonReport{}, err
	}
	report, err := a.inner.Compare(leftSelection, rightSelection, cancellation.check)
	if err != nil {
		return AlgebraComparisonReport{}, publicError(err)
	}
	return report, nil
}
