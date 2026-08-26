// One-shot append-only immutable output construction (Rust
// immutable_output.rs): a fresh empty main file, page 0/1 the meta pair,
// pages 2+ data, reserve_page the single allocation authority,
// append-only ownership (no retire, no discard), and the exact finish
// order (membership refs, ranges, seal, shrink to page_count*4096, dual
// meta, flush, sync).

package writer

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/bitmap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/tree"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// OutputSpec is the identity contract of one output (Rust OutputSpec):
// the address family, value/structure kinds, the fresh value tag, the
// fresh database id and commit nonce, the fixed transaction id 1, and the
// preserved feed-index limit.
type OutputSpec struct {
	AddressFamily  uint8
	ValueKind      uint8
	StructureKind  uint8
	ValueTag       [16]byte
	DatabaseID     [16]byte
	TxnID          uint64
	CommitNonce    [16]byte
	FeedIndexLimit uint64
}

// FreshOutputSpec draws fresh identity for one output (Rust
// OutputSpec::fresh): transaction id always 1, new database id and commit
// nonce.
func FreshOutputSpec(family, kind, structure uint8, valueTag [16]byte, feedIndexLimit uint64) (OutputSpec, error) {
	databaseID, err := randomNonce()
	if err != nil {
		return OutputSpec{}, err
	}
	nonce, err := randomNonce()
	if err != nil {
		return OutputSpec{}, err
	}
	return OutputSpec{
		AddressFamily:  family,
		ValueKind:      kind,
		StructureKind:  structure,
		ValueTag:       valueTag,
		DatabaseID:     databaseID,
		TxnID:          1,
		CommitNonce:    nonce,
		FeedIndexLimit: feedIndexLimit,
	}, nil
}

// OutputBudget bounds one output (Rust OutputBudget).
type OutputBudget struct {
	MaxOutputPages uint64
}

// OutputBuilder is the append-only one-shot builder over a fresh mapping
// (Rust immutable_output::Builder).
type OutputBuilder struct {
	mapping *mapping.Mapping
	path    string
	meta    format.Meta
	budget  OutputBudget
	ranges  *rangeBulkBuilder
	// Encode targets for the catalog and dictionary records of one
	// output. The build loop is single-threaded and every tree insert
	// copies its record into the mapped page before the next encode
	// reuses the buffer, so these are allocated once per output, never
	// per record (the Go generic tree interface makes stack encodes
	// escape).
	recordScratch  [membershipRecordLimit]byte
	hashScratch    [membershipHashKeySize]byte
	catalogScratch [catalogMaxRecord]byte
	// structureScratch owns the structure intern payload of one output
	// (Rust intern_payload payload local): the shape-stenciled generic
	// internStructure leaks its payload argument, so the builder copies
	// the payload into this field before the call instead of allocating
	// per intern.
	structureScratch structurePayload
	// membershipRefs aggregates recurring membership references so each
	// id is applied as one refcount delta (Rust ReferenceBatch).
	membershipRefs membershipReferenceBatch
	// structureRefs aggregates recurring structure references so each id
	// is applied as one refcount delta (Rust ReferenceBatch; only
	// structured outputs enable it).
	structureRefs structureReferenceBatch
	// metadataStaged mirrors the Rust output metadata latch: one
	// WriteMetadata per output.
	metadataStaged bool
	// failed latches a failed mutation (Rust require_active); after a
	// successful Finish the latch is also set so the consumed builder
	// refuses further mutation exactly like the Rust Finished value.
	failed bool
	// finished records a successful Finish: the publish gate requires
	// it, so a failed Finish can never be published as if it sealed.
	finished bool
	// pageProbe is the armed output region of one in-flight page
	// mutation (Rust with_output_protection spanning update_page and
	// copy_page): Update and CopyPage arm it before the page fetch,
	// RestoreDirty releases it after the caller's mutation, and every
	// store entry point consumes an aborted window first (Go has no
	// RAII drop, so the release point of a failed mutation is the next
	// store operation or Close instead of the Rust closure return).
	// The zero guard is the inert no-session value, so library writers
	// never arm and never allocate.
	pageProbe mapping.ProbeGuard
}

// MaxOutputPages returns the page budget.
func (b *OutputBuilder) MaxOutputPages() uint64 { return b.budget.MaxOutputPages }

// FileIdentity returns the device+inode of the attempt file the builder
// created (Rust CreatedOutput::create_with captures the identity at
// creation; the publication discard is identity-guarded with it).
func (b *OutputBuilder) FileIdentity() (device uint64, inode uint64, err error) {
	return b.mapping.FileIdentity()
}

// Meta returns the current builder meta.
func (b *OutputBuilder) Meta() format.Meta { return b.meta }

// Path returns the output file path.
func (b *OutputBuilder) Path() string { return b.path }

// PageCount returns the current page count.
func (b *OutputBuilder) PageCount() uint64 { return b.meta.PageCount }

// NewOutputBuilder starts one immutable output over the empty file at
// path (Rust new_owned_with_extent; the file is created exclusively,
// ftruncated to the budget extent, and mapped read-write). The
// referenceBatchEntries capacity is the membership/structured
// reference-batch entry count computed from the operation heap by the
// caller (Rust ReferenceBatch::new sizes and charges the batch from
// heap.remaining() at builder construction): a power of two up to
// ReferenceBatchEntryLimit, or 0 to disable batching. Direct-value
// outputs never batch regardless of the argument. Structured outputs
// built through this constructor run with the structure reference batch
// disabled (references apply directly), which is byte-identical in
// behavior; callers that can charge a structure batch from the remaining
// operation heap use NewStructuredOutputBuilder like the Rust
// new_owned_with_extent sequence (membership batch first, structure
// batch second).
func NewOutputBuilder(path string, spec OutputSpec, budget OutputBudget, referenceBatchEntries int, check func(clean string) error) (*OutputBuilder, error) {
	return newOutputBuilder(path, spec, budget, referenceBatchEntries, 0, check)
}

// NewStructuredOutputBuilder starts one structured immutable output with
// both reference batches sized from the operation heap exactly like Rust
// new_owned_with_extent: membershipBatchEntries is charged first and
// structureBatchEntries second from the remaining heap (each a power of
// two up to ReferenceBatchEntryLimit, 0 disables that batch). The
// existing membership publish_set callers keep the five-argument
// NewOutputBuilder unchanged.
func NewStructuredOutputBuilder(path string, spec OutputSpec, budget OutputBudget, membershipBatchEntries, structureBatchEntries int, check func(clean string) error) (*OutputBuilder, error) {
	return newOutputBuilder(path, spec, budget, membershipBatchEntries, structureBatchEntries, check)
}

func newOutputBuilder(path string, spec OutputSpec, budget OutputBudget, membershipBatchEntries, structureBatchEntries int, check func(clean string) error) (*OutputBuilder, error) {
	if err := requireNewOutput(spec, budget); err != nil {
		return nil, err
	}
	if budget.MaxOutputPages < 2 || budget.MaxOutputPages > 1<<32 {
		return nil, invalid("immutable construction extent is invalid")
	}
	extent := budget.MaxOutputPages * format.PageSize
	m, err := mapping.Create(path, extent, check)
	if err != nil {
		return nil, err
	}
	return assembleOutputBuilder(m, path, spec, budget, membershipBatchEntries, structureBatchEntries), nil
}

// NewOutputBuilderOverFile starts one immutable output over an
// existing empty file (Rust new_owned_with_extent over the
// workflow::create file, the snapshot and publish_set construction
// path): the file must be empty, is extended to the budget extent,
// and is mapped read-write through a duplicated descriptor. The
// caller keeps the original descriptor; the returned builder owns
// only its mapping, and no lifetime lock is taken here: the
// reservation-path publication engine takes it at its prepare step
// (Rust prepare_cancellable) and the private attempt name keeps the
// in-progress file out of reader reach during the build. The
// direct-value outputs built through this constructor run with the
// structure reference batch disabled like the five-argument
// NewOutputBuilder.
func NewOutputBuilderOverFile(file *os.File, spec OutputSpec, budget OutputBudget, referenceBatchEntries int) (*OutputBuilder, error) {
	return newOutputBuilderOverFile(file, spec, budget, referenceBatchEntries, 0)
}

// NewStructuredOutputBuilderOverFile starts one structured immutable
// output over an existing empty file with both reference batches sized
// from the operation heap (Rust new_owned_with_extent over the
// workflow::create file: membershipBatchEntries is charged first and
// structureBatchEntries second; each a power of two up to
// ReferenceBatchEntryLimit, 0 disables that batch).
func NewStructuredOutputBuilderOverFile(file *os.File, spec OutputSpec, budget OutputBudget, membershipBatchEntries, structureBatchEntries int) (*OutputBuilder, error) {
	return newOutputBuilderOverFile(file, spec, budget, membershipBatchEntries, structureBatchEntries)
}

func newOutputBuilderOverFile(file *os.File, spec OutputSpec, budget OutputBudget, membershipBatchEntries, structureBatchEntries int) (*OutputBuilder, error) {
	if err := requireNewOutput(spec, budget); err != nil {
		return nil, err
	}
	if budget.MaxOutputPages < 2 || budget.MaxOutputPages > 1<<32 {
		return nil, invalid("immutable construction extent is invalid")
	}
	extent := budget.MaxOutputPages * format.PageSize
	// Rust require_new_output proves the created file is still empty
	// before the extent extension: an already-written file is never
	// adopted into a fresh output.
	fi, err := file.Stat()
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
	}
	if fi.Size() != 0 {
		return nil, invalid("immutable output file is not empty")
	}
	if err := file.Truncate(int64(extent)); err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "truncate: " + err.Error()}
	}
	m, err := mapping.MapFile(file, extent, true)
	if err != nil {
		return nil, err
	}
	return assembleOutputBuilder(m, file.Name(), spec, budget, membershipBatchEntries, structureBatchEntries), nil
}

// assembleOutputBuilder builds the append-only output state over one
// read-write mapping (the shared tail of the path-based and over-file
// constructors; the reference batches are sized from the operation
// heap by the caller and capped here).
func assembleOutputBuilder(m *mapping.Mapping, path string, spec OutputSpec, budget OutputBudget, membershipBatchEntries, structureBatchEntries int) *OutputBuilder {
	meta := outputEmptyMeta(spec)
	batchCapacity := 0
	if spec.ValueKind == format.ValueKindMembership || spec.ValueKind == format.ValueKindStructured {
		if membershipBatchEntries > ReferenceBatchEntryLimit {
			membershipBatchEntries = ReferenceBatchEntryLimit
		}
		if membershipBatchEntries < 0 {
			membershipBatchEntries = 0
		}
		batchCapacity = membershipBatchEntries
	}
	structureCapacity := 0
	if spec.ValueKind == format.ValueKindStructured {
		if structureBatchEntries > ReferenceBatchEntryLimit {
			structureBatchEntries = ReferenceBatchEntryLimit
		}
		if structureBatchEntries < 0 {
			structureBatchEntries = 0
		}
		structureCapacity = structureBatchEntries
	}
	return &OutputBuilder{
		mapping:        m,
		path:           path,
		meta:           meta,
		budget:         budget,
		ranges:         newRangeBulkBuilder(meta.TxnID, meta.ValueKind, meta.AddressFamily),
		membershipRefs: newMembershipReferenceBatch(batchCapacity),
		structureRefs:  newMembershipReferenceBatch(structureCapacity),
	}
}

// outputEmptyMeta mirrors database_file.rs empty_meta for the output spec.
func outputEmptyMeta(spec OutputSpec) format.Meta {
	m := format.Meta{
		AddressFamily:  spec.AddressFamily,
		ValueKind:      spec.ValueKind,
		StructureKind:  spec.StructureKind,
		ValueTag:       spec.ValueTag,
		DatabaseID:     spec.DatabaseID,
		TxnID:          spec.TxnID,
		CommitNonce:    spec.CommitNonce,
		PageCount:      2,
		FeedIndexLimit: spec.FeedIndexLimit,
	}
	if spec.ValueKind == format.ValueKindMembership || spec.ValueKind == format.ValueKindStructured {
		m.MembershipIDLimit = 1
	}
	if spec.ValueKind == format.ValueKindStructured {
		m.StructureIDLimit = 1
	}
	return m
}

// requireNewOutput validates the identity and limits (Rust setup.rs).
func requireNewOutput(spec OutputSpec, budget OutputBudget) error {
	var zero [16]byte
	if spec.DatabaseID == zero || spec.CommitNonce == zero || spec.TxnID == 0 {
		return invalid("immutable output identity is invalid")
	}
	if !validKinds(spec.ValueKind, spec.StructureKind) {
		return wrongStructureKind("immutable output value and structure kinds do not match")
	}
	if budget.MaxOutputPages < 2 {
		return budgetExceeded("immutable output pages")
	}
	if spec.FeedIndexLimit > 1<<32 || (spec.ValueKind == format.ValueKindDirect && spec.FeedIndexLimit != 0) {
		return invalid("immutable output feed-index limit is invalid")
	}
	return nil
}

func wrongStructureKind(detail string) error {
	return &format.Error{Code: format.CodeWrongStructureKind, Detail: detail}
}

func wrongState(detail string) error {
	return &format.Error{Code: format.CodeWrongState, Detail: detail}
}

// mutate runs one operation behind the active latch (Rust Builder::mutate).
func (b *OutputBuilder) mutate(operation func() error) error {
	if err := b.requireActive(); err != nil {
		return err
	}
	if err := operation(); err != nil {
		b.failed = true
		return err
	}
	return nil
}

// requireActive refuses operations after a failed mutation (Rust
// require_active).
// armPageWindow releases any aborted page window and arms the output
// region for the next mutation (Rust enter_output inside
// with_output_protection: the region resolves and arms before the page
// fetch, and the caller's mutation runs with the output probe held).
// Without a worker session the guard is the inert zero value, so the
// library writer pays one SessionProbeActive load and no allocation.
func (b *OutputBuilder) armPageWindow() error {
	b.pageProbe.Exit()
	b.pageProbe = mapping.ProbeGuard{}
	if !mapping.SessionProbeActive() {
		return nil
	}
	guard, err := b.mapping.EnterProbe(mapping.RoleOutput)
	if err != nil {
		return err
	}
	b.pageProbe = guard
	return nil
}

// consumePageWindow releases an in-flight or aborted page window (the
// Go analog of the Rust probe drop after the update/copy closure; on
// aborted mutations the release point is the next store operation or
// Close). The zero guard is the no-session no-op.
func (b *OutputBuilder) consumePageWindow() {
	b.pageProbe.Exit()
	b.pageProbe = mapping.ProbeGuard{}
}

func (b *OutputBuilder) requireActive() error {
	if b.failed {
		return wrongState("immutable output construction failed")
	}
	return nil
}

// requireMode checks the value kind and address family of one operation
// (Rust require_mode).
func (b *OutputBuilder) requireMode(kind, family uint8) error {
	if b.meta.ValueKind != kind {
		return &format.Error{Code: format.CodeWrongValueKind, Detail: "immutable output operation does not match its value kind"}
	}
	if b.meta.AddressFamily != family {
		return &format.Error{Code: format.CodeWrongAddressFamily, Detail: "immutable output operation does not match its address family"}
	}
	return nil
}

// PushFeed interns one catalog entry and marks its feed bit (Rust
// push_feed; the feed name is the caller's string, mirroring the reader's
// LookupFeed so the write path never converts a mapped view).
func (b *OutputBuilder) PushFeed(name string, index uint32) error {
	return b.mutate(func() error {
		if b.meta.ValueKind != format.ValueKindMembership && b.meta.ValueKind != format.ValueKindStructured {
			return &format.Error{Code: format.CodeWrongValueKind, Detail: "immutable feed output requires a membership-capable value kind"}
		}
		if uint64(index) >= b.meta.FeedIndexLimit {
			return invalid("feed index exceeds the preserved limit")
		}
		active := b.meta.ActiveFeedCount + 1
		if active < b.meta.ActiveFeedCount {
			return overflow("active feed count")
		}
		if active > b.meta.FeedIndexLimit {
			return corrupt("feed catalog exceeds its index limit")
		}
		if err := insertCatalogEntry(b, b.catalogScratch[:], &b.meta.CatalogNameRoot, &b.meta.CatalogIndexRoot, name, index); err != nil {
			return err
		}
		var retired tree.RetiredPages
		if err := bitmap.SetUsed(b, &b.meta.FeedUsedRoot, b.meta.FeedIndexLimit, bitmap.KindFeed, index, &retired); err != nil {
			return err
		}
		if err := b.RetirePages(retired); err != nil {
			return err
		}
		b.meta.ActiveFeedCount = active
		return nil
	})
}

// PushDirectV4 appends one IPv4 direct range (Rust push_direct_v4).
func (b *OutputBuilder) PushDirectV4(from, to, value uint32) error {
	return b.mutate(func() error {
		if err := b.requireMode(format.ValueKindDirect, format.AddressFamilyIPv4); err != nil {
			return err
		}
		return b.ranges.push(b, rangeRecord{from: tree.Key{Hi: uint64(from)}, to: tree.Key{Hi: uint64(to)}, value: value})
	})
}

// PushDirectV6 appends one IPv6 direct range (Rust push_direct_v6).
func (b *OutputBuilder) PushDirectV6(fromHi, fromLo, toHi, toLo uint64, value uint32) error {
	return b.mutate(func() error {
		if err := b.requireMode(format.ValueKindDirect, format.AddressFamilyIPv6); err != nil {
			return err
		}
		return b.ranges.push(b, rangeRecord{from: tree.Key{Hi: fromHi, Lo: fromLo}, to: tree.Key{Hi: toHi, Lo: toLo}, value: value})
	})
}

// PushMembershipV4 interns the bitmap and appends one IPv4 membership
// range (Rust push_membership_v4 over the caller words).
func (b *OutputBuilder) PushMembershipV4(from, to uint32, words OutputWords) error {
	return pushMembershipWordsV4(b, from, to, words)
}

// PushMembershipV4Words interns one word source and appends one IPv4
// membership range (Rust push_membership_v4 over a generic word
// source): the recovery output streams the verified source bitmap
// without materializing it.
func (b *OutputBuilder) PushMembershipV4Words(from, to uint32, words MembershipWordSource) error {
	return pushMembershipWordsV4(b, from, to, words)
}

// pushMembershipWordsV4 is the shared IPv4 membership push (Rust
// push_membership_v4).
func pushMembershipWordsV4[W membershipWords](b *OutputBuilder, from, to uint32, words W) error {
	return b.mutate(func() error {
		if err := b.requireMode(format.ValueKindMembership, format.AddressFamilyIPv4); err != nil {
			return err
		}
		value, err := internOutputMembership(b, words)
		if err != nil {
			return err
		}
		if err := b.ranges.push(b, rangeRecord{from: tree.Key{Hi: uint64(from)}, to: tree.Key{Hi: uint64(to)}, value: value}); err != nil {
			return err
		}
		return b.addMembershipReference(value)
	})
}

// PushMembershipV6 interns the bitmap and appends one IPv6 membership
// range (Rust push_membership_v6 over the caller words).
func (b *OutputBuilder) PushMembershipV6(fromHi, fromLo, toHi, toLo uint64, words OutputWords) error {
	return pushMembershipWordsV6(b, fromHi, fromLo, toHi, toLo, words)
}

// PushMembershipV6Words interns one word source and appends one IPv6
// membership range (Rust push_membership_v6 over a generic word
// source).
func (b *OutputBuilder) PushMembershipV6Words(fromHi, fromLo, toHi, toLo uint64, words MembershipWordSource) error {
	return pushMembershipWordsV6(b, fromHi, fromLo, toHi, toLo, words)
}

// pushMembershipWordsV6 is the shared IPv6 membership push (Rust
// push_membership_v6).
func pushMembershipWordsV6[W membershipWords](b *OutputBuilder, fromHi, fromLo, toHi, toLo uint64, words W) error {
	return b.mutate(func() error {
		if err := b.requireMode(format.ValueKindMembership, format.AddressFamilyIPv6); err != nil {
			return err
		}
		value, err := internOutputMembership(b, words)
		if err != nil {
			return err
		}
		if err := b.ranges.push(b, rangeRecord{from: tree.Key{Hi: fromHi, Lo: fromLo}, to: tree.Key{Hi: toHi, Lo: toLo}, value: value}); err != nil {
			return err
		}
		return b.addMembershipReference(value)
	})
}

// PushInternedMembershipV4 appends one IPv4 membership range over an
// already interned value (Rust push_interned_membership_v4).
func (b *OutputBuilder) PushInternedMembershipV4(from, to, value uint32) error {
	return b.mutate(func() error {
		if err := b.requireMode(format.ValueKindMembership, format.AddressFamilyIPv4); err != nil {
			return err
		}
		if err := b.ranges.push(b, rangeRecord{from: tree.Key{Hi: uint64(from)}, to: tree.Key{Hi: uint64(to)}, value: value}); err != nil {
			return err
		}
		return b.addMembershipReference(value)
	})
}

// PushInternedMembershipV6 appends one IPv6 membership range over an
// already interned value (Rust push_interned_membership_v6).
func (b *OutputBuilder) PushInternedMembershipV6(fromHi, fromLo, toHi, toLo uint64, value uint32) error {
	return b.mutate(func() error {
		if err := b.requireMode(format.ValueKindMembership, format.AddressFamilyIPv6); err != nil {
			return err
		}
		if err := b.ranges.push(b, rangeRecord{from: tree.Key{Hi: fromHi, Lo: fromLo}, to: tree.Key{Hi: toHi, Lo: toLo}, value: value}); err != nil {
			return err
		}
		return b.addMembershipReference(value)
	})
}

// InternMembership returns the dictionary ID of one bitmap, creating the
// record when needed (Rust intern_membership_value).
func (b *OutputBuilder) InternMembership(words OutputWords) (uint32, error) {
	var value uint32
	err := b.mutate(func() error {
		var err error
		value, err = internOutputMembership(b, words)
		return err
	})
	return value, err
}

// internOutputMembership runs the shape check (word count vs the
// feed-index limit, canonical final word), the feed-activity check over
// every word, and the dictionary intern (Rust
// immutable_output/membership.rs intern).
func internOutputMembership[W membershipWords](b *OutputBuilder, source W) (uint32, error) {
	if err := requireOutputShape(source, b.meta.FeedIndexLimit); err != nil {
		return 0, err
	}
	// Every supplied word must be a subset of the active-feed bitmap
	// before the dictionary sees the source (Rust CheckedWords). The
	// check is one concrete chunked pass over the caller's words, with no
	// interface indirection over the page-bearing store.
	if err := requireOutputFeeds(b, b.meta.FeedUsedRoot, b.meta.FeedIndexLimit, source); err != nil {
		return 0, err
	}
	state := b.membershipState()
	interned, err := internMembership(b, &state, source)
	if err != nil {
		return 0, err
	}
	b.storeMembershipState(state)
	return interned.id, nil
}

// requireOutputFeeds verifies every set bit of source names an active
// feed (Rust CheckedWords::read_words): chunked word reads from the
// source and the feed used-bitmap are compared word by word.
func requireOutputFeeds[W membershipWords](store tree.Store, feedRoot uint32, feedLimit uint64, words W) error {
	const checkWords = 64
	var active [checkWords]uint64
	for start := uint32(0); start < words.WordCount(); start += checkWords {
		values, count, err := words.ReadChunk(start)
		if err != nil {
			return err
		}
		activeSlice := active[:count]
		if err := bitmap.ReadWords(store, feedRoot, feedLimit, bitmap.KindFeed, start, activeSlice); err != nil {
			return err
		}
		for index := 0; index < int(count); index++ {
			if values[index]&^activeSlice[index] != 0 {
				return invalid("membership references an inactive feed")
			}
		}
	}
	return nil
}

// membershipState captures the writable dictionary state (Rust
// membership_state).
func (b *OutputBuilder) membershipState() membershipState {
	return membershipState{
		idRoot:        b.meta.MembershipIDRoot,
		hashRoot:      b.meta.MembershipHashRoot,
		usedRoot:      b.meta.MembershipUsedRoot,
		entryCount:    b.meta.MembershipEntryCount,
		idLimit:       b.meta.MembershipIDLimit,
		recordScratch: b.recordScratch[:],
		hashScratch:   b.hashScratch[:],
	}
}

// storeMembershipState persists the dictionary state into the meta (Rust
// store_membership_state).
func (b *OutputBuilder) storeMembershipState(state membershipState) {
	b.meta.MembershipIDRoot = state.idRoot
	b.meta.MembershipHashRoot = state.hashRoot
	b.meta.MembershipUsedRoot = state.usedRoot
	b.meta.MembershipEntryCount = state.entryCount
	b.meta.MembershipIDLimit = state.idLimit
}

// addMembershipReference records one reference and applies it directly
// when the batch is disabled, or flushes a full batch and retries (Rust
// add_membership_reference).
func (b *OutputBuilder) addMembershipReference(value uint32) error {
	switch outcome, err := b.membershipRefs.addReference(value); {
	case err != nil:
		return err
	case outcome == referenceAdded:
		return nil
	case outcome == referenceDirect:
		return b.applyMembershipReference(value)
	case outcome == referenceFull:
	}
	if err := b.flushMembershipReferences(); err != nil {
		return err
	}
	switch outcome, err := b.membershipRefs.addReference(value); {
	case err != nil:
		return err
	case outcome == referenceAdded:
		return nil
	case outcome == referenceFull:
		return corrupt("empty membership reference batch stayed full")
	case outcome == referenceDirect:
		return b.applyMembershipReference(value)
	}
	return nil
}

// applyMembershipReference applies one refcount delta immediately (Rust
// apply_membership_reference).
func (b *OutputBuilder) applyMembershipReference(value uint32) error {
	state := b.membershipState()
	if err := applyMembershipDelta(b, &state, value, 1); err != nil {
		return err
	}
	b.storeMembershipState(state)
	work.MembershipRefcountBatch(1)
	return nil
}

// flushMembershipReferences applies every pending delta (Rust
// flush_membership_references).
func (b *OutputBuilder) flushMembershipReferences() error {
	if b.membershipRefs.isEmpty() {
		return nil
	}
	state := b.membershipState()
	for index := 0; index < b.membershipRefs.capacity(); index++ {
		id, count, ok := b.membershipRefs.takeReference(index)
		if !ok {
			continue
		}
		if err := applyMembershipDelta(b, &state, id, count); err != nil {
			return err
		}
	}
	b.membershipRefs.finishFlush()
	b.storeMembershipState(state)
	work.MembershipRefcountBatch(1)
	return nil
}

// requireOutputShape validates the word count bound and the canonical
// final word (Rust require_shape over the checked source).
func requireOutputShape[W membershipWords](words W, feedLimit uint64) error {
	wordCount := words.WordCount()
	if wordCount == 0 {
		return invalid("membership word count exceeds the feed-index limit")
	}
	maximum := (feedLimit + 63) / 64
	if uint64(wordCount) > maximum {
		return invalid("membership word count exceeds the feed-index limit")
	}
	chunkStart := ((wordCount - 1) / 64) * 64
	values, _, err := words.ReadChunk(chunkStart)
	if err != nil {
		return err
	}
	if values[wordCount-1-chunkStart] == 0 {
		return invalid("membership bitmap is not canonical")
	}
	return nil
}

// Finish seals the output, truncates the file to the exact final size,
// writes the dual meta, flushes and syncs (Rust finish). Finished
// builders refuse further mutation.
func (b *OutputBuilder) Finish() error {
	b.consumePageWindow()
	if err := b.requireActive(); err != nil {
		return err
	}
	// Rust finish flushes the structure references before the membership
	// references, then the ranges.
	if err := b.flushStructureReferences(); err != nil {
		b.failed = true
		return err
	}
	if err := b.flushMembershipReferences(); err != nil {
		b.failed = true
		return err
	}
	root, count, err := b.ranges.finish(b)
	if err != nil {
		b.failed = true
		return err
	}
	b.meta.RangeRoot = root
	b.meta.RangeRecordCount = count
	if err := b.sealPages(); err != nil {
		b.failed = true
		return err
	}
	bytes := b.meta.PageCount * format.PageSize
	if err := b.mapping.Shrink(bytes); err != nil {
		b.failed = true
		return err
	}
	// Rust finish wraps the dual meta encode in with_output_protection
	// (immutable_output.rs:711): the armed Output probe covers the
	// write, while the resize, flush, and sync stay outside exactly
	// like the Rust arms.
	if err := b.mapping.Probe(mapping.RoleOutput, func() error {
		p0, err := b.mapping.Page(0)
		if err != nil {
			return err
		}
		p1, err := b.mapping.Page(1)
		if err != nil {
			return err
		}
		if err := b.meta.EncodeMapped(p0); err != nil {
			return err
		}
		return b.meta.EncodeMapped(p1)
	}); err != nil {
		b.failed = true
		return err
	}
	if err := b.mapping.FlushRange(0, bytes); err != nil {
		b.failed = true
		return err
	}
	if err := b.mapping.SyncFile(); err != nil {
		b.failed = true
		return err
	}
	b.failed = true
	b.finished = true
	return nil
}

// sealPages verifies every data page is owned by the output transaction
// and seals its CRC inside the armed Output probe (Rust seal_pages
// with_output_protection).
func (b *OutputBuilder) sealPages() error {
	return b.mapping.Probe(mapping.RoleOutput, func() error {
		for pageNumber := uint32(2); uint64(pageNumber) < b.meta.PageCount; pageNumber++ {
			if err := b.mapping.VisitPage(pageNumber, func(page []byte) error {
				if !outputPageOwned(page, b.meta.TxnID) {
					return corrupt("immutable output page ownership is invalid")
				}
				return format.SealPageChecksum(page)
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// outputPageOwned reports the magic and born-transaction ownership of one
// output page (Rust page_header::owned_by: the 4-byte page magic and the
// born transaction at offset 8).
func outputPageOwned(page []byte, txn uint64) bool {
	return len(page) >= format.PageSize &&
		page[0] == format.PageMagic[0] && page[1] == format.PageMagic[1] &&
		page[2] == format.PageMagic[2] && page[3] == format.PageMagic[3] &&
		format.U64(page[format.HeaderBorn:]) == txn
}

// Close releases the mapping; callers that never finish must close.
// An in-flight or aborted page window is released before the unmap so
// the worker control never holds a dangling armed region.
func (b *OutputBuilder) Close() error {
	b.consumePageWindow()
	return b.mapping.Close()
}

// Mapping exposes the finished mapping to the publication stage.
func (b *OutputBuilder) Mapping() *mapping.Mapping { return b.mapping }

// Store implementation over the append-only output (Rust impl Store for
// Builder).

// TargetTxn returns the output transaction (always 1).
func (b *OutputBuilder) TargetTxn() uint64 { return b.meta.TxnID }

// PageLimit returns the current page count.
func (b *OutputBuilder) PageLimit() uint64 { return b.meta.PageCount }

// Inspect returns one data page view (Rust inspect_page: the fetch
// runs inside the armed Output probe when a worker session is active,
// and directly otherwise, so the library writer path never allocates).
// Inspect returns one data page view for reading (Rust inspect_page +
// with_output_protection: the caller's inspection runs inside the armed
// output region). The window is armed before the fetch and released at
// the next store operation or Close, exactly like a mutation window, so
// a fault while the caller decodes the returned page records with the
// Output role.
func (b *OutputBuilder) Inspect(pageNumber uint32) ([]byte, error) {
	b.consumePageWindow()
	if err := requireOutputPage(pageNumber, b.meta.PageCount); err != nil {
		return nil, err
	}
	if err := b.armPageWindow(); err != nil {
		return nil, err
	}
	page, err := b.mapping.Page(pageNumber)
	if err != nil {
		b.consumePageWindow()
		return nil, err
	}
	return page, nil
}

// Allocate reserves the next output page (Rust allocate = reserve_page).
func (b *OutputBuilder) Allocate() (uint32, error) {
	b.consumePageWindow()
	return b.reservePage()
}

// reservePage is the single page allocation authority (Rust reserve_page):
// page-count overflow is PageSpaceExhausted, the budget bound is
// BudgetExceeded.
func (b *OutputBuilder) reservePage() (uint32, error) {
	if b.meta.PageCount == 1<<32 {
		return 0, &format.Error{Code: format.CodePageSpaceExhausted, Detail: "v4 page-number space is exhausted"}
	}
	if b.meta.PageCount >= b.budget.MaxOutputPages {
		return 0, budgetExceeded("immutable output pages")
	}
	page := uint32(b.meta.PageCount)
	b.meta.PageCount++
	work.PageCreated(1)
	return page, nil
}

// Update returns one data page view for mutation (Rust update_page +
// require_output_owner). The output region is armed before the page
// fetch and stays armed across the caller's mutation (Rust
// with_output_protection); the caller mutates the page and then calls
// RestoreDirty, which re-verifies the output ownership and releases the
// window. The output has no dirty chain, so the captured tag is always
// zero.
func (b *OutputBuilder) Update(pageNumber uint32) ([]byte, uint32, error) {
	if err := requireOutputPage(pageNumber, b.meta.PageCount); err != nil {
		b.consumePageWindow()
		return nil, 0, err
	}
	if err := b.armPageWindow(); err != nil {
		return nil, 0, err
	}
	page, err := b.mapping.Page(pageNumber)
	if err != nil {
		b.consumePageWindow()
		return nil, 0, err
	}
	return page, 0, nil
}

// RestoreDirty re-verifies the output ownership after a successful
// mutation or copy and releases the armed page window (Rust
// require_output_owner + probe drop inside with_output_protection).
// The output has no dirty chain, so the tag is never re-armed; the
// fetch runs inside the still-armed window.
func (b *OutputBuilder) RestoreDirty(pageNumber uint32, tag uint32) error {
	if err := requireOutputPage(pageNumber, b.meta.PageCount); err != nil {
		b.consumePageWindow()
		return err
	}
	page, err := b.mapping.Page(pageNumber)
	if err != nil {
		b.consumePageWindow()
		return err
	}
	if !outputPageOwned(page, b.meta.TxnID) {
		b.consumePageWindow()
		return corrupt("immutable output page ownership is invalid")
	}
	b.consumePageWindow()
	return nil
}

// CopyPage returns the source and destination page views of one copy;
// both views stay inside the mapping and the destination ownership is
// re-checked through RestoreDirty after the caller copies (Rust
// copy_page; both page fetches run inside the armed Output probe). The
// output has no dirty chain, so the destination tag is always zero;
// work.PageCopied counts the copy.
func (b *OutputBuilder) CopyPage(source, destination uint32) ([]byte, []byte, uint32, error) {
	if err := requireOutputPage(source, b.meta.PageCount); err != nil {
		b.consumePageWindow()
		return nil, nil, 0, err
	}
	if err := requireOutputPage(destination, b.meta.PageCount); err != nil {
		b.consumePageWindow()
		return nil, nil, 0, err
	}
	if err := b.armPageWindow(); err != nil {
		return nil, nil, 0, err
	}
	src, err := b.mapping.Page(source)
	if err != nil {
		b.consumePageWindow()
		return nil, nil, 0, err
	}
	dst, err := b.mapping.Page(destination)
	if err != nil {
		b.consumePageWindow()
		return nil, nil, 0, err
	}
	work.PageCopied(1)
	return src, dst, 0, nil
}

// DiscardPrivate refuses every discard: the output is append-only (Rust
// discard_private).
func (b *OutputBuilder) DiscardPrivate(pageNumber uint32) error {
	b.consumePageWindow()
	return corrupt("immutable output attempted to discard an append-only page")
}

// RetirePages refuses every non-empty retirement (Rust RetiringStore for
// Builder).
func (b *OutputBuilder) RetirePages(retired tree.RetiredPages) error {
	b.consumePageWindow()
	if retired.Len() == 0 {
		return nil
	}
	return corrupt("immutable output attempted to retire an existing page")
}

// requireOutputPage bounds one store page to the data area [2, pageCount)
// (Rust require_output_page).
func requireOutputPage(pageNumber uint32, pageLimit uint64) error {
	if pageNumber < 2 || uint64(pageNumber) >= pageLimit {
		return corrupt("immutable output page is outside bounds")
	}
	return nil
}
