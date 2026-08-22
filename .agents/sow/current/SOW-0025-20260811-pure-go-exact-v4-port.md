# SOW-0025 - Pure-Go Exact v4 Semantic Port

## Lead review rules (user decision, 2026-08-21; overrides SWARM.md)

These rules govern this SOW's review process and override the shared
swarm guide. They are placed at the top so they are re-read after every
compaction. SWARM.md is intentionally not updated: this SOW is the
authority for its own review process.

1. ONE review level only. No level-2 gate, no other models: only five
   reviewers, all on the lead's own model, each holding one disjoint
   aspect of the milestone scope, adversarial mode, running with the
   final-review skill.
2. Spawn all five once at first use; every later round is a message to
   the same agents. Never respawn between rounds; restart them only
   between milestones.
3. The milestone closes only when all five reviewers report no P0-P2
   findings. No reviewer is ever replaced by another model.
4. The Rust implementation is the mandatory baseline for ALL five
   aspects. The two implementations must be identical in logic,
   operation order, system calls, errors, and philosophy; the smallest
   divergence in how a thing is done is a performance and correctness
   red flag. Go must follow exactly what Rust does, while expressing
   that logic in natural, maintainer-grade Go.
5. Aspect split (all aspects are judged against the Rust baseline):
   (1) Rust parity - exact logic, operation order, syscalls, errors,
   mmap-only, memory safety, lifetime, durability/crash semantics;
   (2) Go idioms - identical logic and structure, but written as
   natural Go, never foreign or translated-looking;
   (3) absolute performance - the most performant form possible; even
   a single unnecessary branch, copy, or allocation in the hot path is
   a defect;
   (4) wire format and integrity - on-disk bytes, locking, Go<->Rust
   cross-open interoperability;
   (5) APIs, docs, records - public API, errors, documentation, and
   SOW records in sync with Rust.

Recorded as Review Process below.


## Status

Status: in-progress

### Status (2026-08-22) - chalk: slice B (public feed workflows) starts

Slice A (internal draft machinery) is gated PASS at HEAD b80295a /
da1e769 by all five aspects. Slice B implements the public v4/go
surface over the existing draft machinery, Rust baseline
live_writer/{feed_workflow,feed_lifecycle,membership,workflow}.rs,
feed.rs, feed_catalog.rs, workflow.rs, writer_core/{edit,core}.rs,
draft_store/{feed_merge,membership}.rs:

- Internal writer additions: base-meta feed lookup
  (Core.LookupBaseFeed), current-meta feed lookup and enumeration
  (Core.LookupCurrentFeed, feed cursor over the index tree),
  MembershipOperation apply (DraftStore.applyMembership over the
  existing rangeTransform + combineMemberships), feed membership
  delete (deleteCurrentFeedMembership), full-map comparison sweep
  (Core.CompareMaps, Rust workflow/compare.rs), the WriterEdit feed
  bindings, and a nonce-returning begin_transaction (Core.BeginTransaction).
- Public surface: FeedName validated binding, CreateFeed/ReplaceFeed
  (BeginCreateFeed/BeginReplaceFeed, AddRangesV4/V6(+Slice),
  FinishInput), FinishedWorkflow/PreparedWorkflow single Go handle
  (DirectTransaction precedent), PreparedFeedChange for
  RenameFeed/DeleteFeed, MembershipTransaction with
  FeedRef/MembershipRef/TransactionFeedCursor, and the public
  MembershipOperation enum.
- Tests: mirror Rust feed_workflow_tests.rs (three vectors with exact
  work counters and zero allocations), the membership transaction
  surface semantics (references, epochs, apply/ensure/rename/delete,
  cursor, metadata, commit/abort), work-counter and zeroalloc pins,
  and Rust cross-open at the close gate.

No open user decisions: the exact-feed workflows mirror the Rust public
API one-to-one (records: 2026-08-22 chunk-3b-5 entry), and the Go
handle-collapse precedent (DirectTransaction / FinishedHistoryProjection)
applies to FinishedWorkflow.

- Next: implement chunk B1 (internal writer), run the pinned battery,
  commit, then chunk B2 (public surface), then chunk B3 (tests) and the
  five-aspect close gate.

### Status (2026-08-22) - slice A round-6 cleanup: all five aspects PASS on b80295a

Round-5 residue landed on top of f1cedb6 and re-validated end-to-end
(HEAD b80295a):

- unionRun's duplicated 5-line insert_private_rejected comment was
  removed (range_coverage.go:677-679 retains the single copy).
- The F1 record now states the fits=false split path is not observed
  in the current test vectors (30/30 measured fits=true) instead of
  "currently unreachable", since growth splitting and rejected-gap
  slack are independent (see the F1 note for why no regression test
  pins the branch).
- Error-detail parity sweep completed: every Go Code*Exhausted
  production site now matches the Rust Display strings -
  PageSpaceExhausted "v4 page-number space is exhausted"
  (draft_store.go:468, output.go:686, range_bulk.go:356),
  MembershipIdExhausted "membership-ID space is exhausted"
  (membership_dictionary.go:140), StructureIdExhausted
  "structure-ID space is exhausted" (structure_dictionary.go:37),
  FeedIndexExhausted already exact (feed_catalog.go:103). Wire codes
  unchanged.

Full battery re-run on b80295a (all under nice): gofmt clean, go
vet, go test ./... and -tags v4work (both -count=1 and -race),
GODEBUG=checkptr=2, and the six GOOS/GOARCH cross-builds - all green.
Rust suite untouched.

All five aspects re-reviewed b80295a: PASS (Rust parity, Go idioms,
performance, wire format/integrity, APIs/docs). No open findings;
slice A remains gated PASS at this HEAD.

### Status (2026-08-22) - slice A parity-residue round: three unrecorded Rust-parity findings fixed (APIs/docs FAIL)

The APIs/docs re-review (HEAD b9ea00b) failed on records accuracy:
three Rust-parity findings from the first slice-A review were never
recorded in the SOW and remained open in code. All three were verified
against the code and the Rust baseline, and fixed:

- **F1 (unionRun position propagation).** unionRun ignored the fit
  result of insertPrivateRejected and returned hasPosition=true
  unconditionally; Rust union_run returns the insert_rejected_gap
  Option<PrivatePosition>, None when the rejected leaf must split
  (range_mutation.rs:131-143, coverage.rs:451-452). The Go split path
  returns a zero PrivatePosition, so the next edge-proven insert would
  raise a spurious "cached B+tree position is not its claimed edge".
  Fix: the fit flag now propagates (range_coverage.go). Reachability
  evidence: the fits=false split path is not observed in the current
  test vectors (30/30 probe rejects in a 4,000-record general tree
  measured fits=true), but it is not structurally unreachable: growth
  splitting and rejected-gap slack are independent. The fix guarantees
  the split path caches nothing, exactly like Rust, removing the
  latent landmine at zero cost. No unit test pins the fits=false
  branch: a fabricated full-header LocalReject would corrupt a real
  tree (EditLeaf writes the page), and Go and Rust share the same
  stale-edge corrupt guard, so the pin would only re-test shared code.
- **F2 (flushed edge lifetime).** finishPrivateUntracked cleared the
  cached edge after FlushEdge; Rust finish_private flushes via
  as_mut and keeps the edge (coverage.rs:233-242), so a later
  edge-proven insert reuses it instead of descending again. Fix: the
  flush keeps the edge (range_coverage.go). New v4work pin
  TestWorkUnionInputFlushKeepsEdge binds the invariant; proven
  binding by temporary bug reintroduction (pre-fix code fails it).
- **F3 (error-string parity).** pageSpaceExhausted detail was "range
  tree page space exhausted"; Rust displays "v4 page-number space is
  exhausted" (sdk_error.rs:356; wire code 25 identical). Fix:
  range_bulk.go matches the Rust Display string.

Validation (all under nice): gofmt clean, go vet, go test ./... and
-tags v4work (both -count=1 and -race), checkptr=2, and the
linux/386, linux/arm, linux/arm64, windows/amd64, darwin/arm64 and
freebsd/amd64 cross-builds - all green. Rust suite untouched.

### Status (2026-08-22) - slice A records-accuracy round: full battery on the committed HEAD, P3 hardening landed

The five-aspect re-review of HEAD c656b71 (the untracked-accounting
fix) returned four PASS and one FAIL:

- PASS: Rust parity, Go idioms, performance, wire format/integrity.
  The P1/P2 fixes were verified; the exact splice counters of the Rust
  vector hold in Go (coalesced 2000, tree lookups 8, cell probes
  < 2200).
- FAIL (APIs/docs, records accuracy only): the round entry claimed the
  full validation battery (go test ./... and -tags v4work with
  -count=1 and -race, checkptr=2, six cross-builds) without a recorded
  run on that HEAD. The full battery was re-run on this HEAD
  (b9ea00b) and the entry now matches what was executed; the P1 label now
  names the defect class (untracked accounting / Rust parity) with the
  reviewing aspect in parentheses, the coverage file is referenced by
  its full path, and the measured allocation baseline is stated as
  ~565.

Actionable non-blocking notes from the four PASS aspects were landed
in the same delta: rangeRecordAdded documents its end-to-end
accounting contract; the splice regression test also asserts that no
membership id other than the sealed feed is charged (pending slots
hold only member.id and the delta tree root stays 0); a new v4work
counter twin pins the Rust splice vector exactly
(TestWorkUnionInputSpliceLargeRun); and the rangeCtx.untracked field
comment warns that a marked context must never be reused for a
tracked operation.

### Status (2026-08-22) - slice A second reviewer round: one P1 and two P2 verified and fixed

The aspect-reviewer round on the encode-scratch fix (HEAD 4bd7f82)
returned three more findings, all verified against the tree:

- **P1 (defect class: untracked-accounting / Rust parity; found by
  the Performance-aspect reviewer): insert() bypassed the new
  untracked flag.** The wrapper removal converted every per-record
  accounting site except insert(), which still called
  ctx.store.RangeRecordAdded directly; on the marked-untracked
  coverage path the union-run splice (unionRun -> insert,
  v4/go/internal/writer/range_coverage.go:725) charged a spurious
  membership refcount delta into the draft (the old
  untrackedRangeStore wrapper made it a no-op). Verified end-to-end with the Rust
  private_constant_union_splices_a_large_run... vector through the
  empty-map feed: before the fix the member delta was +2 (splice +1,
  sealed count +1), after the fix exactly +1. Fix: insert() routes
  through rangeRecordAdded; replaceStrictlyInside charges through the
  new storeRecordAdded helper; regression test
  TestFeedMergeEmptyMapSpliceStaysUntracked binds the invariant.
- **P2: encodeRecord had no slot bounds check** (range_edit.go) - a
  derived slot would panic instead of returning the library error
  class. Fix: range-encode slot index validated against the scratch
  array, corrupt error on violation.
- **P2: the general-path alloc ceiling was loose enough to admit one
  new per-record allocation** (826 for 256 records allowed 3.0/record
  vs the 1.98 baseline). Fix: ceiling tightened to 800 (baseline ~565;
  a +1/record regression lands at ~820). The slope-based alternative
  was measured and rejected: the generic gap machinery allocates
  non-linearly (2.89/record at 512 records vs 1.98 at 256), so the pin
  uses one fixed measurement with a documented arithmetic margin.

Validation (all under nice): gofmt clean, go vet, go test ./... and
-tags v4work (both -count=1 and -race), checkptr=2, and the
linux/386, linux/arm, linux/arm64, windows/amd64, darwin/arm64 and
freebsd/amd64 cross-builds - all green. Rust suite untouched.

### Status (2026-08-22) - slice A five-aspect reviewer round: one P2 verified and fixed (per-record encode escape)

The first five-aspect review of the slice A delta (HEAD c2ccd50)
returned one verified blocker:

- **Aspect 2 (Go idioms) FAIL - P2: the carried P3-A (newEncodedRange
  heap-escape per range-record edit) became live in slice A.** The
  coverage-union and feed-merge machinery added live callers of
  `newEncodedRange` on the per-record path (range_coverage.go
  insertPrivateEdge/mergeRejected, range_locator.go probeCached,
  range_edit.go insert/insertPrivateGap/insertPrivateRejected/
  replaceStrictlyInside), while the milestone-1 close record required
  the writer-owned encode scratch to land exactly where callers become
  live. Measured before the fix (fresh-state AllocsPerRun, warmup
  cancelled): ordered-prefix path 1.00 alloc/record, general fallback
  6.97 allocs/record.

Fix applied on top of c2ccd50:

- `rangeCtx` owns a fixed-size encode scratch
  (`encodeScratch [3][format.RangeRecordV6Size]byte`) and the new
  `rangeCtx.encodeRecord(slot, r)` method renders one record into a
  slot, mirroring the Rust `EncodedRange::new` local `[u8; 36]`; the
  generic `rangeFamily` interface otherwise makes stack targets escape
  (range_edit.go). Slot 0 serves the one-record paths; slots 0..2
  serve the up-to-three-cell leaf replacement (Rust
  replace_strictly_inside). `newEncodedRange`/`encodedRange` removed.
- `rangeCtx` gained the `untracked` accounting flag replacing the
  per-record `untrackedCtx` wrapper context of the coverage input
  (`rangeCtx.markUntracked`, range_coverage.go): the same-slice hot
  path allocated one wrapper context per record (the largest remaining
  allocation in the post-fix profile).
- New permanent pins `TestSliceAOrderedUnionAllocCeiling` and
  `TestSliceAGeneralUnionAllocCeiling` (slicea_union_input_alloc_test.go)
  fail if a per-record allocation returns.

Measured after the fix (same harness): ordered-prefix path 0.00
alloc/record, general fallback 1.98 allocs/record. The remaining
general-path cost is the generic tree gap protocol (rejection,
probePredecessor, privatePathSelect escapes), which is the recorded
P3-B optionalCell follow-up of the milestone-1 close gate; it stays
tracked for its slice C slot rather than being redesigned inside this
fix.

Validation (all under nice): gofmt clean, go vet, go test ./...
and -tags v4work (both -count=1 and -race), checkptr=2, and the
linux/386, linux/arm, linux/arm64, windows/amd64, darwin/arm64 and
freebsd/amd64 cross-builds - all green. Rust suite untouched
(Go-only edits, no conformance corpus change; re-run at the close
gate).

### Status (2026-08-22) - slice A union-input tests: correctness and work pins, plus one merge-cardinality bug

The slice A feed-merge and coverage-union machinery is now pinned by
focused internal tests mirroring the Rust vectors, all running over the
real opened mapping (no owned page exists anywhere):

- `slicea_feed_merge_test.go` (fixed and extended): add-feed-to-membership
  interning and its corrupt classes (draft_store/membership.rs +
  membership_dictionary.rs), rename/remove current-feed lifecycle
  (feed_catalog.rs, hole reuse, NameExists class), the empty-map feed
  trio (begin/add/finish) including its non-empty-tree guard, the exact
  no-change result of a created feed with empty coverage, and the
  created-feed merge vector over the committed destination with bit
  probes on the interned union records.
- `slicea_union_input_test.go` (new): the Rust range_mutation_tests.rs
  correctness vectors - random-order buffered union matching a
  512-address scalar reference (2,000 LCG operations), the pending-gap
  rebridging vector [(35,45),(15,32),(30,38)] -> [(15,45)], the
  2,000-key queue-normalized single interval, the late-overlap general
  fallback in both address families, and the unordered empty-map feed
  over overlapping ranges.
- `slicea_union_input_work_test.go` (new, v4work build tag): the Rust
  work::measure vectors - queue normalize (emitted 1 / coalesced 1999 /
  lookups 0), ascending packed construction (lookups, splits and output
  passes all zero; ordered count 4000), monotonic edges (lookups equal
  splits, exactly one edge-path check, structural fence bound), random
  order lookup bound, the 20,000-input leaf-locator hint hit rate, and
  the 100,000-input IPv4/IPv6 assignment locator split (hits > N/3,
  no hint work for IPv6). Total v4work runtime of the new pins: ~0.6 s.

One real bug was found and fixed by these tests: `newFeedPolicy` built
the feed policy with its own family set but never initialized the
embedded `feedProjection.family`, so every created-feed merge over an
IPv4 base counted segments as IPv6 (the projection fell into the IPv6
cardinality branch). `newFeedPolicy` now fixes the projection family at
construction exactly like the history plan (history.go), mirroring the
Rust generic FeedPolicy<K> whose family is fixed at compile time. The
merge vector test's original expected values (added 11 / removed 23 /
four member-only records) were wrong for the real FeedPolicy semantics;
the corrected vector asserts added = covered cardinality (12), removed
0, the seven canonical segments, and the union bitmaps by feed-bit
probes.

Validation (all under nice): gofmt clean, go vet, go test ./... and
-tags v4work (both -count=1 and -race), checkptr=2, and the
linux/386, linux/arm, linux/arm64, windows/amd64, darwin/arm64 and
freebsd/amd64 cross-builds - all green. Rust suite untouched (Go-only
edits, no conformance corpus change; re-run at the close gate).

- Next: slice A open items before the feed workflow surface (slice B):
  confirm with the five-aspect reviewers on this delta, then continue
  the feed lifecycle and workflow-range draft fields.

### Status (2026-08-22) - five-aspect close gate: 2 FAIL verdicts, both fix batches applied

The five-aspect adversarial close gate (lead-model reviewers, final-review
skill, Rust baseline) ran over the chunk 3b-4 delta
`11003d8..925e114` (allocation pass 5,169 -> 39 allocs/run, slices A+B+C,
corpus extension). Verdicts and fixes:

- Aspect 5 (APIs/docs, Aristotle): PASS. 4 P3s, actionable ones fixed in
  `db1307a` (FeedName doc, fixture comment, SOW commit-state wording,
  commit-path alloc pin).
- Aspect 3 (performance, Harvey): PASS. 3 P3s; the commit-path allocation
  pin and AllocsPerRun(4) ceiling landed in `db1307a`; carrying a
  wall-time benchmark was recorded as a post-close bench item, not a
  blocker (test-only necessary-work counters already pin work).
- Aspect 4 (wire/integrity, Newton): PASS. P2.1 the gate commit moved
  while reviewing (re-affirmed below); P3.1 short membership blob reads
  now fail closed instead of trimming (`d9c00cc`).
- Aspect 2 (Go idioms, Anscombe): FAIL -> fixed in this batch.
  - P2-1 per-lookup heap escape in findMembership: membershipFound
    now stores tree.LeafLocation by value plus a located bool
    (membership_dictionary.go); `-gcflags=-m=2` no longer reports the
    location escape. (Same defect as Aspect-1 F1.)
  - P2-2 history prefix panic: historyPrefixWordCount now returns
    corrupt("nonempty history prefix has no feeds") like Rust
    Error::Corrupt, and prefixWords carries its word count so
    WordCount() does not re-scan (history.go).
  - P3-3 retire.go predecessor/atOrAfter returned pointers to locals;
    the retire package now returns (Extent, bool, error) through
    First/After/scanGroup/classify/applyPage (retire.go, draft_prepare).
  - P3-4 writer range_edit.go readPredecessor/readAtOrAfter returned
    pointers; now (rangeRecord, bool, error) through segmentAt,
    rangeReplaceWithHint, trimPredecessor, trimFollowing,
    mergePrevious/Next.
  - P3-5 RetiredPages API shape kept as-is: the tree returns the fixed
    array by value and the bitmap dictionaries accumulate in place
    because Rust passes RetiredPages by &mut and Go mirrors both
    shapes; both are allocation-free (verified with -gcflags=-m=2).
  - P3-6 all 23 `&4095` cancellation cadences in writer and reader are
    now one generic checkEvery[T integer] helper per package
    (membership_algebra.go, history.go, membership_query.go and every
    reader loop); the cadence and the nil-check are defined once,
    identical to Rust CancellationToken::check cadence.
  - P3-7 tree.LocalReject/LocalInsert/EdgeInsert became generic
    (rangeRecord, u32 codecs); the any-boxed neighbor cells are now
    typed values, removing the per-rejection interface boxing and the
    caller-side type assertion (gap.go, range_edit.go).
  - P3-8 feed-name validation moved to the plan boundary only, like
    Rust FeedName::new: collectHistoryWindows refuses invalid window
    names with CodeNameInvalid (pinned by sliceb_history_test.go) and
    the internal catalog operations document the pre-validated input
    contract with the encoder's corrupt guard as the caller-bug backstop
    (feed_catalog.go, catalog_codec.go); feed_catalog_test.go was
    updated to pin the new layering.
- Aspect 1 (Rust parity, Meitner): FAIL -> fixed in this batch.
  - F1 per-lookup allocation in findMembership: same fix as Anscombe
    P2-1.
  - F2 corrupt-path detail mismatch: findMembership now returns
    located=false and each caller attaches its Rust detail; the
    membership-algebra path emits corrupt("range membership ID is
    missing") exactly like Rust algebra.rs stored_word_count, the
    other paths keep "membership ID is missing"
    (membership_dictionary.go, membership_algebra.go).
  - F3 history prefix panic: same fix as Anscombe P2-2.
  - F4 empty-catalog TreeLookup counter: reader LookupFeed and
    LookupFeedIndex now charge work.TreeLookup before the absent-root
    shortcut, mirroring Rust fixed_tree::query (catalog.go, index.go).
  - F5 membership blob-root encode branch: newMembershipEncoded now
    returns corrupt("membership blob root is invalid") for blob roots
    below 2 before the invalid-argument limit check, exactly like Rust
    codec::Encoded::new (membership_codec.go).
- Recorded parity decision (counter granularity): Go charges
  work.BytesMoved at store-layer logical operations (tag, marker, seal
  stamps; draft_work_test pins 16/8/32) while Rust charges per mapped
  write; Rust asserts no bytes_moved totals anywhere, so no
  cross-language counter total is pinned or diverged. The counters are
  test-only no-ops; both sides pin the same per-operation invariants.
  Accepted as documented, no code change.

Validation of the fix batch (all under nice): go test ./... and -tags
v4work (both -count=1 and -race), go vet, gofmt clean, checkptr=2,
GOOS windows/amd64 + darwin/arm64 + freebsd/amd64 + linux/arm64 builds,
the two allocation ceilings (39 and 84/run, unchanged), the Rust suite
and --all-features - all green. Delta for the re-review pin:
`11003d8..<final commit of this batch>`.

- Next: re-review of the fix batch by Aspect 2 (Anscombe) and Aspect 1
  (Meitner) on the pinned commit; when all five report no P0-P2, close
  milestone 1 (push), then start the next M3 surface (feed workflows).

### Status (2026-08-22) - fd171b6 re-review residue fixed: parity and 32-bit cross-build batch

The Aspect-1 (Meitner) and Aspect-2 (Anscombe) re-reviews of
`11003d8..fd171b6` came back with verifiable residue; the Aspect-1 items
were each verified against the Rust source before editing. This batch
fixes all of them, applies the two remaining P3 dispositions, and pins
the new HEAD for the final re-review round:

- F-A: equalMembershipWords corrupt detail now reads
  "membership hash points to a missing ID", exactly like Rust
  membership_dictionary.rs stored_word_count
  (membership_dictionary.go).
- F-B: findMembershipBlobLeaf no longer allocates per tree level; the
  expected level is a value plus a set flag, mirroring Rust Option<u16>
  by value (membership_blob.go; verified no `new(uint16)` escape at
  -gcflags=-m=2).
- F-C: DraftStore.lookupFeed charges work.TreeLookup(1) before the
  absent-root shortcut, matching Rust fixed_tree::query cadence
  (feed_catalog.go).
- F-D1: the unreachable "membership inline record lost its leaf
  location" branch was deleted; the located flag is guaranteed by the
  found-record invariant, and Rust has no such branch
  (membership_dictionary.go).
- F-D2: pushMembershipBlobBranch propagates the raw b.Push error
  instead of wrapping it, matching Rust blob write branch propagation
  (membership_blob.go).
- P1-1 (Anscombe): prepareHistoryPlan compares uint64(windowCount)
  against math.MaxUint32 instead of the raw int, which did not compile
  on 32-bit targets (GOOS=linux GOARCH=386/arm now build)
  (history.go).
- P3-1: the writer's last pointer-Option residue, range_edit.go
  change/segment/writeReplacement and the transform operation, now
  carry optionalValue{value, present} by value like the ordered-merge
  hot path already did (range_edit.go, range_edit_test.go). This also
  removes the per-call &value escape when the compiler does not inline
  the assignment entry points.
- P3-4: tree/gap.go carried two byte-identical neighbor-cell structs;
  merged to one rejectCell type (Rust LocalReject stores a single
  Option<(usize, R)> shape), removing the needless duplicate.
- P3-2 kept by decision: rangeTransform has no production caller yet
  (Rust callers draft_store/membership.rs and structured.rs are the
  next milestone slice); it is the Rust-parity foundation for that
  slice, so it stays under test instead of churning the gate delta.
- P3-3 kept by decision: RetiredPages returns by value from the tree
  while the bitmap dictionaries accumulate in place, mirroring Rust
  &mut vs owned shapes; both forms are allocation-free (verified at
  -gcflags=-m=2).

Validation (all under nice): go test ./... and -tags v4work (both
-count=1 and -race), go vet, gofmt clean, checkptr=2, GOOS/GOARCH
linux/386, linux/arm, linux/arm64, windows/amd64, darwin/arm64 and
freebsd/amd64 builds, and the two allocation ceilings (39 and 84 per
run, unchanged) - all green. The Rust suite is untouched by this
batch (Go-only edits; no conformance corpus change).

- Next: re-review of this batch by Aspect 1 (Meitner) and Aspect 2
  (Anscombe) on the pinned commit (the commit this entry ships in);
  when all five report no P0-P2, close milestone 1 (push), then start
  the next M3 surface (feed workflows).

### Status (2026-08-22) - milestone 1 close gate: all five aspects PASS on 11003d8..499a0e3

Round-2 re-reviews of the residue batch `fd171b6..499a0e3` (pinned
milestone delta `11003d8..499a0e3`, HEAD `499a0e3`, signed, worktree
clean):

- Aspect 1 (Rust parity, Meitner): PASS. All five F-A..F-D2 fixes
  verified line-by-line against `membership_dictionary.rs`,
  `blob_tree.rs`, `fixed_tree/query.rs`, `membership_dictionary/blob.rs`;
  the optionalValue and rejectCell conversions were verified against
  `range_mutation/assign.rs` and `fixed_tree/gap.rs`; fresh
  -gcflags=-m=2 shows no remaining value escapes; full validation
  matrix green at the pinned HEAD. mmap-only/durability statement
  re-confirmed for the whole delta.
- Aspect 2 (Go idioms, Anscombe): PASS. P1-1 (uint64 boundary compare)
  verified against the previously failing linux/386 and linux/arm
  builds, now green; P3-1 (optionalValue in range_edit.go) and P3-4
  (single rejectCell) verified in source and in the escape report; the
  P3-2 and P3-3 dispositions are recorded here and accepted; full
  validation matrix green at the pinned HEAD.
- Aspects 3, 4 and 5 (Harvey, Newton, Aristotle) passed the milestone
  scope on `11003d8..fd171b6`; the residue batch touches no public
  API surface, no wire bytes and no conformance fixture (both
  re-reviewers re-ran the full cross-open/alloc/race battery at
  `499a0e3`), so their PASS stands for the pinned delta.

Carried P3s (all on the range-edit draft path, which has no production
caller until the next milestone ports draft_store membership/structured
applies; all tracked as requirements of that slice, see the next-milestone
entry):

- P3-A: newEncodedRange heap-escapes per range-record edit
  (range_edit.go:49-56, interface EncodeRecord call). Pre-existing at
  11003d8, not on the ProjectHistory path, not covered by the 39/84
  ceilings. Fix plan (validated by Aspect 1): rangeCtx carries a
  writer-owned [RangeRecordV6Size]byte encode scratch and
  EncodeRecord writes into it, mirroring Rust EncodedRange::new
  writing a local [u8; 36] by value.
- P3-B: the closed-gap rejection path allocates two rejectCell structs
  (tree/gap.go:458-467). Pre-existing, cold assign-with-hint retry
  path, returned-by-design pointers. Fix plan: an optionalCell
  value+present pair when the draft-edit path gets an alloc ceiling.
- P3-C: segmentAt returns *segment (one escape per transform segment)
  (range_edit.go:151-169). No production caller today. Fix plan:
  return segment by value when the transform walk becomes live.

No P0-P2 findings remain on the milestone delta. Milestone 1 (pure-Go
exact v4 reader core) CLOSED: pushed as master HEAD 499a0e3, and the
next milestone (M3 surface: feed workflows) starts from the pushed state.
Follow-up mapping: the three carried P3s above are the first recorded
requirements of the next milestone's range-edit slice; no other deferred
items exist in this SOW.

### Status (2026-08-22) - chunk 3b-5 defined: complete feed workflows (M3 surface)

Milestone 1 (reader core + history projection) is closed and pushed at
HEAD 19bf997. Next M3 surface: the complete named-feed workflows of the
Rust live writer. Go has the value-free report types, the ordered merge
base, the draft feed catalog (lookup/ensure/insert/allocate), the draft
membership dictionary (combine/contains-indexes/intern), the range edit
core, and the history workflow; it lacks the feed input machinery, the
feed merge policy, the feed lifecycle ops, the membership/structured
transaction surfaces, and the structured draft apply paths.

Rust authority: live_writer/{feed_workflow,membership,structured,
feed_lifecycle}.rs, live_writer/workflow.rs, draft_store/{feed_merge,
membership,structured,storage,timestamp_refresh,import_merge,
import_cache}.rs, range_mutation/{coverage.rs,coverage/input.rs,
assign.rs}, writer_core edit bindings, feed.rs (FeedName), and the
workflow report/comparison helpers; tests in feed_workflow_tests.rs,
live_writer workflows and draft_store tests.

Slices (disjoint write scopes, exact Rust baseline):

- A (internal/writer draft machinery): the range-mutation coverage and
  assignment inputs (UnionInput/UnionState/OrderedPrefix/
  UnionAssignmentInput, push/finish input untracked, private constant
  ranges, assign_private_input), the empty-map feed trio, the feed
  merge policy + projection + merge_coverage over the existing
  orderedMerge, the draft feed lifecycle (addFeedToMembership with
  intern_added_bit, rename/remove current feed), and the workflow-range
  draft fields. Go tree PrivateEdge/LocalInsert already exists.
- B (public v4/go): FeedName binding, CreateFeed/ReplaceFeed workflows
  (begin_create_feed/begin_replace_feed, add_ranges_v4/v6(+slice),
  finish_input -> FinishedWorkflow/PreparedFeedChange), the
  MembershipTransaction surface (FeedRef/MembershipRef/
  TransactionFeedCursor, apply_v4/v6, empty_membership, add_feed,
  lookup/ensure/rename/delete feed, metadata, commit/abort), the
  workflow completion/classification/report machinery shared with the
  history projection, and tests mirroring feed_workflow_tests.rs and
  the membership/structured transaction tests, plus work-counter and
  zeroalloc pins and Rust cross-open evidence at the close gate.
- C (internal/writer structured apply + public StructuredTransaction):
  draft intern_network_enrichment_v1 (payload encode + structure
  refcounts + membership owner refcounts), assign_structure_input_v4/v6
  with the assignment input, clear_v4/v6, delete_current_structured_feed
  (remove_feed_from_structure payload re-intern), finish_structure_deltas
  (structure_dictionary apply_delta + released-membership accounting),
  the public StructuredTransaction (StructureRef, assign/clear,
  metadata, commit/abort), and tests mirroring the Rust structured
  workflow tests.
- The carried P3s from the milestone-1 gate (newEncodedRange encode
  scratch, segmentAt by value, optionalCell for reject cells) land in
  slice A/C where their callers become live, as recorded.

Recorded decisions (no open user decision): live sidecar remains M4
(no import/lock/refresh workflows); the feed workflows are the exact
ranges-over-membership create/replace and the transaction surfaces,
mirroring the Rust public API one-to-one including reference epochs
(StaleReference/ForeignReference), empty-map fast path, and the
logical change classification.

- Next: slice A implementation on the pushed state, then B, then C,
  then the five-aspect close gate like every chunk.

### Status (2026-08-22) - allocation P1 on the warm ProjectHistory path: 5,169 to 39 allocations per run (committed at 925e114)

The warm ProjectHistory projection path (source 1000 last-seen points,
three destination windows, abort) allocated 5,169 heap objects per call
before this pass; it now allocates 39 - all per-projection or per-window
objects with Rust call-pattern parity, none proportional to the input
record count. The fixes, in order:

1. tree.Key inline hash keys: Key carries an optional 40-byte Raw value
   (tree.RawKey) so the hash-tree probe keys of the membership
   dictionary stop escaping through the generic Codec interface
   (tree/codec.go, writer/membership_dictionary.go). Removed ~700/run.
2. bitmap.AllocateLowestID exhaustion callback: the error value changed
   to a func() error built only on exhaustion (Rust parity), removing
   the per-allocation closure escape (bitmap/used.go, the two
   dictionaries). Removed ~300/run.
3. sha256 digests: hasher.Sum(digest[:0]) replaces Sum(nil)+copy at the
   three codec sites. Removed ~300/run.
4. membershipWords interface: ReadWords(start, output []uint64) became
   ReadChunk(start) ([64]uint64, count, error) (Rust HASH_WORDS), so
   the 64-word batch buffer no longer escapes through the generic
   interface (membership_dictionary.go, membership_blob.go). Removed
   ~300/run.
5. bitmap used-cursor: start/touchChild return a usedCursor value and
   every consumer takes it by value. Removed ~400/run.
6. historyPlan.begin moved to a pointer receiver. Removed ~200/run.
7. historyPolicy gained a scratchWords prefixWords field so the prefix
   decode reuses the bound instead of escaping a stack value through
   generic dispatch. Removed ~300/run.
8. Public FeedName became string ([]byte before): the Rust FeedName is
   a 256-byte value, and a Go string is the natural immutable value; it
   removes the per-window conversion and append-grow copies
   (logical_types.go, history_projection_public.go, public tests).
   Removed ~700/run.
9. MetaCRC32C padding: the [4]byte zero literal became a package-level
   var. Removed ~200/run.
10. Writer-owned encode scratches (the remaining per-record escapes):
    encodeMembershipRecord, encodeCatalogRecord, and writeMemberDelta
    now write into caller-held bounded arrays on DraftStore,
    OutputBuilder, or deltaPending (writer/membership_dictionary.go,
    writer/catalog_codec.go, writer/membership_delta.go,
    writer/draft_store.go, writer/output.go). The Go generic tree
    interface makes stack encodes escape; the Rust borrow checker keeps
    them on the stack, so the writer owns the bounded targets - a draft
    is single-threaded and every tree insert copies the record into the
    mapped page before the next encode reuses the buffer. Removed the
    last ~700/run. This also fixed a latent buffer-reuse bug: the
    membership record head only wrote the blobRoot slot for blob
    records, so an inline record encoded into a reused buffer kept the
    previous blob root and failed validation (masked while every encode
    used a fresh make() buffer).

The permanent gate is TestProjectHistoryAllocCeiling
(v4/go/history_projection_alloc_test.go): AllocsPerRun on the warm
path with a documented ceiling of 50 and a breakdown comment; the
temporary probe files were deleted. Remaining per-run costs are the
inherent ones: the report vectors (4 makes sized by window count, Rust
Vec parity), the plan/merge/draft-store/cursor objects, and the two
fstat sequences of the abort path (require unchanged base + trim +
verify, exactly the Rust discard_unpublished call pattern).

Validation (all under nice): go test ./... and -tags v4work (both
-count=1 and -race), go vet, gofmt clean, checkptr, GOOS
windows/darwin/freebsd and linux-arm64 builds, Rust full suite and
--all-features, Go conformance and mixed subprocess cross-open - all
green. The allocation test is deterministic at 39/run.

- Reviewed by the five-aspect close gate (see the entry below); the
  gate's fix batches are committed and the re-review proceeds on
  delta 11003d8..HEAD.

### 2026-08-21 - user decision: the gate scanner and its mutation corpora are deleted; the gate is full-codebase review

The pure-Go SDK is a small, simple file-format SDK. Reasonable tests are
CI-grade: well under a minute, at most a couple of minutes, single-core.
The mutation corpora and the whole-program scanner that re-analyzed the
entire module once per test case (~9 core-hours per run) are deleted with
this entry: the scanner tool directory and its shell harness
are removed, and every reference to them is purged from the live status
and current guidance; dated historical entries remain in the execution
log and the Status History appendix (preserved also in git history per
the user instruction to commit first, HEAD d16c88e).

The mmap-only and file-I/O policy gate is now a full-codebase adversarial
review: four concurrent fresh reviewers on the lead's own model read the
ENTIRE Go codebase, file by file - two hunt complete-page copies into or
out of the mmap, two hunt file I/O on persistent content outside the mmap.
Every .go file, every build-tagged variant, tests included.

### Status (2026-08-21) - chunk 3b-4 defined: history projection (draft_store/history.rs + live_writer/history_projection.rs)

- Next M3 chunk after the 3b-3 snapshot close: the last-seen history
  projection. Rust authority:
  v4/rust/iprange-livedb/src/draft_store/history.rs (one-pass
  last-seen map projection into named feeds: collect windows, unique
  names, ensure feeds, cutoff ranking, feed-index ordering, prefix
  interning, per-window before/after counts), its OrderedMerge base
  (draft_store/range_merge.rs), the committed-range cursor
  (range_store_cursor.rs), the draft membership algebra
  (membership_dictionary/algebra.rs contains_indexes + combine,
  draft_store/membership.rs ensure paths, draft_store/catalog.rs
  ensure_feed), and the live workflow surface
  (live_writer/history_projection.rs + workflow.rs prepared
  operation, metadata staging, commit/abort); tests in
  tests/history_projection.rs and
  live_writer/history_projection/tests.rs. Go has only the value-free
  report types (logical_types.go:82-119): no implementation calls
  them, and the writer lacks the ordered merge, the committed-range
  cursor, the draft feed catalog, the membership
  combine/contains_indexes algebra, and the prepared-workflow state.
- Slices (disjoint write scopes, exact Rust baseline):
  - A (internal/writer foundations): MembershipOperation enum plus
    dictionary contains_indexes and combine (membership_dictionary/
    algebra.rs; identity shortcuts, canonical word counts, chunked
    operand reads, HASH_WORDS-bounded scratch), the draft feed
    catalog (feed_catalog.rs lookup/insert/allocate feed index over
    the dual name/index trees and the feed used bitmap), the bounded
    heap charge helper (heap.rs parity with the "history projection
    heap" labels), range inclusive-cardinality helpers for both
    families (key.rs inclusive_cardinality), and the three new
    necessary-work counters source_passes, history_window_tests,
    membership_combinations (no-ops in production).
  - B (internal/writer merge + workflow state): the committed-range
    forward cursor over the base generation through the draft
    mapping (range_store_cursor.rs SelectedStore validation), the
    ordered old/input merge (range_merge.rs: Incoming, OrderedMerge,
    Policy, coalescing Output over the existing rangeBulkBuilder,
    RefcountRun over the membership dictionary), the history
    plan/merge/policy with prefix interning and per-window observe
    (draft_store/history.rs verbatim, including cancellation
    checkpoints and error classes), and the Draft/Core workflow
    state machine (begin_membership_workflow, workflow_input_open/
    active, abandon, draft finish, mutate semantics, no-change
    discard).
  - C (public v4/go): Writer.ProjectHistory with an immutable source
    and cancellation, FinishedHistoryProjection (Report/Changed/
    Commit/Abort, set/clear metadata on the changed handle),
    CommitResult/AbortResult mapping, plus tests mirroring
    tests/history_projection.rs and the module-work pins
    (zeroalloc/v4work), and the Rust cross-open evidence at the
    close gate.
- Recorded decisions (no open user decision):
  1. Live source refusal: the Go boundary takes the immutable reader
     only and refuses Live with the Rust Unsupported class
     (OsUnsupported 58) and detail "live history source requires the
     live sidecar coordination (Milestone 4)", exactly like the
     snapshot Live refusal; LiveReader is approved M4 scope.
  2. The Rust FinishedHistoryProjection enum collapses to one Go
     handle (DirectTransaction precedent): Report and Abort work on
     both variants (Abort on a no-change result reports
     NoPendingTransaction, Rust FinishedHistoryProjection::abort
     parity), Commit/SetMetadataJSON/ClearMetadataJSON require the
     changed variant and fail with the Rust class of the equivalent
     unreachable call. The PreparedState captures the cancellation
     token at prepare and checks it during commit exactly like Rust
     commit_operation. Go has no Drop hook: a changed prepared
     handle keeps the draft owned until Commit/Abort/Close, the
     DirectTransaction ownership pattern (documented divergence).
  3. Destination feeds for the Rust-parity multi-window scenario are
     built white-box through the newly ported draft feed catalog and
     membership interning (Go has no public create_feed workflow
     yet; feed workflows are the next M3 surface). Public tests
     cover the reachable surface: created feeds on empty
     destinations, the no-change rerun, invalid request classes, the
     full IPv6 space count, and the aborted-draft recovery.
  4. Heap accounting mirrors Rust heap.rs: every retained plan
     vector charges the draft budget MaxHeapBytes under the
     "history projection heap" labels (BudgetExceeded class), and
     the prefix bitmaps read through caller-owned word buffers only
     (no mapped views reach the dictionary read), the OutputWords
     contract.
  5. The old-range cursor reads the committed base generation with
     base-meta selection (Rust SelectedStore: selected txn and page
     count from base.meta, never the draft), so the merge compares
     the source scan against the committed destination exactly like
     Rust; release_private is false (history never consumes the
     base tree).
- Next: slice C (the public Writer.ProjectHistory facade), then the
  five-aspect review at the close gate like every slice.

### Status (2026-08-22) - chunk 3b-4 cross-open evidence committed: Go-produced history destination (HEAD 11003d8)

- The shared conformance corpus now carries a Go-produced membership
  destination built through the public Writer.ProjectHistory workflow:
  v4/conformance/go/history-membership-ipv4.iprdb (69 KB), source =
  1000 singleton last-seen points (last_seen 10+index%3), windows
  one/two/three with exclusive cutoffs 9/10/11 so the feeds keep
  1000/666/333 points (manifest breakdown 334 one-only, 333 one+two,
  333 one+two+three, 3 feeds, address_count 1000). cases.json now
  lists nine fixtures; the Rust conformance suite opens and verifies
  every range, feed projection, and cardinality of the new file, the
  Go conformance inventory includes it, and both mixed subprocess
  gates check the feed catalog and record count from a fresh process.
- The two older Go fixtures were regenerated with the current writer
  during the corpus extension. The regenerated bytes differ from the
  committed originals in the meta-page database_id (offset 32-47) and
  commit_nonce (offset 56-71) fields and in the page checksums that
  cover them; both readers verified the observable data (format, range
  records, feed catalog) is byte-identical between the regenerated and
  committed files, and the corpus contract allows non-identical bytes
  when observable data is equal.
- Go regeneration path: TestRegenerateGoFixtures gained
  regenHistoryMembershipIPv4 and republishes all three Go files after
  staging verification.
- Validation at HEAD: go test ./... and -tags v4work (both -race),
  checkptr (-gcflags=all=-d=checkptr=2), go vet, gofmt clean, GOOS
  windows/darwin/freebsd and linux/arm64 builds on both tag sets,
  Rust full suite and --all-features, Rust conformance and mixed
  subprocess, Go conformance and subprocess cross-open - all green.
  Tests finish in seconds; no scanner runs.
- Next: the five-aspect adversarial close gate for chunk 3b-4
  (delta 99166af..11003d8, slices A+B+C and the corpus extension),
  then the next M3 surface (feed workflows).

### Status (2026-08-22) - chunk 3b-4 slice C COMMITTED: public Writer.ProjectHistory facade (HEAD b9e942f)

- Slice C (the public feed-workflow facade) committed at b9e942f:
  - history_projection_public.go: Writer.ProjectHistory with the Rust
    request classification order (feed-workflow-ready, window count,
    source kind/reader, direct value kind, last_seen tag, family,
    same-file identity through device+inode, cancellation), the
    allocation-free drive through the reader direct cursors with the
    canonical-source checks and overflow classes, the finished handle
    with the no-change discard and the changed workflow finish;
  - FinishedHistoryProjection: report, abort (no-change
    ErrorNoPendingTransaction), SetMetadataJSON/ClearMetadataJSON with
    the cancellation checks and abort-on-error, Commit with the
    DirectTransaction sequence and the result status mapping;
  - internal/writer/history_workflow.go: BeginHistoryProjection /
    Push4/Push6/Finish binding the plan and merge inline per call (no
    closures on the per-record drive).
- Core fix found by slice-C validation: the two-commits-on-one-writer
  path failed at the second RequireUnchangedBase because the in-memory
  format.Meta struct carried the stored page checksum, which encode
  recomputes and never reconciles (Rust MetaV4 has no checksum field
  at all). The field is removed; ParseIdentity verifies the checksum
  locally. Pinned by writer_double_commit_test.go. This is a pre-fix
  P1-class parity bug that would have broken every second writer
  transaction.
- Work-counter parity fix: the public drive double-charged
  RangeConsumed (the reader direct cursor already charges it, Rust
  range_cursor::next parity). The drive now charges only the logical
  input-source and source passes.
- Tests: history_projection_public_test.go (multi-window vector with
  the exclusive-cutoff counts 1000/666/333, full IPv6 space, aborted
  draft recovery, metadata stage with the TransactionAborted class of
  the refused second stage, invalid requests, unrelated-feeds rerun,
  no-change abort), history_projection_v4work_test.go (Rust
  one_source_pass pins: 1 input pass, 3 source passes, 1000 consumed,
  1000 emitted, 1 output pass, 3000 window tests; 64 unused prefixes
  intern nothing), history_projection_zeroalloc_test.go (BySize >=
  4096 heap objects stay flat across 64 project/abort runs).
- Validation: go test and -tags v4work (both -race), checkptr, vet,
  gofmt, GOOS windows/darwin/freebsd and linux/arm64 builds on both
  tag sets, all green. No scanner runs; tests finish in seconds.
- Next: the five-aspect adversarial review at the close gate for the
  slice, then the milestone-1 close gate.

### Status (2026-08-22) - chunk 3b-4 slice B COMMITTED: ordered merge, history projection, workflow state (HEAD e1fae2c)

- Slice B (internal/writer merge + workflow state) committed at
  e1fae2c:
  - committed-range forward cursor over a base generation through the
    draft mapping (range_store_cursor.go SelectedStore; base txn and
    page count pinned, never consumes, recorded decision 5) on the
    shared tree.ForwardCursor (internal/tree/cursor.go);
  - ordered old/input merge (range_merge.go: incomingRange,
    orderedMerge, mergePolicy, coalescing mergeOutput over
    rangeBulkBuilder, refcountRun with the checked sign arithmetic of
    Rust range_merge.rs);
  - one-pass history plan/merge/policy (history.go: window collection,
    unique names, ensure feeds with the created count, cutoff ranking,
    feed-index ordering, cached prefix interning, per-window and
    aggregate observe with adjacency runs, balanced finish report, the
    "history projection heap" charges, cancellation checkpoints);
  - operation-private membership refcount delta state
    (membership_delta.go track_buffered/flush/track and the delta
    fixed tree; membership_draft.go
    finishMembershipDeltasWithCheckpoint; range edits route every
    membership record through trackMembershipRefcount and fail closed
    on structured kinds);
  - Draft/Core prepared-workflow state machine (workflow.go: HasDraft,
    DraftChanged, WorkflowInputOpen/Active, MetadataStaged,
    OperationAbandoned/OperationIs, AbandonOperation,
    BeginRangeWorkflow/BeginMembershipWorkflow, Mutate, WriterEdit
    PrepareHistoryFrom/BeginHistory/PushHistory/FinishHistory, the
    membership and direct workflow finishes with the no-change
    discard).
- Tests: sliceb_history_test.go mirrors the Rust
  tests/history_projection.rs vectors (exact multi-window counts and
  the no-change rerun, empty-source preservation, plan validation and
  heap budget, the canonical-input merge contract, the workflow gates,
  the direct-replacement publish), the tree cursor tests, the
  range-accounting fence updates, and v4work necessary-work pins (one
  source pass, four ranges consumed, 8 segments x 3 windows = 24
  window tests, six membership delta spills).
- Fixed during the slice: work.Reset() did not clear
  membershipDeltaSpills (the counter leaked across v4work pin tests),
  and the structured-kind fails-closed fixture wrote a
  bootstrap-invalid meta (missing structure kind and dictionary
  limits) instead of the valid empty structured meta.
- Validation: go test, -tags v4work, both -race, checkptr, vet,
  gofmt, and the GOOS windows/darwin/freebsd plus linux/arm64 builds
  and vet on both tag sets, all green.

### Status (2026-08-22) - chunk 3b-4 slice A COMMITTED: membership algebra, draft feed catalog, heap budget (HEAD 3495436)

- Slice A (internal/writer foundations) committed at 3495436: the
  membership algebra (contains_indexes over caller-owned selected-bit
  output, combine with identity shortcuts and canonical word counts),
  the draft feed catalog (ensure/insert over the dual name and index
  trees plus the feed used bitmap), the bounded heap charge helper
  under the Rust heap labels, the inclusive-cardinality helpers for
  both families, and the source_passes/history_window_tests/
  membership_combinations necessary-work counters (production no-ops).

### Status (2026-08-21) - chunk 3b-3 slice C+D CLOSED: five-aspect PASS at HEAD 861aef8

- M3 chunk-3b-3 slice C+D (snapshot writer surface) closes with all
  five aspect reviewers at PASS, no P0-P2 findings. Delta reviewed:
  b0f785a -> 200798c -> 6cb88df -> 8749f99 -> 4e077a6 -> e2858bb ->
  fcfc951 -> 861aef8. Aquinas (wire/integrity) PASSed at b0f785a
  including the Rust cross-open of six Go-produced snapshots; Ohm
  (APIs/docs) FAILed at b0f785a with three findings, all fixed at
  200798c and re-reviewed PASS; Hume (Go idioms) FAILed at b0f785a
  (the floor-of-zero heap-charge wrap and two P2s), fixed at 6cb88df
  and e2858bb and 861aef8, PASS; Aristotle (performance) FAILed at
  b0f785a (P2-1: the metadata deflate heap charge mirrored Rust's
  512 KiB miniz constant while the Go stdlib workspace measures
  ~821 KiB), fixed at 8749f99 with the honest 840 KiB charge and an
  enforcement test, PASS; Pauli (Rust parity) PASSed the full slice at
  8749f99 with the b0f785a P1 verified fixed in-range, then PASSed the
  P3 parity round at fcfc951.
- Fixed across the review rounds:
  - floorPowerOfTwo(0) returned 1 (Rust reference_batch.rs:113-118 has
    the zero guard), wrapping the snapshot heap charge for budgets
    under 32 bytes and bypassing the metadata heap gate; fixed and
    pinned by the tiny-heap tests (TestSnapshotTinyHeap*).
  - Real writer bug discovered in slice C+D validation: after a
    successful exchange the Go writer never removed the previous
    destination that RENAME_EXCHANGE swapped onto the private attempt
    name; Rust retire_steps unlink_previous does (main_file.rs:296-320).
    The writer now retires it identity-guarded with the Rust-verbatim
    classification surface.
  - The metadata deflate heap charge under-charged the Go stdlib
    workspace (measured peak 821,285 bytes with GC disabled vs the
    mirrored 512 KiB miniz constant); the honest 840 KiB constant is
    enforced by TestMetadataDeflateHeapOverheadCoversWorkspace
    (race-skipped; the detector shadow inflates HeapAlloc).
- P3 parity round before the close: retireExchangedPrevious and
  verifyCustody now mirror Rust verify_private_or_retired + unlink_exact
  (previousCustody binds identity + byte length + an open descriptor to
  the old main inode; zero-link already-retired vs multi-link conflict
  vs identity/byte-length proofs; post-unlink zero-link proof with
  CodeCleanupConflict residue "retired previous destination still has a
  link"; Rust-verbatim problem details). The three retirement crash
  points are injected (publication.after_main_rename,
  after_previous_unlink, after_retirement_sync) and pinned by
  TestCrashPublishReplacePreservesExactPreviousOrDesiredState
  (skip where the atomic exchange is unavailable, mirroring the Rust
  cfg guard). TestRetireExchangedPreviousBranchClassification pins
  every refusal branch deterministically; the post-unlink CleanupConflict
  proof stays a documented race-window guarantee (no deterministic
  construction without a blocking checkpoint; Rust pins it via
  checkpoint observers, and the project rules ban test-only production
  machinery). A pre-cancelled snapshot refuses before any destination
  artifact exists (Rust lock_file_cancellable order); the metadata copy
  charge includes the reader's length+1 overflow probe; the flate
  workspace test uses runtime.KeepAlive. Accepted non-blocking items
  recorded: probe-detail string divergence on unreachable EACCES
  corners, the split-capture TOCTOU (conservative residue direction),
  the fewer crash-checkpoint positions (the Go machine compresses the
  intermediate steps), and the standing performance P3s (feed-name
  string(), membership word passes, sha256 per intern) with their
  documented OutputWords contract.
- Cross-open evidence: all six Rust fixtures and the six Go-produced
  snapshots cross-open byte-exact (Aquinas, wire/integrity aspect).

### Status (2026-08-21) - chunk 3b-3 slices A+B COMMITTED: reader cursor surface and writer structure dictionary (HEAD 172c472)

- Slice A (internal/reader): exported NewMembershipRangeCursor4/6,
  NetworkEnrichmentV1Range4/6 + NewNetworkEnrichmentV1Cursor4/6
  (structured_value/cursor.rs), MetadataJSONLen, FileIdentity, and
  ConfirmUnchanged (source final-check: VerifyIdentity + meta re-read,
  mirroring BasicSource::final_check/bind_current, RecoveryCandidateChanged
  51 wrapping identical to Rust candidate_changed).
- Slice B (internal/writer): structure dictionary
  (structure_table/structure_codec/structure_dictionary.go),
  PushNetworkEnrichmentV1V4/V6 with structure-mode guards and membership
  interning (immutable_output/structured.rs), and the structure reference
  batch through NewStructuredOutputBuilder (immutable_output/
  reference_batch.rs; membership batch charged first, structure batch
  second from the remaining heap).
- Real bug fixed in slice B: the Go membership hash tree compared raw
  little-endian digest bytes while Rust Ord compares (digest, word_count,
  id) numerically, so dedup broke at membership id >= 256 (two records
  hashing to the same first-le-8 could share one ID). The hash probe now
  compares big-endian digests (hashProbe) while the wire cells stay
  little-endian; readers are unaffected (they use the ID tree). Verified
  against Rust structure_value/codec.rs and membership_tree.rs.
- All four test modes green (test, -tags v4work, both -race), vet,
  gofmt; all five platform builds compile.

### Status (2026-08-21) - chunk 3b-3 slices C+D COMMITTED: snapshot machine and public facade (HEAD 172c472+)

- Slice C (internal/snapshot): the machine mirroring api.rs + build.rs +
  terminal.rs - Live refusal at the API layer (OsUnsupported 58, detail
  per recorded decision), budget validation with Rust-verbatim details
  (pages >= 2; open files 3 for replace/Live else 2), source open BEFORE
  the destination create, identity compare (source vs private output,
  32-byte LE device+inode encode), heap-charged reference batches (slot
  16 bytes, ENTRY_LIMIT 1024, floor power of two), the six family copy
  loops with per-item cancellation checks, SnapshotWords exact-count
  verify ("membership length changed while copying"), metadata copy under
  the remaining heap budget, ConfirmUnchanged between finish and publish,
  source release before publish, cancellation gate, and the writer
  publish mapping to Cause+CleanupState. Reader gained the exported
  LookupMembershipID (Rust GenerationReader::membership).
- Slice D (public v4/go): SnapshotTo, SnapshotSourceMode,
  SnapshotPublicationPolicy alias, SnapshotBudget, SnapshotResult,
  SnapshotPreparationFailure (Cause+Cleanup collapse), plus tests
  mirroring snapshot_operations.rs over the committed Rust fixtures
  (direct/membership/structured/structured-nothreat), the
  zeroalloc/v4work pins, and the malformed-traversal vs CRC-damage pair.
- Real writer bug fixed in slice C validation: after a successful
  exchange (PolicyReplaceExisting) the Go writer never removed the
  previous destination that RENAME_EXCHANGE swapped onto the private
  attempt name; Rust retire_steps unlink_previous does
  (main_file.rs:296-320). The writer now captures the previous
  destination identity in verifyCustody and, after the destination proof,
  unlinks the private name identity-guarded and syncs the retained
  directory (retireExchangedPrevious); a failed retirement keeps the
  already-proven main published and reports ResiduePossible. Regression
  pinned in TestPublishExchangeReplaces and the snapshot replacement
  tests (assert_no_private_artifacts parity).
- Live-specific Rust tests (sidecar budgets, live generation pinning,
  live self replacement) are deliberately not ported: the Go boundary
  refuses the live source before any of them can run, per the recorded
  decision. The Go adaptation pins the refusal itself
  (TestSnapshotLiveRefusedAtBoundary) including the Rust api.rs ordering
  (refusal precedes budget validation).
- All four test modes green, vet, gofmt, all five platform builds; the
  v4work pins assert the one-pass copy (RangeConsumed == source range
  count) and copy determinism (identical generations, identical
  WordReads).
- API-docs review fixes (Ohm P2s, applied after the first review round):
  the nil-budget and invalid-mode boundary guards are now documented on
  SnapshotTo and pinned by TestSnapshotBoundaryGuards (both refusals are
  Go-boundary additions; Rust passes the budget by value and its mode
  enum is closed); the immutable-source-replacement-during-copy race
  (Rust snapshot_operations.rs:538) is now ported end-to-end
  (TestSnapshotImmutableSourceReplacementDuringCopyBlocksPublication
  over a 20k-range generated source, controller renames the source when
  the private output appears, final ConfirmUnchanged refuses with
  RecoveryCandidateChanged, no output, no private residue); the
  slice-A reader test used syscall.Stat_t untagged and broke GOOS=windows
  vet on both tag sets - the identity oracle is now the mapping owner's
  portable StatIdentity (mapping.StatIdentity exists on all five target
  platforms). The Rust Outcome collapse is named in the SnapshotTo doc.

- Performance review fix (Aristotle P2-1, applied after the second
  review round): the deflate heap charge under-charged the Go stdlib
  workspace. The gate mirrored Rust metadata.rs DEFLATE_HEAP_OVERHEAD
  (512 KiB, the miniz backend's pinned workspace), but compress/flate at
  DefaultCompression pins ~821 KiB live heap (measured with GC disabled
  across 1 KiB to 20 MiB payloads; stdlib layout: hashHead 512 KiB +
  hashPrev 128 KiB inline, 64 KiB window, 64 KiB token queue). An
  under-charge let the deflate attempt exceed the caller's declared heap
  budget. The Go charge is now its own honest constant (840 KiB) and is
  enforced by TestMetadataDeflateHeapOverheadCoversWorkspace, which
  re-measures the peak workspace and pins both a sanity floor and the
  upper bound (the Rust parity pattern: "allocation tests enforce it");
  the test skips under -race because the detector's shadow memory
  inflates HeapAlloc (race_disabled.go/race_enabled.go). The pre-existing
  storedZlib fallback remains the honest layout when the budget fits the
  bound but not the deflate workspace.

- Post-gate P3 parity round (all five reviewers PASS at HEAD 8749f99;
  applied before the slice close-out): the exchange retirement machine
  is now Rust-exact. verifyCustody binds the replacement destination
  (previousCustody: identity + byte length + an open descriptor to the
  old main inode, replacement.rs PreviousMain parity) and
  retireExchangedPrevious mirrors verify_private_or_retired and
  unlink_exact: the bound inode must keep its captured byte length, a
  zero-link inode is already retired and requires the private name
  absent, a multi-link inode is the link-count conflict, the private
  name must still name the captured inode with exactly one link, and
  the identity-guarded unlink must drop the last link (CleanupConflict
  "retired previous destination still has a link" residue). The two
  "retired ..." detail strings became the Rust-verbatim problem texts.
  The three retirement crash points (publication.after_main_rename /
  after_previous_unlink / after_retirement_sync) are injected with the
  existing fault machinery and pinned by
  TestCrashPublishReplacePreservesExactPreviousOrDesiredState (Rust
  replacement_crashes_preserve_exact_previous_or_desired_state): after
  the exchange the main holds the complete new generation and the
  private name holds the exchanged previous; after the unlink no
  private artifact survives. A pre-cancelled snapshot now refuses
  before any destination artifact exists (Rust lock_file_cancellable
  order); the metadata copy charge includes the reader's length+1
  overflow probe; the flate workspace test uses runtime.KeepAlive
  instead of the dead `_ = enc`.

- Retirement parity review round (Pauli PASS at 8749f99; Hume FAIL at
  4e077a6 with two P2s, both fixed at e2858bb; branch pins at the close):
  the replacement crash test skips where the atomic name exchange is
  unavailable (Rust cfg(linux, apple) guard parity), the zero-link
  retirement branch propagates a non-ENOENT probe error as CodeIO
  instead of collapsing it into NameExists, and a direct-call table
  pins every retirement refusal branch (TestRetireExchangedPrevious
  BranchClassification: already-retired Clean/NameExists, changed byte
  length, multi-link, vanished private name, symlink and directory at
  the private name, foreign inode, and the last-link unlink). The
  post-unlink CleanupConflict proof stays a race-window guarantee with
  no deterministic construction without a blocking checkpoint (Rust
  pins it via checkpoint observers; the Go fault machinery has no
  mutation hook and adding test-only production machinery is a defect
  per project philosophy). The branch table skips where the atomic
  name exchange is unavailable, like the crash suite (the exchange
  policy refuses to create an attempt there). The stale verifyCustody
  doc now describes the previousCustody binding.

### Status (2026-08-21) - chunk 3b-3 defined: snapshot writer surface (snapshot::snapshot_to)

- Next M3 chunk after the 3b-2 close: the compact-snapshot surface.
  Rust authority: v4/rust/iprange-livedb/src/snapshot/{api.rs,build.rs,
  terminal.rs,source.rs,snapshot.rs} and its dependencies
  immutable_output/structured.rs + structured_value/{table.rs,manager.rs,
  codec.rs,network_enrichment_v1.rs,cursor.rs}; tests in
  tests/snapshot_operations.rs. Go has no counterpart: the public
  facade, the machine, the reader structured cursor, and the writer
  structure dictionary are all missing.
- Slices (disjoint write scopes, exact Rust baseline):
  - A (internal/reader): exported membership-range cursor
    constructors, NetworkEnrichmentV1Cursor4/6 ordered structured
    cursor (structured_value/cursor.rs), MetadataJSONLen,
    FileIdentity, and ConfirmUnchanged (source final-check for the
    snapshot: VerifyIdentity + meta re-read compare, mirroring
    BasicSource::final_check/bind_current).
  - B (internal/writer): structure dictionary on the one-shot output
    builder - intern/apply_delta/flush over the structure-ID fixed
    tree (structured_value/table.rs+manager.rs+codec.rs geometry
    already in internal/format/structure.go), the structure reference
    batch (immutable_output/reference_batch.rs), and
    PushNetworkEnrichmentV1V4/V6 (immutable_output/structured.rs)
    including structure-mode family guards and membership interning.
  - C (internal/snapshot): the snapshot machine mirroring api.rs and
    build.rs: Live refusal, budget validation (max_output_pages>=2,
    open-files 3 for the replace policies / Live else 2, Rust-verbatim
    InsufficientResourceBudget details), source open through the
    reader, identity compare, heap-charged reference batches, the six
    family copy loops with per-item cancellation checks, metadata copy
    with the heap budget, source final-check + release before publish,
    and the publish mapping to the terminal shapes.
  - D (public v4/go): SnapshotTo, SnapshotSourceMode, SnapshotBudget,
    SnapshotResult, SnapshotPreparationFailure collapse (early/new/
    discarded/from_publication into Cause+Cleanup, live-only fields
    dropped), plus tests mirroring snapshot_operations.rs and the
    zeroalloc/v4work pins.
- Recorded decisions (no open user decision):
  1. Live source mode refuses honestly at the boundary with the Rust
     Unsupported class (OsUnsupported 58) and detail "live snapshot
     source requires the live sidecar coordination (Milestone 4)";
     live coordination is the approved M4 scope and the Rust
     non-unix cfg path refuses the whole surface the same way.
     reject_live_self is therefore unreachable and not ported.
  2. The output identity is preserved from the source meta verbatim
     (database id, transaction id, commit nonce - Rust
     GenerationReader::output_spec), unlike the fresh identity of
     algebra publish_set.
  3. The source is opened BEFORE the destination create and released
     (reader Close) BEFORE the publish rename, and the source
     final-check runs between builder finish and publish, exactly like
     Rust finish_current (changed source refuses publication).
  4. Reference-batch heap accounting mirrors Rust: membership batch
     charged for membership/structured kinds, then the structure
     batch charged from the remaining heap for structured; metadata
     charged under "snapshot metadata input heap" and written with
     the remaining budget.
  5. Failure shapes collapse to Cause+CleanupState on the Go boundary
     (AlgebraPreparationFailure precedent); source_cleanup/
     coordination_cleanup/housekeeping fields are always empty while
     Live is refused and are documented, not carried.
- Next: slices A and B run in parallel; then C, then D; then the
  five-aspect review at the close gate like every slice.

### Status (2026-08-21) - slice 3 CLOSED: five-aspect PASS at HEAD 8345891

- M3 chunk-3b-2 slice 3 (publish_set surface) closes with all five
  aspect reviewers at PASS, no P0-P2 findings. Delta reviewed:
  3897b4b (F-1 typing/probe/crash fixes) -> 45d453f (NetBSD cause
  translation) -> 8345891 (test portability). Pauli (Rust parity),
  Hume (Go idioms), Aristotle (performance), Aquinas (wire/integrity)
  PASSed at 3897b4b; Ohm (APIs/docs) FAILed at 3897b4b with three
  findings, all fixed and re-reviewed to PASS at 45d453f; the
  test-portability delta at 8345891 (test-only build tags and helper
  placement, no production semantics) was verified across the full
  matrix and Ohm re-confirmed PASS including the close records.
- Fixed with this entry:
  - The NetBSD no-replace refusal now surfaces the Rust-verbatim
    public cause: CodeDurabilityUnsupported (34) with the exact
    problem.rs detail "filesystem lacks required durable namespace
    operations" (problem.rs:62-64), instead of the Go-internal
    CodeOSUnsupported marker (58). The staging classifier still keys
    on the internal marker (publication_staging.go:410) and translates
    only at the public Cause; the marker never leaves the mapping
    owner. Ruling verified against Rust: re-keying the classifier on
    code 34 would misclassify the FreeBSD mid-machine SyncDirectory
    EINVAL (linkat machine, publish_link_noreplace.go) as NotPublished
    while Rust classifies the main-rename-stage Unsupported as
    outcome_unknown (attempt.rs from_armed) - the marker encodes the
    acquire position, which code 34 alone cannot.
  - Comments corrected: neither mapping_publish_netbsd.go nor
    mapping_publish_posix.go claims a "preparation failure" anymore;
    the writer returns the not-published result (attempt discarded).
    The publication_staging.go rename-refusal comment names netbsd as
    the only refusal target (Rust rename_noreplace Unsupported on
    non-linux/apple/freebsd; Windows implements the primitive through
    NtSetInformationFile, windows_mutation.rs:24-39). The Go Windows
    stubs stay documented Go-platform refusals.
  - Pre-existing test portability debt fixed (Ohm P3): makePagesFile
    lived in the linux-tagged mapping_test.go but is referenced by the
    untagged v4work pins, and the FreeBSD linkat-machine tests carried
    no !windows tag while testing the !windows machine - so
    GOOS!=linux cross-vet/cross-test builds of internal/mapping failed
    to compile. makePagesFile moved to an untagged portable helper
    file; both machine test files gained the !windows tag. Every 5-OS
    cross-vet and cross-build on both tag sets now compiles.
- Known edge recorded for the sidecar milestone, not a blocker: on a
  Linux filesystem without the RENAME_NOREPLACE primitive the staged
  Go flow classifies the main-rename EINVAL as OutcomeUnknown, while
  Rust refuses earlier at the coordination acquire (NotPublished). The
  Go staging deliberately has no coordination rename yet (the
  reservation-sidecar machinery lands with the M4 sidecar work per the
  approved scope), so the acquire-position refusal cannot be
  distinguished; the DurabilityUnsupported code is identical in both.
  Closure of this edge is tracked with the M4 sidecar coordination
  chunk, not deferred silently.
- Validation at 8345891, all niced: go test ./... and -tags v4work
  (9 packages ok each), -race both tag sets, vet and gofmt clean,
  5-OS (linux/darwin/freebsd/netbsd/windows) build and vet of ./... on
  both tag sets - all green, including GOOS=windows with v4work which
  previously failed to compile for testing. Rust tree unchanged.
- Next: chunk 3b-2 slice 3 done; the next M3 chunk is the snapshot
  writer surface - Rust snapshot::snapshot_to with SnapshotBudget,
  SnapshotSourceMode, SnapshotPublicationPolicy, SnapshotResult and
  SnapshotPreparationFailure (v4/rust/iprange-livedb/src/lib.rs
  snapshot export; no Go counterpart exists yet) - reusing the
  publication staging and output builder closed by this slice, per the
  Milestone-3 surface list (snapshots and reports). Slice 3 closes
  with no open user decision.

### Status (2026-08-21) - re-review round: FreeBSD no-replace machine, NetBSD classification, records corrections

- Re-review verdicts so far (same five reviewers, delta ae57dfa ->
  adcbd90 -> HEAD): Ohm (APIs/docs) returned one P2 - CleanupInProgress
  was recorded as code 77 but is 64 (codes.go line 77 vs sdk_error.rs:76
  CleanupInProgress = 64) - plus two P3; Aquinas (wire/integrity)
  returned one P2 - on the freebsd/netbsd targets the fail-if-exists
  rename refusal now retained one residue attempt file per attempt,
  while Rust refuses the unsupported primitive as the preparation
  failure with the attempt discarded and implements no-replace on
  FreeBSD with the crash-safe linkat machine - plus two P3. Pauli, Hume,
  and Aristotle re-reviews still running.
- Fixed with this entry:
  - FreeBSD no-replace publication implemented as the crash-safe linkat
    machine (Rust namespace_mutation.rs link_noreplace): identity-proved
    source probe, linkat, link-state classification, directory syncs,
    identity-proved alias unlink, and the destination-only proof; crash
    points publication.freebsd.after_noreplace_{link,link_sync,
    alias_unlink,alias_sync} at the exact Rust positions. The machine is
    compiled on every non-Windows target so the exact syscall sequence
    runs in build-host tests; the FreeBSD entry point
    (mapping_publish_freebsd.go) is its only production caller.
  - NetBSD keeps the no-replace refusal (mapping_publish_netbsd.go) and
    the writer now classifies the Unsupported refusal as the Rust
    acquire-failure result: NotPublished with both artifacts discarded
    and the content computed from the main slot (Rust attempt.rs
    from_private -> Ok(not_published)); no residue accumulates per
    attempt, and it is a result, never a preparation error.
  - Custody identity compare: verifyCustody compares the path probe to
    the creation-time identity captured from the builder descriptor
    instead of rebinding it (Rust verify_name); Discard stays
    identity-guarded on the creation capture. The fail-if-exists
    publish-time re-checks now cover the twin AND the main name (Rust
    reservation_file.rs acquire + arm_with require_absent):
    NotPublished with the foreign file preserved in both cases, and the
    remaining rename-race window is pinned by a test-only fault point to
    the outcome_unknown classification with the attempt retained (Rust
    from_armed).
  - Records: CleanupInProgress is code 64 everywhere; the direct-cursor
    gate detail strings now match what the Rust public path emits
    (require_direct: "direct lookup requires a direct-value database"
    and "lookup address family does not match the database"); the
    scanner-purge sentence is scoped to live guidance.
- Validation: go test both tag sets, -race (both), vet, gofmt, 5-OS
  cross-builds and cross-vet of the OS-tagged tests all green; ten new
  machine unit tests plus a four-point child-process crash suite (each
  FreeBSD crash point exits 86 in a spawned child, then the transition
  recovers in-process to the destination-only state).
- Re-review of this delta is with the same five reviewers; the slice
  closes only when all five report no P0-P2.

### Status (2026-08-21) - slice 3 close gate: five-aspect review executed, findings fixed, re-review pending

- M3 chunk-3b-2 slice 3 (publish_set surface) is at the close gate. The
  single-level five-aspect review above ran at HEAD 2a5e78e with the Rust
  implementation as the mandatory baseline; all five reviewers returned
  findings (none P0): Pauli (Rust parity) FAIL - three P1 publish
  classification gaps plus two P2; Hume (Go idioms) FAIL - fourteen stale
  gate-era comments plus three hand-rolled error-type chains; Aristotle
  (performance) FAIL - two P2 hot-path items (per-cell leaf re-validation,
  metadata two-pass decode); Aquinas (wire/integrity) PASS with one P2
  (Unicode-vs-ASCII fold in the reader reserved-name check); Ohm
  (APIs/docs) FAIL - two P1 cursor gate gaps plus one P2 (abortAfter code
  class, stale Validation records).
- All findings are fixed with this entry:
  - publish classification matches Rust: rename-refusal is
    PublicationOutcomeUnknown with the attempt retained (attempt.rs
    outcome_unknown); fail-if-exists twin checks and the replace-policy
    early custody checks (missing destination, foreign twin preserved,
    proven single-link attempt, identity-guarded discard) mirror
    publication/replacement.rs and reservation_file.rs; attempt names
    are hex-encoded;
  - file identity capture (FileIdentity via fstat) proves attempt
    custody before publish and discard;
  - error-type consolidation: join/merge error chains are single
    fmt.Errorf wraps with the close-failure cause appended;
  - abortAfter with unresolved state maps to CodeCleanupInProgress
    (64), nested under the abort error exactly like Rust CleanupIncomplete
    (sdk_error.rs CleanupInProgress = 64);
  - the stale gate-era comment vocabulary is purged from production
    sources (zero non-test hits);
  - reserved-name checks use ASCII-only folding with NUL rejection from
    the shared internal/format/name.go authority (Rust path.rs);
  - cursor constructors gate kind and family at open with Rust-exact
    error strings, pinned by cursors_gate_test.go;
  - metadata decoding is single-pass: the chain is validated on the same
    visit that feeds the inflater, capped at 5,182 pages (Rust MAX_PAGES);
  - treeCursor caches the validated leaf (Rust Cursor::leaf); the
    necessary-work pins moved with the measured reduction (catalog 71 ->
    2, direct 4 -> 2, projection 7 -> 5 page visits; LeafValidation
    counts unchanged).
- Validation after the fixes: go test ./... both tag sets, -race (both),
  vet, gofmt, 5-OS cross-builds (linux/darwin/freebsd/netbsd/windows)
  all green; full suite ~9 s, niced. Rust tree unchanged.
- Delta re-review is in progress with the same five reviewers; the slice
  closes only when all five report no P0-P2.
- M3 record (compact): chunk 1 - reader correctness fixes; chunk 2 - joins
  and aggregation with the zero-membership-ID fix; chunk 3a - read-side
  algebra; chunk 3b-1 - tree variable; chunk 3b-2 slices 1-3 - output
  machinery and the publish_set writer surface, slice 3 at close.
- Superseded pre-rules gates: at 6ded376 the previous level-1 five-aspect
  arrangement reported all PASS with a reduced level-2 quorum (minimax,
  mimo), and the four-reviewer full-codebase mmap/file-I/O gate PASSed at
  2a5e78e (record below); the single-level five-aspect rules at the top
  of this SOW supersede both, and the five-aspect re-review at 2a5e78e
  found the failures listed above.

### 2026-08-21 - close gate: four-reviewer full-codebase review (PASS, one P1 fixed)

- Four concurrent fresh reviewers on the lead's model read the ENTIRE
  pure-Go codebase file by file: 179 .go files each (production + tests +
  every build-tagged variant), zero files skipped, committed at HEAD
  d16c88e.
- Verdicts: 4/4 PASS on the mmap-only / file-I/O policy:
  - complete-page copies into or out of the mmap: none in production;
    view-holder whitelist clean (only internal/mapping, internal/format,
    internal/reader, internal/writer, and the public facade handle mapped
    page views; internal/tree, internal/bitmap, internal/retire,
    internal/bootstrap receive views only through the Store callbacks);
  - file I/O on persistent content outside the mmap: none; the descriptor
    surface is confined to internal/mapping (open, mmap, ftruncate, msync,
    fsync, close); zero unsafe/reflect/exec laundering.
- Unanimous P1, fixed with this entry: the metadata read path accumulated
  the whole compressed stream (up to the section-11 bound) in owned heap
  before inflating; the Rust authority streams mapped chunks into the
  inflater. The reader now validates the chain in pass 1 while capturing
  only the two header bytes and four trailer bytes, and inflates in pass 2
  straight from the mapped chunk views through metadataStream (implements
  ReadByte, so no bufio read-ahead and byte-exact stream-end detection).
  The finding reviewer re-reviewed the delta: PASS.
- The delta re-review caught one more P1 before commit: without ReadByte
  the flate wrapper would read ahead and trailing-junk rejection would be
  dead; now pinned by TestMetadataStreamFlateCounting (mechanism) and
  TestMetadataJunkBeforeTrailerRejected (real-fixture corruption: junk
  between the final DEFLATE block and the intact trailer).
- Rulings recorded (accepted, Rust parity): the publication digest copies
  1024-byte mapped spans into a sub-page stack buffer (exact output_
  digest.rs parity); bounded record-scale cells and feed-name conversions
  are the sanctioned logical boundaries; test-only page fixtures are not
  production. The in-memory flate metadata decode is the sanctioned
  logical-operation boundary for metadata (bounded by section 11).
- Validation after the fix: go test ./... both tag sets, -race, vet,
  gofmt, 5-OS cross-builds green; full suite ~8 s, niced. Rust unchanged.

## Review Process (user decisions 2026-08-12, 2026-08-17, 2026-08-21)

1. Five aspect reviewers on the lead's own model (copies), disjoint
   aspects, adversarial mode, running with the final-review skill;
   spawned once at first use, reused by message for every delta round,
   never respawned between rounds, restarted only between milestones.
   One level only - there is no level-2 gate and no other model is used.
2. The milestone closes only when all five reviewers report no P0-P2
   findings.
3. mmap-only / file-I/O policy gate (2026-08-21): enforced by the
   five-aspect reviewers above (aspect 2) plus periodic full-codebase
   sweeps when the surface under review requires them. No scanner or
   mutation-corpus enforcement exists.
4. All builds, tests, benchmarks, and scans run under nice; any step
   expected to exceed ~2 wall-minutes or ~10 core-minutes is named with
   its expected cost in the report and recorded in this SOW's validation
   plan before it runs.
## Requirements

### Purpose

Deliver the pure-Go peer of the accepted Rust v4 SDK so `update-ipsets` and Go
consumers can use the exact current unsigned Phase-1 database without cgo or a
Rust runtime dependency. The result must preserve the format's mmap-only,
bounded, durable, two-level architecture and its measured performance
discipline rather than mechanically translating Rust source.

Make the work safe to hand to an independently run implementer. Evidence at
each milestone must let the user judge whether the implementation is accurate,
focused, maintainable, and worth continuing before a large rewrite is accepted.

### User Request

- Commit and push the existing work so the repository starts clean.
- Create a self-contained SOW for porting the accepted Rust implementation to
  pure Go.
- Keep the implementation minimal-complete, thin, clear, and performance-first.
- Use the implementation and its evidence as a controlled evaluation of an
  independently run implementer; the user will decide whether and how to
  continue from the milestone results.

### Assistant Understanding

Facts:

- The Rust engine is complete for the current approved Phase-1 contract and has
  passed correctness, durability, resource, performance, conformance, C-boundary,
  and native supported-platform gates. Its final behavior and measurements are
  summarized in `v4/rust/README.md` and completed SOWs 0020-0024.
- The normative authorities are
  `.agents/sow/specs/binary-format-v4.md` and
  `.agents/sow/specs/design-iprange-engine.md`. Rust is implementation evidence;
  its physical page placement and internal types are not a second specification.
- The current Go module is an older unreleased experiment. It has 50 production
  Go files and 44,088 newline-counted production lines, but its public package
  exposes only basic types, cardinality, and error declarations.
- The old Go wire model permits only direct and membership values, keeps the old
  `retention` tag and 1 MiB metadata limit, lacks all structured-value meta state,
  and does not open the current shared corpus
  (`v4/go/internal/exactv4/contract.go:11-18,63-73,98-101,135-162` and
  `v4/go/types.go:13-34`).
- The old Go storage layer performs positional content reads and writes and owns
  complete 4 KiB page arrays outside file mappings
  (`v4/go/internal/exactv4/page_source_linux.go:13-49`,
  `v4/go/internal/exactv4/page_source_other.go:12-54`,
  `v4/go/internal/exactv4/os_linux.go:495-577`, and
  `v4/go/internal/exactv4/private_page_pool.go:92-103`). These are prohibited by
  the current format contract.
- The old Go tests, vet, and formatting pass. This proves those old internal
  fragments are internally consistent; it does not prove current format or SDK
  parity. No Go test references `v4/conformance/cases.json` or opens a current
  Rust-produced fixture.
- The Rust-provided C ABI remains the only C SDK. Porting or regenerating that ABI
  in Go is outside this SOW.
- Snapshot signing remains Phase 2 under pending SOW-0017 and is outside this
  SOW.

Inferences:

- Treating the current Go tree as a nearly finished implementation would preserve
  the architecture that Rust deliberately replaced. The safe approach is a
  semantic port from the current spec and accepted Rust behavior, reusing an old
  Go component only after current tests prove it conforms.
- A literal Rust-to-Go line translation would copy language-specific ownership
  machinery and the Rust implementation's size. The Go design should reproduce
  each invariant and observable operation once using idiomatic Go and a smaller
  module graph.
- The isolated physical-fault worker, Windows namespace/security behavior, and
  mixed-language live coordination are the highest feasibility and portability
  risks. They must be proved early, not left to final integration.

Unknowns:

- The exact subset of old Go tests or codecs worth retaining. Milestone 0 must
  classify each current production file as retain, rewrite, or delete before any
  destructive edit.
- The final Go/Rust timing delta. The accepted 5-10% band is a target where the
  runtimes permit it; only matched measurement can establish justified
  exceptions.
- Whether the pure-Go fault worker can satisfy every POSIX signal-chaining rule
  without project-owned assembly or another new native boundary. The implementer
  must prove the existing no-cgo contract. If a new boundary appears necessary,
  implementation stops for a user design decision before adding it.

### Acceptance Criteria

- The final module is a pure-Go implementation with no cgo, Rust library,
  external database engine, or alternate content path.
- It supports only the exact current v4 identity. No v3 code, old-v4 parser,
  compatibility mode, importer, exporter, or obsolete fixture remains.
- Public Go behavior covers the complete Phase-1 semantic surface listed in
  `v4/rust/iprange-livedb/src/lib.rs` and the design spec, except the explicitly
  Rust-provided C ABI. Idiomatic Go names are allowed; missing semantics are not.
- The public SDK exposes logical advanced direct, membership, and structured
  transactions plus the typed high-level workflows. It never exposes page
  numbers, roots, raw feed indexes as mutation authority, membership IDs,
  structure IDs, bitmap combinations, or allocator state.
- One internal reader core owns healthy selected-generation access. One internal
  writer core owns healthy mapped COW mutation, allocation, retirement, page
  sealing, and committed-generation publication. Higher-level APIs only sequence
  logical operations over those owners.
- Validation/recovery, external reader coordination, and filesystem
  namespace/publication retain their separate narrow owners while reusing the
  canonical codecs and mapped output builders. No second physical tree or
  publication implementation exists in a workflow.
- Every persistent artifact is content-accessed only through file-backed
  mappings. Static and runtime gates reject `Read`, `Write`, `ReadAt`, `WriteAt`,
  `Pread`, `Pwrite`, `ReadFile`, `WriteFile`, buffered/stream content transfer,
  and equivalent Windows calls against SDK artifacts.
- No complete database page exists in a Go heap/stack array, cache, anonymous
  mapping, or copied byte slice. Page creation and COW edits occur at final
  offsets in file-backed mappings; mapped-to-mapped cell copies are allowed.
- Open, lookup, scan, mutation, commit, query, and snapshot do not implicitly
  run full validation. Explicit validation and recovery alone perform the full
  graph and checksum work.
- Direct, membership, and structured files implement the exact current meta,
  page, dictionary, metadata, retirement, sidecar, reservation, and publication
  contracts. `NetworkEnrichmentV1` uses one common structured manager plus its
  independent codec, including lazy threat membership.
- The opaque compressed metadata payload preserves exact caller bytes, absent
  versus empty state, read-your-writes, and the 20 MiB uncompressed limit.
- Generic direct assignments and structured range assignments apply every input
  in arrival order. A later range overwrites only its covered addresses.
  Unordered ingestion normalizes directly in the destination and creates no
  sorting file or complete-feed heap image.
- Named feeds, feed-index reuse, membership interning, structured interning,
  typed references, stale/foreign reference rejection, and deletion/rename
  behavior match the current contract. Callers never construct internal ID
  combinations.
- Exact direct replacement, first-seen refresh, last-seen refresh, named-feed
  create/replace/rename/delete, membership import, one-inode immutable feed
  construction, history projection, queries, provider joins, global named-feed
  algebra, compact snapshot, commit resolution, and lifecycle/publication
  resolvers match Rust semantics and reports.
- Operation failure aborts the private draft. No later commit can publish partial
  work after a failed source or sink. Commit, abort, close, cleanup, and
  outcome-unknown results retain exact evidence and retry obligations.
- Live reader registration, writer exclusion, reader-safe transaction-grouped
  reclamation, lowest-free-page allocation, sidecar identity/replacement, and
  forked-handle rejection match the exact contract.
- Linux, macOS, and Windows implement the supported live contract. FreeBSD 14
  implements immutable reading, offline validation/recovery, and durable
  immutable publication while every live entry rejects before path access or
  mutation.
- The Go distribution supplies its own exact version-matched
  `iprange-v4-worker`. It claims only SDK-owned in-region physical mapping faults,
  chains every unrelated POSIX `SIGBUS` disposition exactly, uses the equivalent
  Windows exception rule, and never mislabels an unrelated worker crash as source
  unreadability.
- Go independently produces all required conformance fixtures. Both readers
  actually open and semantically verify every Go- and Rust-produced fixture,
  including structured values, full IPv6 cardinality, and exact metadata bytes.
- Mixed Rust/Go subprocess tests pass in both directions for reader slots, writer
  exclusion, reclamation, stale-slot cleanup, sidecar replacement, transition
  states, reservations, publication inspection, and resolution.
- Warm successful point lookups and cursor steps allocate zero Go heap bytes.
  Writer allocations and heap use are fixed or bounded by declared budgets, not
  proportional to input records or sparse page numbers.
- Test-only necessary-work counters pin tree descents, pages visited/copied,
  range passes/splits/merges, dictionary work, checksum sealing, synchronization,
  and artifact creation. They compile out of production binaries.
- Matched release benchmarks cover the accepted Rust matrix and representative
  update-ipsets data. Each retained dominant cost maps to required format work;
  unexplained wasted work is fixed and the audit repeated.
- Operation-by-operation Go performance targets the accepted Rust result within
  5-10% where runtime behavior permits. Every material exception has matched
  profiles, hardware/runtime evidence, and a documented cause. CI uses a loose
  disaster threshold only after local performance acceptance.
- Current Go tests, race/checkptr/fuzz/property tests, malformed corpus tests,
  crash/fault tests, resource tests, cross-compilation, and authorized native
  platform tests pass. Skips cannot hide a required platform or producer.
- The final production graph has no unreachable source, broad dead-code
  suppression, duplicate physical authority, or test-only production mechanism.
  Production LOC, file sizes, function sizes/complexity, and exact-clone evidence
  are reported honestly against the directional engineering philosophy.
- Every valid finding in the final first-principles audit is repaired and the
  identical audit is repeated. This SOW cannot complete while an actionable
  correctness, durability, coordination, bounded-resource, performance,
  layering, duplication, portability, documentation, or conformance issue
  remains.

## Analysis

Sources checked:

- `AGENTS.md`, especially the v4 engineering philosophy and Rust-first gate.
- `.agents/skills/project-v4-rust/SKILL.md` for the proven Rust verification and
  portability workflow that the Go peer must reproduce semantically.
- `.agents/sow/specs/design-iprange-engine.md:15-50,57-129,131-169,366-449,451-519`.
- `.agents/sow/specs/binary-format-v4.md`, complete current contract, especially
  sections 3-5, 8-15, 16, 18-21.
- `v4/rust/README.md` and `v4/rust/iprange-livedb/src/lib.rs` for the accepted
  public semantic inventory and measured baseline.
- Completed SOWs 0019-0024 for the mmap-only correction, final authority/hot-path
  audit, update-ipsets workflows, performance proof, structured values, and
  randomized structured correctness.
- `v4/conformance/README.md`, `v4/conformance/cases.json`, and all six current
  Rust-produced fixtures.
- Complete current Go production/test inventory, `v4/go/go.mod`, public files,
  current format constants, storage/page sources, OS code, and test references.
- Baseline commands: `go test ./...`, `go vet ./...`, and `gofmt -l .`; all pass
  before porting.
- Official Go `runtime`, `unsafe`, `golang.org/x/sys/unix`, and
  `golang.org/x/sys/windows` documentation; Microsoft file-mapping documentation;
  and the platform syscall references already normative in the format spec.
- `etcd-io/bbolt @ 01f7d9658a8a` as a limited Go platform-wrapper reference:
  `bolt_unix.go:55-83`, `bolt_windows.go:65-119`, and `db.go:454-570`.

Current state:

- Git started this planning slice clean and synchronized after commit `900b345`.
- The Rust peer and Rust-produced corpus exist. The Go peer, Go-produced corpus,
  cross-open proof, and mixed-language coordination proof do not.
- The current Go tree is large but not product-shaped: 44,088 production lines,
  1,609 production function declarations, several files above 1,000 lines, and a
  4,794-line largest file. Twenty-seven production files contain positional
  content access and/or complete page arrays.
- The current Go public package has no reader, writer, workflow, query,
  validation, recovery, snapshot, publication, or live lifecycle constructor.
- `v4/go/go.mod` currently declares Go 1.23 and `golang.org/x/sys v0.35.0`.
  Preserve that support floor unless a required API proves it impossible; a
  toolchain/dependency-floor change requires evidence and user approval.

Risks:

- Preserving the old Go storage architecture would reproduce the already-fixed
  positional-I/O/page-buffer failure and invalidate performance/resource claims.
- Translating Rust module-for-module would likely reproduce its language-specific
  size and obscure Go's simpler ownership model.
- Porting only the happy-path reader/writer would miss lifecycle, outcome,
  cleanup, recovery, and cross-process contracts that prevent data loss.
- A Go mmap slice remains valid only while its retained mapping/handle lifetime is
  valid. Escaping page slices or racing `Close` can create stale mapped access.
- Go runtime signal ownership makes the exact isolated-worker contract a
  high-risk area. No runtime-internal linkname, swallowed signal, cgo fallback,
  or Rust-worker dependency is acceptable.
- OS-specific namespace, security, locking, flush, shrink, and crash behavior
  cannot be established by cross-compilation alone.
- Tight timing gates before local optimization would create noise. Loose CI gates
  before local proof would hide waste. Local matched profiles come first.
- Deleting the old tracked Go tree without an exact inventory could remove useful
  tests or independently correct codecs. No tracked file is deleted until the
  user approves the exact proposed deletion set.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

- The repository needs a pure-Go peer, but its current Go tree implements an
  obsolete, incomplete, positional-I/O architecture. The accepted Rust reset
  changed both the storage architecture and the public semantic surface. A port
  must therefore follow the current specs and observable Rust behavior, not
  continue the old Go experiment or transliterate Rust internals.

Evidence reviewed:

- The full normative architecture and binary-format specifications.
- The complete accepted Rust public export inventory, README evidence, current
  corpus, and SOW 0019-0024 outcomes.
- Every current Go production file name, production/test counts, LOC, public API,
  wire constants, platform build tags, content-transfer calls, complete-page
  arrays, conformance references, and baseline tests/vet/formatting.
- Official current Go/OS mapping and unsafe-memory references plus the limited
  bbolt platform-wrapper evidence listed above.

Affected contracts and surfaces:

- `v4/go/` public SDK, internal physical format implementation, OS platform
  layers, fault worker binary, tests, benchmarks, source/runtime architecture
  gates, module metadata, README, and package documentation.
- `v4/conformance/` manifest, Go-produced fixtures, cross-read tests, malformed
  transformations, and mixed Rust/Go subprocess harnesses.
- Rust test/harness code only where minimal cross-language orchestration is
  needed. Rust production behavior, bytes, public API, and C ABI are frozen.
- Project instructions and a new Go runtime project skill after the workflow is
  proven.

Existing patterns to reuse:

- Rust's authority boundaries, semantic tests/models, public workflow reports,
  conformance generator method, necessary-work accounting, update-ipsets
  benchmark scenarios, source/mmap gates, native platform matrix, and final
  audit structure.
- Shared language-neutral `cases.json` and the current Rust-produced files as the
  first immutable-reader target.
- Current Go scalar key/cardinality code and tests only if literal vectors prove
  exact current behavior.
- Small OS-specific mapping wrappers as a pattern. Do not copy bbolt's format,
  freelist, remapping, global locking, page objects, or durability model.

Risk and blast radius:

- This is a replacement of an unreleased Go experiment and eventually affects
  nearly all `v4/go/` production code and tests.
- Data-loss risk is concentrated in COW ownership, dirty-page sealing, durability
  ordering, abort, cleanup, and outcome resolution.
- Memory-safety/process-crash risk is concentrated in mapped lifetime, checked
  offsets, concurrent reuse, physical faults, and signal/exception chaining.
- Interoperability risk is concentrated in little-endian codecs, canonical
  validation, structure/membership dictionaries, metadata compression, sidecar
  locks, and publication/resolver records.
- Performance risk is hidden copying, validation, allocation, synchronization,
  hashing, lookup, and maintenance in hot paths.

Sensitive data handling plan:

- Use only synthetic addresses, feeds, metadata, paths, fault cases, and public
  aggregate benchmark facts in durable artifacts.
- Authorized update-ipsets replay may use operational files locally, but durable
  evidence records only sanitized aggregate sizes, counts, timings, and shapes.
- Do not record credentials, source filenames, literal operational ranges,
  personal/customer/community data, private endpoints, or host aliases.

Implementation plan:

1. Produce the Milestone 0 parity/gap map before production edits: every public
   Rust semantic export and every normative operation maps to its Go API/module,
   test, and status; every old Go production file is classified retain/rewrite/
   delete; the exact proposed deletion set is presented for user approval.
2. Prove the platform foundation in the smallest permanent form: retained
   file-backed mappings, checked offset/view lifetime, flush/sync, identity,
   locks, growth/shrink behavior, and isolated fault-worker feasibility. Delete
   any exploratory code not converted into the final owner or a permanent test.
3. Implement the current portable codecs and one mmap-only immutable reader that
   cross-opens and semantically verifies all Rust-produced fixtures, including
   structured data. This is the first implementation-quality checkpoint for the
   user.
4. Implement one mmap-only physical writer core: final-offset page construction,
   COW range/tree edits, free/used bitmaps, retirement, checksum sealing at
   commit, meta publication, abort, and reclaim. Generate independently produced
   Go fixtures through public APIs.
5. Build the public advanced logical transactions over that core: direct,
   membership, feed catalog/references, common structured manager, and modular
   `NetworkEnrichmentV1` codec. Add randomized scalar-model tests before high-level
   workflows.
6. Build the typed high-level workflows as thin sequencing layers: immutable
   creation, feed lifecycle, direct replacement, first-seen, last-seen,
   membership import, history, metadata, snapshots, queries, joins, and algebra.
7. Complete live coordination, lifecycle transitions, publication/reservation,
   cleanup/housekeeping, commit resolution, and supported-platform behavior over
   the same cores.
8. Complete explicit validation, candidate inspection, best-effort recovery, and
   the version-matched Go worker. Reuse canonical codecs and final mapped-output
   builders; do not add healthy-path validation or a second writer.
9. Add bidirectional producer/cross-open and mixed Rust/Go subprocess gates. Run
   complete correctness, crash, resource, race/checkptr, fuzz/property,
   architecture, mmap syscall, cross-target, and authorized native matrices.
10. Port the Rust benchmark scenarios, measure locally operation by operation,
    profile every material difference, remove wasted work, and repeat until the
    final audit finds no actionable issue. Add loose CI disaster limits only
    after local acceptance.
11. Update package docs, Go README, conformance docs, project instructions, and a
    proven `project-v4-go` runtime skill. Complete the SOW only with implementation,
    artifacts, final audit, SOW move, and one closure commit.

Validation plan:

- Baseline and every milestone: `go test ./...`, `go vet ./...`, `gofmt`, race
  where supported, checkptr, leak/descriptor/residue checks, and exact changed-
  surface tests.
- Literal portable vectors for every integer, meta field, page header, record,
  hash, checksum, structured payload, sidecar/control block, and publication
  record. Cross-architecture codec execution must include a big-endian target or
  emulator.
- Public black-box semantic/property models for arrival-order normalization,
  direct/membership/structured transactions, workflows, commit/abort, and
  reference invalidation.
- Explicit malformed/corrupt corpus, fault injection, truncation, crash-point,
  cleanup retry, outcome resolution, worker chaining, and worker-build mismatch
  tests.
- Static source gates plus Linux runtime tracing provide enforcement
  evidence for mmap-only persistent content and the absence of complete
  out-of-map page images; neither mechanism alone is claimed as proof for
  every possible Go program.
- Go-produced fixtures are built only through public Go operations, verified
  against `cases.json`, opened by Rust, and then committed. Rust fixtures are
  actually opened and checked by Go.
- Mixed subprocesses cover every section-21 direction and state; language-local
  unit tests are insufficient.
- Local benchmarks use the same fixture identities, work counts, timing
  boundaries, allocation/RSS/descriptor/file measurements, warm-up, isolated
  samples, and semantic-result checks as Rust. Profiles and necessary-work
  counters identify retained costs.
- Local cross-compilation covers supported target graphs. Runtime platform proof
  occurs only on user-authorized native Windows GNU, Apple ARM64, and FreeBSD 14
  environments; operational host details are never written to the repository.
- Before closure, run the exact final audit format below. If it reports a valid
  issue, fix it and repeat the entire audit rather than completing the SOW.

Artifact impact plan:

- AGENTS.md: add the proven Go workflow/guards and index the Go project skill at
  closure; do not weaken the current engineering philosophy.
- Runtime project skills: create `.agents/skills/project-v4-go/SKILL.md` only
  after commands, platform boundaries, and evidence are proven.
- Specs: change only if implementation exposes a genuine contradiction. Stop for
  a user design decision before changing behavior or bytes; otherwise record
  that the Go peer implements the existing specs unchanged.
- End-user/operator docs: add `v4/go/README.md` and package documentation for the
  completed SDK, worker installation, platform support, resource behavior, and
  reproducible verification/performance commands.
- End-user/operator skills: none currently exist; reassess at closure if a
  downstream/public workflow is introduced.
- SOW lifecycle: this remains the sole Go-port work item. Move it to `current/`
  only when implementation begins; close it only with all code/artifacts and the
  closure commit. SOW-0017 remains separate Phase 2.

Open-source reference evidence:

- `etcd-io/bbolt @ 01f7d9658a8a`
  - `bolt_unix.go:55-83`
  - `bolt_windows.go:65-119`
  - `db.go:454-570`
- This reference is evidence that Go can isolate basic Unix/Windows mapping
  mechanics behind small platform files. Its database architecture is not reused
  because it does not implement this format's mmap-only writer, external reader
  table, exact durability, recovery, or public semantics.

Open decisions:

- Decision 5A (NetworkEnrichmentV1 location representation) was
  ratified on 2026-08-13 (option A, long-term-best): the parity matrix
  now records `Location NetworkEnrichmentV1Location` plus
  `HasLocation` as the zero-allocation equivalent of Rust's
  `Option<NetworkEnrichmentV1Location>` (the decision-4A lookup
  contract wins over the earlier pointer spelling; a pointer field
  would force a heap allocation per lookup or unsafe pointers into
  the mapping). Every other product and
  format decision is closed; the current specifications and accepted
  Rust semantics are frozen for this port.
- The obsolete Go deletion set from Milestone 0 was resolved by decision 1A
  (user decision, 2026-08-12) and executed at HEAD e65e8b7 (105 tracked
  files removed with the compiling replacement); no approval remains
  outstanding for it.
- If pure Go cannot meet the exact fault-worker contract without a new assembly
  or native boundary, stop and present evidence and options. Do not silently add
  cgo, use the Rust worker, reach into Go runtime internals, or weaken fault
  ownership/chaining.

Resolved decisions (2026-08-11, Milestone 0 closure):

- Deletion set (Decision 1): user chose C - decide after Milestone 1 evidence.
  The exact proposed set (100 tracked files + 2 untracked leftovers) is
  recorded in `.agents/sow/pending/pure-go-v4-port-milestone-0-report.md`
  section 7. No tracked file is deleted and no untracked leftover is removed
  before that decision; deletion, when approved, executes atomically with the
  compiling, tested Milestone 1 replacement.
- Fault-worker native boundary (Decision 2): user chose A - wait for the
  Milestone 1 feasibility evidence, then decide. No new native boundary
  (assembly shim, cgo, runtime internals, Rust worker) may be added before
  that decision.

## Resolved Decisions (2026-08-12, hot-path re-review)

The user adopted the external re-review's decisions after the reopening:

- Decision 1 (deletion set) = A with correction: the obsolete Go tree
  (internal/exactv4: 105 tracked files - 47 production + 58 test, the
  recorded "100" was stale - + untracked exactv4.test + empty directory)
  is removed in the FINAL Milestone 1 commit, not the first
  Milestone 2 writer commit. Before removal the verified scalar types must
  be relocated out of internal/exactv4 (v4/go/types.go is the only new-tree
  importer). .reasonix/ is preserved. Git history already preserves the
  deleted sources.
- Decision 2 (worker boundary) = A: a minimal project-owned assembly
  sigaction shim preserving the exact fault-isolation contract
  (binary-format-v4.md:3084); it affects validation/recovery workers only,
  uses no Go runtime code, cgo, or unwinding inside the handler, and is
  proven natively on each supported POSIX platform in the worker milestone.
- Decision 3 (closed-state class) = A with corrections: closed readers and
  pinned handles report WrongState (11); numeric code 9 remains HandleClosed (no
  table renumbering); WordCount becomes error-capable "(uint32, error)";
  under decision 4 per-view Release disappears and second-close errors
  apply to the reader and the pinned handle.
- Decision 4 (hot-path API shape) = A: caller-owned pinned reader handles.
  Pin once outside the workload; one pin may be shared across concurrent
  immutable lookups. Lookups, scans, view operations, and cursor steps are
  zero-allocation and zero-atomic. Views are lightweight values valid while
  their pin remains open; reader Close returns HandleBusy while pins exist;
  Pin close must not race its operations; insufficient feed-name buffers
  report BufferTooSmall plus the required size.
- Structure-kind authority conflict resolved: direct/membership + nonzero
  structure kind -> FormatInvalid (32); structured + unknown nonzero
  structure kind -> UnsupportedStructure (67); follows the mode-specific
  override at binary-format-v4.md:430 and current Rust bootstrap behavior
  (validate_direct/validate_no_structures -> KindInvariant; finish_open ->
  UnsupportedStructure for unknown codes on an otherwise valid structured
  meta).
- Decision 4A amendment (pin pointer contract, recorded at the re-review):
  Every Pin value references one shared private close state: pointer
  aliases (p2 := p1) and value copies (p2 := *p1) close the same logical
  pin, the count is decremented exactly once, and a second Close through
  any alias or copy reports WrongState. Pinned by
  TestPinPointerAliasSharesClose and the added value-copy tests
  (TestPinValueCopySharesClose, TestPinValueCopyKeepsReaderBusy).
- External audit close-out (resolved): the duplicated Cardinality129
  arithmetic and the duplicated public/internal error-code tables were
  centralized - internal/format is the single authority and v4/go/types.go
  + v4/go/errors.go re-export aliases, so the copies can no longer drift.
- Decision 5A (NetworkEnrichmentV1 location representation) = A, recorded
  2026-08-13: the user delegated the choice to the implementing agent with
  the long-term-best and minimal-complete mandate and did not select an
  option; option A was adopted - `Location NetworkEnrichmentV1Location`
  plus `HasLocation` is the documented zero-allocation equivalent of
  Rust's `Option<NetworkEnrichmentV1Location>` (a pointer field would
  force a per-call heap allocation or unsafe pointers into the mapping).
  The parity matrix row 26 records the ratified spelling and the
  v4/go/reader_public.go doc comment aligns.

### 2026-08-12 - decisions 1A-4A implemented (hot-path contract)

- Deletion (1A): scalars relocated in-line into v4/go/types.go (no more
  internal/exactv4 import); the obsolete tree was then removed in the final
  milestone-1 implementation commit - 105 tracked files (47 production +
  58 test; the recorded "100" was stale) + untracked v4/go/exactv4.test and
  the empty v4/go/exactv4/ directory; .reasonix/ untouched; import-graph
  gate updated; git history preserves the sources.
- Worker boundary (2A): recorded for the worker milestone - minimal
  project-owned assembly sigaction shim, no Go runtime code/cgo/unwinding
  in the handler, proven natively per POSIX platform.
- Closed class (3A): closed readers/pins -> WrongState (11), second Close
  -> WrongState, code 9 = HandleClosed, WordCount -> (uint32, error), per-view
  Release removed (views are pin-valid values).
- Hot-path facade (4A): ImmutableReader.Pin / Pin.Close (one lifetime
  registration outside the workload; HandleBusy while pins exist);
  pin-level membership and enrichment lookups, reader-level direct
  lookups/scans/cardinality, and LookupFeedInto (caller buffer,
  BufferTooSmall + required size) are zero-allocation and zero-atomic
  (plain-state closed checks under the documented no-race-Close contract).
  Measured 0.000000 heap bytes/run for every hot path; atomics only at
  Pin/Close.
- Location shape (5A, recorded 2026-08-12 with the round-11 fix): the
  approved parity matrix writes `Location *NetworkEnrichmentV1Location`
  (milestone-0 report row 26). Decision 4A forbids per-lookup allocations,
  and a pointer inside a by-value result cannot reference stable storage
  without one; Rust itself models the field as
  `Option<NetworkEnrichmentV1Location>`. 5A: express the option as
  `Location NetworkEnrichmentV1Location` + `HasLocation bool`, with the type
  name and fields matching the matrix and the Rust authority exactly. The
  closure report surfaces this for user veto.
- Structure-kind rule: direct/membership + nonzero structure kind ->
  FormatInvalid; structured + unknown nonzero kind -> UnsupportedStructure;
  pinned by reader and format tests (fails on the pre-fix tree).
- Counts at reopening are recorded in the Status entry above (production 4,797
  raw / tests 4,676 raw), taken after the meta-precedence parity fix, the
  sole-meta kind regression test, the final-review regression guards, and
  the public tag/metadata-limit API completion.
  Gates: go test ./... (4 packages) incl -race, vet, gofmt, import graph,
  SOW audit - all green.

### 2026-08-12 - six-reviewer re-verification after decisions 1A-4A

- All six reviewers re-reviewed the rebuilt tree at their disjoint briefs
  after the decisions implementation. Findings during the round and their
  closures:
  - Pin value-copy P2 (mapping reviewer): a dereference copy of a Pin
    could double-decrement the pin count. Closed as a formal decision-4A
    amendment: Pin is a pointer type; aliasing the pointer shares the
    single close state; value copies are unsupported like C opaque
    handles, with typed-error-only consequences; pinned by
    TestPinPointerAliasSharesClose (fails on a double-decrement).
  - Record-drift findings (conformance/reports reviewer): report sections
    2 and 12 still described the pre-deletion 5-package tree and the
    view-guard zero-alloc contract; the SOW current-state entries said "5
    packages". All fixed at 2fdcce4; dated historical entries keep their
    then-accurate wording.
  - External audit close-out (resolved): Cardinality129 and error-code
    tables centralized on the single internal/format authorities; the
    public package re-exports aliases.
- Final verdicts at HEAD 2fdcce4: codecs PASS; bootstrap/meta/direct
  ranges PASS (kind matrix verified pre-fix-failing); membership/blob/
  structured/metadata PASS (metadata rewrite verified incl. empty-present
  and incomplete-final-block probes); mapping/lifetimes/platform PASS
  (pin contract amendment verified); public API/errors/zero-alloc PASS
  (12 public + 8 internal checks at exactly 0 allocs, zero atomics in hot
  paths, WrongState class, LookupFeedInto BufferTooSmall semantics);
  conformance/tests/reports PASS (counts verified with the documented
  method and recorded in the close-out entry; deletion of 105 files
  verified exactly; records internally consistent).
- Gates at HEAD 2fdcce4: go test ./... (4 packages) incl -race, go vet,
  gofmt, import graph, 9-target cross-compile matrix, SOW audit - all
  green. Milestone 1 (immutable reader) review gate: CLOSED.

### 2026-08-14 - external-review rework (milestone 1 re-open, verified findings)

- An independently run external review PASS-failed milestone 1 with three
  findings at HEAD b230bd1, all reproduced by the lead before any change:
  1. P1 - nine separately written binary-search loops decode full records per
     probe and re-decode the selected record; the Rust fixed-tree layer reads
     key-only during search and decodes the selected record once.
  2. P1 - the mmap enforcement machinery totals 14,519 lines (4,738-line
     reader, and the gate misses the complete-page rule: a production
     function copying a mapped page into an owned [4096]byte compiles and
     passes the gate (spec binary-format-v4.md:108).
  3. P2 - the SOW Status had grown to 463 lines of round-by-round scanner
     history, hurting auditability.
- Resolved decisions (direction 1-5 of the review, accepted):
  1A. Add one authoritative fixed-tree search primitive (key-only probes,
      single decode of the selected record) in internal/reader and refactor
      every range/catalog/membership/blob lookup onto it. The blob branch
      keeps per-probe child validation (pinned by
      TestBlobBranchProbedChildValidation; Rust blob select_branch does the
      same). Catalog name probes keep full record-shape validation because
      the name IS the record payload (Rust read_key decodes the entry);
      their feed-index-limit check moves to the selected record only,
      mirroring feed_catalog.rs decode_leaf.
  2A. Add test-only necessary-work counters in internal/reader (build-tag
      `v4work`; zero-cost no-op otherwise) pinning tree lookups, descents,
      page visits/parses, key probes, selected-leaf validations, membership
      word reads, and structure decodes, plus reader benchmarks on the
      committed conformance fixtures and profile evidence in the report.
  3A. Replace the type-light AST taint engine with a type-aware scanner
      built on go/types (stdlib-only, module-aware gc export importer),
      keeping the import/selector/.s/.syso/embed/linkname bans and the
      *os.File capability surface, and add the complete-page ownership rule
      (copy/append/array-conversion sinks at or above PageSize, and
      append/copy of mapped-page-sourced values), with the metadata
  4A. Compact the Status and move the round-by-round history into the
      verbatim appendix at the end of this file.
  5A. Repeat the independent review: six-resident swarm, then sol, before
      milestone 1 closes and Milestone 2 starts.

### 2026-08-14 - external-review rework implemented

Implementation record for the reopened milestone (all three findings
reproduced first at HEAD b230bd1; the rework landed as one commit at
HEAD recorded in the first review entry below):

- Finding 1 (hot path): internal/reader/search.go adds the single
  authoritative greatestLE primitive (key-only probes, last-probe reuse,
  one decode of the selected record), mirroring v4/rust/iprange-livedb/
  src/fixed_tree/page.rs lower_bound semantics. Range branch/leaf
  lookups (range.go), catalog feed lookups (catalog.go), membership ID
  lookups (membership.go) and the blob branch selection (blob.go) all
  route through it. The four format key readers (RangeEntryKeyV4/V6,
  RangeRecordKeyV4/V6) make the probe cost a 4-byte (or 16-byte) read plus
  the slot-offset check. The blob branch keeps per-probe child validation
  (pinned by TestBlobBranchProbedChildValidation); catalog name probes
  keep full-shape validation because the name is the record payload
  (decision 1A). Test-only necessary-work counters live in
  internal/work (build tag v4work; const false no-op stubs otherwise, so
  production binaries carry zero counter state - pinned
  by TestWorkCountersDisabled). Pins: TestWorkRangeLookupMultiLevel,
  TestWorkMembershipBlobWords, TestWorkStructureLookup, plus
  bench_test.go on the committed fixtures and the synthetic multi-level
  databases. Benchmarks (i9-12900K, 200k iterations, -benchtime=200000x):
  LookupDirect4MultiLevel 158.8 ns/op, LookupDirect4MissMultiLevel
  72.05 ns/op, LookupDirect6 68.56 ns/op, LookupFeed 227.7 ns/op,
  MembershipLookupWord 94.81 ns/op, StructuredLookup 120.1 ns/op - all
  0 B/op, 0 allocs/op. CPU profile of Lookup* (reader_cpu.prof in the
  session evidence): dominant costs are SlottedPage.Record slot checks
  (26%), the intentional catalog name validation FeedNameValidString
  (48% of LookupFeed), DecodeCatalogNameRecord, greatestLE dispatch; no
  full-record decode appears on the range/membership probe paths.
- Finding 2 (enforcement): v4/go-gate is now a type-aware scanner over
  go/types (stdlib-only, module-local loader with source type-checking
  per OS config and the pinned x/sys checkout), keeping the text bans
  (banned imports/selectors, .s/.syso, //go:embed, //go:linkname, dot
  imports), the *os.File/*os.Root capability surface (approved lifecycle
  methods, same-package/module-internal callees, x/sys owner), the
  interface-erasure and generic-result rules (including named interfaces
  such as io.Reader), and the complete-page ownership rule: copy/append/
  array-conversion/string sinks that move a mapped page view at or above
  PageSize into owned memory fail with a specific violation; bounded
  record decodes below PageSize and the metadata inflater nodes
  (exempted by exact shape) stay legal. The page-taint flow is
  interprocedural through per-package symbolic summaries (pageflow.go);
  bounded slices carry their constant span (page[48:112] is a 64-byte
  was replaced by 320 table cases inside the tool (257 rejections, 63
  benign acceptances) covering source-transfer, complete-page, and
  file-capability forms,
  target, module graph, x/sys checksum pins, and 9 environment mutations:
  internal-import boundary, x/sys outside the mapping owner, assembly
  object, go.mod replace, go.work, poisoned x/sys cache/proxy, unlistable
  module). Reviewer reproduction now fails the gate: copy of m.Page(0)
  into [4096]byte, append(page...), View(0, PageSize) copy,
  [4096]byte(page), string(page), and r.page(pgno) copy all produce
  rule-specific violations; the bounded copy and the decoded metadata
  chunk append stay accepted. Gate totals: 5,104 lines (go-gate) + 552
  lines (shell) = 5,656, against module production 5,049 and tests
  5,180.
- Swarm round 1 of the rework review (2026-08-14, at the rework
  commit) returned five passes plus two P1 gate-bypass classes, both
  verified and closed in a follow-up commit:
  - function-typed variables as call targets (var clone = bytes.Clone;
    clone(page)) were approved because the callee body is not scanned;
    approvedFuncVar now accepts such variables only when the package-level
    initializer provably binds a scanned function (a func literal, a
    forms P8 (reject) and P9/P15 (benign).
  - complete-page sinks inside closure bodies (defer func(){ copy(out[:],
    page) }(), go func(){...}(), and directly called func literals) were
    invisible to the page-taint flow: pageflow now analyzes defer/go
    statements, select comm clauses, labeled and bare blocks, and function
    literals in expression or callee position (parameters bound to
    call-site taints, return taint propagated, captured variables shared
    with the enclosing state). Pinned as forms P11-P13 (reject) and P14
    (benign bounded copy inside a defer closure).
  Reviewer P3s were fixed in the same commit: the dead err re-check after
  work.LeafValidation in lookupMembershipID was removed and the counter
  moved into membershipLeafFind at the actual record-decode point (it no
  longer fires on a clean miss), matching the range/catalog/blob leaf
  helpers.
- Swarm round 2 (2026-08-14, at the round-1 fix commit) re-verified the
  delta and the lead's own adversarial pass then closed one more hole in
  the same class, fixed in a follow-up commit:
  - a function-typed variable initialized with a func literal and never
    reassigned (var f = func(p []byte){ copy(out, p) }; f(page)) was
    approved, but the literal body was analyzed only at the declaration
    with the parameter unbound, so the complete-page sink stayed
    invisible. Calls through such variables now analyze the literal body
    with the call-site arguments bound (pageflow), and package-level
    variables that are reassigned anywhere lose their initializer proof
    (rules.go reassignedVars), so a var later bound to bytes.Clone is
    (benign bounded slice through the same literal) and P18 (reject,
    reassigned var).
- Swarm round 3 (2026-08-14, at the round-2 fix commit) re-verified the
  delta; one remaining theoretical bypass in the same class was closed
  in a follow-up commit:
  - a two-hop function-variable chain (var a = func(p){ copy(out, p) };
    var b = a; b(page)) was approved through the initializer chain but
    the literal body was re-analyzed only for direct initializers, so
    the sink inside stayed invisible at the call. evalCall now follows
    the same bounded chain (funclitOf, at most two hops, no reassigned
    hop) and binds call-site arguments to the literal's parameters.
    through the same chain).
  Also fixed from reviewer P3s: the SOW Status production-LOC figure was
  corrected to the measured 5,049 (reader core 1,894) after the
  membership.go counter cleanup.
- Swarm round 4 (2026-08-14, at HEAD 2ad4001): the independent round
  review returned nine gate findings; every one was reproduced by the
  lead against the tree (exact mutation probes, gate exit 0) and closed
  in one commit at HEAD f96d13d:
  - helper-summary parameter flow lost page taint (maxLen accumulated
    from 0, so maxUnknown -1 never registered): fs.eval and evalResults
    now treat maxUnknown as at least the summary max;
  - direct function aliases and local closures lost page taint: local
    funcs are bound (st.localFuncs) and calleeTarget resolves local and
    package-level chains so calls through them analyze the literal body
    with call-site arguments bound;
  - container element extraction was unmodeled: IndexExpr/IndexListExpr
    now derive element page values while preserving the parameter source
    (generic xs[0] stays caller-dependent);
  - pointer and type-parameter byte params were not page-tainted:
    typeCanCarryPage recurses through *types.Pointer and accepts
    *types.TypeParam, and the expression-position StarExpr case is now
    modeled;
  - branch/loop states overwrote instead of joining: If branches,
    Switch/TypeSwitch/Select, and For/Range (zero-iteration join) now
    clone-and-join, and append results carry source taint so
    post-loop append(owned, out...) is caught;
  - multi-result calls mapped one RHS to the first LHS only: fresh
    callResults per assignment slot, with the stale cache deleted before
    re-evaluation;
  - named array/string conversion sinks were unrecognized: the sink
    check unwraps Named/Alias to the underlying type for both
    [N]byte(page) and string(page);
  - file-capability laundering through nested or package-level func
    literals: os.Stdin/Stdout/Stderr access and any call whose result
    type is file-bearing now fail outside the mapping owner (the mapping
    owner keeps its legit os use);
  - Mapping.View was minted expecting three arguments while the method
    takes two: the mint now uses Args[1] for the view length.
  63 benign) + 9 shell mutations exit 0, production scan clean on all
  five targets, go test ./... (both tag sets), -race, vet, gofmt zero
  diffs, cross-compilation - all green.
- Swarm round 5 (2026-08-14, at HEAD 57522e8): the independent round
  review returned ten gate findings (Luna resident) plus one
  multi-result struct-field taint gap (MiniMax P2), every one
  reproduced by the lead against the tree (exact mutation probes, gate
  exit 0 on the misses) and closed in one commit at HEAD 65ca62a:
  - helper summaries tested only the first source: choose(nil, page,
    true) lost the page taint; fs.eval/evalResults now taint when ANY
    recorded source is tainted and accumulate maxLen only over tainted
    sources;
  - named/naked returns were not summarized: analyzeFunc records
    named-result slots and feeds stores to them back into results and
    struct fields after the body;
  - void unknown function-variable calls were not transfers: an
    unproven var func([]byte) called with a page was invisible; such
    calls now transfer (scalar-result calls stay reads);
  - append(dest, src...) checked only the source: a complete mapped
    page view as the destination (append(page[0:4096:4096], ...))
    reallocates the full page into owned memory and is now rejected;
  - index and dereference stores were untracked: slots[0] = page and
    *h = page now bind the container/pointed-to variable;
  - range variables were never bound: range over [][]byte{page} now
    derives the loop value from the container taint;
  - interface/map/channel carriers were missing: any(page) keeps the
    argument taint, direct interface parameters carry page values
    (variadic and stdlib-form interfaces stay exempt), and channel
    send/receive round trips are modeled;
  - switch fallthrough dropped the previous case state: each case body
    now joins the falling-through case end state;
  - nested func-literal return context leaked: every ReturnStmt is now
    checked against its own enclosing function (literal or
    declaration) result types;
  - unproven package func vars with interface results failed open: a
    var func() io.Reader outside the mapping owner is now a capability
    launder (restricted to never-reassigned package-level func vars so
    stdlib callbacks stay clean);
  - multi-result struct-field taint (MiniMax P2): (chunk, err) helpers
    lost field taint on the struct slot; every returned expression now
    propagates composite-literal struct fields (first slot wins on
    name collisions) and evalCall records struct fields even when the
    whole result slot is tainted; parameter-sourced summary bounds now
    resolve the caller's constant slice symbol (page[48:112] stays a
    64-byte view) instead of reading the zero symbol as constant.
  rejections, 63 -> 66 benign). Gate tooling 5,104 -> 5,465 go-gate
  lines (+552 shell = 6,017 total).
  66 benign) + 9 shell mutations exit 0, production scan clean on all
  five targets, go test ./... (both tag sets), -race, vet, gofmt zero
  diffs, cross-compilation (windows/darwin/freebsd/netbsd) - all green.
  Round re-review (HEAD 2f5f71a): GLM reported a 4-line gate LOC drift
  (records said 5,469, measured 5,465; the debug strip after measuring
  removed four lines) - corrected in the records at 361d4c1 (5,465 go-gate
  + 552 shell = 6,017). Qwen then reported the cross-file package
  func-var scope hole: collectPkgFuncVars collected only the file under
  check, so var factory func() any declared in a.go and called from
  b.go of the same package skipped the interface-result fail-closed
  rule; reproduced by the lead (two-file probe, gate exit 0; same-file
  control rejects). Fixed at HEAD 0d007a8: the map is now built from
  Gate tooling 5,465 -> 5,474 go-gate lines (+552 shell = 6,026 total).
  66 benign) + 9 shell mutations exit 0, production scan clean on all
  five targets, go test ./... (both tag sets), -race, vet, gofmt zero
  diffs, cross-compilation - all green.
- Swarm round 6 (2026-08-14, at HEAD 83ea65b): the independent round
  review returned nine further gate findings (Luna resident), every one
  reproduced by the lead against the tree (exact mutation probes, gate
  exit 0 on the misses) and fixed in one commit:
  - scalar-result function-variable calls with a page argument were not
    transfers: an unproven callback var func([]byte) int can copy a full
    mapped view inside its unscanned body; such calls now transfer
    (probe f1, pin P57);
  - named function types hid the signature from the package func-var
    collection: type factory func() any; var f factory with an unbound
    body called outside the mapping owner escaped the interface-result
    fail-closed rule; the collector now unwraps *types.Named to the
    underlying signature, and the rule stays silent only for variables
    bound to a func literal somewhere in the package (their bodies are
    benign) (probe f2, pin P58);
  - map and channel parameters were not page carriers: m["x"] and <-ch
    through map[string][]byte and chan []byte parameters lost the taint;
    paramCanCarryPage now unwraps map/chan element and key types (probe
    f3, pin P59);
  - same-named fields of different struct-result slots kept only the
    first source: split5(a,b) (S,S) returning S{Data:a}, S{Data:b}
    dropped the slot-1 page; propagateStructResult now unions the field
    sources (probe f5a, pin P60);
  - returned local struct variables were not summarized: box5(p)
    { s := S{Data:p}; return s } lost the field taint, and selecting
    .Data directly on a call result was not evaluated (a fixpoint-pass
    cache staleness also kept callFields stale: the selector-on-call
    path now drops the cached call result before re-evaluation); both
    shapes closed (probes f5b, pin P61);
  - string conversion of a definite full-page view was pinned only by
    maxLen: string(page[0:4096]) slipped through; the sink now uses
    definitePageSpan (constant maxLen or constant sym bound) (probe f6,
    pin P62);
  - append into a complete mapped view was checked only for maxLen ==
    pageSize: append(page[0:8192:8192], ...) slipped through; the
    destination check now uses pageFull (probe f7, pin P63);
  - the owned byte-builder family was not a copy sink: bytes.NewBuffer
    (page) and bytes.Buffer.Write* / strings.Builder.Write* own the
    bytes with a result type the transfer rule cannot see; a new
    ownedCopySink rule rejects them (probe f8, pin P64);
  - import "unsafe" was not banned: unsafe.Slice over a mapped
    descriptor would mint page views the type layer cannot trace; the
    import ban now includes unsafe (probe f9, pin P65).
  The format record decoders' byte fields (FeedEntry.Name and friends)
  are grammar-bounded below a complete page; once the return-local-
  struct fix made those bounds visible at the copy sinks, the field caps
  are minted by value (formatFieldCaps: catalog name records 255, blob
  leaf data 4048, inline membership bitmaps 4000, structure payloads 32)
  exactly like the existing DecodeMetadataChunk mint - the production
  LookupFeedInto copy stays legal while a full page passed through the
  same path still fails.
  (272 -> 281 rejections, 66 benign). Gate tooling 5,474 -> 5,691
  go-gate lines (+552 shell = 6,243 total). Round-6 hardening (a
  literal-bound package func var later rebound to a non-literal has an
  unknowable callee and must stay fail-closed, not exempt) pinned as
  tooling 5,691 -> 5,712 go-gate lines (+552 shell = 6,264 total).
  rejections, 66 benign) + 9 shell mutations exit 0, production scan
  clean on all five targets, go test ./... (both tag sets), -race, vet,
  gofmt zero diffs, cross-compilation - all green.
- Round 7 (independent full-pass re-review): five residents passed the
  round-6 delta; the sixth returned a fresh fail with nine static-gate
  bypass classes outside the round-5/6 delta. All nine were probe-
  verified and fixed: P67 opaque function-field callees (h.cb(page)),
  P68 slice-indexed callees (fs[0](page)), P69 func-literal named
  results with naked returns, P70 pointer struct literals
  (&B{Data: page}).Data, P71 page boxing through any containers plus
  type assertions, P72 collection literals dropping definite element
  bounds, P73 package-global stores invisible to cross-function
  summaries, P74 string conversion of page views with unknown bounds
  (the sink moved from definite-span to tainted-and-not-provably-
  sub-page; plain []byte parameters and minted record fields stay
  legal), P75 reflect byte extraction (reflect added to the banned
  import set). Model work: interface values are page carriers,
  derivedPageValue keeps definite bounds, slice expressions carry an
  honest bound, container literals aggregate element bounds, func-
  literal summaries get the named-result pass, &-wrapped composite
  literals carry field taints, and package-scope stores write through
  348 -> 357 (282 -> 291 rejections, 66 benign); gate tooling
  5,712 -> 5,906 go-gate lines (+552 shell = 6,458 total).
  rejections, 66 benign) + 9 shell mutations exit 0, production scan
  clean on all five targets, go test ./... (both tag sets), -race, vet,
  gofmt zero diffs, cross-compilation, audit - all green.
- Round 12 (delta re-review): luna failed the round-11 delta with
  fifteen findings (fourteen real, one rejected false positive), all
  probe-verified by the lead against a fresh HEAD build before any fix:
  P127 variadic parameter slots did not join trailing arguments
  (pick([]byte{1}, page) inside pick(xs ...[]byte) read only the first
  argument; trailing args now join into the variadic slot in evalCall,
  summary duplicates carry the variadic slot, and func-literal variadics
  bind every trailing argument); P128 var-decl struct initializers lost
  field taints (var b B = B{Data: page}; DeclStmt now records
  composite/call/index field taints); P129 nested selector stores were
  invisible (o.Inner.Data = page; assignTarget now records dotted paths
  through selectorChain); P130 indexed call-result struct fields lost
  page bounds (makeList(page)[0].Data; propagateStructResult records
  container-of-structs element fields and the indexed-call selector
  branch re-evaluates the call before reading its recorded fields);
  P131 opaque callbacks with field-only page values escaped (cb(b) with
  b.Data = page; whole-value field promotion now runs on every missed
  and resolved argument and on the interface-miss receiver); P132
  unknown-bound views collapsed to zero inside struct fields
  (B{Data: m.View(0, n)}; evalComposite now propagates maxUnknown field
  values); P133 string-param field conversion with a local struct var
  escaped (var b B; b.Data = page; sink(b) where sink stringifies the
  field; the local store now reaches the string-param call check
  through whole-value promotion); P134 interface methods with an
  empty-interface result escaped (m.Make() any on a parameter and
  mgrVar.Make() any on a package interface variable; the
  interface-result rule now also fires for any-typed results, and
  func() any parameters and locals fail closed like opaque package
  function variables); P135 structs holding a file into an interface
  formal escaped (sink(H{F: f}) with sink func(any);
  checkInterfaceErasure now holds struct-with-file arguments); P136
  structs holding a file into runtime map keys escaped (m[H{F: f}] = 1;
  checkAssign mirrors the collection-slot rule); P138 else-nested branch
  reassignment diverged (if c1 ... else { if c2 ... }; joinWith now
  forwards ambiguous-binding state across both directions); P139
  package-initializer struct fields were invisible to later stores
  (var g = B{Data: pageSrc}; the pkg-init loop records struct-literal
  field taints into the shared package state and replays them); P140
  package method values resolved to stale initializers (var get =
  holder.Get; pkgBindings seed statement state, calleeTarget hands
  method values to resolveMethodValue with the receiver, and reassigned
  package variables are excluded). F10 (a named function type
  var f Fn; f(file) already flagged by the file-valued argument rule)
  was rejected as a duplicate class, not a new finding. One
  whole-value field promotion graduated fail-closed opaque-call struct
  fields (clean stdlib chains such as io.NopCloser(x.Get()()) over a
  bytes.Reader-like result were flagged); synthetic fail-closed
  callFields are now marked and excluded from promotion while field
  benign). Gate tooling 7,704 -> 8,097 go-gate lines (+552 shell =
  8,649 total).
  rejections, 67 benign) + 9 shell mutations exit 0, production scan
  clean on every scanned target, all fifteen round-12 probes reject on
  every scanned target, go test ./... (both tag sets), -race, vet (go
  and go-gate), gofmt zero diffs, cross-builds for all shell-harness
  targets (incl. netbsd), the import-graph gate with the 422-case
- Round 13 (delta re-review): luna failed the round-12 delta with seven
  findings, all probe-verified by the lead against a fresh HEAD build
  before any fix (probes /tmp/probe-r12l2, all exit 0 pre-fix and all
  reject after): P141 variadic ...any interface erasure escaped
  (sink(nil, file) with sink(xs ...any) stopped at the first formal;
  checkInterfaceErasure now evaluates each trailing argument against the
  variadic element type); P142 variadic string-param conversion escaped
  (sink([]byte{1}, page) with sink(xs ...[]byte){ string(xs[1]) } read
  only the first trailing argument; checkStringParamCalls now enumerates
  every trailing argument of a variadic slot, and noteStringConvs
  records index reads of parameter-sourced slices); P143 range and
  pointer rebinding of function bindings escaped (for _, f = range fs
  and *p = page-returning literal through p := &f left the old binding
  proof live; RangeStmt and bindLocalFunc now invalidate callable
  bindings); P144 nested struct-parameter fields escaped (take(o)
  returning o.Inner.Data with a caller-supplied nested page; the read
  side resolves dotted param paths through leafPathType and the copy
  and argument-flow fallbacks materialize every leaf path through
  paramLeafPaths); P145 map-key struct fields escaped (m[box{Data:
  page}] = 1 then for k := range m { k.(box).Data }; indexed stores now
  record struct-literal key fields on the container and key-only ranges
  bind them to the key variable); P146 method-value receiver
  string-conversion escaped (get := b.String; get() with String()
  converting b.Data; evalCall records resolved method values in
  callMethodValues and checkStringParamCalls reads the captured receiver
  from it); P147 non-empty interface result type-assert recovered a file
  (factory().(*os.File) with factory func() io.Reader; the capability
  launder now fails any assertion that recovers a file-bearing type out
  of an interface that *os.File itself satisfies, while scanned
  io.ReadCloser-shaped results stay benign per the content-transfer
  design); P148 (found by the lead in the same-failure search) type-
  switch cases recovered the same descriptor with the same escape
  (switch v := factory().(type) { case *os.File: return v }; the case
  (364 rejections, 67 benign). Gate tooling 8,097 -> 8,475 go-gate
  lines (+552 shell = 9,027 total).
  rejections, 67 benign) + 9 shell mutations exit 0, production scan
  clean on every scanned target, all eight round-13 probe files plus the
  type-switch recovery probe reject on every scanned target, go test
  ./... (both tag sets), -race, vet (go and go-gate), gofmt zero diffs,
  cross-builds for every shell-harness target, the import-graph gate
- Round 14 (delta re-review): luna failed the round-13 delta with six
  findings, all probe-verified by the lead on a fresh HEAD build before
  any fix (probes /tmp/probe-luna2, /tmp/probe-p16, /tmp/probe-luna5):
  P150 one-sided branch joins trusted an unproven local callable
  (g := f with f an opaque parameter, then g = func() body on one
  branch only; the old merge re-seeded the literal binding after the
  join so a later g() resolved a callee the other path may not hold;
  joinWith now marks one-sided local callable bindings ambiguous and
  calls through them fail closed); P151 struct-field provenance lost
  for selector-valued arguments (take(h.Box) with h.Box.Data assigned a
  page resolved only the whole-value taint; argFlowOf now flattens a
  selector base's recorded field taints, dropping the base path prefix,
  and falls back to parameter-derived leaf sources); P152 promoted
  embedded struct fields bypassed parameter-field resolution (take(o)
  returning o.Data with Data embedded through an anonymous inner
  struct; leafPathType and paramFieldType now resolve through
  types.LookupFieldOrMethod exactly like go/types selection, and the
  lookup package is passed because go/types hides unexported fields
  from a nil-package lookup - holder.data reads regressed without it);
  P153 container field provenance only for direct struct literals
  (m[b] = 1 with b.Data a page, plus the whole-value m[box{Data: page}]
  form: indexed stores now derive the key's field taints through the
  argument flow instead of the literal-only path; luna's original
  map[lbox]int shape was invalid Go, so the valid map[any] variant is
  the pinned form); P154 bounded param-field flow collapsed to unknown
  (b := box{Data: page[48:112]}; take(b) over-flagged after the
  round-13 param-field source because the paramField symbol case had no
  length resolution; eval/evalResults now resolve the field length from
  the call-site argument flow, keeping bounded flows legal); P155
  (records) the Status metrics were stale and contradicted the source
  round-13 close in f478278 while the Status still carried the
  rejections, 68 benign) and go-gate 8,611 lines (+552 shell = 9,163
  total), with this entry completing the trail. The lead's first
  full page through a package func-literal variable, P19 the two-hop
  chain, P140 package method values): the merge treated identical
  package initializer seeds as divergent (func-literal bindings have no
  stable text for the equality check) and let branch-local invalidation
  erase package-scope proofs; the merge now keeps identical nodes and
  exempts package-scope callables (their binding is proven by the
  package initializer and reassignment is policed by reassignedVars).
  8,611 go-gate lines (+552 shell = 9,163 total).
  rejections, 68 benign) + 9 shell mutations exit 0, production scan
  clean on every scanned target, all round-14 probe files reject (or
  stay accepted for the bounded form) on every scanned target, go test
  ./... (both tag sets), -race, vet (go and go-gate), gofmt zero diffs,
  cross-builds for every shell-harness target, the import-graph gate
- Round 15 (delta re-review): luna failed the round-14 delta with six
  findings, all reproduced by the lead before any fix (probes
  /tmp/probe-luna14, /tmp/probe-luna15): P155 switch-without-default
  kept the pre-switch callable binding reachable after the join (g := f
  with f an unproven parameter, one case rebinds g to a closure, no
  default: the old switchJoin dropped the pre-switch state so a later
  g() resolved the closure on every path; switchJoin now tracks the
  default clause and joins the pre-switch state when none exists, the
  same path zero-iteration loops take); P156 dereferenced arguments
  lost struct-field provenance (take(*p) with p := &b and b.Data a
  page: argFlowOf handled only Ident/Selector/CompositeLit/CallExpr;
  the StarExpr case now resolves the pointed-to object through recorded
  pointer bindings and alias chains (derefTarget) and materializes its
  fields (fieldTaintsOf), with the declared leaves of a struct-pointer
  parameter as the fallback); P157 indexed arguments lost container
  element provenance (take(xs[0]) with xs := []box{{Data: page}} and
  container parameters: the IndexExpr case resolves the container
  object and reads its element fields, and container parameters
  ([]box, map[K]box, chan box) expose their element leaves through
  paramFieldFallback); P158 type-asserted arguments lost provenance
  (take(v.(box)) with v any holding box{Data: page}: the TypeAssertExpr
  case unwraps Ident/StarExpr/CallExpr bases and reuses the same field
  resolution); P159 a naked return of one multi-valued call forwarded
  only the first result slot (return source6(p) with (error, []byte)
  and _, b := wrap(page): the per-result loop evaluated the call once
  and bound only slot 0; the ReturnStmt case now re-evaluates the call
  in the current state, distributes every callResults slot and the
  callFields, and falls through to the per-result loop for calls with
  no per-result records (mints like r.m.Page(pgno), whose early-return
  specialization records neither)); P160 a concrete method on a type
  declared OUTSIDE the scanned module could mint a file descriptor
  (net.TCPListener.File() returns a real *os.File with a net-package
  receiver, invisible to the os-only selector check: the selector mint
  branch now fails closed for concrete external-package receiver
  methods; not probe-pinnable because the loader cannot resolve net
  and no stdlib external concrete method returns *os.File, so the fix
  is code-review-verified and the scanned-tree behavior is unchanged -
  external package bodies were already skipped). The lead disproved
  the channel finding (all eight f5 channel probe shapes were already
  rejected, so no gate change was needed for send/receive paths) and
  the map-literal shape (a []byte field makes the struct key
  non-comparable, so m[lbox]int is invalid Go; the valid map[any]
  -> 375 rejections, 68 benign). Gate tooling 8,611 -> 8,805 go-gate
  lines (+552 shell = 9,357 total).
  rejections, 68 benign) + 9 shell environment mutations exit 0,
  production scan clean on every scanned target, all round-15 probe
  files reject (or stay accepted for the bounded form) on every
  scanned target, the round-14 probe trees (/tmp/probe-luna2,
  /tmp/probe-p16, /tmp/probe-luna5) return the same verdicts as a
  clean HEAD build (luna2/p16 rejected, luna5 accepted), go test
  ./... (both tag sets), -race, vet (go and go-gate), gofmt zero
  diffs, cross-builds for every shell-harness target, the import-graph
- Round 26 (full re-review after restart, 3ca6218): the swarm
  re-reviewed at 3ca6218 after the transport restart. MiMo PASS, kimi
  PASS, minimax PASS, qwen confirmed no additional findings. Luna
  returned FAIL with two findings: (1) P1 — Go OpenImmutable maps the
  entire physical file before bootstrap, violating the O(1) bootstrap
  and committed-extent-only mapping rules (spec section 3); Rust
  map_reader maps 2 pages, bootstraps, then remaps to committed_bytes.
  (2) P2 — the content-transfer gate scans 5 OS configs while the
  records claim 11-target cross-compilation coverage. The P1 was
  verified against Rust database_file.rs:106-114 and mapping.rs:262-266.
  The fix: OpenImmutable now maps exactly 2 pages for bootstrap, the
  reader bootstraps from those pages, then calls the new
  Mapping.Remap(committedBytes) which grows the mapping in place via
  mremap(MREMAP_MAYMOVE) on Linux and munmap+mmap on other POSIX
  targets; the Windows stub gains Remap and PhysicalSize stubs. The
  reader bootstrap now uses PhysicalSize (the locked file extent) for
  meta validation instead of the mapped size. A new regression test
  verifies that a 1 GiB corrupt-tail file fails bootstrap with
  FormatInvalid; the O(1) property is structural (2-page mmap →
  bootstrap → Remap only on success). The P2 was analyzed: the 5 OS configs (linux, darwin,
  freebsd, netbsd-as-other-POSIX, windows) cover every distinct
  mapping-owner code path; the remaining 6 targets (linux/386, arm,
  arm64, loong64, darwin/arm64, windows/arm64) compile the same source
  files as the scanned configs, so the typed scanner sees identical
  targets. The records were updated to state this explicitly.
  audit all pass.
- Round 25 (delta re-review, 0018a41): the round-24 closing commit
  review FAILed with ten findings across two residents: luna reported
  six gate escapes and one records nit; glm reported two reader P2s and
  two records nits. The lead probe-verified every finding on the true
  round-24 build: all six luna shapes escape (rc 0) and the seven pin
  shapes reject (rc 1) after the fixes; a same-family sweep then found
  three further escape shapes (asserted field-map key ranges and
  returned/bound asserted selectors under interface-typed switch
  cases), pinned as P240-P242; glm-1 is a false positive with
  full evidence below; glm-2 is a real reader divergence. Real classes:
  P233 type-asserted INDEXED CHANNEL send and receive
  (v.(*H).Chs[0] <- lbox{Data: page}; y := <-v.(*H).Chs[0]; y.Data
  with v an any holding *H): recordChanSendFields and chanRecvFields
  resolved the index-chain root only through objOfDeref, so a
  TypeAssertExpr root bound nothing and the received element laundered
  the page; the new chainRootObject resolves the asserted base
  variable, and both sites now record and resolve under the "Chs."
  prefix;
  P234 type-asserted INDEXED STRUCT stores (v.(*H).Items[0] =
  lbox{Data: page} then v.(*H).Items[0].Data): the store recorded
  element fields only on plain and dereferenced roots; the store's
  selectorIndexChain root now resolves through chainRootObject, the
  same object the typeAssertBaseOf read path resolves;
  P235 type-asserted FIELD MAP key stores (v.(*H).M[&b] = 1 after
  b.Data = page, with for k := range v.(*H).M reading k.Data): the
  map-key store bound only selectorChain or dereference roots; the
  branch now also handles a typeAssertBaseOf root and records the key
  fields under the field prefix on the asserted base variable, the
  same path the asserted key-only range resolves;
  P236 address-of TYPE-ASSERTED INDEXED element mutations
  (set(&v.(*H).Items[0], page) with set(b *lbox, p []byte) { b.Data =
  p }): applySummaryMutations' IndexExpr branch resolved the
  selectorIndexChain root through objOfDeref only; it now binds the
  asserted base under the "Items." prefix;
  P237 returned TYPE-ASSERTED indexed elements of interface
  parameters (return v.(*H).Items[0] with v an any parameter):
  propagateStructResult's IndexExpr branch handled only plain and
  dereferenced roots; an interface-typed parameter asserted to a
  holder now projects the asserted type's "Items."-prefixed leaf paths
  with the parameter source, exactly like `return v.(T)`;
  P238 returned indexed elements of CALL-PRODUCED selected containers
  (return makeH(p).Items[0]): propagateStructResult only handled an
  indexChainRoot call; a selected call-produced root now re-evaluates
  the call and strips the "Items." prefix from the callee's flattened
  element fields;
  P239 type-switch `case any` variables (switch x := h.get().(type) {
  case any: b := x.(lbox); return b.Data }): an interface-typed case
  projected no concrete leaves and the implicit variable carried no
  whole-value taint, so the body-side assertion laundered the page;
  typeSwitchJoin now binds whole-value taint on the implicit variable
  of interface-typed cases, and argFlowOf projects the asserted
  type's leaves for bind-then-read forms of any whole-tainted local
  interface value.
  Same-family sweep (three further shapes, probes 8, 9 and 11,
  escaped the pre-fix build and reject now):
  P240 type-switch `case any` + asserted FIELD MAP key range
  (switch x := h.get().(type) { case any: for k := range
  x.(*H).M { return k.Data } } with get returning *H{M:
  map[*lbox]int{{Data: page}: 1}}): the same interface-typed-case
  join gap left the asserted map-key store with no bound leaf, so
  the range laundered the page; typeAssertBaseOf stores and the
  whole-tainted case variable close it;
  P241 returned type-asserted SELECTOR values of interface
  parameters (return v.(*H).Inner with v an any parameter and
  H.Inner holding the page): propagateStructResult's SelectorExpr
  branch never projected an interface-parameter asserted base, so
  the returned struct laundered the page; the new
  typeAssertBaseOf branch projects the asserted type's leaf paths
  with the parameter source and propagates whole-tainted locals;
  P242 type-switch `case any` binding an asserted SELECTOR
  (switch x := h.get().(type) { case any: b := x.(*H).Inner;
  return b.Data }): the case bind of an asserted selector lost
  every leaf (no typeAssertBaseOf projection on the implicit
  variable), so the bound struct laundered the page; the
  SelectorExpr argument flow now projects whole-tainted locals'
  asserted leaves.
  Reader: glm-1 (membership id zero treated as corruption) is a FALSE
  POSITIVE: spec binary-format-v4.md:562-567 forbids storing zero as a
  membership id, and both readers reject a stored zero at lookup (Go
  lookupMembershipID corrupts, Rust membership_tree find -> require_id
  corrupts), so no valid file diverges. glm-2 is REAL: ContainsIndex
  read its word through wordBytes and skipped the trailing-zero
  canonical check that Word, ReadWords and Rust contains_index all
  apply (spec section 9); ContainsIndex now reads through
  readWordsInner so a zero FINAL word is corrupt here too, with a
  runtime regression test that patches the conformance fixture bitmap
  (mirroring membership_view_tests.rs word(1) on an inline [1, 0]
  image).
  Records corrections: the round-24 validation sentence "119 reject"
  is corrected to 120 above (the 125 prior trees are 120 rejections
  plus the five recorded benigns l18m, r10-base, r11-base, luna1b,
  luna1c); the round-24 and round-23 "ten cross-builds" sentences are
  corrected to eleven (the harness lists 11 GOOS/GOARCH targets
  including netbsd/amd64); the milestone-1 report "4 packages" and
  "10 pairs" are corrected to 5 packages (root, format, reader,
  mapping, work) and eleven pairs.
  ten new fail pins P233-P242). Gate tooling 10,801 -> 11,016
  go-gate lines (+552 shell = 11,568 total).
  rejections, 71 benign) + 9 shell environment mutations exit 0,
  production scan of v4/go clean (rc 0), all eleven r25 probe shapes
  reject (rc 1) on the closing build (one probe per isolated tree),
  go test ./... (both tag sets) including -race -count=1, vet (go and
  go-gate), gofmt zero diffs, eleven cross-builds, the import-graph

- Round 24 (delta re-review, d482c1c): luna failed the round-23
  closing commit with seven findings; the lead probe-verified all six
  code findings on the true round-23 build (probes
  /tmp/probe-l23-l1..l6, with l2a/l2b as the literal-return control
  and the captured-assignment escape) and confirmed the seventh is a
  records contradiction. Real classes, all fixed in this round:
  l1 foreign EXPORTED struct fields in an unproven callee result were
  skipped wholesale by failClosedCallFields (the structDeclPkg !=
  pc.pkg early return): with a LOADABLE foreign package
  (encoding/pem), an unproven call result pem.Block{Bytes: page}
  laundered the page through the exported field read. The
  round-23 l23-1/1b/1c probes of this shape were dismissed there as
  unreachable because their imports are banned or fail to type-load;
  the pem variant proves the code gap is real whenever the foreign
  package loads. The guard now walks foreign EXPORTED fields and
  skips only foreign UNEXPORTED fields (the bytes.Reader.src shape
  the existing benign pins rely on);
  l2 a directly called func-literal COMPOSITE LITERAL argument
  (func(x B) { out = x.Data }(B{Data: page})) lost the field taint:
  materializeStructFields returned early on a nil ident, so the
  argFlowOf fallback was unreachable for composite literals; the
  ident-nil path now falls through to argFlowOf;
  l3 INDEXED SELECTOR-rooted channel sends and receives
  (h.Chs[0] <- B{Data: page}; x := <-h.Chs[0] with h.Chs []chan B)
  lost element provenance: the indexChainRoot-only recording missed
  selector roots, so recordChanSendFields and chanRecvFields now
  record and resolve the base object under the "Chs." prefix;
  l4 returned INDEXED SELECTOR values (return h.Items[0] with
  h.Items []lbox) lost the element records: propagateStructResult's
  IndexExpr branch only resolved the indexChainRoot; it now also
  strips the "Items."-prefixed records on the base object;
  l5 address-of INDEXED SELECTOR elements (&h.Items[0] passed to a
  pointer-mutation helper) never bound the container:
  applySummaryMutations' IndexExpr branch only handled the
  indexChainRoot; it now binds the base object under the "Items."
  prefix;
  l6 MULTI-TYPE type-switch cases (switch v := h.get().(type) {
  case B, *B: ... }) lost the case leaves: go/types types the
  implicit variable with the guard's interface type, so
  paramLeafPaths(cv.Type()) projected nothing; typeSwitchJoin now
  projects the page-carrying leaves of every listed case type.
  Records contradiction: the round-23 validation sentence said
  507/507 (437 rejections, 69 benign) while the count line correctly
  says 70 benign; the sentence is corrected above.
  acceptances; six new fail pins P226-P231 and one benign pin P232).
  Gate tooling 10,695 -> 10,801 go-gate lines (+552 shell = 11,353
  total).
  rejections, 71 benign) + 9 shell environment mutations exit 0,
  production scan of v4/go clean (rc 0), all eight l24 probe trees
  reject (rc 1) on the closing build (l1..l6 plus the l2a literal
  return and l2b captured-assignment forms), all 125 prior probe
  trees re-scanned with the closing build: 120 reject and the five
  recorded benigns stay accepted (probe-l18m, probe-r10-base,
  probe-r11-base, luna1b/luna1c), all twenty round-23 l23 probe
  trees keep their verdicts on the closing build (all reject except
  the self-pointer benign q2 rc 0), go test ./... (both tag sets)
  including -race -count=1, vet (go and go-gate), gofmt zero diffs,
  and the SOW audit all pass.

- Round 23 (delta re-review, 07081a2): luna failed the round-22
  delta with seven findings; the lead probe-verified all seven on the
  true round-22 build (probes /tmp/probe-l23-1..7 with lettered
  variants): five were real escape classes, two are false positives,
  and one reported variant is invalid Go. Real classes:
  P219 type assertions on func-field any call results
  (h.get().(B).Data with get func() any) projected no asserted leaves:
  the base's whole-value taint was only projected inside the objOf
  guard, so a DIRECT call base with no binding object never ran the
  projection; the projection now runs for every assertion base after
  the recorded-field and parameter paths;
  P220 type-switch variables from the same base (switch v :=
  h.get().(type) { case B: v.Data }) carried no asserted leaves:
  typeSwitchJoin mirrors the value-switch join and records the
  page-carrying leaves of every matched case type on the implicit
  per-case variable (info.Implicits) when the asserted base is
  whole-tainted;
  P221 pointer-mutation methods on INDEXED elements (xs[0].Set(page))
  never bound the container: applySummaryMutations now recognizes
  chainContainsIndex receivers and binds the root container's element
  records, under the selected field path when the receiver carries
  one;
  P222 indexed channel send/receive (cs[0] <- B{Data: page}; x :=
  <-cs[0]; x.Data with cs []chan B) lost element provenance:
  recordChanSendFields and chanRecvFields now record and resolve the
  indexChainRoot container;
  P223 pointer-wrapped map parameters (m *map[*B]int under a key-only
  range) lost the key leaves: mapUnderlying unwraps the pointer layer.
  False positives, probe-verified already rejected on the pre-fix
  build (no change): l23-1/1b/1c foreign exported struct fields
  skipped by failClosedCallFields are unreachable - image is a banned
  import and net/crypto/x509 fail to type-load (missing vendored
  x/net), so the gate fails closed on the type-check error; l23-4/4b/
  4c variadic direct func-literal argument flow is rejected in every
  probe shape. Invalid-Go variant: the l23-5 function-call form
  set(xs[0], page) does not type-check (an indexed element is not
  addressable without &), so only the method form is a real class.
  unchanged). Gate tooling 10,463 -> 10,648 go-gate lines (+552
  shell = 11,200 total). Five real escape classes fixed; two false
  positives documented with probe evidence; one invalid-Go variant
  recorded; none dismissed.
  The same round's full-scope pass then returned two further verified
  findings (Qwen), both probe-verified before any fix:
  P224 a two-value type assertion or map index (b, ok := v.([]byte), b,
  ok := m[k]) types the expression node as the (T, bool) tuple, and the
  whole-value carrier test (evalExpr TypeAssertExpr/IndexExpr/
  IndexListExpr) read typeCanCarryPage(tuple) as false, laundering the
  page taint of every comma-ok byte read into the bound variable
  (probe /tmp/probe-l23-q1 exited 0 pre-fix; valueCarrierType now
  unwraps the value slot of a 2-tuple before the carrier test);
  P225 a self-referential named pointer (type P *P) routed through the
  unproven-callee result walk hung the scanner: derefStruct's Named
  unwrap loop and mapUnderlying's pointer unwrap (added for the l23-7
  fix) had no seen guard, so failClosedCallFields(P) recursed forever
  (probe /tmp/probe-l23-q2 timed out at 90s pre-fix; both walkers now
  stop at the revisiting named type, mirroring containerElemTypeSeen).
  acceptances). Gate tooling 10,648 -> 10,695 go-gate lines (+552
  shell = 11,247 total).
  rejections, 70 benign) + 9 shell environment mutations exit 0,
  production scan of v4/go clean (rc 0), all sixteen l23 probe trees
  reject (rc 1) on the closing build, all 125 prior probe trees
  re-scanned with the closing build: 120 reject and the five recorded
  benigns stay accepted (probe-l18m, probe-r10-base, probe-r11-base,
  luna1b/luna1c), the q1/q2 verification probes reject or complete as
  pinned (q1 two-value assert rc 1, q1b two-value map index rc 1, q2
  self-pointer rc 0 without hang, q2b func-param form rc 0), go test
  ./... (both tag sets) including -race -count=1, vet (go and go-gate),
  gofmt zero diffs, eleven cross-builds, the import-graph gate, and the
  SOW audit all pass.
- Round 22 (delta re-review, 113867a): luna failed the round-21
  delta with nine findings; the lead probe-verified all nine on the
  true round-21 build (git archive HEAD, probes /tmp/probe-l22-1..9):
  eight were real gate escapes, one (l22-4/4b, func-literal closure
  materialization of a call argument) is a false positive: the probes
  carry the page view through the literal parameter to its result and
  then into append, a real complete-page copy the gate correctly
  rejects (rc 1 on both the pre-fix and closing builds), and one
  (l22-9) is a real false rejection fixed below. Fixed with durable
  pins P210-P218:
  P210 nested opaque carriers (x.Outer().Items[0].Data with Outer
  returning an unscanned interface result whose container FIELDS
  expose element leaves): containerElemTypeSeen and leafNameSet now
  unwrap *types.Pointer and recurse container fields at any depth;
  P211 two-variable map ranges over container VALUES and
  pointer-wrapped container KEYS keep their element leaves;
  P212/P213 address-of SELECTED FIELD and INDEXED ELEMENT arguments
  (&h.Inner, &xs[0]) bind mutation summaries to the base object and
  the container element fields; P214 directly called func-literal
  pointer-parameter mutations export their summary and re-bind at
  the call site, with the literal's recorded sources rebased to its
  own parameter slots; P215 struct-field channel send/receive and
  select-clause sends keep the "Ch."-prefixed provenance (selector
  and alias channel shapes); P216 indexed whole-struct stores through
  DEREFERENCED containers ((*q)[0] = B{Data: p}) record on the
  pointer and its alias target; P217 runtime map-key stores through
  field containers (h.M[&b] = 1) record key fields under the field
  prefix; P218 benign bounded struct-field helper copies stay legal:
  applySummaryMutations now skips writes whose recorded sources are
  all clean and reports tainted only when an argument is actually
  tainted (the previous over-taint rejected clean arg copies), and
  failClosedCallFields was rewritten as a package-aware recursive
  leaf walker that stops at foreign struct declarations so stdlib
  private fields (bytes.Reader.src) never taint benign copies.
  acceptances). Gate tooling 10,045 -> 10,463 go-gate lines (+552
  shell = 11,015 total). Eight real escapes fixed; the l22-4/4b
  false positive documented with probe evidence (no change needed);
  the l22-9 false rejection fixed; none dismissed.
  rejections, 69 benign) + 9 shell environment mutations exit 0,
  production scan of v4/go clean (rc 0), all nine l22 probes behave
  as pinned (rc 1 for l22-1/2/3/4/4b/5/6/7/8, rc 0 for l22-9 on the
  closing build; pre-fix the eight escapes exited 0 and l22-9 exited
  1), all 125 prior probe trees re-scanned with the closing build:
  119 reject (all round-19/20/21 shapes) and the five recorded
  benigns stay accepted (probe-l18m, probe-r10-base, probe-r11-base,
  luna1b/luna1c), go test ./... (both tag sets) including -race
  -count=1, vet (go and go-gate), gofmt zero diffs, the import-graph
  gate, and the SOW audit all pass.
- Round 21 (delta re-review, d5d5be9): luna failed the round-20
  delta with eight findings; the lead probe-verified all eight as real
  escapes on the round-20 build before any fix (probes /tmp/probe-l20-
  1/2/3/4/5/6/7/8) and fixed them with durable pins:
  P200 a TWO-variable map range bound the key fields to the VALUE
  variable (for k, v := range m with m map[*box]int lost k.Data); the
  range branch now detects map ranges and splits the recorded entry
  fields between the key and the value variable by the declared leaf
  names of the map key and element types;
  P201 a container literal element produced by a CALL lost its fields
  (xs := []box{makeBox(p)}; xs[0].Data) and P202 an element that is
  the ADDRESS of a variable lost them ([]*box{&b}); elementFieldsOf
  now falls back to argument-flow renaming, and argFlowOf resolves
  &b through the pointed-to variable's recorded fields;
  P203 an indexed whole-struct store into a FIELD container lost its
  element provenance (h.Items[0] = B{Data: p}): the store now records
  under the "Items.Data" flattened path on the base object, the same
  path the read resolves;
  P204 a dereference store through an ALIASED pointer lost the original
  variable (q := &b; *q = B{Data: page}; b.Data): the StarExpr store
  and the selector-field store now also record on the derefTarget
  alias, so reads under either name stay sourced;
  P205 pointer-receiver METHOD mutations never reached the caller
  (b.Set(p) writing b.Data = p; b.Data): funcSummary gained a
  mutFields map exported from the callee's final pointer-parameter
  field records (tainted stores only, value params excluded);
  applySummaryMutations re-binds the recorded sources to the actual
  argument values at every resolved call site, including receivers,
  address-of arguments, and field-chain receivers;
  P206 struct field provenance died at a channel send/receive (ch <-
  B{Data: page}; x := <-ch; x.Data): the send records the sent
  value's fields on the channel variable (tainted sends join; clean
  sends never erase), and argFlowOf's receive case binds them to the
  received variable;
  P207 an address-of argument lost the variable's fields (takePtr(&b)):
  argFlowOf resolves the & operand through the recorded fields;
  P208 an interface method call returning a CONTAINER failed open on
  element fields (x.Boxes()[0].Data): both unprovable-callee paths
  now fail closed on the page-carrying element and key leaves of
  container results (failClosedCallFields), matching the existing
  direct-struct-field policy;
  P209 a range over a TYPE-CONVERTED container lost the element leaves
  (for _, x := range []B(h.Items)): the range call branch detects
  conversions and resolves the operand's element fields.
  Gate tooling 9,648 -> 10,045 go-gate lines (+552 shell = 10,597
  total). All eight were real; no finding was dismissed.
  rejections, 68 benign) + 9 shell environment mutations exit 0
  probe trees reject (rc 1; the 30 round-19/20 shapes and the eight
  round-21 shapes), luna1b/luna1c stay accepted as the recorded known
  open question, go test ./... (both tag sets) including -race
  -count=1, vet (go and go-gate), gofmt zero diffs, the import-graph
  gate, and the SOW audit all pass.
- Round 20 (delta re-review, 64c8c13): luna failed the round-19
  delta with six findings; the lead probe-verified three real escape
  families on the round-19 build before any fix and pinned them:
  (a) selector-held container elements lost provenance in five shapes
  (for _, x := range h.Items, h.Items[0].Data, take(h.Items[0]),
  for _, x := range m[0].Items with m [1]holder, and a struct-field
  MAP key-only range for k := range h.Keyed): the range path now
  resolves selector/index/assert/star container roots through
  argFlowOf, compositeFields records container-field element fields
  under the "Field." prefix, paramLeafPaths emits the prefixed element
  leaves at every container depth plus map KEY leaves, and argFlowOf's
  index branch resolves selector/assert roots through the renamed
  fields - pinned P193-P197; (b) NAMED map types lost key provenance
  (type M map[*box]int; for k := range m with m M): types.Unalias
  does not unwrap named types, so the new mapUnderlying helper unwraps
  the named underlying chain and compositeFields/paramFieldFallback
  use it for the key-side leaves and literal key unions - pinned P198;
  (c) a dereference struct store lost field provenance (*p = B{Data:
  page} then p.Data and (*p).Data): the AssignStmt StarExpr branch
  now records the struct's field taints on the pointed-to object
  (including the package-level join) instead of only whole-value
  stores - pinned P199 with two create ops. The other three findings
  were probe-verified false positives: (1) unproven scalar indirect
  calls (func-field, interface-method, local func variable, and
  package-captured callable shapes) all reject on the round-19 build;
  (3) selector-valued arguments after a clean sibling store (clean
  sibling struct, clean container field, pointer parameter, and
  call-produced shapes) all reject; (5) append-built struct containers
  (literal plus variable append, indexed store, and parameter-sourced
  rejections, 68 benign acceptances). Gate tooling 9,497 -> 9,648
  go-gate lines (+552 shell = 10,200 total).
  rejections, 68 benign) + 9 shell environment mutations exit 0
  scanned target, every round-19 and round-20 probe shape escapes the
  fresh HEAD build (rc 0) and rejects the closing build (rc 1; 30/30
  probe trees), luna1b/luna1c stay accepted as the recorded known open
  question, go test ./... (both tag sets) including -race -count=1,
  vet (go and go-gate), gofmt zero diffs, the import-graph gate, and
  the SOW audit all pass.
- Round 19 (delta re-review): luna failed the round-18 delta with
  twelve findings; the lead reproduced eight real escape classes on a
  fresh HEAD build before any fix (probes /tmp/probe-luna2/4a/4b/5/6/7/
  8b/8c/9): P183 an EMPTY default (default:) discarded the pre-switch
  state (the old code joined only when no default existed, so a switch
  with a no-op default lost the page held before the statement);
  P184 a returned selected field of a call lost provenance (func
  retSel(p []byte) inner { return makeBox(p).Inner }: the return
  summary resolved only whole values, so the call-site read x :=
  retSel(page); x.Data was unsourced; propagateStructResult now strips
  the selection prefix from the base's recorded fields, parameter
  leaves, or callee flattening); P185 a returned element of an INLINE
  literal index lost provenance (return []box{{Data: p}}[0]: the
  index-return branch had no composite-literal root handling, now
  resolved through the literal's element-field union); P186 a
  container PARAMETER under range never bound its declared element
  leaves (for _, x := range xs with xs []box: the range path resolved
  only recorded local containers and inline literals, so the loop
  value carried no parameter-sourced fields; the range branch now
  applies paramFieldFallback to a parameter container); P187 a nested
  selector read after an interface assertion resolved only the
  asserted type's DIRECT leaf (v.(outer).Inner.Data leaked: the
  assertion branch read the recorded map by the single field name;
  typeAssertBaseOf now unwraps the full dotted chain and assertLeafType
  resolves the path against the asserted type's leaves, including
  container element leaves); P188 an asserted struct VALUE bound to a
  take argument lost its asserted leaves (take(v.(outer).Inner)):
  argFlowOf's selector branch had no assertion-root case; the new
  branch renames the source leaves to the selected value's direct
  field names exactly like recorded-base arguments); P189 a NAMED
  container type unwrapped no underlying chain (type matrix [1][1]box:
  containerElemType looked only at direct alias types, so the matrix
  parameter exposed no element leaves; the recursive
  containerElemTypeSeen unwraps named underlying chains with a
  self-reference guard and paramFieldFallback applies it at every
  level); P190 a map-key PARAMETER kept no key leaves for a key-only
  range (for k := range m with m map[*box]int: the key-side leaf
  fallback existed only for recorded stores; paramFieldFallback now
  adds the declared key leaves and key container chains). The lead's
  same-failure sweep over those fixes proved two further escapes on
  the same HEAD build and pinned them here: P191 a container literal
  with a VARIABLE element lost the element fields (b := box{Data:
  page}; xs := []box{b}: compositeFields resolved only literal
  elements; the new elementFieldsOf resolves variables and parameters
  through the recorded fields and nested containers through
  recursion); P192 a map composite-LITERAL key lost its struct fields
  through the key-only range (m := map[*box]int{{Data: page}: 1}:
  compositeFields now unions the KEY side exactly like the value
  acceptances). Gate tooling 9,234 -> 9,497 go-gate lines (+552 shell
  = 10,049 total). The remaining findings were verified not real:
  luna1b/luna1c (the two-value asserted-container bind and the
  interface-parameter container element) are intentionally accepted
  shapes and stay accepted as a known open question, three reports
  were false positives or dead code, and the stale-record report
  by this record sync.
  rejections, 68 benign) + 9 shell environment mutations exit 0,
  production scan clean on every scanned target, every round-19 probe
  shape escapes the fresh HEAD build (rc 0) and rejects the closing
  build (rc 1), the P172 two-value-assertion regression probe stays
  rejected, l19 and all prior probe trees keep their verdicts
  (luna1b/luna1c stay accepted), go test ./... (both tag sets), vet
  (go and go-gate), gofmt zero diffs, the import-graph gate with the
- Round 18 (lead same-failure sweep over the round-17 fixes): the
  interface-type-assertion family had three real escapes on a fresh
  HEAD build before any fix (probes /tmp/probe-l19): P171 a returned
  asserted interface-param struct lost caller provenance (func as(v
  any) box { return v.(box) } with x := as(any(box{Data: page})):
  propagateStructResult only handled literals, indexes, and
  parameters, so a returned type assertion over an interface
  parameter carried no leaves and the explicit any() conversion
  dropped the argument's fields; the return case now joins the
  asserted type's leaves with the parameter source, and single-arg
  type conversions keep the converted argument's field provenance);
  P172 a two-value asserted read inside a helper lost caller
  provenance (if b, ok := v.(box); ok { return b.Data } with v any:
  two-value assertions type the expression node as the (T, bool)
  tuple, so every assertion site must read the asserted type from the
  assertion's TYPE EXPRESSION, not from the expression node's type;
  the evalExpr direct-read, argFlowOf helper-binding, and
  propagateStructResult return paths now materialize the asserted
  type's leaves for interface-typed parameters, fixing both the
  helper-side binding and the direct v.(T).Data read). The same sweep
  then verified the IndexListExpr family on a clean self-contained
  tree after the earlier probe tree proved poisoned: P173 a field read
  through a multi-level index of a literal-bound matrix escaped (m :=
  [1][1]box{{{Data: page}}}; append(..., m[0][0].Data...): evalExpr's
  selector index branch and argFlowOf's IndexExpr binding resolved
  only ONE trailing index, so the root container's element-field
  taints were unreachable at depth two); P174 the same one-level gap
  for element bindings (x := m[0][0] then take(x)); P175 a forced
  element extraction from a literal escaped the bind path (x :=
  []box{{Data: page}}[0]: argFlowOf's IndexExpr case had no
  composite-literal root handling at all, unlike the read path);
  P176 a multi-level index of a call result stayed rejectable only
  through an unrelated path and is now resolved explicitly by the
  same root unwrap. A shared indexChainRoot walk now unwraps every
  trailing index level to the root expression, and the read, bind,
  and return sites dispatch the root through the same
  literal/ident/call resolution the one-level cases used. The same
  sweep then found the return-path half of the class: P177 a returned
  matrix element lost provenance (func retElem(m [1][1]box) box {
  return m[0][0] } with append(..., retElem([1][1]box{{{Data:
  page}}}).Data...): propagateStructResult's IndexExpr return branch
  resolved only ONE trailing index, so the root's recorded fields and
  parameter leaves were unreachable; the return branch now unwraps
  the full index chain) and P178 the bound form (x :=
  retElem([1][1]box{{{Data: page}}}); take(x)). Closing P177 required
  the declared-leaf fallback to unwrap every container depth:
  paramFieldFallback materialized element leaves for only ONE
  container level ([]box worked, [1][1]box exposed nothing), so the
  matrix parameter lost its caller fields even after the return
  branch could name the root; the fallback now loops over every
  392 rejections, 68 benign). Gate tooling 9,003 -> 9,136 go-gate
  lines (+552 shell = 9,688 total). The same sweep then closed the
  selector-over-index half of the family: P179 a nested field read
  through a multi-dim indexed local escaped (m := [1][1]outer{{{Inner:
  inner{Data: page}}}}; append(..., m[0][0].Inner.Data...): selectors
  above the index chain never reached the root record, because the
  indexed selector branch entered only when the base under the outer
  field name was directly an IndexExpr); P180 the bound form (x :=
  m[0][0].Inner then take(x)); P181 the inline literal form
  ([1][1]outer{{{Inner: inner{Data: page}}}}[0][0].Inner.Data); and
  P182 pins the nested-selection call-result form (makeMatrix(page)[0]
  [0].Inner.Data, already rejectable through the call-root branch).
  selectorIndexChain now collects every selector name above and
  between index levels, evalExpr reads the full dotted path on the
  root record (with the declared-leaf fallback for container
  parameters), and argFlowOf's selector case strips the collected
  wrapper path onto the selected value's direct field names for
  396 rejections, 68 benign). Gate tooling 9,136 -> 9,234 go-gate
  lines (+552 shell = 9,786 total).
  rejections, 68 benign) + 9 shell environment mutations exit 0,
  production scan clean on every scanned target, all round-18 probe
  shapes reject (l19 type-assertion trees, multi-dim index read/bind/
  return trees, forced literal extraction, and selector-over-index
  trees) and every prior probe tree keeps its verdict (l17b, l16,
  luna15, f2only2, luna2, p16 reject; luna5 stays accepted), go test
  ./... (both tag sets), -race, vet (go and go-gate), gofmt zero
  diffs, cross-builds for every shell-harness target, the import-graph
- Round 17 (lead same-failure search over the round-16 fixes): the
  round-16 call-base fallbacks covered direct argument positions only;
  the lead verified five escape families on a fresh HEAD build before
  any fix (probes /tmp/probe-l17b): P166 a struct value bound from a
  call-produced container element lost the element fields (x :=
  makeList(page)[0] then take(x): the AssignStmt and DeclStmt field
  switches resolved only recorded variable bases, while argFlowOf's
  round-16 fallback applied to direct arguments; both binding sites now
  resolve non-literal right sides through argFlowOf, the same
  resolution direct argument flow uses, and materializeStructFields
  applies the same fallback to closure parameters); P167 a bound
  selected field of a call-produced struct lost provenance (x :=
  makeBox(page).Inner: the argFlowOf selector call-branch built its
  chain from the base only, so the outermost selection was never
  stripped and the callee's flattened paths never matched; a new
  callRootChain walk now collects every selector name down to the
  producing call, and callProducedFields strips the full dotted prefix,
  shared by argument flow, binding sites, and closure-parameter
  materialization); P168 the same bound-selection gap for recorded
  local bases (x := b.Inner with b.Inner.Data a page: the binding sites
  had no SelectorExpr case at all; the selectorChain rename now
  applies, with the parameter fallback for parameter-held bases);
  P169 bound container-parameter elements lost the caller's leaves
  inside a helper (x := xs[0] with xs a container parameter:
  fieldTaintsOf's parameter fallback now feeds the binding resolution
  exactly like direct argument flow); P170 nested call-produced
  selector chains stayed reachable at every depth (makeBox(page)
  .Inner.Inner.Data reads, take(makeBox(page).Inner.Inner) arguments,
  and bound intermediates: propagateStructResult recorded only the
  top-level field name of a returned struct literal instead of the
  flattened dotted paths, so every nested selection missed; the
  returned-literal path now joins compositeFields' flattened records,
  and the evalExpr selector read resolves the full dotted chain through
  benign). Gate tooling 8,935 -> 9,003 go-gate lines (+552 shell =
  9,555 total).
  rejections, 68 benign) + 9 shell environment mutations exit 0,
  production scan clean on every scanned target, all round-17 probe
  shapes reject on every scanned target and every prior probe tree
  keeps its verdict (l16, luna15, f2only2, luna2, p16 reject; luna5
  stays accepted), go test ./... (both tag sets), -race, vet (go and
  go-gate), gofmt zero diffs, cross-builds for every shell-harness
  audit all pass.
- Round 16 (delta re-review): luna failed the round-15 delta at HEAD
  e03ecba with five findings; the lead reproduced four as real escapes on
  a fresh HEAD build before any fix (probes /tmp/probe-l16) and the fifth
  is code-review-verified (the loader cannot resolve net, no loadable
  external concrete method returns a page-bearing value, and scanned
  method values are approved callees): external method-value
  receivers bypassed the unprovable-receiver transfer check (get :=
  b.String; get() with String() converting a page-bearing b.Data: the
  checkCall selector branch only covers direct SelectorExpr calls, so a
  captured method value hid the full-page receiver; checkCall now
  resolves the method-value receiver through the flow pass and fails
  closed on a complete page); P162 partial local struct records
  suppressed parameter leaves (o.Other = 1 inside a helper erased the
  caller-supplied o.Data on the way out: fieldTaintsOf returned the
  partial local record for a struct parameter without joining the
  declared leaf sources, the same suppression materializeStructFields
  already avoided; fieldTaintsOf now copies local/package records and
  joins paramFieldFallback for every unrecorded path); P163
  call-produced containers and structs lost element/field provenance in
  argument flow (take(makeList(page)[0]) and take(makeBox(page).B)
  resolved only recorded variable bases: the IndexExpr and StarExpr
  cases now fall back to the callee's callFields, and selector bases
  build the dotted prefix across nested selector chains and rename the
  callee fields onto the selected argument); P164 returned struct
  parameters and container elements lost caller field provenance
  (func id(b box) box { return b } with x := id(box{Data: page}) read
  clean: propagateStructResult only propagated the returned object's
  local/package records, so an identity helper erased the caller's
  field taints; the return case now joins the package struct records,
  the paramFieldFallback leaves of the returned parameter, and the
  IndexExpr-returned local/parameter container element fields); P165
  promoted embedded leaves were absent from the parameter leaf fallback
  (wrap(louter{linner{Data: page}}) read clean because paramLeafPaths
  recorded only linner.Data, while the field read resolves the promoted
  alias o.Data; the anonymous-embedding walk now repeats with the
  (375 -> 379 rejections, 68 benign). Gate tooling 8,805 -> 8,935
  go-gate lines (+552 shell = 9,487 total).
  rejections, 68 benign) + 9 shell environment mutations exit 0,
  production scan clean on every scanned target, all round-16 probe
  files reject on every scanned target together with the round-15
  probe trees (/tmp/probe-luna15, /tmp/probe-luna14) and the previous
  round trees (/tmp/probe-f2only2, /tmp/probe-luna2, /tmp/probe-p16,
  /tmp/probe-luna5) returning the same verdicts as a clean HEAD build,
  go test ./... (both tag sets), -race, vet (go and go-gate), gofmt
  zero diffs, cross-builds for every shell-harness target, the
  pass.
- Round 11 (delta re-review): luna failed the round-10 delta with twelve
  further findings, all probe-verified by the lead (each reproduced as a
  real escape on a fresh HEAD build before any fix): P112 branch-local
  func-literal bindings diverged at joins (f reassigned on one path
  resolved only that branch's body; the join state now marks the binding
  ambiguous and calls through it fail closed); P113 partial struct-local
  stores suppressed caller field taints (writing b.Data clean locally
  erased the untouched b.Other's param-field source on a copy —
  materializeStructFields now fills unrecorded fields from the parameter
  fallback); P114 package-global whole-struct stores did not write
  pkgStructs (g = B{Data: page} in one helper was invisible to field
  reads in another); P115 indexed struct stores lost element field
  taints (xs[0] = B{Data: page} did not mark the container, so xs[0].Data
  read clean); P116 nested and promoted struct fields only resolved
  direct names (o.Inner.Data and embedded promotion now flatten through
  compositeFields and the selector chain); P117-P118 closure parameters
  with struct fields bound only whole-value taint (func-literal calls
  now materialize the closure parameters' fields from the argument flow;
  the refined shape b.Data = page; f(b) is pinned too); P119 opaque
  interface methods returning owned strings over a page-carrying
  receiver escaped (s.Text() with s holding B{Data: page}: the receiver
  of an interface-method miss is now evaluated and its whole-value plus
  struct-field taints are recorded at the transfer point, so the call
  site fails closed as a complete-page transfer); P120 dynamic
  interface-method struct results and type-asserted struct values lost
  field taints (callFields fail closed on byte-bearing fields;
  TypeAssertExpr reads the recorded element fields); P121-P122
  string-param conversion through helper chains lost the caller's
  field-sourced slot (propagateConvertParamSources narrows to plain
  parameter references and carries the converting marker through
  outer -> s chains); P123 runtime map-key taint was not evaluated
  (m[&page] = 1 now joins the container taint); P124 struct-with-file
  into conversion/send/range slots (any(H{F: f}), ch <- H{F: f}, and
  range over file structs now fail closed); P125 runtime map-key file
  393 -> 407 (326 -> 340 rejections; the 67 benign forms are
  unchanged). Gate tooling 7,282 -> 7,704 go-gate lines (+552 shell =
  8,256 total).
  rejections, 67 benign) + 9 shell mutations exit 0, production scan
  plus the 6b/9b shapes) reject on every scanned target, go test
  ./... (both tag sets), -race, vet (go and go-gate), gofmt zero
  diffs, cross-builds for all eleven shell-harness targets, and the
  SOW audit all pass.
- Round 10 (delta re-review): luna failed the round-9 delta with
  fourteen further findings, all probe-verified by the lead (each
  reproduced as a real escape on a fresh HEAD build before any fix):
  P97 a conditional clean store deleted the whole field taint at the
  join (if c { b.Data = []byte{1} }; return b.Data ran clean — forward
  and reverse join passes now delete only unasserted clean markers and
  keep any branch taint); P98-P100 local struct copies (c := b),
  pointer aliases (q := &b), and range over an inline struct literal
  (for _, x := range []box{{Data: p}}) dropped the argument field
  unions (materializeStructFields and the inline-composite range rule);
  P101 method-expression aliases misbound the receiver (get := box.Get;
  get(b) treated b as the receiver and the summary lookup missed — the
  alias now resolves to the bare function and the call binds the
  explicit first argument as slot 0); P102 closure struct results lost
  their fields (f := func() box { return box{Data: page} }; f().Data
  ran clean — func-literal struct results now record their field
  unions); P103 unprovable callees with page-carrying results failed
  open (interface methods, function-typed struct fields, and
  call-produced callees with an unknown body whose result shape can
  hold a full page now fail closed, while struct-wrapped reader results
  stay benign); P104 interface method-expression bindings (var put =
  sink.Put; put(s, page)) bypassed the approved-callee rules; P105
  method receiver string conversions missed the owned-string check
  (the receiver is argument slot 0 for method values and Args[0] for
  method expressions); P106 a user helper spreading a page collection
  into fmt.Sprintf laundered the page (param-sourced fmt spreads are
  now recorded in summaries and flagged at the call site); P107 a
  struct holding an *os.File placed into an interface slot laundered
  the descriptor; P108 file-bearing map keys; P109 append of a file
  into a non-file-bearing []any element slot; P110 range of file
  values into an any (new checkRange); P111 io.ReadFull(bytes.NewReader
  (page), out) slipped through the reader-arg exemption, which now
  applies only to variable-shaped arguments (bytes.NewReader of a
  378 -> 393 (311 -> 326 rejections; the 67 benign forms are
  unchanged). Gate tooling 6,684 -> 7,282 go-gate lines (+552 shell =
  7,834 total).
  rejections, 67 benign) + 9 shell mutations exit 0, production scan
  plus the 2a/2b/2c shapes) reject on every scanned target, go test
  ./... (both tag sets), -race, vet (go and go-gate), gofmt zero
  diffs, cross-builds for all eleven shell-harness targets, and the
  SOW audit all pass.
- Round 9 (delta re-review): luna failed the round-8 delta with thirteen
  further findings, all probe-verified by the lead (each reproduced as a
  real escape on a fresh HEAD build before any fix): P83 method values
  stored in locals (get := r.page; get(1)) lost the method summary and
  the minted page taint; P84 method receivers were absent from summaries
  (box{Data: page}.Get() returned clean, so the receiver is now
  parameter slot 0 of every method summary and method calls bind the
  receiver expression as the first argument); P85 the fmt variadic-spread
  exemption skipped the page check unconditionally (args := []any{page}
  spread into fmt.Sprintf is now flagged unless the spread is clean or
  param-sourced, which keeps the corrupt/headerErr helpers benign); P86
  defined string types (type S string) escaped the owned-string summary
  marker (noteStringConvs unwraps named types to the basic string
  underneath); P87 approved function-variable aliases (var a = f; a(page))
  bypassed the string-parameter call check (callCalleeFuncOrVar follows
  package-level var chains with the same proof rules as approvedFuncVar);
  P88 a clean store to one struct field shadowed the page provenance of
  other fields (sink6 wrote b.Other and returned b.Data; clean field
  stores now keep a marker and field reads fall through to the package
  and parameter sources); P89 a clean indexed store erased the container
  taint (slots[1] = []byte{0} after slots[0] = page; element stores now
  join, never delete); P90 local indexed containers of structs lost
  element field taint (xs := []box{{Data: page}}; xs[0].Data — slice
  literals now record element field unions on the bound container and
  indexed reads and range values consult them); P91 returning the address
  (or dereference) of a local tainted struct lost its fields (return &s
  propagates the recorded struct fields); P92 map composite keys were
  never evaluated (map[*[]byte]int{&page: 1} held the page only through
  its key); P93 file-bearing values hidden in interface-valued collection
  literals ([]any{f}) laundered the descriptor (the composite rule now
  requires the element slot type to bear files); P94 dynamic selector and
  indirect-calld file results missed the fail-closed capability rule
  (struct function fields h.get() and interface methods returning
  *os.File now fail outside the mapping owner); and P95 recursive carrier
  types (type R []R) recursed the type walk into a scanner stack overflow
  (typeCanCarryPage/paramCanCarryPage now carry a seen set; a recursive
  container cycle is the least fixed point, false, and the scan
  benign; the added benign form pins recursive carrier types with no page
  flow). Gate tooling 6,229 -> 6,684 go-gate lines (+552 shell = 7,236
  total).
  rejections, 67 benign) + 9 shell mutations exit 0, production scan
  p5c/p11b) and the recursive-type probe reject or terminate without a
  crash, go test ./... (both tag sets), -race, vet (go and go-gate),
  gofmt, cross-builds for windows/darwin/freebsd/netbsd, and the SOW
  audit all pass.
- Round 8 (delta re-review): luna failed the round-7 delta with seven
  further findings, all probe-verified by the lead: P76 func-literal
  multi-named results with naked returns (scanner panic, index out of
  range in analyzeFuncLitCall), P77 helper parameters converted to owned
  strings inside the callee (string(p) in the summary is caller-bound,
  so the call site fails closed), P78 stale call results across summary
  fixpoints (expression caches were per-package accumulations; every
  fixpoint pass now clears them and a final accumulation sweep replays
  all expressions against the stabilized summaries for the rule pass),
  P79 struct-field flow through dereferenced writes ((*b).Data = p) and
  indexed composite reads ([]B{{Data: p}}[0].Data), P80 package-global
  stores joining bounds instead of last-writer replace (setFull/setBound
  keep the conservative full bound), P81 []any and nested map/chan /
  slice-of-interface parameters as carriers (the fmt-spread exemption
  moved to the call-site rule, not the carrier type), and P82 interface
  method calls (s.Apply(page)) plus call-produced dynamic callees
  (factory()(page)) as unproven indirections. The round also surfaced a
  variantable regression in the lead's own wiring (rules-side lookups
  lost the per-pass values; fixed by the accumulation sweep) and a
  summaryDup gap that would have kept the fixpoint loop running forever;
  298 rejections, 66 benign); gate tooling 5,906 -> 6,229 go-gate lines
  (+552 shell = 6,781 total).
  rejections, 66 benign) + 9 shell mutations exit 0, production scan
  clean on all five targets, go test ./... (both tag sets), -race, vet,
  gofmt zero diffs, cross-compilation, audit - all green.
- Finding 3 (records): this Status is the compact record; the pre-rework
  history is preserved verbatim in ## Status History (appendix).
  string escaping (case 107), and copied shell-only module-graph cases
  (18, 238, 243, 248); benign cases 49/59/63/67/81/83/90 referenced
  undefined types or non-compiling assignments valid only for the old
  syntax-only scanner. All were repaired to compilable equivalents that
  preserve each tested rule (cleanup order of multi-op case files is now
  LIFO so metadata.go double ops restore the original), and the four
  already enforced them per target).
- Validation: go test ./... (both tag sets), -race, checkptr, go vet,
  + 9 shell mutations, exit 0), production scan across all 5 target
  configs, cross-compilation, Rust conformance corpus cross-open, and the
  SOW audit - all green at the closing commit.
- Review process: six-resident swarm (k3, glm, mimo, minimax, qwen,
  luna) then sol per the user's review decision; outcome recorded in the
  Execution Log below.
## Implications And Decisions

1. **Long-term-best: semantic peer, not incremental compatibility.**
   The current Go experiment is not a supported predecessor. Current specs and
   observable Rust semantics win every conflict. Reuse requires proof.
2. **Long-term-best: pure Go.**
   No cgo or Rust runtime dependency. The Rust-provided C ABI remains untouched.
3. **Long-term-best: one physical authority.**
   Internal reader/writer owners implement persistent operations once; public
   advanced and high-level APIs are logical wrappers.
4. **Long-term-best: mmap-only persistent content.**
   Lifecycle and durability syscalls remain necessary, but content-transfer I/O
   and complete out-of-map pages are prohibited.
5. **Long-term-best: semantic conformance.**
   Go and Rust files need not be byte-identical, but every observable value,
   error class, outcome, and cross-process coordination state must agree.
6. **Long-term-best: correctness before optimization, optimality before CI
   standardization.**
   Implement one authoritative path, measure its necessary work, remove waste,
   approve local performance, then add loose CI disaster gates.
7. **Minimal-complete scope.**
   All unsigned Phase-1 Go semantics and their consequences are in scope. C ABI,
   signing, v3, old-v4 compatibility, update-ipsets downloader/parser changes,
   and speculative structures are not.
8. **Controlled handoff.**
   Milestone 0 is an evidence checkpoint; the immutable cross-reader is the first
   code-quality checkpoint. No remote push, native-host access, tracked deletion,
   format change, or new native boundary is authorized merely by this SOW.

## Plan

1. **Milestone 0 - exact gap and replacement map.** Read-only inventory, parity
   matrix, proposed Go API/module graph, current-file classification, deletion
   approval list, risk register, and projected size. No production edit.
2. **Milestone 1 - portable mapped immutable reader.** Exact current codecs,
   mapping/lifetime owner, public immutable reader, all Rust fixture cross-reads,
   malformed bootstrap rejection, zero-allocation lookup/scan evidence, and the
   first platform/worker feasibility report.
3. **Milestone 2 - mapped COW writer and Go producer.** One physical writer,
   current allocation/retirement/commit rules, direct/membership/structured
   construction, public generation of the Go corpus, Rust cross-open.
4. **Milestone 3 - complete logical SDK.** Advanced transactions, typed
   workflows, metadata, queries, joins, algebra, snapshots, reports, cancellation,
   cleanup, and randomized public models over the internal core.
5. **Milestone 4 - live/platform/recovery completion.** Sidecar coordination,
   lifecycle and publication resolvers, supported-platform boundaries, explicit
   validation/recovery, worker, crash/fault/resource proof.
6. **Milestone 5 - cross-language and performance acceptance.** Mixed-process
   matrix, native proof, representative update-ipsets replay, matched Rust/Go
   benchmarks, profiles, necessary-work audit, code-size/duplication audit, docs,
   skill, and repeated clean final audit.

Every milestone report records:

- exact files changed and commits;
- behavior completed and behavior still missing;
- commands and factual results;
- production LOC, largest files/functions, allocations/resources, and benchmark
  deltas relevant to that milestone;
- same-failure and duplicate-authority searches;
- deviations or new decisions requiring the user; and
- whether the next milestone is safe to start.

## Required Final Audit Format

Use these sections in this order:

1. **Executive conclusion** - accepted or not accepted, with no hedging.
2. **Scope and source graph** - every production source classified and compiled.
3. **Single authority and two-level API** - physical owner map and bypass search.
4. **Hot-path necessary work** - counters, profiles, allocations, copies, syscalls,
   checksums, synchronization, and rejected optimizations.
5. **Correctness and durability** - public models, failures, commit/abort/crash,
   cleanup, reclamation, and outcome resolution.
6. **mmap-only and bounded resources** - static/runtime proof, heap/RSS/VM,
   descriptors, scratch, sparse scaling, and no complete page copies.
7. **Cross-language conformance** - both producer sets and both subprocess
   directions actually executed.
8. **Portability** - cross-target compilation versus separately identified native
   runtime evidence and exact unsupported boundaries.
9. **Maintainability** - production LOC, largest files/functions, complexity,
   clone/similarity results, dead code, layering, and justified exceptions.
10. **Findings and iteration record** - every finding, repair, repeat result, and
    remaining issue. Any remaining actionable issue means not accepted.

## Execution Log

### 2026-08-11 - planning and clean handoff

- Committed and pushed ignore rules for local generated build trees, stamp files,
  and Go test executables as commit `900b345`.
- Verified local `master` and `origin/master` at that commit with no visible
  uncommitted or untracked generated state.
- Updated the Rust and conformance README status text to record that the proven
  Rust result now authorizes this pending Go port; no runtime behavior changed.
- Read the complete current architecture/binary specifications, current/pending
  SOWs, Rust project skill, accepted Rust public/benchmark evidence, conformance
  corpus, and current Go production/test tree.
- Ran the current Go test, vet, and formatting baselines successfully.
- Confirmed no Go implementation edit was made. This file defines the future
  port; it does not start it.

### 2026-08-11 - Milestone 0 completed (read-only)

- Files changed: created
  `.agents/sow/milestones/SOW-0025-milestone-0-report.md` (full inventory,
  parity matrix, proposed module/API graph, per-file classification, exact
  deletion set, risk register, size projection); appended this execution-log
  entry. No production or test file in `v4/go/` was touched.
- Commits: `972960a` is the pre-milestone HEAD; the report and this entry are
  committed together as the Milestone 0 closure commit (local `master`, no
  remote push — per the controlled-handoff rule).
- Commands and results: `go test ./...` exit 0, `go vet ./...` exit 0,
  `gofmt -l .` empty, at HEAD `972960add710` with a clean tree. Measured:
  50 production files / 44,088 newline-counted lines, 59 test files / 37,403
  lines, 4 files with content-transfer calls, 24 files with complete page
  arrays, zero conformance references, 64 matched error codes (65-69 missing),
  sidecar stale magics/layouts, Rust production base 82,516 lines (livedb).
- Milestone 0 report outcome:
  - transfer files (6 production + 5 tests): `errors.go`, `key.go`,
    `cardinality.go`, `page.go`, `name_binding.go`, `process_identity.go`
    and their tests — each re-verified against literal vectors in Milestone 1
    before reuse;
  - proposed tracked deletions: 98 files (44 production + 54 test) — exact
    list in the milestone report, awaiting user approval (Decision 1);
  - fault-worker native-boundary policy: pure-Go feasibility proof is a
    Milestone 1 exit criterion; minimal project-owned assembly shim requires
    user decision (Decision 2);
  - projected Go production size: ~32-44k lines (midpoint ~37k), ~40-53% of
    the Rust base;
  - next milestone safe to start once Decisions 1 and 2 are answered.
- Same-failure and authority searches: content-transfer (4 files), complete
  page arrays (24 files), stale constants (retention 2 files, 1 MiB limit 1
  file, v3 zero), structured/threat/conformance references (zero) — each class
  is re-searched as a gate in later milestones; the old tree has one read and
  one write boundary internally, but that authority design is the rejected
  one, so the new tree mechanically enforces owner boundaries with
  set as the Rust `check-source-graph.sh`).
- No deviation from the approved SOW plan. Decisions requested from the user:
  (1) approve the 100-tracked-file + 2-untracked-leftover deletion set;
  (2) worker boundary: reopened by the milestone-1 evidence — SetPanicOnFault
  proves only a panic-based subset, not the spec-exact SA_SIGINFO contract
  (milestone-1 report §11); the user chooses between a minimal project-owned
  assembly sigaction shim and a spec change. Both are recorded in the
  milestone report with evidence and recommendations.

### 2026-08-11 - Milestone 0 corrected after external review (read-only)

- An external review of the milestone report found material errors. Every
  finding was verified against the sources before any change; all verified
  findings were corrected, one claim was not reproducible, and the two
  original recommendations were withdrawn as unsafe:
  - `process_identity.go` moved from transfer to delete: it implements a
    PID/process-start stale-slot recovery model (`process_identity.go:15`,
    `sidecar.go:394-396`) that the current spec explicitly excludes
    (`binary-format-v4.md:2130-2138`). Its test was moved to the deletion set
    too. Deletion set is now 100 tracked files (45 production + 55 test).
  - Error-code mapping corrected: names match Rust 1-64 in every position
    except code 46 (Go `ErrorLiveCoordinationDomainMismatchRequiresReset` vs
    Rust `LiveCoordinationMalformedRequiresReset`, `sdk_error.rs:58`); the
    report no longer claims an exact match and transfer requires resolving
    code 46 plus adding 65-69.
  - Prohibited-I/O classification corrected: content-transfer calls are
    `ReadAt`/`WriteAt`/`unix.Pread` in 3 files (`os_linux.go:503,565`,
    `page_source_linux.go:27`, `page_source_other.go:26`); `Truncate`/`Sync`
    are required lifecycle/durability syscalls and are not prohibited.
  - Decision recommendations changed to evidence-first for both decisions
    (deletion executes atomically with the compiling, tested Milestone 1
    replacement; fault-worker boundary is decided after the Milestone 1
    feasibility evidence, per the SOW's own stop-for-decision wording).
  - Design-spec line count corrected (519, not 530); exported-symbol report
    corrected to measured values (199 top-level exported declarations; 43 of
    50 files export nothing) and no longer labeled a "public SDK surface";
    size projection explicitly labeled estimate-only, not a target.
  - The report moved from an undocumented `.agents/sow/milestones/` directory
    to `.agents/sow/pending/pure-go-v4-port-milestone-0-report.md`, the best
    interpretation of the review's "wrong SOW path" finding. The reviewer
    later retracted that finding ("the original path was correct; moving the
    report was unnecessary, although harmless"). The report stays at the new
    location; both the SOW and this log reference the new path.
  - `.reasonix/` is harness session state, untracked by design; it is not repo
    content and was not committed.
- Files changed: `.agents/sow/pending/pure-go-v4-port-milestone-0-report.md`
  (moved and corrected, 496 lines), this execution-log entry. Still zero
  production/test edits in `v4/go/`.
- Commits: `579c1a3` (initial Milestone 0 report + log), `04032d6` (verified
  corrections, report moved to `pending/`, SOW correction log entry),
  `b91301d` (removal of the superseded old report path). All local, no remote
  push.
- Milestone 1 begins with moving SOW-0025 to `.agents/sow/current/` with
  `Status: in-progress`; it remains `open` in `pending/` while implementation
  has not started.

### 2026-08-11 - Milestone 1 completed (implementation checkpoint)

- Moved SOW-0025 to `.agents/sow/current/` with `Status: in-progress`
  (commit `0de8793`) and implemented the milestone:
  - New production packages: `internal/format` (wire codecs, meta identity
    and kind invariants, page/slotted, range, catalog, membership,
    structured, blob, metadata chunks, 129-bit cardinality, single 1-69
    error-code table with code 46 per the current Rust contract), `internal/
    mapping` (the only mapping owner; POSIX + honest Windows stub),
    `internal/reader` (the only healthy-generation reader core), and the
    public facade `reader_public.go` at the module root. ~3,720 new
    production lines, ~1,700 test lines.
  - Conformance: all six committed Rust fixtures open and verify with exact
    `cases.json` semantics (metadata states, full-IPv6 cardinality strings,
    boundary probes, 70 feed names, word-level bitmaps incl. blob-backed and
    1 MiB metadata, structured values + threat memberships); all three
    invalid mutations rejected with code 32.
  - Zero-allocation evidence: direct v4/v6, membership (inline + blob),
    structured, feed (internal), scans, cardinality = 0 allocs; public feed
    lookup = exactly the returned string copy. Recorded in both root and
    reader zero-allocation tests.
  - Portability: cross-compiles for darwin/freebsd/windows/linux-arm; Windows
    open refuses with os-unsupported stub; runtime proof on Linux amd64.
  - Worker feasibility: pure Go disproven for POSIX with an empirical probe
    (runtime-fatal `unexpected fault address`, os/signal never delivers
    mapping SIGBUS, no si_addr/sigaction surface in x/sys); Windows vectored
    exception path is pure-Go feasible. Fallback = minimal project-owned
    assembly sigaction shim; per recorded Decision 2 the user decides with
    this evidence.
  - Commits: `0de8793` (SOW move), `913f4e6` (reader + tests),
    `9441f85` (independent-review repairs: blob-branch txn threading,
    slotted-page bounds, metadata allocation bound, catalog reserved bytes,
    synthetic two-leaf blob regression database), `1df90fa` (milestone
    report), `4eec44e` (third-pass repairs), `03a910f` (fourth-pass
  repairs: borrow-count lifetime, view API, absence-vs-corruption,
  concurrency race tests, worker and report fact corrections,
  passthrough, conformance enumeration, multi-level range-tree test,
  corrected worker conclusion), `1e1ac4b` (fifth-pass repairs: meta tail
  zero invariant, mandatory aux checks, exact zlib stream verification,
  namespace safety under the lifetime lock, structure payload decode at
  lookup, public error typing, adversarial regression suite). No tracked
  file deleted (Decision 1 = C).
  - Full report: `.agents/sow/pending/pure-go-v4-port-milestone-1-report.md`.
    Baseline gates re-run: `go test ./...` (incl. race), `go vet`, `gofmt`,
    cross-compilation matrix — all green; SOW audit clean.
- At this historical checkpoint, both then-pending decisions had their evidence:
  the deletion set (then counted as 100 tracked files + 2 untracked leftovers;
  later corrected to 105 tracked files) and the worker
  boundary — the third-pass evidence demonstrates only a panic-based fault
  subset via `runtime/debug.SetPanicOnFault` (empirical recover with exact
  fault address); the spec-exact worker contract needs either a minimal
  project-owned assembly sigaction shim (spec-exact) or an explicit
  spec change. Both decisions were subsequently resolved as 1A and 2A. The
  contemporaneous statement that Milestone 2 would be safe after those answers
  is superseded by the later Milestone 1 reopenings.
- Commands and results: `wc -l` design spec 519; `grep` evidence for error
  code 46 in `errors.go:54` and `sdk_error.rs:58`; sidecar/spec magic and slot
  layout at `sidecar.go:11,15,394-396` vs `binary-format-v4.md:2104,2130-2138`;
  I/O call sites listed above; baseline gates unchanged (already green).

### 2026-08-11 - gap analysis and repair pass (six-agent)

- `94723aa` records the fifth-pass repairs in this log and the M1 report.
- `9a835e4` adds `.agents/sow/pending/pure-go-m1-gap-analysis.md`: six
  concurrent read-only subagents, each with a disjoint brief (codecs, reader
  semantics, architecture, public API/errors, test evidence,
  worker/mapping/report facts) at HEAD `94723aa`. Result: one BLOCKER (B1,
  structure radix divisor one level too deep) and ten MAJOR findings,
  plus three MINOR and one refuted claim.
- `58c4d8f` repairs every verified finding with a regression test: B1 span
  fix, blob-walk record/geometry validation, slotted exactness, OFD lifetime
  lock, API binding guards, absence/word-exact conformance evidence,
  honest LOC, import-graph gate strengthening (every import checked +
  sync/sync-atomic/unsafe ban), and bootstrap minima. No tracked file
  deleted.
- `bb7f485` corrects the SOW worker-feasibility sentences and the report's
  stale claims to match the corrected section 11.
- Gates re-run green at `bb7f485`: `go test ./...` (5 packages, incl.
  cross-compile matrix.

### 2026-08-12 - mandatory six-agent review round 2 and fix pass

- Per the mandatory iterative review gate (one review round per milestone
  before it can close), six new concurrent reviewers with disjoint briefs
  (codecs / bootstrap+ranges / membership+structured+metadata / mapping+
  lifetimes+platform / public API+errors+zero-alloc / conformance+reports)
  reviewed HEAD `bb7f485`. Verdicts: 4 FAIL (codecs 1 P1 + 4 P2; bootstrap
  1 P1 + 1 P2; membership 1 P1 + 3 P2; mapping 1 P1 + 3 P2), 1 FAIL on
  paperwork (reports 5 P1 + 5 P2), 1 PASS-with-2-P1 that are the already
  recorded closed-state error-class user decision. No P0.
- Fixed in this pass (all regression-pinned):
  - catalog `feed_index >= feed_index_limit` now corruption
    (`internal/reader/catalog.go`, `guard_regression_test.go`).
  - kind-classification: registered-but-invalid combinations (structured
    kind 0, direct/membership kind 1) report FormatInvalid (32); only
    unknown kinds report UnsupportedStructure (67)
    (`internal/format/meta.go`).
  - dangling structure reference now the typed corrupt error (structure
    range names an absent structure ID), matching the membership twin
    (`internal/reader/structure.go`).
  - FreeBSD immutable opens now use the canonical whole-file shared flock
    lifetime lock instead of refusing every open (`mapping_lifetime_freebsd.go`).
  - `Mapping.View`/`Page` after Close report the typed wrong-state error;
    `Mapping.Close` is idempotent (`internal/mapping/mapping.go`).
  - metadata zlib FCHECK validated; `deflateStreamLen` probes bounded at
    the declared length (CPU-amplification fix) (`internal/reader/metadata.go`).
  - blob leaf `%8` alignment explicit; checked blob extent arithmetic;
    blob branch validates every probed entry's child page
    (`internal/reader/membership.go`).
  - `publicError` preserves the typed code through wraps; error-code names
    59/62/69 aligned to the Rust `Id` spelling.
    sources and the stdlib `syscall` package.
  - test hygiene: public zero-alloc suite releases its v6 view; structured
    conformance absence probes added; Info()-after-close pinned; literal
    byte vectors added for the v6 range, membership leaf/branch, structure
    record, enrichment payload, and blob-branch codecs.
  - report facts corrected (honest production LOC 4,492 raw / tests 3,794
    raw, zero-alloc table labels, OFD lock wording, metadata allocation
    overhead 0-88 bytes) and this SOW log repaired.
- Commits: see the round-2 fix commit. Gates green after the pass; the six
  reviewers re-review the repaired tree before Milestone 1 closes.

### 2026-08-12 - six-agent review round 3 (final verification)

- All six reviewers re-reviewed the round-2 tree at HEAD with their same
  disjoint briefs. Verdicts: codecs PASS, bootstrap/ranges PASS,
  membership/structured/metadata PASS, public API/errors/zero-alloc PASS
  (fixable issues outside the recorded closed-state decision: none),
  mapping/lifetimes PASS after the gate fix, conformance/reports PASS after
  this record.
- Commits and fixes between the round-2 record and HEAD `3b4f3d5`:
  - `a5d7cf8` + `78cebc4` — import-graph gate comment stripping (line and
    stateful multi-line block), verified both directions.
  - `9203c28` — regression pins: `TestSoleMetaGeometry`,
    membership-kind1/kind2 subtests (both fail on the pre-fix code).
  - `e0a1687` — blob path zero-allocation: value-pair expected-level state;
    internal zero-alloc suite now measures blob word/scan at 0.
  - `a348c42` — real family-min and from-1 conformance absence probes;
    explicit `94723aa` log line.
  - `3b4f3d5` — P0 fix: blob coverage check underflowed when the request
    fell past the selected leaf's end (`end-off` wrapped), allowing a
    silent out-of-leaf word read or a slice panic on crafted files; the
    explicit `off > end` guard restores corruption semantics and
    `TestBlobGapRejectedCorruption` pins it (fails pre-fix in both modes).
- Report counts corrected at HEAD: production 4,500 raw lines (tests
  excluded), tests 3,922 raw, zero-allocation checks 18 (8 internal + 10
  public). (Superseded: the round-4 follow-up entries below carry the
  verified counts at each later HEAD.) Milestone report sections 11c-11d record both passes; the
  reviewers' final verdicts are all PASS with no P0-P2 remaining.
- Gates at HEAD: `go test ./...` (5 packages, incl. -race),
  matrix — all green; SOW audit clean.
- Pending user decisions unchanged: (1) deletion set — 100 tracked files +
  2 untracked leftovers, atomic with the Milestone 2 writer commit;
  (2) worker boundary — assembly sigaction shim vs spec change vs drop;
  (3) closed-state error class — HandleClosed (9) vs WrongState (11),
  Release idempotency, WordCount released-view silent-0; plus the recorded
  authority conflict (unknown nonzero structure_kind on direct/membership:
  spec text 67 vs Rust 32).

### 2026-08-12 - external audit pass (verified, all six findings real)

- An externally run audit of the round-3 tree reported six correctness and
  coordination failures plus performance waste. Every claim was verified
  against code, the spec, and the Rust reference before any change; all six
  correctness findings reproduced and were fixed with regression tests:
  1. View copies could double-release one borrow: two copies of one public
     view each decremented the reader's child count, so a later Close could
     succeed while a live child existed (use of the closed mapping). The
     released flag moved from the view value into a shared viewGuard with an
     atomic CAS; copies of a view now share one borrow, second Release is a
     no-op, and every copy reports HandleClosed after release. Public
     lookups that return a view now pin exactly one small guard allocation
     (the copy-safety cost; mapped traversal stays zero-alloc).
  2. Immutable sidecar handling: a present canonical `.readers` sidecar
     returned LiveCoordinationUnsupported (44) instead of the Rust WrongMode
     class (11), and a dangling `.readers` symlink was accepted as absent.
     Uses os.Lstat (symlink-aware, mirroring fs::symlink_metadata) and code
     11; pinned by TestSidecarPresence.
  3. Immutable lifetime lock was non-blocking F_OFD_SETLK; Rust blocks
     (F_OFD_SETLKW) while a writer holds the exclusive lock. Both Linux and
     macOS now use the blocking form (darwin F_OFD_SETLKW = 91).
  4. Path identity was verified only before the lock; Rust rechecks after
     locking and after mapping. mapping.OpenImmutable now re-verifies
     identity and re-runs the namespace check at all three points.
  5. validateStructuredMeta omitted structure_entry_count <
     structure_id_limit (Rust CountInvariant); both metas with
     entry_count == id_limit opened. Added the check; pinned by
     TestStructureEntryCountBound.
  6. Catalog name-branch keys were not grammar-validated (Rust decode_entry
     validates leaf and branch names through one decoder). DecodeCatalogName
     Branch now rejects invalid names; pinned by TestCatalogBranchNameGrammar.
- Performance waste, all fixed:
  - metadata stream validation re-inflated the payload O(log n) times (the
    1 MiB fixture twelve times); replaced with one single-pass inflation
    whose consumed-byte position (flate reads byte-at-a-time from a
    bytes.Reader) proves the exact stream end. ~1.4x measured on the
    micro-benchmark and far fewer allocations; trailing-byte, truncation,
    and Adler checks preserved (TestMetadataTrailingBytesRejected passes).
  - ReadWords decoded the whole record or walked the blob tree once per
    word; now one record decode (inline) or one blob walk (blob) per batch.
  - range/catalog/membership/structured descents pre-read the root page and
    re-decoded every page header inside OpenSlotted; the root pre-read is
    gone (first iteration captures the level) and OpenSlottedHeader reuses
    the already-decoded header (page.go).
    literals as a comment (a real call after a string containing `//` could
    bypass the gate); the stripper is now a quote-aware awk state machine,
    and in-memory decompression reads (consumedReader) are the documented
    exemption.
- CORRECTED non-finding (2026-08-12 re-review): the earlier claim that the
  per-call atomic in the public facade is parity with the frozen C ABI
  (handle.rs Gate::enter gates every C call) was wrong in mechanics. Rust
  reader lookups are a plain Option check (ReaderHandle::get, no gate, no
  atomic); the C facade's real costs are one Arc::clone (atomic refcount)
  and one Box per view-handle lookup, and view ops carry the caller-
  serialized AtomicBool fail-fast gate. The binding Go criteria are
  SOW-0025:175 (zero Go heap bytes for warm point lookups/cursor steps) and
  design-iprange-engine.md:373/:404; the round-4 facade (1 guard alloc per
  view lookup, 1 string alloc per feed lookup, per-call atomic load) does
  not meet them. Open as the hot-path API decision, not a resolved
  interpretation.
- Gates at HEAD: go test ./... (5 packages, incl. -race), go vet, gofmt,
  import-graph (with quote-aware stripper), 9-target cross-compile matrix —
  all green; zero-alloc suite updated for the one-guard-per-view contract;
  report counts and this record updated in the same commit.

### 2026-08-12 - hot-path contract re-review (milestone 1 reopened)

- An external re-review after the round-4 closure re-examined the public
  facade against the frozen performance contract. Verified (measurements
  reproduced at HEAD): membership lookup+word+release = 1 allocation (16B
  guard) and 2 atomic ops; feed lookup = 1 allocation (string); direct
  lookup/scan = 0 allocations but 1 atomic load each; every view op adds 1
  atomic load. SOW-0025:175, design-iprange-engine.md:373/:404 and the
  binary-format-v4.md:2537 checked-word_count requirement are not met by
  the round-4 facade. The earlier "Gate::enter parity" justification was
  wrong in mechanics (Rust reader lookups are a plain Option check; SDK
  core is atomic- and allocation-free; the C facade pays one Arc clone +
  one Box per view-handle lookup and gates view ops).
- Fixed now (no API change): metadata staging at internal/reader/metadata.go
  allocated the worst-case bound and then grew an io.ReadAll output (~2.3
  MiB / dozens of allocations for 1 MiB although exact lengths are declared
  in the selected meta). The chain now allocates exactly its declared
  compressed length (bootstrap bounds make it a safe capacity) and
  decompression reads into one exact output allocation with a one-byte
  overflow probe; truncation/trailing/Adler checks unchanged, pinned by the
  existing tests.
- HISTORICAL OPEN ITEM (subsequently resolved as decision 4A): the public facade
  API shape that closes the
  gap — caller-owned pinned lookup handles with token-style views and zero
  allocations/atomics inside the hot loop (long-term-best, recommended) vs
  keeping the guard facade with a SOW/spec amendment. Also unresolved at that
  checkpoint: the
  facade moves to the WrongState closed class, WordCount becomes
  error-capable, and Release/second-close semantics are decided explicitly
  (decision 3 corrections from the re-review). Reopened milestone 1 does
  not close before the hot-path contract is met.

### 2026-08-12 - external audit round-4 reviewer follow-up (mapping)

- The bootstrap/mapping reviewer's round-4 P2 was that the mapping owner
  still lacked regression tests for (a) the blocking F_OFD_SETLKW wait
  semantics and (b) the post-lock/post-mmap path identity recheck.
- Writing test (b) exposed that the shipped three-point recheck compared
  the opened fd against the initial path stat — a comparison that can never
  fail once the fd is open — so a replacement after open could still publish
  a mapping of the old unlinked inode while the path named a new database.
  Corrected at v4/go/internal/mapping/mapping.go: every recheck now
  re-stats the path itself with os.Lstat (symlink-aware, like Rust
  fs::symlink_metadata) and requires it to still name the opened inode;
  a mismatch or non-regular path entry under the lock is the WrongState
  class (code 11), matching Rust WrongMode ("live path identity changed").
  The initial pre-open stat remains only as the early non-regular-file gate
  and no longer vetoes the opened file, matching Rust's
  open-what-the-path-names semantics.
- TestOpenImmutableRefusesPathReplacedDuringOpen fails on the pre-fix tree
  and passes after the correction; TestOpenImmutableWaitsForExclusive-
  LifetimeLock pins the blocking wait (fails on a non-blocking lock).
- The darwin lifetime lock now retries EINTR in the wait loop, matching the
  linux peer and the Rust live_lock platform module (one loop for
  linux+apple), at v4/go/internal/mapping/mapping_lifetime_darwin.go.
- Counts at HEAD refreshed (4950366): production 4,592 raw lines / tests
  4,196 raw lines (report sections 2 and 11f). Gates at HEAD: go test ./... (5
  packages, -race, mapping -count=3), go vet, gofmt, import-graph, five
  cross-compiles (darwin/amd64, darwin/arm64, freebsd/amd64, windows/amd64,
  linux/386) — all green.

### 2026-08-12 - external audit round-4 follow-up 2 (membership P0)

- The membership/structured/metadata reviewer's round-4 verdict found one
  remaining P0 in batched blob-membership reads: the earlier
  single-descent blob case still issued one span request, so ReadWords
  crossing a blob-leaf boundary failed as corruption on a conforming file
  (reproduced on the committed synthetic two-leaf blob database with
  `ReadWords(505, 4)`: "blob leaf does not cover the requested bytes"),
  while per-word Word() succeeded and the Rust reference loops per leaf
  (blob_tree.rs read_words_from).
- Fixed in v4/go/internal/reader/membership.go: the traversal split into
  blobLeaf (one descent to the covering leaf, returning its mapped bytes
  and logical start) plus the single-span blobRead wrapper; the batched
  path now loops per covering leaf, copies min(available, remaining)
  words, advances, and keeps the no-advance guard and the trailing-zero
  word canonical check.
- TestBlobReadWordsAcrossLeafBoundary (blob_test.go) fails on the pre-fix
  tree with the exact reported error and passes at HEAD; blob per-word
  reads and all 8 internal zero-alloc subtests remain green.
- Counts at HEAD (ac6bef1): production 4,634 raw lines / tests 4,252 raw
  lines (report sections 2, 11f, 11g).

### 2026-08-12 - external audit round-4 follow-up 3 (mapping P2 + records)

- The mapping/lifetime reviewer's re-verification found one remaining P2:
  an unlinked (not replaced) path mid-open mapped the Lstat failure to
  CodeIO (31), while Rust verify_path_inner refuses with NameNotFound (18).
  Fixed at v4/go/internal/mapping/mapping.go (os.IsNotExist ->
  CodeNameNotFound before the IO fallback); pinned by
  TestOpenImmutableRefusesPathUnlinkedDuringOpen.
- The conformance/reports reviewer's P2 was record lag only: the round-3
  "counts corrected at HEAD" sentence in the external-audit entry is now
  explicitly annotated as superseded by the round-4 follow-up entries;
  report section 13 now lists section 11e; every historical "Counts at
  HEAD" line carries its commit.
- Counts at HEAD (this commit): production 4,639 raw lines / tests 4,308
  raw lines. Gates: go test ./... incl -race (mapping -count=3), go vet,
  gofmt, import graph — all green. Gates at HEAD: go test ./... incl -race,
  go vet, gofmt, import graph — all green.

### 2026-08-13 - round-45 final review mmap-gate denylist gaps closed (HEAD 14c0698)

- The round-44 re-verification completed with all six narrow reviewers at
  PASS (HEAD e5fea20). The round-45 full-scope final review then failed
  with three P2 findings, all in the mmap source gate: os.CopyFS was
  absent from the selector ban (a directory copy streams artifact bytes
  with no banned selector; the live reproducer exited 0);
  os.OpenInRoot/os.OpenRoot were absent from the file-producer table (a
  Go 1.26 OpenInRoot *os.File, or an older-toolchain *os.Root handle,
  reached flate.NewReader untainted and streamed file bytes, and
  Root.Open/Create/OpenFile also produce files); and the blanket-approved
  x/sys surface still carried descriptor-transfer primitives
  (unix.Tee, unix.Vmsplice, unix.IoctlFileClone/CloneRange/DedupeRange,
  darwin unix.Clonefile/Clonefileat).
- Fixed at HEAD 14c0698: CopyFS, Tee, Vmsplice, IoctlFileClone* and
  Clonefile* join the banned selector set (CopyFileRange/Sendfile/Splice
  were already banned); os.OpenInRoot and os.OpenRoot join the file
  producer table as position-0 file taints, so every Root method outside
  the approved lifecycle surface fails closed; all three live reproducers
  plus the OpenRoot/ReadAll and darwin Clonefile variants are rejected.
  then proved a P0 in the same class: a *os.Root stored in a struct
  field (h := gateRootField{r: root}; h.r.Open(name)) dropped the
  file taint, so the returned *os.File reached flate.NewReader
  untainted and the stream was consumed through the exact inflater
  exemption shape (gate exit 0, /tmp reproducer); the type model now
  resolves *os.Root as a file-bearing type everywhere *os.File does
  (struct fields, parameters, helper returns, type assertions,
- The P0 closure landed at HEAD 262756c on top of the round-45 gate fix
  (14c0698, records 70dcc42); the record trail and counts above reflect
  the full chain 14c0698 -> 70dcc42 -> 262756c -> e1410eb.
- The following adversarial re-review then found three producer-value
  P0 escapes, all proven live with the metadata-exemption consumption
  chain at gate exit 0: file method values (open := root.Open;
  open(name)), initialized func-typed variables with file-bearing
  declared results (var newRoot func(string) (*os.Root, error) =
  os.OpenRoot and the pre-existing *os.File form var openPath
  func(string) (*os.File, error) = os.Open), and stdlib producer
  values bound without a declared type (openPath := os.Open). Fixed
  in v4/go-gate/main.go: the file method in value position is checked
  against the approved capability surface, the declared result type of
  an initialized func-typed variable registers as a func-file, and
  stdlib producer values register as func-files wherever bound;
- The producer-value closure landed at HEAD 5ff9116 on top of the
  Root-taint fix (262756c); the exact round-45/46/47 chain is 14c0698
  (gate gaps), 70dcc42 (its records), 262756c (Root laundering), e1410eb
  (its records), 5ff9116 (producer values), 8c6cc44 (its records). Gates:
  go test ./... incl -race, go vet,
  CGO_ENABLED=0
  build and test, four cross-compiles, SOW audit — all green.
- Counts unchanged at this commit: production 4,792 raw lines / tests
  4,877 raw lines (the gate scanner lives outside the module). Decision
  5A remains open for user ratification; Milestone 2 remains blocked
  until the re-review passes.

### 2026-08-13 - round-48 gate re-review bound method expressions and cross-package producer vars closed (HEAD aec609c)

- The round-48 adversarial re-review (six narrow reviewers: codecs,
  membership/zero-alloc, mapping/pin/gate, metadata/bootstrap, records,
  gate hunting) passed codecs, membership/zero-alloc, mapping/pin/gate,
  and metadata, and failed with two gate findings plus one records
  finding.
- P0 - bound method expressions: `open := (*os.Root).Open` followed by
  `open(root, name)` binds the Open method with the receiver as an
  explicit first argument. The receiver node is a type expression and
  never carries value taint, so the value-position selector check could
  not see it; the same held for the package-level initializer
  `var openRootPkg = (*os.Root).Open`. Both reproduced live at gate
  exit 0 with the exempted inflater chain consuming the file.
- P2 - same-module cross-package producer vars: `internal/format`
  declares `var OpenRoot = os.OpenRoot` (and the *os.File sibling
  `var Open = os.Open`); a caller in `internal/mapping` invoking
  `format.OpenRoot(dir)` cannot see the declaring directory's taint
  registry, so the returned *os.Root/*os.File reached a flate reader
  untainted, reproduced live at gate exit 0.
- P2 (records) - the round-47 exec-log bullet never cited the terminal
  records HEAD by hash; the corrected chain 14c0698 -> 70dcc42 ->
  262756c -> e1410eb -> 5ff9116 -> 8c6cc44 is now named in the
  round-45/46/47 bullets above and in this entry.
- Fixed at HEAD aec609c: the selector rules now recognize method
  expressions whose receiver type resolves to a file-bearing handle
  (methodExprFileType, rejecting every method outside the approved
  lifecycle surface in both function and package-level scans), and a
  process-wide package-level producer-var registry
  (qualifiedProducerVars plus the per-directory pkgProducerVarsByDir)
  resolves same-module producer vars through the call-site import path,
  with the clause-name fallback for plain (non-renamed) imports;
- Replayed at the new gate: all round-47 replays (R4-R14) and the new
  probes (P10 method expression, P12 exempted-inflater chain, P14
  package-level method expression, P15 cross-package producer var)
  are rejected; the benign close-value/Fd-value/Chdir controls still
  pass. Gates: go test ./... incl -race, go vet, gofmt, import graph
  four cross-compiles, SOW audit — all green. Counts unchanged at
  this commit. Decision 5A remains open for user ratification;
  Milestone 2 remains blocked until the final review pass.

### 2026-08-13 - round-49 gate re-review closed method-expression spelling variants, value-bound cross-package producers, and interface-conversion laundering (HEAD 041cf6098ee6)

- The round-49 adversarial re-review (six narrow reviewers: codecs,
  membership/zero-alloc, mapping/pin/gate, metadata/bootstrap, records,
  gate hunting) passed membership/zero-alloc and metadata, and failed
  codecs (P1 value-bound cross-package producer vars), mapping/pin/gate
  (P1 renamed-import and alias-over-renamed method expressions, P2
  wrapper-promoted methods), records (P1 stale count prose), and gate
  hunting (P0 nested-parenthesized method expressions, P0
  interface-conversion laundering).
- P0 - nested-parenthesized method expressions: `open := ((*os.Root)).Open`
  (and deeper paren nesting) wrapped the receiver so the single paren
  strip left a hidden type spelling while Go still compiled and invoked
  the bound open; the same gap applied to package-level initializers.
- P1 - renamed-import and alias spellings: `import o "os"; (*o.Root).Open`
  escaped because only the canonical os.Root spelling resolved, and the
  alias hop `type RR = o.Root` over a renamed import compounded it. A
  wrapper struct `type WE struct{ *os.Root }` promoted Open/OpenFile/
  Create into the method set, so `(*WE).Open` escaped with no Root
  spelling in the receiver at all. All four were reproduced live at gate
  exit 0.
- P1 - value-bound cross-package producer vars: form 260 pinned the
  direct call `format.Open(name)`; binding the producer into a local
  (`open := format.Open`), a package-level var (`var bound = fm.Open`),
  or a renamed qualifier (`fm.Open`) dropped the func-file taint and the
  invoked binding returned an untainted *os.File (live, gate exit 0).
- P0 - interface-conversion laundering: `zr := gateZR(f)` converts a
  live *os.File into a fresh named interface; the conversion resolved as
  a function call, zr carried no taint, and the exact metadata inflater
  exemption shape consumed file bytes (live, gate exit 0).
- P1 (records) - the SOW `## Status` close-out and the reviewer-findings
  close-out still reported the round-47 count (two hundred six forms)
  instead of the round-48 count and closure; corrected here.
- Fixed at HEAD 041cf6098ee6: method-expression receivers now strip parens
  to a fixpoint, translate renamed stdlib imports, chase alias chains,
  and walk defined-struct embedding chains for promoted file-handle
  methods (fileCapableReceiverType); same-module package-level producer
  vars resolve in value position through the process-wide registry
  (producerVarSelector in classify, function and package-level
  bindings); type conversions of file-tainted values keep the file
  taint in every binding and argument context, so the inflater
  exemption no longer shields a laundered reader
- Replayed at the new gate: all round-47 replays (R1-R14), round-48
  probes (P1-P15), the new nested-paren/renamed/alias/wrapper
  method-expression probes, the cross-package value-bound producer
  probes (function-level, package-level, renamed-import), and the
  interface-conversion launder probe are rejected; the benign controls
  still pass. Gates: go test ./... incl -race, go vet, gofmt, import
  test, four cross-compiles, SOW audit — all green. Counts unchanged
  at this commit: production 4,792 raw lines / tests 4,877 raw lines.
  Decision 5A remains open for user ratification; Milestone 2 remains
  blocked until the final review passes.

### 2026-08-13 - round-50 gate re-review closed generic interface erasure, composite-literal and generic-wrapper launders, and deep embedding chains (HEAD 04f4271e3c5f)

- The round-50 adversarial re-review (six narrow reviewers: codecs,
  membership/zero-alloc, mapping/pin/gate, metadata/bootstrap, records,
  gate hunting) passed membership/zero-alloc and metadata, and failed
  with four live gate escapes: codecs (P0 generic identity erasing
  file taint into an interface result), mapping/pin/gate (P1
  composite-literal field launder, P1 method expression on an
  instantiated generic wrapper, P2 embedding chain deeper than the
  bounded walk), and records (stale round-48 count prose in the
  Status opening summary, fixed in this pass).
- P2 (records) - the Status opening summary still carried pre-round-48
  count prose (two hundred six forms) and the exec-log label for it
  misattributed the staleness to round-48; the current-state prose is
  unified on the round-50 count and this entry drops the stale label.
- P0 - generic interface erasure: `func probeWrapR[T io.Reader](v T)
  io.Reader { return v }` binds a file argument to a type parameter
  and returns an interface-typed result; the generic result
  propagation only tracked exact type-parameter results, so
  `zr := probeWrapR(f)` erased the taint and the exempted inflater
  shape consumed file bytes (live, gate exit 0).
- P1 - composite-literal field launder: `s := gateLaunderS{r: f}`
  followed by `zr := s.r` dropped the taint because field taint was
  registered only for selector writes (live, gate exit 0).
- P1 - instantiated generic wrapper: `type gW[T any] struct{ *os.Root }`
  with `open := (*gW[byte]).Open` hid the promoted method behind the
  generic instantiation spelling (live, gate exit 0).
- P2 - deep embedding chain: a five-level wrapper chain
  (gDE5 -> ... -> *os.Root) exceeded the four-hop embedding walk
  budget; `(*gDE5).Open` bound the method untainted (live, gate exit 0).
- Fixed at HEAD 04f4271e3c5f: generic results that can only compile as
  interface-erased carriers (any, error, anonymous interface, io.*
  interfaces, bare same-package declared results) keep the file taint
  when a type-parameter-bound argument is file-tainted
  (interfaceErasedResult); composite-literal bindings register
  named-element field taint, including nested literals and selector
  field assignments (registerCompositeFieldTaints); method-expression
  receivers strip generic instantiation suffixes before the struct and
  embedding registry lookups (genericBase); the embedding walk tracks
  visited types and runs to a fixpoint instead of a fixed four-hop
- Replayed at the new gate: all round-47 (R1-R14) and round-48 (P1-P15)
  probes, the round-49 probe families (nested-paren, renamed-import,
  alias-over-renamed, wrapper, value-bound producer, interface
  conversion), and the four new probes are rejected; the benign
  controls still pass. Gates: go test ./... incl -race, go vet,
  CGO_ENABLED=0 build and test, ten cross-compiles across the
  per-target listing matrix, SOW audit — all green. Counts unchanged
  at this commit: production 4,792 raw lines / tests 4,877 raw lines.
  Decision 5A remains open for user ratification; Milestone 2 remains
  blocked until the final review passes.

### 2026-08-13 - round-51 gate re-review closed file-bound generic result erasure and positional composite-literal field launders (HEAD 875b19205fb3)

- The round-51 adversarial gate hunt (six narrow reviewers:
  codecs, membership/zero-alloc, mapping/pin/gate, metadata/bootstrap,
  records, gate hunting) failed with seven live escapes across three
  root causes in the content-transfer scanner:
- P0 - spelling-based interface-erased results: generic interface
  results were recognized only under the literal `io` qualifier, so a
  renamed import (`import r "io"`; `func gateHuntWrapA[T r.Reader](v T)
  r.Reader { return v }`), another stdlib interface (`fs.File`), and a
  chan send of the erased call value all flowed a file into the
  exempted inflater shape (live, gate exit 0; probes A/B/F).
- P0 - container-of-type-parameter results: `[]T` and `[2]T` results
  never resolved to file taint, so `files[0]`/`arr[0]` read a clean
  value (live, gate exit 0; probes C/E). Maps and func wrappers share
  the miss.
- P0 - positional (unkeyed) composite-literal elements: the registry
  skipped non-keyed elements, hiding a file field (`s := gateHuntS{f}`;
  `zr := s.r`) and an os.Root opener
  (`gateHuntRootIface{root}`; `zr0.Open(name)`) (live, gate exit 0;
  probes D/G). Keyed twins were already pinned.
- Vacuous-form discovery: forms 267-268 as separate files were
  rejected by the unconditional `.ReadFull` selector ban regardless of
  taint, so they never exercised the exemption-shape escape; the
  zero MISSes. Forms 269-270 (method-expression classes) were genuine.
- Fixed at HEAD 875b19205fb3:
  `genericParamFilePositions` now taints every declared result
  position once a file-typed argument binds a type parameter (the
  generic body is opaque; exact parameter, interface spellings of any
  qualifier, containers, channels, and func wrappers all keep the
  taint); `registerCompositeFieldTaints` resolves unkeyed elements
  through the struct's declared field order (embedded fields by type
  text), mirrors the order cross-package like the struct registry, and
  metadata.go save/restore appends (`append_mut`) and import-block
  injection (`inject_import`).
- Pinned as durable exemption-shape appends: forms 267-268 converted
  and 271-277 added (renamed-io interface result, io/fs interface
  result, slice-of-T, positional field, array-of-T, chan send of an
  erased value, positional os.Root opener), raising the rejection set
  round-50 scanner misses exactly the seven new forms (and the
  converted 267-268 against the pre-round-50 scanner), the fixed
  scanner rejects all 227. Gates: go test ./... incl -race, go vet,
  tree gate, CGO_ENABLED=0 build and test, ten cross-compiles across
  the per-target listing matrix, SOW audit — all green. Counts
  unchanged at this commit: production 4,792 raw lines / tests 4,877
  raw lines. Decision 5A remains open for user ratification; Milestone
  2 remains blocked until the final review passes.

### 2026-08-13 - round-52 gate re-review closed embedded, var-bound, anonymous struct-literal, container-element, pointer-literal, and explicit-instantiation launders (HEAD 5acd2a6; records 6470f21)

- The round-52 adversarial gate hunt (six narrow reviewers:
  scanner diff review, gate hunting) failed with four live escapes
  across three root causes in the content-transfer scanner plus one
  further live escapes across three more classes, all confirmed
  against the final scanner:
- P0 - embedded-type composite-literal field laundering: type gateEmb
  struct{ io.Reader } with s := gateEmb{f} (positional) and s :=
  gateEmbR{root} / gateEmbR{Root: root} over type gateEmbR struct{
  *os.Root } left the binding container-tainted while the promoted
  Reader/Open methods stayed live, so io.ReadFull(s.Reader) and
  s.Open(name) streamed file bytes into the exempted shape (live,
  gate exit 0; probes embed/embedR/embedRk; the constructor twins
  embedC/embedAR were already rejected). Root cause: embedded fields
  were registered by type TEXT (io.Reader, *os.Root) while Go names
  embedded fields by type NAME (Reader, Root), so positional taint
  landed on a dead key; the keyed *os.Root twin escaped because the
  binding stayed kindContainer and promoted s.Open needed kindFile to
  trip the approved capability surface.
- P0 - var-bound generic instantiation: var gateHuntAliasXP =
  gateHuntWrapXP[*os.File] registered nothing (the variable has no
  generic parameters of its own), so files := gateHuntAliasXP(f) read
  a clean []*os.File result and files[0] fed the exempted inflater
  (live, gate exit 0; probe varinst). The direct explicit
  instantiation wrap[*os.File](f) was already rejected by the
  argument-approval rule; only the variable route slipped past.
- P0 - anonymous struct-literal positional elements: x := struct{ r
  io.Reader }{f} has no declared struct name, so the field-order
  registry had no entry and x.r stayed clean (live, gate exit 0;
  probe anons); the anonymous twin s := struct{ *os.Root }{root}
  combined the missing positional order with a missing embedded-
  handle escalation and promoted s.Open(name) flowed an untainted
  file into the exempted inflater (live, gate exit 0; probe
  anonroot).
- P0 - elided inner composite literals in containers: the element
  type name is omitted inside the container literal (s := []S1{{fn}},
  m := map[string]S1{"a": {fn}}, s := S6{in: []S1{{fn}}}, and a chan
  *os.File element []S5{{ch}}), so the inner composite literal carried
  no explicit type text and its fields were never registered; calling
  s[0].fn(name) or receiving from s[0].ch returned a clean io.Reader
  into the exempted inflater (live, gate exit 0; probes e1a, e10,
  e1c, e20, e36).
- P0 - pointer composite literals: s := &S1{fn} and s := &S2{r: f}
  addressed the struct before use; the unary & was never unwrapped,
  so neither positional func-file nor keyed any-field binding was
  registered and the file reached the exempted inflater untainted
  (live, gate exit 0; probes e8a, e8b, e34, e35).
- P0 - func-valued arguments to explicitly instantiated generics:
  gateH2E7[io.Reader](name, closure) and the variadic
  gateH2E31[io.Reader](name, closure) use an index-expression callee,
  which never reached the Ident-callee type-parameter mapping; the
  closure's *os.File surfaced through the erased body as a clean
  io.Reader (live, gate exit 0; probes e7, e31, e33 with e33 closed
  by the opaque-body result umbrella).
  run_mut expanded the GOMODCACHE/GOPROXY environment prefix as a
  command name and both x/sys-poisoning mutations "rejected" with
  exit 127 before the gate executed (silent vacuity since c86e162);
  run_mut now prefixes env. Running them for real exposed a second
  dormant defect in the same forms: the forged go.sum was written
  with a double h1: prefix (--dirhash already emits h1:), so the
  module could never resolve and the forms rejected via the
  fail-closed listing rule instead of the content pin; the /go.mod
  sum was also computed with the wrong format. The evil tree's
  go.mod is byte-identical to the official v0.35.0 go.mod, so the
  correct self-consistent forge pins the official /go.mod sum and
  the evil module/tree hash. The continuation harness review then
  proved the fix still incomplete on go1.26: Go only treats a module
  as downloaded (go list -m resolves a Dir) after the zip is
  verified and the .ziphash marker written, so a manually-seeded
  cache rejected via the fail-closed listing fallback and the
  content pin still never ran. Both forms now pre-verify the seeded
  cache/proxy with go mod download under the poisoned environment
  (the forged go.sum makes the evil zip self-consistent, so the
  toolchain writes the marker), after which the gate's own checkout
  content-hash boundary violation fires for real in both forms.
- Fixed at HEAD: embedded struct fields register by type name
  (embeddedFieldName strips pointer, package qualifier, and generic
  instantiation), so positional and keyed embedded elements share
  the key a selector read uses; registerGenericInstantiationVar
  records var-bound explicit instantiations and
  genericParamFilePositions resolves their calls through the base
  generic's fixed type arguments (parameter text substituted before
  the file-binding check, so the opaque-body rule covers both
  inference and fixed-argument calls); registerCompositeFieldTaints
  resolves anonymous literal order from the literal's own fields
  (named by name, embedded by type name);
  compositeLitEmbedsFileHandle escalates named and anonymous structs
  whose embedding chain reaches *os.File/*os.Root.
- Fixed at 5acd2a6: the explicit-instantiation callee route
  mounts genericParamFilePositions on index-expression callees whose
  base is an identifier (g[io.Reader](...));
  registerContainerElementFields records elided slice, map,
  nested-container, and channel elements by the element struct's
  declaration through a new element-field taint registry, and the
  read side resolves container element selects (s[0].fn, m["a"].fn,
  c[0].ch) against it; pointer composite literals unwrap their
  leading & before field registration and LHS taint application;
  genericMethodResults claims every declared result position when
  any call argument bears a file, covering closures surfaced through
  erased generic bodies.
- Pinned as durable exemption-shape appends: forms 278-282 (embedded
  *os.Root wrapper literal, var-bound generic instantiation with
  fixed file arguments and the erase-to-interface twin, anonymous
  positional element, anonymous embedded handle) and forms 283-288
  (explicit single and variadic instantiation with a func-file
  argument, elided slice element, map elided element, pointer
  composite literal, nested and channel elided container elements),
  raising the rejection set to two hundred thirty-eight
  the eleven new forms (and forms 245-246 were vacuous before the
### 2026-08-13 - round-53 final review reopened the gate: embed and //go:embed compile-time database copies closed (HEAD 2e0e3667db3c)

- The round-53 final review failed with two P2 findings: (1) the mmap-only
  gate accepted //go:embed database content - a production source embedding
  because the embed package was absent from the banned import set and the
  directive scan rejected only //go:linkname; (2) the
  NetworkEnrichmentV1Location pointer-vs-value parity deviation, which is
  the already-open decision 5A awaiting user ratification, not a new code
  defect.
- P2-1 closed at 2e0e3667db3c: the embed import is banned (a blank
  `_ "embed"` import alone would bypass it, so the directive is also
  rejected), and every production //go:embed directive now fails the AST
  scan as a compile-time content copy. Pinned as pre-fix-failing forms
  289 (non-blank embed import) and 290 (blank embed import with an
  embedded probe.db under a //go:embed directive). The pre-fix scanner
  misses exactly the two new forms; the fixed scanner rejects all two
- Vacuity against the round-51 scanner: exactly thirteen MISSes, forms
  278-290 (the eleven round-52 forms plus the two round-53 forms).
- P2-2 resolved by decision 5A (ratified 2026-08-13, option A): the
  value-plus-HasLocation representation is the approved zero-allocation
  equivalent of Rust's Option<NetworkEnrichmentV1Location>; the parity
  matrix now records the ratification, so the milestone gate no longer
  depends on this item.
- Gates at this commit: go test ./... incl -race, go vet, gofmt,
  CGO_ENABLED=0 build and test, ten cross-compiles, SOW audit - all green.
### 2026-08-13 - round-54 cosmetic alignment: dead word-read state removed, zero Pin reports WrongState, inflater wording aligned (HEAD 72e4d89d75fb)

- Removed dead state in internal/reader/membership.go readWordsInner
  (var data []byte, var err error, and the never-true error check after
  the storage switch); behavior identical.
- The zero-value Pin now reports WrongState on Close and checkOpen,
  matching the inert zero-view contract instead of panicking; pinned by
  TestPinZeroValueClose.
- Wording: "the three in-memory inflater nodes" became "the in-memory
  findExemptions comment, Validation section).
- Blank-import decision recorded: blank imports of banned packages remain
  intentionally uncaught by the import ban - blank imports expose no
  names and cannot transfer bytes, and the only blank-import-sensitive
  mechanism, //go:embed, is separately rejected as a directive.
- Gates at this commit: go test ./... incl -race, go vet, gofmt,
  ten cross-compiles, SOW audit - all green. Counts: production 4,789 raw lines / tests
  4,887 raw lines (the dead-state removal and zero-Pin test account for
  the delta; the gate scanner lives outside the module).

## Validation

Acceptance criteria evidence:

- Milestone 1 (immutable reader): CLOSED (final check 2026-08-17, HEAD
  4f11e3d; re-closed after the 2026-08-14 external-review rework whose
  three verified findings - duplicated search loops, overgrown gate
  machinery, stale records - were all fixed and pinned). Milestone 2
  (writer core) is implemented; Milestone 3 is in progress: chunks
  1-3b-2 are implemented; chunk-3b-2 slice 3 (publish_set surface)
  CLOSED at HEAD 8345891 with the five-aspect review at PASS, no
  P0-P2 (Status entry above).
- Slice-3 evidence: single-level five-aspect adversarial review at HEAD
  2a5e78e with the Rust implementation as the mandatory baseline; all
  findings fixed and re-reviewed to no P0-P2 by the same five
  reviewers (Pauli/Hume/Aristotle/Aquinas PASS at 3897b4b; Ohm FAIL at
  3897b4b, PASS at 45d453f; test-portability delta 8345891 verified
  across the full matrix).

Tests or equivalent validation:

- `go test ./...` (all packages, default tag set) - green.
- `go test -tags v4work ./...` (necessary-work counters and fault hooks
  compiled in) - green.
- `go test -race ./...` and `go test -race -tags v4work ./...` - green.
- `go vet ./...` - clean; `gofmt -l .` - empty.
- Cross-compilation: linux, darwin, freebsd, netbsd, windows (amd64) -
  all build.
- Conformance: 6/6 Rust fixtures cross-open with exact semantics; 3/3
  invalid mutations rejected with the typed error; structured absence
  probes included.
- The mmap-only / file-I/O policy gate: the full-codebase four-reviewer
  review PASSed at 2a5e78e with one P1 fixed (metadata streaming, record
  in Status above); enforcement now lives in review aspect 2 plus
  periodic full-codebase sweeps (Review Process, step 3). No scanner or
  mutation corpus exists.
- `.agents/sow/audit.sh` - clean.
- Necessary-work counters: pinned in query_work_test.go against the
  frozen fixtures; the pins moved with the leaf-cache reduction (Status
  entry above).

Real-use evidence:

- The Rust peer has accepted representative update-ipsets replay evidence.
  Equivalent Go evidence is an implementation acceptance requirement, not
  a planning claim.

Reviewer findings:

- Single-level five-aspect review, 2026-08-21 (HEAD 2a5e78e): five
  reviewers, disjoint aspects, Rust authority baseline; verdicts FAIL x4
  plus PASS-with-P2 x1; all findings fixed and validated (Status entry
  above).
- Re-review round 1 (delta ae57dfa): Ohm FAILed one P2 (code 77 vs 64
  records) plus two P3; Aquinas FAILed one P2 (freebsd/netbsd residue
  regression and the missing FreeBSD no-replace capability) plus two P3;
  Pauli, Hume, and Aristotle re-reviews were still running when this
  entry was written. All round-1 findings are fixed and validated
  (Status entry above, including the FreeBSD linkat machine, the NetBSD
  preparation classification, and the custody identity compare).
- The slice closes only when all five reviewers report no P0-P2 on the
  final delta.
- The earlier milestone-1 gate rounds and the retired scanner-era form
  narratives are preserved in git history and are not reproduced here;
  their dated execution-log entries remain below.

Same-failure scan:

- The publish-classification fixes cover every publication outcome path
  (rename refusal, fail-if-exists, replace, discard, cleanup) and every
  deliverable that reports a publication outcome; the error-type
  consolidation covered all hand-rolled join chains in the writer; the
  cursor gates cover both direct and feed-range constructors for both
  address families; the ASCII fold is one shared authority used by
  reader and writer; stale gate-era comment vocabulary has zero non-test
  hits (grep-verified).

Sensitive data gate:

- This SOW contains repository-relative paths, public upstream identity,
  generic platform names, code metrics, and synthetic/public benchmark
  descriptions only. It contains no raw secret, credential, operational
  host alias, personal or customer/community data, private endpoint, or
  proprietary incident detail.

Artifact maintenance gate:

- AGENTS.md: unchanged; this SOW's review rules stand at the top of this
  file per the user decision (SWARM.md is intentionally untouched).
- Runtime project skills: project-final-review and project-v4-rust
  unchanged; no new skill was needed for this round.
- Specs: no format or behavior change was made; the v4 contract is
  unchanged.
- End-user/operator docs: v4/conformance/README.md corrected (compressed
  metadata-chain size example); no other public document changed.
- End-user/operator skills: none exist and no public workflow changed.
- SOW lifecycle: this file remains `in-progress` under `current/`;
  Milestone 1 is closed, Milestone 2 is implemented, Milestone 3 is in
  progress; SOW-0017 remains the separate Phase-2 item.

Specs update:

- None: the fixes align the Go implementation with already-normative
  Rust behavior; nothing in the wire contract changed.

Project skills update:

- None for this round; the review process itself is recorded in this
  SOW's top rules per the user decision.

End-user/operator docs update:

- The Go SDK documentation is an implementation deliverable; the only
  document corrected this round is v4/conformance/README.md (chain-size
  example).

End-user/operator skills update:

- None exist; reassess at closure.

Lessons:

- Passing tests around private fragments do not establish a usable SDK or
  current wire compatibility.
- A semantic port needs an explicit parity matrix and cross-language
  execution; source similarity is neither required nor sufficient.
- The most dangerous way to port this engine is to preserve the obsolete
  Go storage architecture because it already looks substantial.
- Single-reviewer passes converge; only adversarial multi-agent passes
  with disjoint briefs found the catalog-limit, kind-class,
  dangling-reference, FreeBSD, and paperwork defects.
- Necessary-work pins are part of the code under review: when a
  performance fix legitimately reduces page visits, the pins move with
  the measured reduction, never the other way around.

Follow-up mapping:

- Snapshot signing remains tracked by pending SOW-0017.
- The Linux no-replace-primitive acquire/main classification edge
  recorded in the slice-3 close entry is tracked to the M4 sidecar
  coordination chunk (the staged Go flow has no coordination rename
  until then); it is inside this SOW, so no separate SOW file is
  warranted.
- No other deferred item is created by this milestone.

## Outcome

Milestone 1 (immutable reader) is closed. Milestone 2 (writer core) is
implemented. Milestone 3 is in progress: chunks 1-3b-2 are implemented
and closed at the five-aspect review gate (slice 3, the publish_set
writer surface, closed at HEAD 8345891 with all five aspect reviewers
at PASS). The next M3 chunk is the snapshot writer surface
(snapshot::snapshot_to parity). SOW completion remains pending final
validation and user acceptance.

## Lessons Extracted

Pending implementation.

### 2026-08-12 - external audit close-out round (milestone-1 gate re-verification)

An external full-scope review found five real P0/P1 contract defects after
the six-reviewer pass declared PASS. All were verified against code and
specs, fixed, and pinned by pre-fix-failing tests:

1. Obsolete retention semantics had been reintroduced: `RetentionTag()` and its
   "retention" test survived the deletion (binary-format-v4.md:311 forbids
   the compatibility alias; milestone-0 report classified them for
   deletion). Removed from v4/go/types.go and types_test.go.
2. Pin value copies double-decremented the reader pin count: `p2 := *p1`
   carried its own closed flag. Every Pin now references one shared
   private pinState; value copies and pointer aliases close the same
   logical pin. Pinned by TestPinValueCopySharesClose,
   TestPinValueCopyKeepsReaderBusy, and
   TestPinValueCopyCannotReleaseSecondPin (all fail on the pre-fix code).
3. DirectSemantic registry drift: public Go values were 0/1/2 while the
   Rust engine-defined registry is Generic=1, FirstSeen=2, LastSeen=3.
   Go now exports 1/2/3; TestPublicSemanticFoundation pins them.
4. No-threat structured values had no clean absence result: the Go
   ThreatMembership returned a zero view with nil error. It now returns
   (MembershipView, bool, error) mirroring the Rust Option; a new
   Rust-produced corpus fixture (rust/structured-ipv4-nothreat.iprdb, six
   fixture manifest) with membership-id-zero values pins the absent path
   in both readers. Rust verify asserts Option None for the empty-feeds
   ranges; Go asserts present=false.
5. Duplicated authorities removed: error-code tables (public errors.go vs
   internal/format/codes.go) and Cardinality129 arithmetic (public types.go
   vs internal/format/cardinality.go) were independent copies. The
   internal/format package is now the single authority; the public package
   re-exports typed aliases (ErrorCode = format.ErrorCode and per-name
   constants; Cardinality129 = format.Cardinality129 plus wrappers).
   Uint64/Uint128 moved into the format authority to preserve the public
   method set.
6. Final-review regression guards: compile-time alias assertions pin the
   public ErrorCode and Cardinality129 as the internal/format types, and a
   negative source guard forbids the reintroduction of the obsolete
   retention symbol in any non-test production source; all three guards
   fail on the pre-fix tree.

Gates re-run at HEAD: go test ./... (4 packages), -race, vet, gofmt,
import graph, 9 cross-compiles, SOW audit, Rust conformance (6 fixtures),
and the regenerated no-threat fixture all pass. ZERO allocations and zero
atomics on every measured public hot path are unchanged (12 public + 8
internal checks at exactly 0).

## Followup

- SOW-0017 remains the separate Phase-2 signing item.

## Regression Log

### 2026-08-12 - final-review process regression

The session's round-6 final review reported PASS at HEAD 29e1dde after verifying
the supplied defect list and all mechanical gates. A fresh independent review
then found material issues outside that checklist:

- exported writable canonical ValueTag variables drive DirectSemantic and can
  be reassigned process-wide or raced with readers;
- Go exposes ImmutableInfo while the approved parity matrix, normative spec,
  and Rust authority use DatabaseInfo, with no recorded deviation;
- the milestone report retained stale statements about view allocations, the
  worker decision, and structured-view revalidation.

Root cause: the final review behaved as a closed checklist verifier, inherited
the prior repair narrative, sampled corrected record summaries, and allowed
green automation to end the review before an open-world public-contract and
record audit. The external review's claim that Milestone 2 lacked a
Pre-Implementation Gate was not reproduced: this SOW already has a ready gate.

Process repair: created and registered the generic `project-final-review`
runtime skill. Future final reviews start from zero trust, reconstruct authority
and complete scope, audit public interfaces and full records before running
gates, verify regression evidence and same-failure classes, and perform a
separate disproof pass before PASS. The skill is intentionally generic so the
same failure mode is prevented across all project work.

Validation for the process repair uses HEAD 29e1dde as the historical benchmark:
the workflow must identify the mutable tag authority, API-name deviation, and
three stale report claims even though every mechanical gate passes. Resolution:
the product fixes landed at HEAD 73bba50 (immutable tag accessors, DatabaseInfo
rename, corrected report statements) with the metadata figure re-measured at
2d2197a; the round-7 through round-10 re-review results are recorded in the
Gate execution record, with the closing round-10 PASS at HEAD 253f9d5.

Append regression entries here only after this SOW was completed or closed and
later testing or use found broken behavior. Use a dated
`## Regression - YYYY-MM-DD` heading at the end of the file. Never prepend
regression content above the original SOW narrative.

## Regression - 2026-08-12 - round-10 false PASS and evidence-protocol hardening

A fresh independent review of closure HEAD 1c71299 invalidated the round-10 PASS
at 253f9d5. Four implementation/contract defects already existed at the reviewed
revision, and the closure commit introduced a fifth records contradiction:

- the approved public API requires `NetworkEnrichmentV1Location` with
  `Location *NetworkEnrichmentV1Location`, but the Go facade flattened the
  coordinates and added `HasLocation` without an approved deviation;
- structured lookup performs semantic flags/coordinate validation that the Rust
  hot path and the normal-operation contract intentionally omit, and a test
  incorrectly labels this extra work as Rust parity;
- direct scans, blob branches, and inline membership word reads repeat decodes or
  record reconstruction, while the milestone report claims one page-header
  decode per visited page;
- the closure header said Milestone 1 was closed and Milestone 2 could start while
  Validation still said reopened/blocked and contradicted the skill update;
- `Mapping.File()` exposes an unused raw file capability, and the source gate's
  method-call regex does not catch content-transfer helpers such as `io.ReadAll`
  or `io.Copy`.

Root cause: the generic final-review skill described the right review lanes but
did not make adversarial disproof the reviewer's explicit success condition. The
round-10 execution therefore sampled exported names, equated zero
allocations/atomics with necessary work, trusted the source gate, ignored an
unused boundary escape hatch, and attached PASS to a revision followed by an
unreviewed closure commit.

The first repair draft overcorrected with six mandatory evidence artifacts and a
formal closure protocol. The user rejected that as compliance-heavy and likely
to narrow thinking. Final process repair: `project-final-review` now gives the
reviewer one explicit mission - prove with concrete evidence that the work is
faulty, incomplete, harmful, or unsafe to merge. It requires understanding the
objective and blast radius, grants authority to inspect any relevant surface and
create `/tmp` tests or mutations, treats green evidence as something to attack,
continues after the first finding, and permits PASS only when the strongest
plausible disproof attempts fail. It also explicitly forbids modifying the
reviewed repository, interfering with running processes, or installing/removing
software. This framing is generic and applies to every project final review.

Resolution: the five external-audit findings were fixed at HEAD ca30026 with
pre-fix-failing regression pins, and the round-11 final-review findings
(view-lifetime guard retargeting, mmap-gate escapes, stale records) were fixed
at HEAD 2fd6cae; the round-12 gate findings were fixed at HEAD 4fdc671
(whole-tree selector scan, dot-import and bufio bans, durable gate
decision log for the user's ratification; it was ratified on
2026-08-13 (option A) after the user delegated the choice to the
implementing agent. The complete re-review trail is recorded in the Gate
execution record; the closing result is appended there when it completes.

### 2026-08-13 - sixth-sweep gate rewrite (AST scanner) (HEAD c42325a)

- The sixth final review failed with five P2 findings, all in the mmap
  gate and the records: split-after-the-dot selectors; type-blind
  exact-literal exemptions; the open-ended stdlib denylist
  (compress/gzip regex bug, log/slog, runtime/trace,
  sweep; and completion claims ahead of the review trail.
- The line-oriented text scan is replaced by the AST gate scanner at
  v4/go-gate/main.go (stdlib only): it parses every production file
  regardless of build tags, line wrapping, comments, aliases, or file
  names; syntactically taints *os.File values; bans 37 content-transfer
  imports and 56 selector families; constrains *os.File use to the
  mapping-lifecycle methods and same-package/module-internal/x-sys
  consumers; and exempts the three exact in-memory inflater nodes only
  with file-taint verification (c.r.Read(p)/c.r.ReadByte() over a
  *bytes.Reader field, and the two exact io.ReadFull(zr, out[...int(meta.
  MetadataUncompressed)]) shapes with a non-file zr).
  independent reproducers of the sixth review; an innocent
  modified; the startup sweep is removed. HEAD 81ca524 then pinned the
  aliased-os producer form as the forty-first; HEAD 6b05801 tainted
  *os.File results returned by same-package accessor methods.
- Gates at those HEADs: go test ./... incl -race, go vet, gofmt,
  all green. Counts: production 4,772 raw lines / tests 4,832 raw
  lines.

### 2026-08-13 - seventh-sweep gate hardening (alias/container/pipe classes)

- The narrow re-review of the sixth-sweep records found two P2 items:
  the accessor-method taint (HEAD 6b05801) had no mutation pin and no
  record entry, and the records cited HEAD c42325a for the 41-form
  count although that commit pinned forty forms (the aliased-os form
  arrived with HEAD 81ca524).
- Ampere additionally proved two untainted file-escape classes in the
  scanner: a separately built `&os.ProcAttr{Files: []*os.File{f}}`
  lost the container taint (both as a composite literal and as a
  field assignment), and a type alias `type zrAlias = *os.File`
  defeated the exemption predicate and the taint resolution.
- Fixed (HEAD e2dc7e0): file producers now carry result positions
  (`os.Pipe` = both results; error results never tainted); type
  aliases are collected and resolved in classifyType, signatures, and
  producer conversions; composite literals propagate container taint
  from any file/container element; `os.StartProcess` joins the banned
  selectors (closing the field-assign variant); `os.Pipe` joins the
  producers; multi-result assignment taints only the file positions.
  41 accessor-method, 42 alias-conversion, 43 alias-parameter,
  44 separately built ProcAttr, 45 os.Pipe producer, 46 innocent
- Gates at HEAD e2dc7e0: go test ./... incl -race, go vet, gofmt,
  audit - all green. Counts: production 4,772 raw lines / tests 4,832
  raw lines (gate scanner lives outside the module).

### 2026-08-13 - eighth-sweep gate hardening (field storage and channel transport)

- The seventh-sweep re-review (Ampere round 2) found one P1 and one P2
  still open in the scanner: a *os.File parked in a struct field
  (`var zb box; zb.r, _, _ = os.Pipe()`), then assigned to the exact
  exempted inflater reader (`zr = zb.r`) in metadata.go, passed the gate
  (compiling code, exemption granted); the same class over a
  `chan *os.File` (`zr = <-ch`) also passed.
- Root causes: package-level var state was per-file in the scanner, so a
  type-only struct var declared in one file was invisible to functions
  in another; struct-field writes and type-only/new(T) instances were
  not registered; channel element taint did not exist (the make() chan
  type text was never printed), and field-type aliases were resolved in
  classifyType but not in field reads.
- Fixed (HEAD c4b1b52): shared per-package taint state (all package vars
  collected before any file runs); struct-field write taint overlay;
  type-only var and new(T) struct registration; chan *os.File taint for
  declarations, make(), parameters, sends (ch <- f), receives
  (x := <-ch), and range loops; container index reads; alias-resolved
  field types in isFileExpr/isContainerExpr; SelectStmt traversal.
  exact inflater exemption in metadata.go with a struct-field stored
  file and a channel-transported file; form 49 pins the benign
  same-shaped control (int field) that must pass, proving the taint is
  not a false positive. Durable rejection set: forty-seven mutation
  forms; the interplay between the exemption guard and the file taint
  is now mutation-tested.
- Gates at HEAD c4b1b52: go test ./... incl -race, go vet, gofmt,
  audit - all green. Counts: production 4,772 raw lines / tests 4,832
  raw lines (gate scanner lives outside the module).

### 2026-08-13 - ninth-sweep gate hardening (closures, assertions, nested channels)

- The eighth-sweep re-review (Ampere round 3) found three more escape
  classes in the scanner: an inline FuncLit returning *os.File
  (`zr = func() *os.File { ... return f }()`) escaped the taint because
  closure bodies were not taint-propagated and closure calls were not
  producers; a type assertion `zb.r.(*os.File)` erased the taint of a
  file hidden in an interface field (no TypeAssertExpr
  classification); and the channel family had two gaps - chan chan
  *os.File (and chan C with C = chan *os.File) did not resolve
  iteratively, and the single-variable range form `for z := range ch`
  put the element in the Key slot, which was not tainted.
- Fixed (HEAD ddc5f9c): closure bodies are walked for taint propagation
  (they capture the outer state); closure calls and func()-typed
  identifiers are producers when they return *os.File; TypeAssertExpr
  taints when the asserted type is *os.File and otherwise delegates to
  the asserted value; chanElemFile resolves nested channels and alias
  chains iteratively; the single-variable channel range taints the Key
  slot; IndexExpr reads of containers are file-tainted in isFileExpr.
  assertion, two-hop channel transport, and single-variable channel
  range, all shadowing or exercising the inflater exemption; the benign
  control (form 49) and the innocent-file survival check (form 46)
- Gates at HEAD ddc5f9c: go test ./... incl -race, go vet, gofmt,
  audit - all green. Counts: production 4,772 raw lines / tests 4,832
  raw lines (gate scanner lives outside the module).


### 2026-08-13 - tenth-sweep gate hardening (parenthesized producers, alias funcs, type-switch binds)

- The ninth-sweep re-review (Ampere round 4) found five more scanner gaps:
  a parenthesized producer call `zr = (getFile)()` and a parenthesized
  closure `zr = (func() *os.File { ... return f })()` both hid the callee
  shape behind a ParenExpr node; a closure declared as
  `func() io.ReadCloser { ... return f }` hid the returned *os.File behind
  an interface result type; a type-only alias
  `type fileFn = func() *os.File; var getFat fileFn` registered nothing in
  the funcs table; and a type-switch guard `switch zv := x.(type) { case
  *os.File: ... }` never tainted the bound identifier.
- Fixed (HEAD 7caf351): unwrapParen strips parentheses in producerCall and
  the rules walk so selector and argument checks see the same call;
  closures whose body returns a tainted value are marked retFile at their
  FuncLit node and every declared result position is treated as
  file-tainted; funcTypeResultsFile resolves alias text through the
  package alias map before falling back to AST result checks; the
  type-switch prepass binds the guarded identifier when a case type
  resolves to *os.File.
  parenthesized benign control (HEAD 5c88ba3). Durable rejection set:
  scanner.
- Extended during the round-5 gate re-review (HEAD 3952097): defined
  func types (type F func() *os.File) now register in the alias map,
  func() *os.File values returned through same-package helpers keep
  the producer taint (callResultsFuncFile), and type-switch cases
  binding defined func types enter funcFile. Forms 60-63 pin the
  three classes plus the benign bytes.Reader control.
- Extended again during the round-5 re-review (HEAD b168aba): method
  receivers now resolve through the struct instance (not the
  receiver variable name) in callResultsFuncFile, and a callee that
  is itself a call returning func() *os.File is a file producer
  (zb.mk()(), useDef2(getDef3)()). Forms 64-67 pin the method
  boundary, both double-call shapes, and the benign int control.
  Documented residual: a *os.File exported by a third-party package
  (other than os.Stdin/Stdout/Stderr) is invisible to the syntactic
  taint unless the code names *os.File textually or moves it through
  an already-tainted route.
- Ampere round 5 found four more producer routes (HEAD 5f97f94):
  struct-field func values, chan of func() *os.File, any-erased func
  returns recovered by type assertion, and os.Stdin/Stdout/Stderr
  through interface closures. Fixed with the kindFuncFile/
  kindChanFuncFile split, struct-field func resolution in
  producerCall/classify, func-file type assertions, and the std
  handle file-expression set. Forms 68-72 pin the four classes plus
  the benign chan-of-func() int control.
- Ampere round 6 found three more producer routes (HEAD 36fe279):
  nested struct-field func chains (nh.inner.fn()), named
  interface-typed helpers whose bodies return tainted files or
  os.Stdout (getNamed()/getStd()), and chan-of-func values passed
  through same-package helpers. Fixed with a resolveStruct field-
  chain walk, a per-directory pre-scan that marks named producers
  (retFuncs) before any runFile, and callResultsChanFuncFile.
  Forms 73-77 pin the three classes plus the benign named io.Reader
  helper control.
- The named-producer pre-scan was extended to methods with a
  fixpoint (HEAD 5a4f8dc): mb.named() returning os.Pipe or
  os.Stdout through io.ReadCloser, and deep() -> mid() chains,
  are now producers (retMethods + prescanFileProducers). Forms
  78-81 pin the three classes plus the benign method control;
  form 77 was reworked to a compiling bytes.Reader wrapper shape.
- Ampere round 7 found a fourth producer route (HEAD 1a54443 -> 8696af3):
  a nested method-receiver chain (mhv.inner.mk()(), where mk is a
  method on minner reached through the mholder.inner field, returning
  a defined func type producing *os.File). Fixed by resolving the
  receiver expression through resolveStruct instead of requiring a
  plain identifier; the same fix applies to the chan-of-func caller
  (callResultsChanFuncFile). Form 82 pins the escape; form 83 pins
  the benign nested-method control.
- Ampere round 8 found two more producer families (HEAD 96f0515):
  method values are never classified (a method value bound to a
  variable, returned through an interface-typed helper, bound from a
  nested receiver chain, or sent through a package-level channel), and
  generic type-parameter pass-through erases the file taint
  (idf[T any](f T) T instantiated with *os.File). Fixed with method
  resolution in classify (declared *os.File results, retMethods
  taint, defined func-type results), a retFuncFiles/retMethodFiles
  body-scan registry, double-call resolution on func-file variables,
  package-level channel taint propagation, and type-parameter result
  mapping for generic calls. Forms 84-89 pin the six escapes; forms
  90-91 pin the benign method-value and benign generic controls.
  Residual documented: third-party exported *os.File and cross-file
  local-channel flows remain visible only through declarative types.
- Ampere round 9 found three consumer-path gaps (HEAD aa019c8):
  generic container element shapes ([]T) never bound the type
  parameter; method values returning chan-of-func-file never tainted;
  and struct fields assigned file-producing closures/chans lost their
  taint because consumers read only the declared field text. Fixed
  with token-boundary element matching in the generic rules,
  chan-result resolution for method values and chan-marked variables,
  full-kind fieldTaint writes, fieldTaint consumers in classify and
  producerCall, and package-var fieldTaint propagation from the
  prescan. Forms 92-95 pin the four escapes; forms 96-97 pin the
  benign generic-container and benign func-field controls.
- Self-audit before Ampere round 10 closed the remaining channel
  consumers: receive classification (ARROW) distinguished chan-file
  element kind, RangeStmt classified the ranged expression whole
  (struct-field channels and method values), and SendStmt recorded
  selector-typed channel fields. Forms 98-100 pin the three escapes;
  forms 101-102 pin the benign range and benign receive controls.
- Ampere round 10 found the container-element route (HEAD ed0a0f9):
  map/slice fields holding file-producing funcs (fm.m["k"]()) were
  invisible because producerCall had no IndexExpr callee case,
  applyLHS did not record element writes, and classify read only
  file-container elements. Fixed with an elementTaint registry
  (element reads/writes, composite element kinds, declared element
  shapes for fields/params/vars), an IndexExpr callee case in
  producerCall, and exprText coverage for map/ellipsis/index types.
  Forms 103-106 pin the four escapes; form 107 pins the benign map
  field control.
- Ampere round 11 found the anonymous-receiver method route (HEAD
  63d665d): func (T) m() with no receiver variable name was invisible
  because receiverOf required a receiver name and never resolved the
  receiver type for method-value keys. Fixed by deriving the receiver
  struct from the receiver type expression, trimming pointer/generic
  spellings, keying alias-resolved method signatures, and never
  misregistering methods as package funcs. Forms 108-111 pin the four
  escapes (direct file, interface-hidden, pointer receiver, map-field
  method value); form 112 pins the benign anonymous-receiver control.
- Ampere round 12 found the alias-receiver route (HEAD 5fe4b4f):
  receiver aliases (type a = s) keyed retMethods and instance lookups
  inconsistently -- receiverOf returned the raw alias text while call
  sites resolved it through structBase, so interface-hidden results
  were invisible; and resolveStruct/classifyStruct/classify accepted
  only registered struct names for composite literals, so alias-named
  instances (rF{}.m()) never resolved. Fixed by resolving receiver
  aliases inside receiverOf and resolving alias names in every
  composite-literal struct lookup. Forms 113-114 pin the two escapes
  (alias-variable interface-hidden call, alias-literal direct file);
  form 115 pins the benign alias-receiver control.
- Ampere round 13 found the receiver-resolution class (HEAD
  85db9dc): defined-type receivers (type b a) were never registered,
  generic instantiations (gsG[int]) never stripped to the base name,
  embedded-field promotion (hE{gsE}) was dropped at collection, and
  pointer aliases (type p = *s) keyed methods under the pointer
  spelling. Fixed with a defined-type chain map, a resolveStructName
  helper (aliases + defined types + pointer + generic suffixes) used
  by structBase/receiverOf/every composite-literal lookup, and an
  embedded-method walk (methodMeta) for method-value and call
  resolution. Forms 116-119 pin the four escapes; form 120 pins the
  benign embedded-promotion control.
- Ampere round 14 found the pointer-defined-type route (HEAD
  36f6e82): var p *d with type d gs (defined type) and no initializer
  registered neither the pointer nor the defined chain because
  resolveStructName applied the defined-type lookup before the
  pointer trim. Fixed by running the alias/defined/pointer/generic
  reductions to a fixpoint in resolveStructName. Form 121 pins the
  escape; form 122 pins the benign pointer-defined-type control.
- Ampere round 15 found the indexed-receiver class (HEAD 99b211a):
  new(d) with d a defined type never resolved through the defined
  chain, and array/map-index receivers (arr[1].get(), mm["k"].get())
  had no IndexExpr resolution at all. Fixed with a varTypes registry
  (package vars, local typed vars, and short-decl composite
  literals), element-type stripping to the base struct in resolveStruct
  and classifyStruct, and defined-chain resolution in both new()
  cases. Forms 123-125 pin the three escapes; form 126 pins the
  benign array-index receiver control.
- Ampere round 16 found the element-receiver class (HEAD 7f72ca3):
  indexed bases beyond bare variables (struct fields, call results,
  dereferenced pointer-to-containers), make() short declarations,
  range-variable element receivers, and chan-receive receivers were
  all invisible. Fixed with a typeOfBase resolver (variables, struct
  fields, call results, deref/paren wrappers), a stripElemType
  helper (container and channel wrappers), exprElemStruct for range
  and receive element binding, make() type registration for
  short-declared containers, and ARROW receive resolution in
  resolveStruct/classifyStruct. Forms 127-132 pin the six escapes;
  form 133 pins the benign make() map receiver control.
- Ampere round 17 found the range-literal-receiver class (HEAD
  255f34c): range variables never recorded their element type in
  varTypes, so container/chan-typed bindings were blind as indexed or
  receive bases, and composite-literal index bases (map[string]*gs{...}
  ["a"]) had no typeOfBase case. Fixed by recording one wrapper-stripped
  element type for range bindings and adding the CompositeLit case to
  typeOfBase. Forms 134-135 pin the two escapes; form 136 pins the
  benign composite-literal indexed receiver control.
- Ampere round 18 found the bound-receiver class (HEAD 3cfe554):
  type-switch bound variables (case *gs: v.get()) never registered as
  struct instances, and multi-assignment call results (a, _ := f())
  never recorded their declared result type, so both were blind as
  receivers or index bases. Fixed by registering case structs and case
  type texts for type-switch bindings and recording the declared
  result type per index in applyLHSMulti. Forms 137-138 pin the two
  escapes; form 139 pins the benign type-switch bound receiver
  control.
- Ampere round 19 found the call-result-binding class (HEAD
  90ea53c): single-value call results (a := mkArr()), method-call
  results (a := box.mkArr()), type-switch default-clause bindings
  (switch v := iv.(type) { default: v.get() }), and multi-assign
  element reads (a, _ := mm["k"], 0) all failed to record the binding's
  type or struct instance, so each stayed blind as an indexed or
  receiver base. Fixed by recording result-0 declared types and
  non-call element types in applyLHS/applyLHSMulti (one wrapper
  stripped, struct instances only when the binding type itself names a
  struct), a typeOfBase IndexExpr case, and default-clause
  registration from the switched expression in the type-switch
  handler. Forms 140-143 pin the four escapes; form 144 pins the
  benign single-LHS call-result index control.
- Ampere round 20 found the interface-and-channel binding class
  (HEAD 2b3006a): explicit generic instantiations (a := mkGen[*gsG]())
  never substituted the call's type arguments into the result type,
  type-switch default clauses over interface-valued expressions
  (mkIf().(type), sd.f.(type)) never resolved the interface to its
  signature-identical implementations, and multi-assign from a channel
  receive ((<-chS), 0) recorded no element type. Fixed by substituting
  explicit type arguments in applyLHS/applyLHSMulti via
  genericSubstitutedResults, registering interface method signatures as
  pseudo-structs (with methodFull text matching), an ifaceImplProducer
  union over signature-identical implementations, and a typeOfBase
  UnaryExpr ARROW case. Forms 145-148 pin the four escapes; forms
  149-150 pin the benign generic and interface-default controls.
- Ampere round 21 found the generic receiver-binding class (HEAD
  5f18d4f): generic-receiver method results never substituted
  receiver type arguments, explicit-instantiation calls
  (mkT[*os.File]()) and their direct method flows were invisible,
  and argument-inferred generic struct bindings
  (mkT2(&gsG{}).get()) bound no file taint. Fixed with a
  recvTypeParams registry feeding genericMethodResults, a unified
  genericCallResults helper (explicit and inferred via
  inferTypeArgs), a typeOfBase unary-& case for generic literal
  receivers, and generic-first wiring in producerCall/classify/
  resolveStruct/classifyStruct/applyLHS/applyLHSMulti. Forms
  151-156 pin the six escapes; forms 157-158 pin the benign
  bytes-only controls.
- Ampere round 22 found the alias-spelled generic binding class
  (HEAD 82b96bf): a generic type argument written through a type
  alias (type zfA = *os.File; gRA[zfA]{}) was substituted as
  literal text and never alias-resolved, so the taint checks
  compared container results like []zfA instead of []*os.File and
  the io.ReadFull exemption could grant a file-backed zr. Fixed in
  substituteTypeParams: every type argument is reduced through
  resolveTaintType (alias and defined-type chains to a fixpoint)
  before substitution, and the substituted result is reduced
  again. This covers receiver instantiations, explicit and
  inferred generic calls, method values, and embedded promotion.
  Forms 159-164 pin the six alias escapes; forms 165-166 pin the
  benign bytes-backed alias controls.
- Ampere round 23 found four reader-shape escape classes (HEAD
  85c6f2c), all reaching the io.ReadFull exemption with gate exit 0:
  (1) container element reads from call results (a[0] of a
  []*os.File generic or plain result, m["k"], *p on *zfA) never
  classified the element; (2) chan-of-file carriers were blind in
  return positions (return <-c) and method/generic-call results
  (h.ch() chan *os.File, mkC[*os.File](), rr.mc()) never registered
  as chan taint; (3) cross-package aliases as generic type arguments
  (mapping.MappingFile) could not resolve because each directory is
  an independent pkgInfo; (4) generic method results naming a struct
  (r := rr.mk() with T bound to wS) never registered as struct
  instances, losing field taint on r.f. Fixed with elementReadKind
  (declared-element shape of index/deref bases) in classify,
  isFileExpr and isFileOrContainer; chanCarrier (identifier
  registries plus call classification) for receive positions, and a
  declared-results chan loop for method and function calls; a
  process-wide qualifiedAliases registry keyed package-clause name;
  and genericMethodResults/methodMeta consultation in resolveStruct
  and classifyStruct. Forms 167-174 pin the eight escapes; forms
  175-178 pin the benign bytes-backed controls.
- Ampere round 24 found the import-renamed qualified alias class
  (HEAD 932a0e8): a locally renamed import qualifier (import mm
  ".../internal/mapping") was never translated back to a package
  path, so mm.MappingFile generic type arguments, local alias chains
  of renamed imports (type MChainRen = mm.MappingFile), container
  element spellings ([]mm.MappingFile), declared variables
  (var z mm.MappingFile; z.Chdir()), and type assertions
  (v.(mm.MappingFile).Chdir()) all bypassed the gate with exit 0.
  Fixed with a per-directory alias registry (pkgAliasesByDir keyed by
  relative package dir), a per-file import snapshot (currentImports),
  and aliasLookup qualifier translation through the import map before
  matching the scanned directory; resolveTypeText, resolveDefinedType
  and resolveStructName all route through aliasLookup. Forms 179-182
  pin the four rejects; forms 183-184 pin the benign renamed-qualified
  bytes controls.
- Ampere round 25 found the func-typed generic-method class
  (HEAD 21b5742): producerCall's generic-method branch claimed a
  func-typed result (mresG of func() *os.File after the direct
  *os.File position check missed) as a direct file position, so
  applyLHS recorded the binding via st.file instead of st.funcFile
  and calling the bound func lost the taint - gRZ[func() *os.File]
  {}.mk()() reached the io.ReadFull exemption with gate exit 0.
  Fixed by removing the funcTextFile producer claim so classify's
  existing generic-method loop yields kindFuncFile and applyKind
  registers the func-file; the non-generic method control was
  already caught, proving the shape is right and only the generic
  registration was blind. Forms 185-189 pin the five rejects
  (direct, embedded promotion, local alias, renamed-import func
  alias, unapproved method on the invoked result); form 190 pins
  the benign bytes-backed func control.
- Ampere round 26 found two adjacent escape classes at HEAD
  e1b1229: (1) mixed multi-result non-generic functions
  (getFn() (func() *os.File, error) bound as f, _ := getFn();
  f()) lost the func-file at the exact func-typed position
  because callResultsFuncFile required every declared result to be
  a func-file, with no per-position fallback for plain functions;
  (2) a defined type over a qualified or complex underlying
  (type x mm.A, type x []*os.File) registered nothing in
  definedTo, and cross-package defined func types
  (type F func() *os.File in mapping) were invisible to
  qualified references, so both reached the io.ReadFull exemption
  with gate exit 0. Fixed with callResults/callResultKinds/
  callResultKindAt (per-position result kinds through generic and
  declared signatures), per-position carrier registration in
  applyLHSMulti for call RHS, definedTo registration for every
  non-func non-ident underlying type text, and qualified
  registration of defined func types. Forms 191-196 pin the six
  rejects (mixed pos 0, mixed pos 1, defined over renamed alias,
  local defined func type, cross-package defined func type, defined
  over defined); forms 197-198 pin the benign bytes controls.
- Ampere round 27 found three adjacent escape classes at HEAD
  180024b: (1) a generic receiver bound to an interface whose method
  declares mixed results (Get() (func() *os.File, error)) was claimed
  by producerCall as a raw file position because interface method
  signatures are stored as pseudo-fields, so applyLHSMulti recorded
  st.file instead of st.funcFile and invoking the bound func lost the
  taint (both mixed positions and the chan-of-func variant); (2) all
  non-generic methods lost their declared results in callResults
  because methodMeta's ok flag reports body-marked producers
  (retMethods), not method existence - mixed method results
  (mk() (func() *os.File, error)) bound with no position taint; (3)
  a defined type over an alias (type D A; A = func() *os.File) and an
  alias over a defined func type (type E = D2) were invisible to
  cross-package spellings: only aliases and defined func types
  entered the qualified registries, and a first-hop bare name from
  another directory could not resolve further. Fixed with declared-
  result precedence in classify plus a per-position kind preference
  in applyLHSMulti (func-file/chan carriers keep their invoke-able
  kind when a producer claim overlaps), method-existence detection in
  producerCall's func-field claim (a declared method is not a func
  field), non-nil-results acceptance in callResults for ordinary
  methods, and defined-type registration plus a per-directory
  fixpoint (finalizeDirAliases) closing alias/defined chains in the
  qualified registries. Forms 199-205 pin the seven rejects
  (interface mixed pos 0, interface mixed pos 1, interface
  chan-of-func, defined over alias, alias over defined func, method
  mixed pos 0, method mixed pos 1); form 206 pins the benign
  interface-typed bytes control.
- Ampere round 28 found three adjacent escape classes at HEAD
  a8097a1: (1) an interface embedding a file-producing interface
  (type IEmb interface{ IBase }, IBase.Get() func() *os.File)
  resolved no promoted method because methodMeta's embedded walk
  propagated only body-marked producers (ok flag) and dropped
  declared results, so x.Get()() on an interface-typed generic
  result reached the io.ReadFull exemption with gate exit 0, in
  both the single-result and the mixed multi-result shapes; (2) a
  defined struct in another package used as a generic type argument
  (gRZ28[mm.S28]{}, s.Get()) never resolved because each directory
  keeps an independent package info and only aliases/func types
  were mirrored across directories; (3) a qualified defined chain
  of nine named hops (mm.J28 behind alias A28 and hops B28..I28)
  exceeded the single-pass fixpoint budget and was map-order
  dependent (passing only when the map iteration chanced to make
  the final hop resolvable early). Fixed by propagating promoted
  declared results in methodMeta's embedding walk, process-wide
  mirrors of remote structs/methods/method-full/embedded chains
  with a parse-time seed-merge (local wins on collision),
  self-entries for struct spellings in the qualified alias
  registries, result-type resolution through resolveStructName in
  classifyStruct/resolveStruct, and a full fixpoint loop for the
  per-directory alias/defined closure with self-hop guards
  (resolveDirText budget 8 -> 64). Forms 207-210 pin the four
  rejects (embedded interface single, embedded interface mixed,
  cross-package struct method, nine-hop qualified chain); forms
  211-212 pin the benign bytes controls.
- Ampere round 29 found two adjacent escape classes at HEAD
  8385134/fffc4dc: (1) a renamed import qualifier on a cross-package
  interface embedded in a reader interface or struct (type IEmb
  interface{ mm.IMapBase }) never reduced to a registered key: the
  interface branch mirrored structs/methods/embedded chains but - 
  unlike the struct branch - registered no qualifier self-entry, so
  the promoted file method resolved nothing and x.Get()() reached the
  io.ReadFull exemption with gate exit 0; the clause-name spelling
  passed because the clause-qualified mirror key happened to match;
  (2) a generic interface instantiated at the embedding site
  (type IEmbGN interface{ IBaseGN[func() *os.File] }) promoted the
  declared results with the raw type parameter unsubstituted, so
  Get() T never matched the file shapes; the same gap covered the
  chan-of-func variant, a renamed generic interface instantiation,
  and a cross-package generic struct receiver. Fixed by registering
  the qualified self-entry for interface names exactly like structs
  (pkgAliasesByDir + qualifiedAliases), recording generic interface
  type parameters in recvTypeParams (with a process-wide mirror
  seeded per parseDir, closing the remote generic-receiver gap), and
  substituting the embedding's type arguments in methodMeta's
  promoted-method walk (args from parseBracketArgs, substitution via
  did not compile (unused "bytes" import in the reader file) - was
  the missing check is documented in the form comment. Forms 213-217
  pin the five rejects (renamed-qualifier interface embedding,
  generic-interface instantiation with func-file argument, same with
  chan-of-func argument, renamed-qualified generic interface
  instantiation, cross-package generic struct method); forms 218-221
  pin the benign bytes controls.
- Ampere round 30 found the defined-hop instantiation class at HEAD
  b53652d/9be245e: a defined type over an instantiated generic
  interface embedded in an interface (type D IBaseG[func() *os.File];
  type IEmb interface{ D }) lost the type arguments at the embedding
  walk because methodMeta's promoted-walk extracted brackets from the
  raw embedded spelling ("D") while the instantiation lives in the
  defined chain's target text ("IBaseG[func() *os.File]"), so the
  promoted results propagated the raw type parameter and Get() T
  reached the io.ReadFull exemption with gate exit 0; the renamed-
  qualified cross-package twin had the same shape. The same gap
  existed in genericMethodResults' embedded loop. Fixed by extracting
  the embedding's type arguments from the resolved text
  (parseBracketArgs(resolveTaintType(emb, info))) in both walk sites,
  so alias and defined chains carrying the instantiation substitute
  exactly like a direct spelling. Forms 222-223 pin the two rejects
  (reader-local and renamed-qualified defined-over-instantiated
  embedding); form 224 pins the benign bytes control.
- Ampere round 31 found the nested generic-instantiation class at
  HEAD f2d40d4: a multi-level generic-interface embedding (type
  InnerL[T] interface{ Get() T }; type IBaseGL[T] interface{
  InnerL[T] }; type IEmb interface{ IBaseGL[func() *os.File] })
  substituted type arguments only at the frame owning the brackets,
  so the frame declaring Get returned the raw type parameter "T"
  and x.Get()() reached the io.ReadFull exemption with gate exit 0
  (three-level and chan-of-func variants included); the intermediate
  frame's identity substitution (InnerL[T] with its own argument T)
  masked the leak during the audit's control probes. Fixed by
  threading type parameters and arguments down the embedding chain
  in methodMeta: every frame substitutes its own instantiation into
  the next embedded type text before recursing, the declaring frame
  applies the accumulated arguments to its declared results, and a
  frame-level interface-parameter registry (ifaceParams, mirrored
  process-wide like the receiver-parameter registry) carries generic
  interface parameters across packages, with the genericMethodResults
  receiver-substitution walk given the same threading and the
  embedded-entry argument list. Forms 225-226 pin the two rejects
  (two-level and three-level/chan variants); form 227 pins the
  benign bytes control.
- Round-41 narrow re-review found a sidecar error-class
  divergence at HEAD 550d107: Go's immutable open stat'ed the main
  file before checking the canonical .readers sidecar, so a live
  database whose main file was missing/renamed but whose sidecar
  remained returned Io (31) while Rust's open_immutable refuses
  with WrongMode (11) because require_sidecar_absent runs before
  open_read_only. Fixed at the reader level: OpenImmutable now
  applies the same sidecarAbsentUnderLock check before the main
  file is touched (the under-lock re-check inside the mapping open
  stays authoritative), pinned by the pre-fix-failing test
  TestSidecarPresence/missing-main-sidecar-present.
- Round-42 gate re-review found the x/sys source-content gap at HEAD
  6733d1c: the path-only allowlist accepted a poisoned GOMODCACHE
  checkout (evil extracted dir plus download cache at the allowed path)
  and a file proxy serving an evil x/sys with a self-consistent forged
  go.sum (both proven live with a smuggled unix.Pread2, gate exit 0 on
  both vectors), because nothing pinned the module content; the gate now
  pins the exact version, the module-cache path, the extracted-tree
  content hash, and the module zip/go.mod sums to the official v0.35.0
  values, and the assembly-object rejection is case-insensitive, pinned
  the fail-open listing gap: the per-target go list ./... loop swallowed
  listing failures, so a module the go toolchain cannot list (symlinked
  package files, parse errors) passed with an empty package list and no
  import checks; go list failures now fail the gate per target and the
  per-package import listing fails closed too, pinned as form 248,
  gate re-review then found the mmap-gate denylist gaps: os.CopyFS
  directory copies, os.OpenInRoot/os.OpenRoot handles reaching stream
  wrappers, and the x/sys descriptor-transfer primitives (unix.Tee,
  unix.Vmsplice, unix.IoctlFileClone/CloneRange/DedupeRange, darwin
  unix.Clonefile/Clonefileat) bypassed the scan (all proven live, gate
  exit 0); CopyFS and the x/sys primitives join the banned selector set,
  os.OpenInRoot/os.OpenRoot join the file-producer table so Root methods
  through a struct field: h.r.Open(name) after h := struct{r
  *os.Root}, gate exit 0) was then closed by resolving *os.Root as a
  file-bearing type everywhere *os.File does, pinned as form 252,
  producer-value re-review then closed the file-method-value,
  initialized func-typed-variable (Root and *os.File), and plain
  stdlib-producer-value escapes (forms 253-256); the round-48
  adversarial re-review then closed bound method expressions on
  file-bearing receiver types (form-local and package-level) and
  same-module cross-package producer vars (forms 257-260), raising
- Gates at current HEAD: go test ./... incl -race, go vet, gofmt,
  rejects 249-256 cover os.CopyFS directory copies, os.OpenInRoot/
  os.OpenRoot handles reaching stream wrappers, the x/sys
  descriptor-transfer primitives, *os.Root laundering through struct
  fields, file method values, initialized func-typed variables with
  file-bearing declared results, and stdlib producer values bound
  without a declared type, and round-48 rejects 257-260 cover bound
  method expressions on file-bearing receiver types and same-module
  cross-package producer vars, and round-49 rejects 261-266 cover
  doubly-parenthesized, renamed-import, alias-over-renamed, and
  wrapper-promoted method expressions, value-bound cross-package
  producer vars, and interface conversions laundering file values,
  and round-50 rejects 267-270 cover generic identity functions
  erasing a file taint into an interface result, composite-literal
  field laundering, method expressions on instantiated generic
  wrappers, and embedding chains deeper than the original walk
  budget, and round-51 rejects 271-277 cover file-bound generic
  result erasure and positional composite-literal field launders
  (with 267-268 converted to exemption-shape appends), and round-52
  rejects 278-288 cover embedded file-handle literal launders,
  var-bound generic-instantiation results, anonymous struct-literal
  positional elements, elided container-element fields, pointer
  composite literals, and explicit-instantiation callee closures),
  and round-53 rejects 289-290 cover the embed import and the
  //go:embed directive (compile-time database copies),
  ten cross-compiles,
  SOW audit - all green. Counts: production 4,789 raw lines / tests
  4,887 raw lines (gate scanner lives outside the module).


## Status History (appendix, 2026-08-11 .. 2026-08-13)

This appendix preserves the full pre-2026-08-14 Status narrative, moved
verbatim when the Status was compacted on 2026-08-14. It is historical
review detail: rounds 1-54 of the gate hardening, the milestone-1 review
history, and the close-out attempts that were later invalidated. Current
truth lives in ## Status; this appendix is context, not authority.


Sub-state: milestone 1 REOPENED pending re-review. The round-10 PASS at HEAD
253f9d5 and the closure commit at HEAD 1c71299 were invalidated by a fresh
independent audit; all five P2 classes were fixed at HEAD ca30026: the
implemented NetworkEnrichmentV1Location surface (value + HasLocation,
recorded as open decision 5A awaiting user ratification, ratified later on 2026-08-13), implicit semantic validation removed from
structured lookup, hot-path decodes cut to one page-header decode per
visited page with membership word reads served from the lookup-time record
decode, the contradictory closure records corrected, and Mapping.File
removed with the content-I/O source gate extended to io.ReadAll/io.Copy.
Regression pins: plausible-corruption decode acceptance, record-geometry
rejection at lookup, and the vector codec. The round-11 final review then
found one P1 (pin variable reassignment retargets the view guard and a
word read segfaults on released memory) and three P2 (decision 5A
unratified; mmap gate bypassable plus a Windows Mapping.File escape; stale
close-out records), all fixed at HEAD 2fd6cae with the cross-reader
reassignment regression test, or recorded for the user's decision (5A).
The round-12 final review then confirmed the lifetime fix but found two
remaining P2: decision 5A still unratified, and the mmap source gate still
bypassable (x/sys descriptor reads, bufio wrappers, dot imports, and
build-tagged packages). Fixed at HEAD 4fdc671: the gate is now a
whole-tree selector scan (find across all build tags) with dot-import and
mmap-only evidence (strace of an open/read/close session: openat, OFD
lock, mmap, munmap, unlock, close with no read/pread/readv/lseek on the
database descriptor) recorded in the report. The round-13 final review
then failed with three P2: decision 5A still unratified; the gate still
accepted indirect content-transfer forms (fmt.Fscan/Fscanf, io.CopyN/
CopyBuffer, reflection MethodByName, raw unix.Syscall(SYS_READ),
unix.CopyFileRange, Sendfile/Splice), its line-level tolerated-call
exemption could hide a forbidden transfer on the same line, and a
windows-tagged package could still import internal packages unseen by a
linux-only go list boundary check; plus two P3 comment corrections.
All fixed at HEAD dbdf2b7: tolerated calls are blanked as exact call
nodes instead of whole lines, the selector set covers every indirect
form, gzip and compress/zlib wrapper imports are banned, the boundary
fix then found the decoder/encoder family still open (encoding/json,
xml, gob NewDecoder(f).Decode, image/archive wrappers), os.File.WriteString,
a nested-paren blanking shadow, and reflect.Value.Method(i);
the gate now also bans the reader-consumer packages, covers
WriteString/WriteRune/NewDecoder/Decode/Encode/Method selectors, blanks
io.ReadAtLeast over a file still open, the writer-consumer packages
(log, text/template, html/template, os/exec, net/http, flate.NewWriter)
at HEAD bf33f2a (selectors ReadFull/ReadAtLeast/Print/Printf/Println/
Scan/Scanln/Scanf/NewWriter; the five writer packages join the import
ban; the two in-memory inflater io.ReadFull(zr, ...) nodes are exempted
exactly; the method-value and CopyFileRange forms compile, and the
nested-node probe stays an intentional textual tripwire), and the
of that fix found the io.ReadFull exemption itself paren-crossing (a
nested transfer could still be swallowed; a file-backed flate reader
named zr was exempted by name), the reflection Call invocation
unguarded (FieldByName("Read").Call), and the reader-constructor
packages (debug/elf and the debug/* family, go/parser, go/scanner,
text/scanner, text/tabwriter, mime/quotedprintable) unlocked; all fixed
at HEAD 149a200 (the io.ReadFull exemption is shape-bounded to the two
real nodes io.ReadFull(zr, out[...]), Call/CallSlice join the
selectors, the constructor packages join the import ban), and the
of that fix found the exemptions still name-keyed (a file-backed flate
reader named zr with a buffer named out, and a receiver field r, could
reproduce the tolerated shapes), so at HEAD c03e40c the exemptions are
exact literals and nothing else: c.r.Read(p), c.r.ReadByte(), and the
two io.ReadFull(zr, out[...int(meta.MetadataUncompressed)]) inflater
reads; same-named file-backed readers and other index shapes now fail
closed, two pin forms were added, and a startup sweep removes stale
the same pass; decision 5A remains the single open user decision and
blocks milestone close. Repository counts: production 4,792 raw lines /
tests 4,877 raw lines. Milestone 2 must not start until a new
independent final review passes.

The sixth final review then failed with five P2 findings, all in the mmap
gate and the records: selector splitting after the dot (`file.\nRead(p)`
and `io.\nReadAll(f)` compile and bypass a line scan); type-blind
exact-literal exemptions (a struct whose `c.r` is `*os.File` using exactly
`c.r.Read(p)`, and a function whose `zr` is `*os.File` using exactly
`io.ReadFull(zr, out[:int(meta.MetadataUncompressed)])`, both pass); an
open-ended stdlib denylist (the gzip regex never matches `compress/gzip`;
`log/slog.NewTextHandler`, `runtime/trace.Start`, and `os.StartProcess`
with `ProcAttr{Files: []*os.File}` consume a file unseen); a destructive
gate reports PASS, and untracked user work can be destroyed); and
acceptance records claiming completion while the six-reviewer PASS at
HEAD 360130c was not recorded and round-12 wording said decision 5A was
"fixed". Fixed at HEAD c42325a: the line-oriented text scan is replaced by
an AST, type-light scanner (v4/go-gate/main.go, stdlib only) that parses
every production file - build tags, line wrapping, comments, aliases,
and file names are irrelevant to the token stream - syntactically taints
`*os.File` values (declarations, parameters, os.Open*/os.Create
producers, same-package constructors, struct fields), bans 37
content-transfer imports and 56 selector families, permits `*os.File`
values only into the mapping-lifecycle methods
(Fd/Close/Name/Stat/Sync/Truncate/Chmod/Chown) and
same-package/module-internal/x-sys consumers, and exempts the three
exact in-memory inflater nodes only when their receiver/arguments are
temporary directory: it never touches the reviewed tree, reserves no
reproducers of the sixth review; the startup sweep is gone. HEAD
81ca524 then pinned the aliased-os producer form as the forty-first;
HEAD 6b05801 tainted `*os.File` results returned by same-package
accessor methods. The seventh-sweep hardening (HEAD e2dc7e0) closed
the type-alias conversion and parameter classes, separately built
`os.ProcAttr{Files}` containers, and the `os.Pipe` producer class,
struct-field-storage and channel-transport classes behind the inflater
exemptions (shared per-package taint state, struct-field write taint,
chan *os.File taint including send/recv/range, new(T) instances,
ninth sweep (HEAD ddc5f9c) closed the inline-FuncLit, type-assertion,
two-hop-channel, and single-variable-channel-range escape classes
(forms 50-53, with the benign control at form 49); the durable
(HEAD 5c88ba3) closed the parenthesized-producer,
parenthesized-closure, interface-typed-closure,
alias-typed-function-variable, and type-switch-bound escape classes
(forms 54-58, with the parenthesized benign control at form 59); the
stress-testing the round-4 fixes during the round-5 gate re-review,
the defined-func-type family and its method/nested-callee variants
chan-of-func/asserted-func/os-std-handle family (forms 68-72), and
the round-6 nested-field/named-helper/chan-pass family, the
named-method extension, the nested-method-receiver extension, the
method-value family, the generic pass-through family, the
generic-element family, the chan-result method-value class, and the
field-assignment class, the channel-consumer class, the
container-element class (forms 73-107), the anonymous-receiver
method class (forms 108-111), the alias-receiver method
class (forms 113-114), the receiver-resolution
class (forms 116-119), the pointer-defined-type
class (forms 121), the indexed-receiver
class (forms 123-125), the element-receiver
class (forms 127-132), the range-literal-receiver
class (forms 134-135), the bound-receiver
class (forms 137-138), the call-result-binding
class (forms 140-143), the explicit-instantiation and
interface-binding class (forms 145-148), the
generic-receiver-binding class (forms 151-156), the
alias-spelled generic binding class (forms 159-164), and the
reader-shape binding class (forms 167-174), and the renamed-qualified alias
  class (forms 179-182), and the func-typed
generic-method class (forms 185-189), the mixed result and
qualified-defined class (forms 191-196), the
interface-method and method-result class (forms 199-205), the
embedded-interface and cross-package chain class (forms 207-210), the
remote-interface and generic-instantiation class (forms 213-217), the
defined-hop instantiation class (forms 222-223), the nested generic-
instantiation class (forms 225-226), and the cgo-import,
raw-syscall, linkname, no-error syscall and preadv2/pwritev2 classes
(forms 228-230, 232-235) with the benign lifecycle control (form
231); the durable rejection set is now two hundred thirty-eight
same-module cross-package producer-var class, forms 257-260;
round-49 closed the nested-parenthesized, renamed-import,
alias-over-renamed, wrapper-promoted method-expression,
value-bound cross-package producer-var, and
interface-conversion-launder class, forms 261-266;
round-50 closed the generic interface-erasure, composite-literal
field-launder, generic-wrapper method-expression, and deep
embedding-chain class, forms 267-270, note that forms 267-268 were
initially vacuous (separate-file launders rejected by the
unconditional selector ban) and were converted to exemption-shape
appends in round 51);
round-51 closed the file-bound generic result-erasure class
(all declared result positions of a generic call with a file-typed
argument binding a type parameter stay tainted: renamed-qualifier
interfaces, other-stdlib interfaces, slice/array/map/chan/func
wrappers) and the positional composite-literal field-launder class
(unkeyed elements resolve through the struct's declared field
order, including an os.Root opener hidden behind a positional
field), forms 271-277 plus the converted 267-268, raising the set
round-52 closed the embedded and anonymous struct-literal launder
classes (embedded fields are named by type name, not qualifier
spelling, so positional io.Reader and positional/keyed *os.Root
wrapper literals no longer leave a live promoted handle; variables
bound to explicit generic instantiations keep the base generic's
substituted results; anonymous struct literals resolve positional
order from their own fields, and anonymous structs embedding
*os.File/*os.Root escalate like named wrappers), forms 278-282, and
the continuation class of the same round (elided inner composite
literals in slice, map, nested-container, and channel elements,
pointer composite literals, and func-valued arguments to explicitly
instantiated generics), forms 283-288, raising the set to two
import and //go:embed directive classes (compile-time database
copies), forms 289-290, raising the set to two hundred forty
round-45 closed the mmap-gate denylist gaps (os.CopyFS directory copies, os.OpenInRoot/os.OpenRoot handles reaching stream wrappers, the x/sys descriptor-transfer primitives Tee/Vmsplice/IoctlFileClone*/Clonefile*, *os.Root laundering through fields/params/helpers, file-method values, and func-typed variables with file-bearing declared results or stdlib producer initializers, forms 249-256; round-36 closed the dup/exec subprocess escape and the
bodyless assembly-stub class, forms 236-237; its follow-up closed the
x/sys-owner boundary for every package plus assembly-object files, forms
238-239; round-38 closed the fcntl F_DUPFD descriptor duplication
primitive, form 240; round-39 closed the out-of-tree module-graph
escape (go.mod replace and go.work can attach code the walk never
scans; the graph is validated to exactly this module plus x/sys with
no workspace, forms 241-242; round-40 closed the x/sys source
replacement and hidden dot-directory vectors, forms 243-244; round-42 closed the x/sys source-content gap, forms 245-247; round-43 closed the fail-open listing gap, form 248). The round-24 gate re-review then found the import-renamed qualified
alias class: an import mm ".../internal/mapping" local qualifier was
never translated back to a package path, so mm.MappingFile generic type
arguments, local alias chains of renamed imports, element spellings,
declared variables, and type assertions all escaped with gate exit 0.
Fixed in the go-gate scanner with per-directory alias registration
(pkgAliasesByDir), a per-file import snapshot (currentImports), and
179-182 (rejects) and 183-184 (benign controls). The durable rejection
of this pass complete the trail up to this re-review. The round-25
gate re-review then found the func-typed generic-method class: a
generic method whose type argument binds a func type producing
*os.File (gRZ[func() *os.File]{}.mk() bound to f, then f()) was
claimed by producerCall as a direct file result, so the binding was
recorded as a file instead of a func-file and invoking it lost the
taint - a file-backed zr again slipped through the io.ReadFull
exemption with gate exit 0. Fixed by removing the funcTextFile claim
from producerCall's generic-method branch so classify's own generic
method loop yields kindFuncFile and applyKind records the func-file
bytes control). The durable rejection
of this pass complete the trail up to this re-review. The round-26
gate re-review then found the mixed-result and qualified-defined
class: (1) callResultsFuncFile required every declared result of a
non-generic function to be a func-file, so mixed multi-result calls
(getFn() (func() *os.File, error)) lost the func-file taint at the
exact func-typed position and f() reached the io.ReadFull exemption
with gate exit 0; (2) a defined type over a qualified or complex
underlying (type x mm.A, type x []*os.File) registered nothing, so
the chain never expanded, and cross-package defined func types
(type F func() *os.File in mapping) were invisible to qualified
references. Fixed with per-position result-kind resolution
(callResults/callResultKinds/callResultKindAt routed through
generic and declared signatures), per-position carrier registration
in applyLHSMulti for call RHS, definedTo registration for every
non-func non-ident underlying, and qualified registration of defined
(benign bytes controls). The durable rejection
of this pass complete the trail up to this re-review. The round-27
gate re-review then found the interface-method and method-result
class: (1) a generic receiver bound to an interface whose method
declares mixed results (Get() (func() *os.File, error)) lost the
func-file position because producerCall claimed the interface method
signature (stored as a pseudo-field) as a raw file position, so the
binding was recorded as a file and invoking it lost the taint;
(2) callResults dropped every declared result of a non-generic method
because methodMeta's ok flag reports body-marked producers, not
whether the method exists - mixed method results (mk() (func() *os.File,
error)) bound with gate exit 0; (3) defined types over aliases and
aliases over defined func types (type D A with A = func() *os.File;
type E = D2) were invisible to cross-package spellings because only
aliases and defined func types entered the qualified registries, and a
first-hop name from another directory could not resolve further.
Fixed with declared-result precedence in classify and per-position
kind preference in applyLHSMulti (func-file/chan carriers keep their
invoke-able kind), method-existence detection in producerCall's
field-type claim (a declared method is not a func field),
non-nil-results acceptance in callResults for ordinary methods, and
defined-type registration plus a per-directory fixpoint in the
199-205 (rejects covering both mixed positions, the chan-of-func
variant, the defined-over-alias and alias-over-defined hops, and both
method-result positions) and 206 (benign interface-typed bytes
control). The durable rejection
of this pass complete the trail up to this re-review. The round-28
gate re-review then found the embedded-interface and cross-package
chain class: (1) an interface embedding a file-producing interface
(type IEmb interface{ IBase } with IBase.Get() func() *os.File)
resolved no promoted method because methodMeta's embedded walk
propagated only body-marked producers and dropped declared results,
so x.Get()() on an interface-typed generic result reached the
io.ReadFull exemption with gate exit 0 (both the single-result and
the mixed multi-result shapes); (2) a defined struct in another
package used as a generic type argument (gRZ[mm.S28]{}, s.Get())
was invisible to the reader's local package info; (3) a qualified
defined chain of nine named hops (mm.J28 after alias A and hops
B..I) exceeded the single-pass fixpoint budget and was map-order
dependent. Fixed by propagating promoted declared results in
methodMeta's embedding walk, process-wide mirrors of remote
structs/methods/embedded chains with a parse-time seed-merge,
self-entries for struct spellings in the qualified registries, and
a full fixpoint loop for the per-directory alias/defined closure
and 211-212 (benign bytes controls). The durable rejection
of this pass complete the trail up to this re-review. The round-29
gate re-review then found the remote-interface and generic-
instantiation class: (1) a renamed import qualifier on a cross-
package interface embedded in a reader interface or struct (type
IEmb interface{ mm.IMapBase }) reduced to no registered key because
only structs (not interfaces) registered the qualifier self-entry,
so the promoted file method lost its taint; (2) a generic interface
instantiated at an embedding site (type IEmbGN interface{
IBaseGN[func() *os.File] }) promoted the raw type parameter without
substitution, so Get() T never matched the file shapes (both the
func-file and chan-of-func variants); the adjacent remote shapes (a
renamed generic interface instantiation and a cross-package generic
struct receiver) carried the same gap. Fixed by registering the
qualified self-entry for interface names exactly like structs,
recording generic interface type parameters in the receiver-parameter
registry with process-wide mirrors, and substituting the embedding's
type arguments in the promoted-method walk; the round's P2 (a
non-compiling benign form-212 twin) was fixed by removing its unused
(benign bytes controls). The durable rejection
of this pass complete the trail up to this re-review. The round-30
gate re-review then found the defined-hop instantiation class: a
defined type over an instantiated generic interface embedded in an
interface (type D IBaseG[func() *os.File]; type IEmb interface{ D })
lost the instantiation at the embedding walk because the brackets
live in the defined chain's target text, not in the raw embedded
spelling, so the promoted method results propagated the raw type
parameter and Get() T never matched the file shapes (both the
reader-local and the renamed-qualified cross-package shapes). Fixed
by extracting the embedding's type arguments from the resolved
defined/alias text (resolveTaintType then parseBracketArgs) in both
the promoted-method walk and the generic receiver-substitution walk;
control). The durable rejection
of this pass complete the trail up to this re-review. The round-31
gate re-review then found the nested generic-instantiation class:
a multi-level generic-interface embedding (type InnerL[T]
interface{ Get() T }; type IBaseGL[T] interface{ InnerL[T] };
type IEmb interface{ IBaseGL[func() *os.File] }) substituted only
at the frame owning the brackets, so the frame declaring the method
returned the raw type parameter and Get() T reached the io.ReadFull
exemption (three-level and chan-of-func variants included). Fixed by
threading type parameters and arguments down the embedding chain:
each frame substitutes its own instantiation into the next embedded
type text, the declaring frame applies the accumulated arguments,
and a frame-level interface-parameter registry (ifaceParams, mirrored
process-wide) carries generic interface parameters across packages;
the receiver-substitution walk gained the same threading and the
(rejects) and 227 (benign bytes control); forms 228-230 and 232-235 pin the
round-32 cgo-import, raw-syscall, linkname, no-error syscall, and
preadv2/pwritev2 rejects with form 231 the benign lifecycle
control. The durable rejection set is now one hundred eighty-five
subprocess-escape class: dup'ing the database descriptor onto stdin and
exec'ing a reader (unix.Dup2 + unix.Exec, /bin/cat) streams file content
out with no banned read call, and a bodyless Go declaration attaches an
assembly syscall body the AST scan cannot see. Fixed by banning the
Dup/Dup2/Dup3/Exec/ForkExec selectors and rejecting bodyless
follow-up re-review then closed the remaining owner-boundary gaps: a new
package could import golang.org/x/sys unseen by the per-target loop
(only the four known packages were checked), and a .s/.syso assembly
object was never scanned (the bodyless-declaration ban was the only link
guard). Fixed by moving the x/sys owner rule into the per-target loop
for every package except internal/mapping and rejecting assembly objects
round-37 narrow re-review then found a metadata parity gap: ReadMetadataJSON
accepted a metadata chunk page whose post-data tail bytes were nonzero,
while Rust rejects it as corrupt (metadata.rs:274) and the spec requires
zero tails (binary-format-v4.md:1051). Fixed with an explicit tail-zero
check in internal/reader/metadata.go and the pre-fix-failing regression
pin TestMetadataChunkTailNonzeroRejected. The round-38 re-review then
closed the fcntl F_DUPFD duplication primitive: unix.FcntlInt(fd,
F_DUPFD, 0) can duplicate the descriptor onto stdin like dup, unseen by
the dup-name bans; FcntlInt joins the banned selector set (FcntlFlock,
the mapping owner's lock path, is a different function and stays
module-graph escape: a go.mod replace directive or a go.work workspace
can attach an out-of-tree module whose files the scanner walk never
visits, letting a wrapper call unix.Pread on the database descriptor
unseen (both vectors exited 0). Fixed by validating the module graph
itself - go list -m all must be exactly this module plus
forms 241-242, raising the set to one hundred ninety-two mutation
forms. The round-40 re-review then found the path-only allowlist gap:
replace golang.org/x/sys => <evil dir> keeps the allowed path in the
graph while loading attacker-controlled code the walk never scans
(proven live with unix.Pread2 reading the database), and the walk
skipped every hidden dot-directory, hiding in-tree replacements. Fixed
by banning all replace/exclude directives, verifying the resolved
x/sys source is the module-cache checkout, and scanning hidden
gate re-review then closed the x/sys source-content gap: the path-only
allowlist accepted a poisoned GOMODCACHE checkout or a file proxy
serving an evil x/sys with a self-consistent forged go.sum (both proven
live with a smuggled unix.Pread2 the ban list cannot know, because
nothing pinned the module content); the gate now pins the exact version,
the module-cache path, the extracted-tree content hash, and the module
zip/go.mod sums to the official v0.35.0 values, and the assembly-object
gate re-review then found the fail-open listing gap: the per-target go
list ./... loop swallowed listing failures (2>/dev/null), so a module
the go toolchain cannot list - symlinked package files or parse errors -
passed with an empty package list and no import checks (reproduced with
a symlinked smuggled file in internal/mapping); the package checks now
fail closed on every listing error and the per-package import listing
pass for that fix completed with all six narrow reviewers at PASS (HEAD e5fea20).
The round-45 final review then failed with three P2 findings, all in the mmap source gate: os.CopyFS was absent
from the selector ban (a directory copy streams artifact bytes with no banned selector; the live reproducer
exited 0), os.OpenInRoot/os.OpenRoot were absent from the file-producer table (a Go 1.26 OpenInRoot *os.File,
or an older-toolchain *os.Root handle, reached flate.NewReader untainted and streamed file bytes, and
Root.Open/Create/OpenFile also produce files), and the blanket-approved x/sys surface still carried
descriptor-transfer primitives unseen by the denylist (unix.Tee, unix.Vmsplice, unix.IoctlFileClone/CloneRange/
DedupeRange, darwin unix.Clonefile/Clonefileat). Fixed at HEAD 14c0698: CopyFS, Tee, Vmsplice,
IoctlFileClone* and Clonefile* join the banned selector set (CopyFileRange/Sendfile/Splice were already
banned); os.OpenInRoot and os.OpenRoot join the file-producer table as position-0 file taints, so every Root
method outside the approved lifecycle surface fails closed; all three live reproducers plus the
OpenRoot/ReadAll and darwin Clonefile variants are rejected by the hardened gate. The adversarial re-review
of that fix then found a P0 in the same class: a *os.Root handle stored in a struct field (h :=
gateRootField{r: root}; h.r.Open(name)) dropped the file taint, so the returned *os.File reached
flate.NewReader untainted and the stream was consumed through the exact inflater exemption shape with gate
exit 0; *os.Root now resolves as a file-bearing type everywhere *os.File does (fields, parameters,
helper returns, type assertions, func/chan elements, results), so every laundering route fails closed.
adversarial re-review of that closure then found three P0 escapes in the same producer-value class, all
proven live with full metadata-exemption chains at gate exit 0: (1) a file method value (open := root.Open;
open(name)) escaped the call-receiver ban and the bound method produced an untainted *os.File; (2) a
func-typed variable with an initializer (var newRoot func(string) (*os.Root, error) = os.OpenRoot) lost
its declared file-bearing result type because the type was only consulted for type-only vars; (3) the same
declared-type gap predated the Root work for *os.File (var openPath func(string) (*os.File, error) =
os.Open). Fixed by checking the file method in value position against the approved surface, registering the
declared result type of initialized func-typed variables, and registering stdlib producer values (os.Open
closed bound method expressions on file-bearing receiver types and
same-module cross-package producer vars (forms 257-260, two hundred
parenthesized, renamed-import, alias-over-renamed, and
wrapper-promoted method expressions, value-bound cross-package
producer vars, and interface-conversion laundering of a file into
the metadata inflater exemption (forms 261-266, two hundred sixteen
identity-with-interface-result erasure, the composite-literal field
launder, the instantiated-generic-wrapper method expression, and
the deep embedding-chain method expression (forms 267-270, two
of this pass complete the trail up to this re-review. Repository counts:
production 4,789 raw lines / tests 4,887 raw lines (the round-54
dead-state removal and zero-Pin test account for the latest delta; the gate
scanner lives outside the module). Milestone 2 must not start until a
new independent final review passes; decision 5A was ratified (option A,
2026-08-13); no open user decision remains.
The approved later scope remains unchanged: Milestone 2 is the writer;
sidecars, live coordination, and publication remain Milestone 4.

