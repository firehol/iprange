// Bounded logical copy into one private immutable output (Rust
// snapshot/build.rs): the feed catalog first for membership and
// structured kinds, then the family sweep over the direct, membership, or
// structured ranges, then the heap-budgeted metadata copy. Every item
// checks cancellation, and every membership bitmap is copied through the
// checked reader view with an exact-count verify so a generation that
// changes mid-copy fails instead of publishing a torn snapshot.

package snapshot

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/reader"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// copyInto runs the whole logical copy (Rust copy_into + copy_logical):
// feeds, the family sweep, then metadata.
func copyInto(source *reader.ImmutableReader, builder *writer.OutputBuilder, budget *Budget, check func() error) error {
	meta := source.Meta()
	if meta.ValueKind == format.ValueKindMembership || meta.ValueKind == format.ValueKindStructured {
		if err := copyFeeds(source, builder, check); err != nil {
			return err
		}
	}
	var err error
	switch {
	case meta.AddressFamily == format.AddressFamilyIPv4 && meta.ValueKind == format.ValueKindDirect:
		err = copyDirectV4(source, builder, check)
	case meta.AddressFamily == format.AddressFamilyIPv6 && meta.ValueKind == format.ValueKindDirect:
		err = copyDirectV6(source, builder, check)
	case meta.AddressFamily == format.AddressFamilyIPv4 && meta.ValueKind == format.ValueKindMembership:
		err = copyMembershipV4(source, builder, check)
	case meta.AddressFamily == format.AddressFamilyIPv6 && meta.ValueKind == format.ValueKindMembership:
		err = copyMembershipV6(source, builder, check)
	case meta.AddressFamily == format.AddressFamilyIPv4 && meta.ValueKind == format.ValueKindStructured:
		err = copyStructuredV4(source, builder, check)
	case meta.AddressFamily == format.AddressFamilyIPv6 && meta.ValueKind == format.ValueKindStructured:
		err = copyStructuredV6(source, builder, check)
	default:
		// Bootstrap rejects every other family/kind combination, so the
		// dispatch above is total; the guard keeps the copy honest.
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "snapshot source combination is not addressable"}
	}
	if err != nil {
		return err
	}
	return copyMetadata(source, builder, budget, check)
}

// copyFeeds copies the feed catalog (Rust copy_feeds): every catalog
// entry is pushed through the output builder's own dictionary.
func copyFeeds(source *reader.ImmutableReader, builder *writer.OutputBuilder, check func() error) error {
	cursor, err := source.NewFeedCursor()
	if err != nil {
		return err
	}
	for {
		if err := checkCancellation(check); err != nil {
			return err
		}
		feed, ok, err := cursor.Next()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if err := builder.PushFeed(string(feed.Name), feed.FeedIndex); err != nil {
			return err
		}
	}
}

// copyDirectV4 copies the IPv4 direct ranges (Rust copy_direct_v4).
func copyDirectV4(source *reader.ImmutableReader, builder *writer.OutputBuilder, check func() error) error {
	cursor, err := source.NewDirectCursor4(reader.RangeForward)
	if err != nil {
		return err
	}
	for {
		if err := checkCancellation(check); err != nil {
			return err
		}
		range_, ok, err := cursor.Next()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if err := builder.PushDirectV4(range_.From, range_.To, range_.Value); err != nil {
			return err
		}
	}
}

// copyDirectV6 copies the IPv6 direct ranges (Rust copy_direct_v6).
func copyDirectV6(source *reader.ImmutableReader, builder *writer.OutputBuilder, check func() error) error {
	cursor, err := source.NewDirectCursor6(reader.RangeForward)
	if err != nil {
		return err
	}
	for {
		if err := checkCancellation(check); err != nil {
			return err
		}
		range_, ok, err := cursor.Next()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if err := builder.PushDirectV6(range_.FromHi, range_.FromLo, range_.ToHi, range_.ToLo, range_.Value); err != nil {
			return err
		}
	}
}

// copyMembershipV4 copies the IPv4 membership ranges (Rust
// copy_membership_v4): each range's dictionary ID resolves to a checked
// bitmap view, materialized through the concrete OutputWords type the
// writer requires.
func copyMembershipV4(source *reader.ImmutableReader, builder *writer.OutputBuilder, check func() error) error {
	cursor, err := source.NewMembershipRangeCursor4()
	if err != nil {
		return err
	}
	var words viewWords
	for {
		if err := checkCancellation(check); err != nil {
			return err
		}
		range_, ok, err := cursor.Next()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		view, err := source.LookupMembershipID(range_.Membership)
		if err != nil {
			return err
		}
		words.view = view
		if err := builder.PushMembershipV4Words(range_.From, range_.To, &words); err != nil {
			return err
		}
	}
}

// copyMembershipV6 copies the IPv6 membership ranges (Rust
// copy_membership_v6).
func copyMembershipV6(source *reader.ImmutableReader, builder *writer.OutputBuilder, check func() error) error {
	cursor, err := source.NewMembershipRangeCursor6()
	if err != nil {
		return err
	}
	var words viewWords
	for {
		if err := checkCancellation(check); err != nil {
			return err
		}
		range_, ok, err := cursor.Next()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		view, err := source.LookupMembershipID(range_.Membership)
		if err != nil {
			return err
		}
		words.view = view
		if err := builder.PushMembershipV6Words(range_.FromHi, range_.FromLo, range_.ToHi, range_.ToLo, &words); err != nil {
			return err
		}
	}
}

// copyStructuredV4 copies the IPv4 network_enrichment_v1 ranges (Rust
// copy_structured_v4): the payload and the optional threat membership are
// decoded from the visited view and pushed with the structure batch.
func copyStructuredV4(source *reader.ImmutableReader, builder *writer.OutputBuilder, check func() error) error {
	var words viewWords
	cursor, err := source.NewNetworkEnrichmentV1Cursor4(reader.RangeForward)
	if err != nil {
		return err
	}
	for {
		if err := checkCancellation(check); err != nil {
			return err
		}
		range_, ok, err := cursor.Next()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		value, err := range_.Value.Value()
		if err != nil {
			return err
		}
		membership, err := threatWords(range_.Value, &words)
		if err != nil {
			return err
		}
		if err := builder.PushNetworkEnrichmentV1V4Words(range_.From, range_.To, value, membership); err != nil {
			return err
		}
	}
}

// copyStructuredV6 copies the IPv6 network_enrichment_v1 ranges (Rust
// copy_structured_v6).
func copyStructuredV6(source *reader.ImmutableReader, builder *writer.OutputBuilder, check func() error) error {
	var words viewWords
	cursor, err := source.NewNetworkEnrichmentV1Cursor6(reader.RangeForward)
	if err != nil {
		return err
	}
	for {
		if err := checkCancellation(check); err != nil {
			return err
		}
		range_, ok, err := cursor.Next()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		value, err := range_.Value.Value()
		if err != nil {
			return err
		}
		membership, err := threatWords(range_.Value, &words)
		if err != nil {
			return err
		}
		if err := builder.PushNetworkEnrichmentV1V6Words(range_.FromHi, range_.FromLo, range_.ToHi, range_.ToLo, value, membership); err != nil {
			return err
		}
	}
}

// threatWords resolves one view's optional threat membership into the
// writer's word source (Rust copy_structured_v4/v6 match on
// threat_membership): an absent membership passes nil and the writer
// interns the payload without a bitmap.
func threatWords(view reader.NetworkEnrichmentV1View, words *viewWords) (writer.MembershipWordSource, error) {
	view2, err := view.ThreatMembership()
	if err != nil {
		return nil, err
	}
	if view2.ID() == 0 {
		return nil, nil
	}
	words.view = view2
	return words, nil
}

// viewWords adapts one checked membership view to the writer's
// by-value word-source seam (Rust SnapshotWords + MembershipWords::
// read_words): every chunk is a copy of the mapped words, so snapshot
// copies never materialize an owned bitmap per record and never hand
// the writer a mapped view.
type viewWords struct {
	view reader.MembershipView
}

func (w viewWords) WordCount() uint32 { return w.view.WordCount() }

// ReadChunk copies the up-to-64 words starting at start (the seam chunk
// read; the exact-count verify proves the bitmap length survived the
// read, Corrupt "membership length changed while copying").
func (w viewWords) ReadChunk(start uint32) ([64]uint64, uint32, error) {
	var words [64]uint64
	wordCount := w.view.WordCount()
	if start > wordCount {
		return words, 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "membership length changed while copying"}
	}
	remaining := wordCount - start
	count := uint32(64)
	if remaining < count {
		count = remaining
	}
	copied, err := w.view.ReadWords(start, words[:count])
	if err != nil {
		return words, 0, err
	}
	if uint32(copied) != count {
		return words, 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "membership length changed while copying"}
	}
	return words, count, nil
}

// checkCancellation runs one per-item checkpoint; a nil checkpoint never
// cancels.
func checkCancellation(check func() error) error {
	if check == nil {
		return nil
	}
	return check()
}

// copyMetadata copies the uncompressed metadata under the remaining heap
// budget (Rust copy_metadata): the declared length must fit the budget,
// the chain read must deliver exactly that length, and the compression
// workspace gets the rest.
func copyMetadata(source *reader.ImmutableReader, builder *writer.OutputBuilder, budget *Budget, check func() error) error {
	length, ok := source.MetadataJSONLen()
	if !ok {
		return nil
	}
	if err := checkCancellation(check); err != nil {
		return err
	}
	// The reader's overflow probe allocates length+1 owned bytes, so the
	// honest charge is the probe, not the declared chain length
	// (reader/metadata.go ReadMetadataJSON; Rust has no +1 allocation,
	// Go's charge mirrors its own allocation).
	if length+1 > budget.MaxHeapBytes {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "snapshot metadata input heap"}
	}
	input, present, err := source.ReadMetadataJSON()
	if err != nil {
		return err
	}
	// The reader inflated the declared chain; the exact-count verify
	// mirrors Rust's SizedChain read-file check (Corrupt when the chain
	// no longer matches the meta-declared length).
	if !present || uint64(len(input)) != length {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "metadata length changed while copying"}
	}
	if err := checkCancellation(check); err != nil {
		return err
	}
	return builder.WriteMetadataWithBudget(input, budget.MaxHeapBytes-length-1)
}
