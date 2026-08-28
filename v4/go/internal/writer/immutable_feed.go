// One-inode unordered immutable single-feed publication (Rust
// immutable_feed.rs + immutable_output/unordered.rs): one fresh
// membership output is normalized from an unordered address-range
// source inside the private attempt inode. The normalized tree lives
// in the high page range of the same inode (the workspace), the
// ordered normalized ranges stream into the append-only output pages,
// and the finished output publishes through the reservation machine.
// Production code is mmap-only: the workspace and the output builder
// alias one mapping each, and no complete page ever exists in owned
// memory.

package writer

import (
	"fmt"
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/tree"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// ImmutableFeedBudget bounds one private single-feed construction
// (Rust ImmutableFeedBudget): the maximum simultaneous retained heap
// bytes, the maximum output page count, the maximum workspace page
// count, and the maximum simultaneously open files.
type ImmutableFeedBudget struct {
	MaxHeapBytes      uint64
	MaxOutputPages    uint64
	MaxWorkspacePages uint64
	MaxOpenFiles      uint32
}

// ImmutableFeedReport is the exact semantic work completed before
// immutable publication (Rust ImmutableFeedReport): the input records
// drained from the source, the normalized interval count of the
// published feed, and the exact 129-bit address count.
type ImmutableFeedReport struct {
	InputRecordCount        uint64
	NormalizedIntervalCount uint64
	Addresses               format.Cardinality129
}

// FeedRange4 is one caller-owned IPv4 source record of the immutable
// feed source seam (Rust AddressRange<Ipv4Key> over the borrowed
// batch).
type FeedRange4 struct {
	From uint32
	To   uint32
}

// FeedRange6 is one caller-owned IPv6 source record of the immutable
// feed source seam (Rust AddressRange<Ipv6Key> over the borrowed
// batch).
type FeedRange6 struct {
	FromHi uint64
	FromLo uint64
	ToHi   uint64
	ToLo   uint64
}

// ImmutableFeedPreparedBudget is the validated construction budget
// (Rust unordered::PreparedBudget): the operation heap (charged by the
// caller at builder construction), the output page budget, and the
// total physical page count of the attempt inode.
type ImmutableFeedPreparedBudget struct {
	MaxHeapBytes   uint64
	MaxOutputPages uint64
	TotalPages     uint64
}

// PrepareImmutableFeedBudget validates and folds one caller budget
// (Rust unordered::prepare_budget): at least two output pages and
// three open files, and the output plus workspace page count within
// the v4 page space.
func PrepareImmutableFeedBudget(input ImmutableFeedBudget) (ImmutableFeedPreparedBudget, error) {
	if input.MaxOutputPages < 2 {
		return ImmutableFeedPreparedBudget{}, budgetExceeded("immutable feed output pages")
	}
	if input.MaxOpenFiles < 3 {
		return ImmutableFeedPreparedBudget{}, budgetExceeded("immutable feed open files")
	}
	total := input.MaxOutputPages + input.MaxWorkspacePages
	if total < input.MaxOutputPages {
		return ImmutableFeedPreparedBudget{}, &format.Error{Code: format.CodePageSpaceExhausted, Detail: "immutable feed page space"}
	}
	if total > format.MaxPageCount {
		return ImmutableFeedPreparedBudget{}, &format.Error{Code: format.CodePageSpaceExhausted, Detail: "immutable feed page space"}
	}
	return ImmutableFeedPreparedBudget{
		MaxHeapBytes:   input.MaxHeapBytes,
		MaxOutputPages: input.MaxOutputPages,
		TotalPages:     total,
	}, nil
}

// normalizedFeed is the normalized membership tree of one immutable
// feed build (Rust unordered::Normalized): the workspace tree root,
// its record count, and the drained input record count.
type normalizedFeed[K any] struct {
	root             uint32
	recordCount      uint64
	inputRecordCount uint64
}

// BuildImmutableFeedV4 builds one IPv4 feed into the attempt inode
// (Rust unordered::build mapped by the caller's secured private
// attempt): the output builder already maps the full extent, the
// workspace mapping aliases the same inode at the high page range, the
// drain normalizes into the workspace tree, the cursor streams the
// ordered normalized ranges into the output, and Finish seals and
// shrinks. The workspace mapping is dropped before Finish exactly like
// the Rust drop(workspace). MaximumHeapBytes is the remaining operation
// heap after the caller charged the reference batch (Rust
// heap.remaining()).
func BuildImmutableFeedV4(builder *OutputBuilder, attemptFile *os.File, spec OutputSpec, prepared ImmutableFeedPreparedBudget, feedName string, metadataJSON []byte, nextBatch func() ([]FeedRange4, error), check func() error) (ImmutableFeedReport, error) {
	var zero ImmutableFeedReport
	workspaceMapping, err := mapping.MapFile(attemptFile, prepared.TotalPages*format.PageSize, true)
	if err != nil {
		return zero, err
	}
	workspace, err := newImmutableWorkspace(workspaceMapping, prepared.MaxOutputPages, prepared.TotalPages, spec.TxnID)
	if err != nil {
		workspaceMapping.Close()
		return zero, err
	}
	report, err := buildImmutableFeedMapped4(workspace, builder, spec, feedName, metadataJSON, prepared.MaxHeapBytes, nextBatch, check)
	closeErr := workspaceMapping.Close()
	if err != nil {
		return zero, attachFeedError(err, closeErr)
	}
	if closeErr != nil {
		return zero, closeErr
	}
	if err := builder.Finish(); err != nil {
		return zero, err
	}
	return report, nil
}

// BuildImmutableFeedV6 is the IPv6 form of BuildImmutableFeedV4.
func BuildImmutableFeedV6(builder *OutputBuilder, attemptFile *os.File, spec OutputSpec, prepared ImmutableFeedPreparedBudget, feedName string, metadataJSON []byte, nextBatch func() ([]FeedRange6, error), check func() error) (ImmutableFeedReport, error) {
	var zero ImmutableFeedReport
	workspaceMapping, err := mapping.MapFile(attemptFile, prepared.TotalPages*format.PageSize, true)
	if err != nil {
		return zero, err
	}
	workspace, err := newImmutableWorkspace(workspaceMapping, prepared.MaxOutputPages, prepared.TotalPages, spec.TxnID)
	if err != nil {
		workspaceMapping.Close()
		return zero, err
	}
	report, err := buildImmutableFeedMapped6(workspace, builder, spec, feedName, metadataJSON, prepared.MaxHeapBytes, nextBatch, check)
	closeErr := workspaceMapping.Close()
	if err != nil {
		return zero, attachFeedError(err, closeErr)
	}
	if closeErr != nil {
		return zero, closeErr
	}
	if err := builder.Finish(); err != nil {
		return zero, err
	}
	return report, nil
}

// attachFeedError attaches a cleanup-side close error to the primary
// cause (the snapshot attachClose shape: the primary stays the
// errors.As/Is/Unwrap target with the secondary present in the message
// only).
func attachFeedError(primary, closeErr error) error {
	if closeErr == nil {
		return primary
	}
	if primary == nil {
		return closeErr
	}
	return fmt.Errorf("%w; %v", primary, closeErr)
}

// runCheck runs the cancellation checkpoint hook (nil means
// uncancellable).
func runCheck(check func() error) error {
	if check == nil {
		return nil
	}
	return check()
}

// buildImmutableFeedMapped4 runs the normalize and ordered write of
// one IPv4 feed over the workspace store (Rust build_mapped).
func buildImmutableFeedMapped4(workspace *immutableWorkspace, builder *OutputBuilder, spec OutputSpec, feedName string, metadataJSON []byte, maxHeapBytes uint64, nextBatch func() ([]FeedRange4, error), check func() error) (ImmutableFeedReport, error) {
	var zero ImmutableFeedReport
	normalized, err := normalizeFeed4(workspace, nextBatch, maxHeapBytes, check)
	if err != nil {
		return zero, err
	}
	addresses, err := writeNormalized4(workspace, builder, spec, feedName, metadataJSON, maxHeapBytes, normalized, check)
	if err != nil {
		return zero, err
	}
	return ImmutableFeedReport{
		InputRecordCount:        normalized.inputRecordCount,
		NormalizedIntervalCount: normalized.recordCount,
		Addresses:               addresses,
	}, nil
}

// normalizeFeed4 drains the source into the workspace coverage tree
// (Rust normalize): every range enters the untracked coverage union
// input, and the ordered-prefix or general union path lands in the
// workspace tree. The drain mirrors Rust source::drain: one empty
// batch is refused, cancellation is checked per batch and per 4096
// records, and the source and range work counters tick once per
// drain and per record.
func normalizeFeed4(workspace *immutableWorkspace, nextBatch func() ([]FeedRange4, error), maxHeapBytes uint64, check func() error) (normalizedFeed[key4], error) {
	codec := rangeCodec4{}
	input := newUnionInput(codec, format.AddressFamilyIPv4, format.ValueKindMembership, maxHeapBytes)
	var root uint32
	var recordCount uint64
	var inputCount uint64
	var scratch [3][format.RangeRecordV6Size]byte
	ctx := &rangeCtx[key4]{family: codec, store: workspace, storeView: workspace, root: &root, count: &recordCount, scratch: &scratch}
	work.SourcePass(1)
	work.InputSourcePass(1)
	for {
		if err := runCheck(check); err != nil {
			return normalizedFeed[key4]{}, err
		}
		batch, err := nextBatch()
		if err != nil {
			return normalizedFeed[key4]{}, err
		}
		if batch == nil {
			break
		}
		if len(batch) == 0 {
			return normalizedFeed[key4]{}, invalid("range source returned an empty batch")
		}
		for chunkIndex := 0; chunkIndex*4096 < len(batch); chunkIndex++ {
			if chunkIndex != 0 {
				if err := runCheck(check); err != nil {
					return normalizedFeed[key4]{}, err
				}
			}
			lo := chunkIndex * 4096
			hi := lo + 4096
			if hi > len(batch) {
				hi = len(batch)
			}
			for _, record := range batch[lo:hi] {
				next := inputCount + 1
				if next == 0 {
					return normalizedFeed[key4]{}, overflow("workflow input record count")
				}
				if _, err := pushPrivateUntracked(ctx, key4(record.From), key4(record.To), 1, &input); err != nil {
					return normalizedFeed[key4]{}, err
				}
				work.RangeConsumed(1)
				inputCount = next
			}
		}
	}
	if _, err := finishInputUntracked(ctx, &input); err != nil {
		return normalizedFeed[key4]{}, err
	}
	if err := runCheck(check); err != nil {
		return normalizedFeed[key4]{}, err
	}
	return normalizedFeed[key4]{root: root, recordCount: recordCount, inputRecordCount: inputCount}, nil
}

// writeNormalized4 streams the normalized workspace tree into the
// output builder (Rust write_normalized): one feed catalog entry, the
// one-feed membership interned when the tree is non-empty, the local
// workspace meta that pins the tree bounds for the ordered cursor,
// every coverage range pushed as interned membership over the one
// feed, the exact address count, and the budgeted metadata write.
func writeNormalized4(workspace *immutableWorkspace, builder *OutputBuilder, spec OutputSpec, feedName string, metadataJSON []byte, maxHeapBytes uint64, normalized normalizedFeed[key4], check func() error) (format.Cardinality129, error) {
	if err := builder.PushFeed(feedName, 0); err != nil {
		return format.CardinalityZero(), err
	}
	var membership uint32
	if normalized.recordCount != 0 {
		value, err := internOutputMembership(builder, oneFeedWords{})
		if err != nil {
			return format.CardinalityZero(), err
		}
		membership = value
	}
	meta := outputEmptyMeta(spec)
	meta.PageCount = workspace.pageCount()
	meta.RangeRoot = normalized.root
	meta.RangeRecordCount = normalized.recordCount
	cursor, err := tree.NewForwardCursor[rangeRecord[key4]](rangeCodec4{}, workspace, meta.RangeRoot, false)
	if err != nil {
		return format.CardinalityZero(), err
	}
	addresses := format.CardinalityZero()
	var outputRanges uint64
	for {
		record, ok, err := cursor.Next()
		if err != nil {
			return format.CardinalityZero(), err
		}
		if !ok {
			break
		}
		if outputRanges&4095 == 4095 {
			if err := runCheck(check); err != nil {
				return format.CardinalityZero(), err
			}
		}
		if record.value != 1 {
			return format.CardinalityZero(), corrupt("immutable feed workspace contains a non-coverage value")
		}
		if err := builder.PushInternedMembershipV4(uint32(record.from), uint32(record.to), membership); err != nil {
			return format.CardinalityZero(), err
		}
		count, err := format.IPv4Inclusive(uint32(record.from), uint32(record.to))
		if err != nil {
			return format.CardinalityZero(), err
		}
		addresses, err = addresses.Add(count)
		if err != nil {
			return format.CardinalityZero(), err
		}
		work.RangeConsumed(1)
		outputRanges++
	}
	if err := runCheck(check); err != nil {
		return format.CardinalityZero(), err
	}
	if metadataJSON != nil {
		if err := builder.WriteMetadataWithBudget(metadataJSON, maxHeapBytes); err != nil {
			return format.CardinalityZero(), err
		}
	}
	return addresses, nil
}

// buildImmutableFeedMapped6 runs the normalize and ordered write of
// one IPv6 feed over the workspace store (Rust build_mapped).
func buildImmutableFeedMapped6(workspace *immutableWorkspace, builder *OutputBuilder, spec OutputSpec, feedName string, metadataJSON []byte, maxHeapBytes uint64, nextBatch func() ([]FeedRange6, error), check func() error) (ImmutableFeedReport, error) {
	var zero ImmutableFeedReport
	normalized, err := normalizeFeed6(workspace, nextBatch, maxHeapBytes, check)
	if err != nil {
		return zero, err
	}
	addresses, err := writeNormalized6(workspace, builder, spec, feedName, metadataJSON, maxHeapBytes, normalized, check)
	if err != nil {
		return zero, err
	}
	return ImmutableFeedReport{
		InputRecordCount:        normalized.inputRecordCount,
		NormalizedIntervalCount: normalized.recordCount,
		Addresses:               addresses,
	}, nil
}

// normalizeFeed6 drains the source into the workspace coverage tree
// (Rust normalize); the IPv6 form of normalizeFeed4.
func normalizeFeed6(workspace *immutableWorkspace, nextBatch func() ([]FeedRange6, error), maxHeapBytes uint64, check func() error) (normalizedFeed[key6], error) {
	codec := rangeCodec6{}
	input := newUnionInput(codec, format.AddressFamilyIPv6, format.ValueKindMembership, maxHeapBytes)
	var root uint32
	var recordCount uint64
	var inputCount uint64
	var scratch [3][format.RangeRecordV6Size]byte
	ctx := &rangeCtx[key6]{family: codec, store: workspace, storeView: workspace, root: &root, count: &recordCount, scratch: &scratch}
	work.SourcePass(1)
	work.InputSourcePass(1)
	for {
		if err := runCheck(check); err != nil {
			return normalizedFeed[key6]{}, err
		}
		batch, err := nextBatch()
		if err != nil {
			return normalizedFeed[key6]{}, err
		}
		if batch == nil {
			break
		}
		if len(batch) == 0 {
			return normalizedFeed[key6]{}, invalid("range source returned an empty batch")
		}
		for chunkIndex := 0; chunkIndex*4096 < len(batch); chunkIndex++ {
			if chunkIndex != 0 {
				if err := runCheck(check); err != nil {
					return normalizedFeed[key6]{}, err
				}
			}
			lo := chunkIndex * 4096
			hi := lo + 4096
			if hi > len(batch) {
				hi = len(batch)
			}
			for _, record := range batch[lo:hi] {
				next := inputCount + 1
				if next == 0 {
					return normalizedFeed[key6]{}, overflow("workflow input record count")
				}
				from := key6{hi: record.FromHi, lo: record.FromLo}
				to := key6{hi: record.ToHi, lo: record.ToLo}
				if _, err := pushPrivateUntracked(ctx, from, to, 1, &input); err != nil {
					return normalizedFeed[key6]{}, err
				}
				work.RangeConsumed(1)
				inputCount = next
			}
		}
	}
	if _, err := finishInputUntracked(ctx, &input); err != nil {
		return normalizedFeed[key6]{}, err
	}
	if err := runCheck(check); err != nil {
		return normalizedFeed[key6]{}, err
	}
	return normalizedFeed[key6]{root: root, recordCount: recordCount, inputRecordCount: inputCount}, nil
}

// writeNormalized6 streams the normalized workspace tree into the
// output builder (Rust write_normalized); the IPv6 form of
// writeNormalized4.
func writeNormalized6(workspace *immutableWorkspace, builder *OutputBuilder, spec OutputSpec, feedName string, metadataJSON []byte, maxHeapBytes uint64, normalized normalizedFeed[key6], check func() error) (format.Cardinality129, error) {
	if err := builder.PushFeed(feedName, 0); err != nil {
		return format.CardinalityZero(), err
	}
	var membership uint32
	if normalized.recordCount != 0 {
		value, err := internOutputMembership(builder, oneFeedWords{})
		if err != nil {
			return format.CardinalityZero(), err
		}
		membership = value
	}
	meta := outputEmptyMeta(spec)
	meta.PageCount = workspace.pageCount()
	meta.RangeRoot = normalized.root
	meta.RangeRecordCount = normalized.recordCount
	cursor, err := tree.NewForwardCursor[rangeRecord[key6]](rangeCodec6{}, workspace, meta.RangeRoot, false)
	if err != nil {
		return format.CardinalityZero(), err
	}
	addresses := format.CardinalityZero()
	var outputRanges uint64
	for {
		record, ok, err := cursor.Next()
		if err != nil {
			return format.CardinalityZero(), err
		}
		if !ok {
			break
		}
		if outputRanges&4095 == 4095 {
			if err := runCheck(check); err != nil {
				return format.CardinalityZero(), err
			}
		}
		if record.value != 1 {
			return format.CardinalityZero(), corrupt("immutable feed workspace contains a non-coverage value")
		}
		if err := builder.PushInternedMembershipV6(record.from.hi, record.from.lo, record.to.hi, record.to.lo, membership); err != nil {
			return format.CardinalityZero(), err
		}
		count, err := format.IPv6Inclusive(record.from.hi, record.from.lo, record.to.hi, record.to.lo)
		if err != nil {
			return format.CardinalityZero(), err
		}
		addresses, err = addresses.Add(count)
		if err != nil {
			return format.CardinalityZero(), err
		}
		work.RangeConsumed(1)
		outputRanges++
	}
	if err := runCheck(check); err != nil {
		return format.CardinalityZero(), err
	}
	if metadataJSON != nil {
		if err := builder.WriteMetadataWithBudget(metadataJSON, maxHeapBytes); err != nil {
			return format.CardinalityZero(), err
		}
	}
	return addresses, nil
}

// oneFeedWords is the one-feed membership bitmap of the immutable feed
// output (Rust OneFeed): word_count 1 and the single word 1 (feed
// index 0 active).
type oneFeedWords struct{}

// WordCount returns the canonical bitmap word count (Rust word_count).
func (oneFeedWords) WordCount() uint32 { return 1 }

// ReadChunk returns the one word by value (Rust read_words: any other
// read shape is invalid for the one-feed bitmap).
func (oneFeedWords) ReadChunk(start uint32) (words [membershipChunkWords]uint64, count uint32, err error) {
	if start != 0 {
		return words, 0, corrupt("one-feed membership read is invalid")
	}
	words[0] = 1
	return words, 1, nil
}
