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

// referenceBatchSlotSize and referenceBatchEntryLimit are the Rust
// immutable reference-batch shape constants
// (immutable_output/reference_batch.rs: Slot{id: u32, count: i64} is 16
// bytes; ENTRY_LIMIT is 1024), the same constants the algebra output
// charges with.
const (
	referenceBatchSlotSize   = 16
	referenceBatchEntryLimit = 1024
)

// chargeReferenceBatch sizes and charges one reference batch against the
// snapshot heap exactly like Rust ReferenceBatch::new: the entry capacity
// is the floor power of two of the affordable slot pairs (two 16-byte
// slots per entry), capped at 1024; a heap that cannot fit one entry
// disables the batch with no charge. The charged bytes are deducted from
// the remaining heap.
func chargeReferenceBatch(heap *uint64) int {
	affordable := *heap / (2 * referenceBatchSlotSize)
	if affordable > referenceBatchEntryLimit {
		affordable = referenceBatchEntryLimit
	}
	entries := floorPowerOfTwo(int(affordable))
	if entries == 0 {
		return 0
	}
	*heap -= uint64(entries) * 2 * referenceBatchSlotSize
	return entries
}

// floorPowerOfTwo returns the largest power of two at or below value,
// or 0 for a non-positive value (Rust floor_power_of_two with its
// explicit zero guard: a heap that cannot fit one entry disables the
// batch with no charge).
func floorPowerOfTwo(value int) int {
	if value <= 0 {
		return 0
	}
	power := 1
	for power <= value>>1 {
		power <<= 1
	}
	return power
}

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
		words, err := snapshotWords(source, range_.Membership)
		if err != nil {
			return err
		}
		if err := builder.PushMembershipV4(range_.From, range_.To, words); err != nil {
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
		words, err := snapshotWords(source, range_.Membership)
		if err != nil {
			return err
		}
		if err := builder.PushMembershipV6(range_.FromHi, range_.FromLo, range_.ToHi, range_.ToLo, words); err != nil {
			return err
		}
	}
}

// copyStructuredV4 copies the IPv4 network_enrichment_v1 ranges (Rust
// copy_structured_v4): the payload and the optional threat membership are
// decoded from the visited view and pushed with the structure batch.
func copyStructuredV4(source *reader.ImmutableReader, builder *writer.OutputBuilder, check func() error) error {
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
		words, err := threatWords(range_.Value)
		if err != nil {
			return err
		}
		if err := builder.PushNetworkEnrichmentV1V4(range_.From, range_.To, value, words); err != nil {
			return err
		}
	}
}

// copyStructuredV6 copies the IPv6 network_enrichment_v1 ranges (Rust
// copy_structured_v6).
func copyStructuredV6(source *reader.ImmutableReader, builder *writer.OutputBuilder, check func() error) error {
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
		words, err := threatWords(range_.Value)
		if err != nil {
			return err
		}
		if err := builder.PushNetworkEnrichmentV1V6(range_.FromHi, range_.FromLo, range_.ToHi, range_.ToLo, value, words); err != nil {
			return err
		}
	}
}

// threatWords resolves one view's optional threat membership into the
// writer's concrete word type (Rust copy_structured_v4/v6 match on
// threat_membership): an absent membership passes nil and the writer
// interns the payload without a bitmap.
func threatWords(view reader.NetworkEnrichmentV1View) (writer.OutputWords, error) {
	view2, err := view.ThreatMembership()
	if err != nil {
		return nil, err
	}
	if view2.ID() == 0 {
		return nil, nil
	}
	return membershipWords(view2)
}

// membershipWords materializes one checked membership view (Rust
// SnapshotWords + MembershipWords::read_words): the writer's concrete
// OutputWords contract requires one caller-owned copy per bitmap, and the
// exact-count verify proves the bitmap length survived the read
// (Corrupt "membership length changed while copying").
func membershipWords(view reader.MembershipView) (writer.OutputWords, error) {
	wordCount := view.WordCount()
	words := make([]uint64, int(wordCount))
	copied, err := view.ReadWords(0, words)
	if err != nil {
		return nil, err
	}
	if copied != len(words) {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "membership length changed while copying"}
	}
	return writer.OutputWords(words), nil
}

// snapshotWords resolves one membership dictionary ID to the checked
// bitmap view and materializes it (Rust builder.push_membership_v4/6 over
// reader.membership).
func snapshotWords(source *reader.ImmutableReader, id uint32) (writer.OutputWords, error) {
	view, err := source.LookupMembershipID(id)
	if err != nil {
		return nil, err
	}
	return membershipWords(view)
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
	return builder.WriteMetadata(input, budget.MaxHeapBytes-length-1)
}
