# SOW-0027 - Go/Rust v4 SDK Parity Reconciliation

## Review Gate (user directive 2026-08-26)

Every milestone and slice gate under this SOW uses exactly five reviewers,
all of the lead's own model, running the final-review skill in adversarial
mode, each with one distinct scope. The Rust implementation is the baseline:
the reviewers check the Go work in contrast to Rust for all aspects, while
ensuring optimal Go idioms, performance, accuracy, interoperability, and
compatibility.

1. Rust parity: Go must do exactly what Rust does; the smallest divergence is
   a performance or behavior red flag. This includes mmap-only access and
   memory safety and lifetime ownership.
2. Go idioms: the work must be natural Go despite being identical in logic,
   system calls, philosophy, and structure to Rust.
3. Performance: the absolutely most performant way; even one unnecessary
   branch in the hot path is expensive.
4. Wire format and integrity: Rust/Go interoperability of the file format on
   disk, locking, coordination, and the shared conformance corpus.
5. APIs and records: public APIs, docs, errors, and records all in sync with
   Rust.

Reviewers are level-1 only (no level-2 tier). They are spawned once and kept
open for incremental delta re-reviews; they are restarted only between
milestones. No reviewer model other than the lead's own is used. These rules
live here at the top of the SOW, not in SWARM.md.

## Status

Status: in-progress

Sub-state (2026-08-28): milestone-4 extension B (universal-key
elimination) authorized by user decision and recorded below. The accepted
milestone-4 evidence (rust-ratio-acceptance-20260828.csv) stays valid as the
pre-optimization baseline for the extension.

### Milestone-4 extension B (user decision 2026-08-28): universal-key elimination

User decision: option B from the 2026-08-28 discussion (the single sol
review's P1 "universal keys" finding) - make the Go engine structurally
optimal like Rust: eliminate the universal 88-byte `tree.Key` from the hot
paths so decoded IPv4 range records shrink from 184 bytes to 12 bytes and the
per-record interface dispatch disappears. Wire format, public APIs, and
cross-open compatibility must NOT change. Long-term-best, minimal-complete,
absolute-performance, clean code, one authoritative implementation.

Requirement model (evidence): the Go range writer carries every decoded leaf
as `rangeRecord` with two 88-byte `tree.Key` values (184-byte records) and
dispatches per record through `rangeFamily` / `tree.Codec` /
`RangeStore` / `tree.Store`. Rust carries `Record<K>` with `K = Ipv4Key`
(12-byte IPv4 records, 36-byte IPv6) and monomorphized static code
(`v4/rust/iprange-livedb/src/range_tree.rs` Record/Codec,
`range_mutation.rs` PrivateGap). Result: clamping per-record value copies
from 3 cache lines to 1, eliminating the per-record key construction
conversions, and removing the 88-byte values from the mutation machinery.
The measured deltas this targets (1M medians, rust-ratio-acceptance-
20260828.csv): nested-overwrite 6.7x, update-ipsets-workflow 4.4x,
membership-import 3.5x, lookups 1.9-2.1x.

Design (decided, implementation authority):

1. `tree.Key` is redefined as the canonical-compare-bytes key: fixed
   numeric keys hold their compare bytes big-endian in a `[40]byte` value
   with a size tag (4/8/12/16), hash keys hold digest bytes plus each
   suffix word big-endian (same order as Rust `HashKey` derived Ord), and
   variable keys (catalog names) keep a byte slice. All key compares in
   the tree core become static bytewise compares (no fields to
   materialize, no per-probe conversion; the width-specialized
   `fixedLowerBound` probe keeps its existing static form, reading the
   canonical bytes instead of `Key.Hi`/`Key.Lo`/`Key.Raw`).
2. The writer's range family becomes generic over the key type exactly
   like Rust: `rangeRecord[K]`, `rangeFamily[K]`, `rangeCtx[K]`, and the
   mutation machinery (`range_edit`, `range_merge`, `range_locator`,
   `range_coverage`, `range_draft`, `range_bulk`, comparison, feed merge,
   import paths) parameterized by K with `K = uint32` for IPv4
   (12-byte records) and the IPv6 pair for IPv6 (36-byte records).
3. One authoritative tree core stays; no per-family core copies. The
   core's public shape (`Codec[T]`, gap/insert/delete/read/cursor/walk)
   keeps its function signatures; only the value representation of `Key`
   changes inside.
4. The reader (0 `tree.Key` uses), the on-disk layout, the codec cell
   encodings, and the public surfaces are unchanged; validation, retire,
   and the public entry points mechanically adapt to the compact key.

Validation plan (recorded before implementation):

- Every slice commit: `nice go -C v4/go test ./...`, `go vet`,
  `gofmt -l`, the v4work suite, race/checkptr on the final slice, and the
  cross-open conformance battery.
- Re-measure the matched Rust-ratio evidence with the final release
  binaries (same host, both sides): rerun the 18-case CI gate and refresh
  the committed evidence CSVs. Targets: read paths ~1.2-1.6x Rust,
  write paths ~2-3.5x, validation ~1.5-2x (the accepted milestone-4
  mandate).
- Five-reviewer level-1 round on the delta (scopes: Rust parity, Go
  idioms, performance, wire/integrity, APIs/records), then SOW-0027
  milestone-4 acceptance and SOW close.

Sub-state (2026-08-28): extension-B slices B1 and B2 delivered.
B1 (7f34d161) compacted tree.Key to 48-byte canonical compare bytes; B2
(affacd57 + 17914dbc) made the range writer family-typed (12/36-byte
rangeRecord[K], no universal key and no per-record interface dispatch on
the hot paths), cached the fixed-key numeric limbs (17914dbc), and
restored the draft-private single-descent assign gap path so the private
assign arms match Rust assign_private. Validation: gofmt/vet clean,
plain + v4work + race/checkptr suites green, 18-case CI gate 18/18
within-limit (ci-go-v4-local-20260828d.log, ratios 0.395-1.071;
workflow 3,465 ms vs accepted 5,058 ms = 0.685). Fresh matched 5-sample
Go/Rust ratios (rust-ratio-acceptance-20260828b.csv): membership-import
1.648 (was 3.506), nested-overwrite 4.152 (was 6.655),
update-ipsets-workflow 3.141 (was 4.434), live-direct-random-lookup
1.872, immutable-direct-random-lookup 1.850, live-validation 2.560.
B3 (uncommitted in the working tree at this point; committed with the
next evidence refresh) applied the sol-review verified fixes: the tree
fixed search validates the page shape once and probes through the
Rust-style index-bounded cell view (no per-probe index re-checks), tree
Key ordering compares numeric keys through their cached limbs, and the
range validation leaf ordering works in the family key space (no
per-record general-key materialization). Measured effects (interleaved
matched samples): validation ratio 2.56 -> 2.18, membership-import
1.65 -> 1.62, reads 1.85-1.87 -> 1.96-2.00 (run noise band), write
scenarios unchanged within noise. Sol review verdict: NEEDS CHANGES;
verified P1/P2 findings remaining: (1) the writer still crosses the
tree-core boundary through per-op tree.Key conversions and the gap
machinery dispatches through the frozen interface-shaped signatures
(nested-overwrite 4.15-4.41x; fixing requires a design change to the
frozen core), (2) the reader search still dispatches through closures
per probe and re-checks slot geometry (lookups 1.85-2.0x; reader scope,
outside extension B), (3) the update-ipsets workflow still allocates
10.7M objects (reader algebra: selectedRanges.next 5.5M, sort.Swapper
1.2M, snapshot.membershipWords 1.0M; outside extension B).

User decision (2026-08-28, "1B"): continue optimization to maximum
performance within the current milestone. Two reviewed slices: C1 =
reader + workflow (closure-free typed read probes with once-validated
page views; eliminate the top workflow allocation sources
selectedRanges.next / sort.Slice reflection swapper /
snapshot.membershipWords), C2 = tree-core typed fixed-key entry points
(a deliberate refinement of the frozen core design: the core keeps one
authoritative implementation and its Codec/T/cycle semantics, but its
entry points accept the family-typed fixed key instead of constructing
tree.Key per operation, and the gap/replace machinery dispatches
without the per-record interface hops). C2 explicitly supersedes the
2026-08-28 design item 3 "the core's public shape keeps its function
signatures" for fixed-key codecs, to the extent needed to remove the
per-op key construction and interface dispatch; wire format, public
APIs, work counters, and corruption semantics stay identical. After C1
and C2: 5-reviewer level-1 round, milestone-4 acceptance, SOW close.
C1 delivered (pushed as part of this status write): the reader range
searches are closure-free family-typed probes over the shared
once-validated format.FixedSearch (lookups 1.96-2.00 -> 1.82-1.86),
the 4c selected-runs scan aliasing defect was found by a new coverage
regression test and fixed with value semantics (5.5M -> 0 per-run heap
objects), the algebra boundary position no longer escapes per event
(1.8M objects), the output sort uses slices.Sort instead of the
reflection swapper (1.2M objects), and the snapshot copy streams
membership words through the writer word-source seam without per-record
materialization (1.9M objects). The update-ipsets workflow allocation
count dropped from 10,727,434 to 27,414 objects (369MB to 9.8MB) per
the ci-f gate row (27,409 / 9.8MB at ci-g), with wall-time ratios
unchanged within noise (3.21x) because the tiny-object allocations were
not the CPU bottleneck; the write-path ratio remains nested-overwrite
~4.45x pending the C2 typed tree core.
C2 delivered (pushed with this status): the tree replacement validation
runs through a numeric limb seam (NumericKeyCodec.ReadKeyLimbs on the
range codecs; tree.RequireReplacement no longer materializes a general
tree key per cell). Interleaved head-to-head on the same window:
nested-overwrite 1334ms -> 1247ms (-6.5%), final matched ratio 4.184
(was 4.451). The remaining write-path gap is the generic gap/replace
machinery dispatching through interfaces per probe (codec.ReadLeaf +
LocalGap + rangeFamily calls); Go generics compile to dictionary-based
dispatch and cannot monomorphize these calls statically, so closing the
last ~20% requires either duplicating the tree gap machinery per family
(a trade-off against the one-authoritative-core principle) or code
generation. Recorded as an open decision for the milestone close.

Sub-state (2026-08-28): five-reviewer level-1 round on the B/C delta
(7f34d161..ff0c4fe5) complete. Verdicts: Rust parity PASS,
Go idioms PASS, performance PASS, wire/integrity PASS,
APIs/records FAIL (two P2 record defects, both verified against the
committed files and fixed in this round). Verified fixes applied:

- P2-1 (APIs/records): the evidence README quoted wrong ratio ranges
  for the ci-e/f/g gate logs (0.682-1.072, 0.691-1.050, 0.407-1.050)
  while the committed logs measure 0.417-1.072, 0.525-1.210,
  0.467-1.012; the README entries now quote the measured ranges.
- P2-2 (APIs/records): the workflow allocation claim "27,406 objects
  (369MB to 138MB)" matched no committed artifact; the ci-f/ci-g rows
  show 27,414/27,409 objects and 9.8MB (not 138MB). The SOW and README
  now quote the committed gate rows.
- P3 (performance, verified): the tree fixed probe called the
  non-inlinable fixedCellAt wrapper per probe; fixedLowerBound now
  calls format.FixedSearch.Cell directly (one call layer removed,
  identical behavior and counters).
- P3 (verified): dead havePrevious flag removed from
  requireReplacementLimbs (the index==0 branch already guards the first
  cell; the 2-3 cell length check makes the flag always true).
- P3 (verified): tree.Key.U32 restored its documented leading-32-bits
  semantics for wider keys (uint32(k.hi >> 32), matching the pre-B2
  beU32(data[:4]) contract; no current caller uses wider keys, the doc
  is now accurate).
- P3 (verified): snapshot copyStructuredV4/V6 no longer shadow the
  immutable-reader source parameter with the membership word source.
- P3 (verified): collectHistoryWindows and assembleOutputBuilder now
  allocate only the address-family-selected runs slice and range bulk
  builder instead of both siblings.
- P3-1 (APIs/records, false positive): the "was" pre-B ratios
  (3.506/6.655/4.434) do match the committed
  rust-ratio-acceptance-20260828.csv table (the 4c/4d state); the
  README now names that table as their source.

Re-validation after the fixes: gofmt/vet clean, plain + v4work suites
green, full race/checkptr suite green, conformance cross-open 13/13
plus invalid-mutation cases pass. Gate ci-go-v4-local-20260828h.log:
18/18 within-limit, ratios 0.416-0.993, workflow 3,446 ms (ratio
0.681), 27,406 objects / 9.8MB (the six P3 fixes removed 8 objects
against the ci-f row). Fresh interleaved matched ratios
(rust-ratio-acceptance-20260828f.csv):
membership-import 1.636 (was 1.666), nested-overwrite 4.044 (was
4.184), update-ipsets-workflow 3.209 (was 3.231),
live-direct-random-lookup 1.835 (was 1.863),
immutable-direct-random-lookup 1.830 (was 1.863), live-validation
2.235 (was 2.301). No case regressed; nested-overwrite and validation
improved in the same interleaved window.

Recorded opportunities (not implemented in this round; all P3,
non-blocking, verified): (1) range_bulk finishLevel returns an untyped
any with a caller type-switch; a typed (node, isRoot, err) return would
remove the switch (Go-idiom polish, moderate churn in the bulk
builder); (2) the locator/bulk/history concrete-type family switches
could invert into a Family()/DecodeKey interface on rangeFamily
(Go-idiom polish across four files); (3) ValidateProbeCell still copies
the 32-byte hash digest that the probe discards and the ProbeValidator
method value is an indirect call per probe on membership/structure
trees (micro-optimization on the hash-probe path only, deferred to
avoid touching parity-verified validation code at the gate);
(4) tree.Key grew 16 bytes with the limb cache; all hot-path copies
stay on the stack, no heap escape, net win from B1 already measured.

Open decision for the milestone close (user decision requested): the
nested-overwrite residual is 4.044x Rust after all slices and the
review fixes. Closing the last ~20% requires per-family duplication of
the tree gap/replace machinery (against the one-authoritative-core
principle) or code generation; both contradict the standing working
principles, so the recommendation is to accept the current ratios and
record the trade-off as a follow-up. The user is deciding between
accept (recommended), per-family duplication now, or codegen.



Sub-state (2026-08-26): activated by user decision (Open Decision 1, option A);
SOW-0017 is paused as blocked on this SOW's prerequisite. The static gap
analysis at repository revision `07c0147bf18b4d1e976f460fc2624e90e91be8cc` was
independently re-verified by the lead against the code and the normative specs
(2026-08-26); every material claim was confirmed, including the off-contract
public writer, the direct-only live writer facade, the missing operations
(membership import, one-inode immutable feed, commit resolution, bounded
reclaim, clean-writer metadata read-your-writes), the omission of cancellation
and live join sources, the error/nil/enum contract defects, the in-process
non-Linux worker routing, the private Windows housekeeping, the asymmetric
fixture producers, the same-language immutable-only subprocess tests, the
missing benchmark matrix, and the incorrect SOW-0025 acceptance record.
Implementation starts with the parity ledger freeze (plan step 1).

Sub-state (2026-08-26): milestone 1 (parity ledger freeze) delivered at
`bff1f550`. Milestone 2 slice 2a (live-writer consolidation) is delivered at
`11ff5848` and its five-reviewer gate closed at `6853e4fd` (all five
reviewers PASS on round 3; rounds 1-2 findings all fixed and recorded below): all advanced transactions and
high-level workflows bind to the sidecar-bound `LiveWriter` through the
shared `mutationHost` (core owner, open/healthy probe, abort, discard, and
commit terminals); the live public surface is `LiveWriter.BeginDirect`,
`BeginMembershipTransaction`, `BeginStructuredTransaction`,
`BeginCreateFeed`, `BeginReplaceFeed`, `BeginDirectReplacement`,
`BeginFirstSeenRefresh`, `BeginLastSeenRefresh`, `ProjectHistory`,
`RenameFeed`, `DeleteFeed`, `Abort`, and `Close` with the captured
cancellation carried through every begin, mutation, metadata stage, commit,
and cleanup checkpoint; the `FinishedHistoryProjection` commit path is
unified through the host terminal (reader-table gate on the live path); the
end-to-end live test chain (CreateLive -> OpenLiveWriter -> BeginCreateFeed
-> FinishInput -> Commit -> SnapshotTo -> OpenImmutable) plus live
lifecycle, direct workflow, structured transaction, and live-source history
projection tests pass. The parity ledger flipped the implemented rows from
missing to present (68 present/remove-planned, 18 missing) and the gate test
now closes both the `Writer` and `LiveWriter` method surfaces. The full
battery (plain, v4work, vet, gofmt, race+checkptr) passes at the slice
commits.

Review round 1 (five reviewers: Rust parity, Go idioms, performance,
wire/integrity, APIs/records) found and the lead fixed:

- P1 (Rust parity, F1): `LiveDirectTransaction.Commit` passed a nil
  checkpoint; the captured cancellation token now checkpoints the commit
  attempt, the prepare-and-lock sequence, and the publication loop (Rust
  DirectTransaction::commit -> commit_operation parity).
- P2 (Rust parity, F2): the live direct range ops lacked the Rust
  run_transaction post-mutation checkpoint and the metadata stages lacked
  both checkpoints; pre+post checkpoints are now applied to
  AssignV4/AssignV6/ClearV4/ClearV6/SetMetadataJSON/ClearMetadataJSON.
- P2 (APIs/records): the parity ledger omitted the
  `LiveWriter.BeginDirectReplacement` row and had no tripwire for
  unrecorded `LiveWriter` methods; the row is added and the gate now closes
  both method surfaces (it also caught and recorded `LiveWriter.Abort`).
- P2 (APIs/records): the status record still claimed slice 2a was "under
  way at working tree `bff1f550+`" while the slice was delivered; rewritten
  to present state here.
- P3 (APIs/records): the structured begin probed healthy before the Rust
  structure-kind gate and its doc claimed cancellation-first; the shared
  helper now runs closed-probe, structure-kind, cancellation, healthy,
  value-kind (Rust begin_structured_transaction order) and the doc matches
  both hosts.
- P3 (wire/integrity): a stale Rust test-file citation in
  `live_workflow_public_test.go` was replaced with the real Rust suite name.
- P3 (Go idioms): dangling doc fragment, double coreOf probe, and
  plan-narrative sentences fixed in the slice commits.

Four outcomes remain recorded as accepted Go-shape divergences
(documented in code, not yet resolved SOW decisions): workflow
`CommitResult`/`AbortResult` terminals carry fewer fields than Rust
(workflow commits omit directory/main identity; workflow abort returns
`error`); mixed-language live fixtures stay a recorded slice-2c gap; and
the Windows cleanup-result classification (recorded 2026-08-27 during
the slice 2d native round): Go `EarlyDiscard.Clean` and
`ScratchCleanup.Clean` (`internal/worker/wire_cleanup.go`) accept
`HousekeepingCrashReappearancePossible` as clean when every artifact is
proved absent, where the Rust client predicates
(`worker/client/recovery.rs` discard_clean / scratch_clean) require
`Housekeeping::None` strictly. The divergence is Windows-only (the
POSIX discard and scratch arms emit None on both hosts) and exists
because the Rust Windows machine is self-inconsistent: Rust
`resolver::finish_housekeeping` itself emits the crash-possible class
on a fully absent removal, which the strict Rust client predicates
would then reject, turning every clean Windows discard into a failure;
Rust Windows has never been natively validated (the Rust client suite
is cfg(all(test, unix))). Go implements the proved-absence terminal so
the Windows recovery and cleanup machinery is usable; whether the Rust
client contract should be relaxed on Windows is a cross-SDK decision
for the user, not silently adopted here; and the Windows
worker-control unlink/reopen shape (recorded 2026-08-27 in the slice 2d
gate round): Go unlinks the worker control on every
platform (Rust `remove_path` is `#[cfg(unix)]` at both the call site,
`worker/client.rs`, and the definition, `worker/control.rs`, so Rust
Windows leaves the control file behind), and Go therefore reopens the
control with FILE_SHARE_DELETE in `openControlFile`
(`worker/control_open_windows.go`), where Rust `open_worker`
(`worker/control.rs`) uses stdlib `OpenOptions`, whose read+write-only
share mode would make the Go unlink fail with a sharing violation. Go
unlinks so Windows temp-dir controls do not accumulate per spawn;
whether Rust should adopt the same removal semantics on Windows is a
cross-SDK decision for the user, not silently adopted here.
And the public identity-kind encoding (recorded 2026-08-27 in the
milestone-3 gate round): the Go public `FileIdentity` is the portable
kind+bytes shape matching Rust `LocalFileIdentity`, but the Go live
surface hard-codes kind 1 where Rust emits the platform
`IDENTITY_KIND` (2 on Windows); Go is self-consistent (decode ignores
the kind) and the cross-language battery is linux/amd64-only, so no
host failure exists; a Windows cross-SDK fact exchange would see kind 1
vs Rust's 2 - recorded as an observation in the same family as the
commit-result-terminal divergence, remedy deferred to the user.

Sub-state (2026-08-26): slice 2b part 1 delivered at `532fa582`
(clean-writer metadata read-your-writes and bounded reader-safe
reclamation; gate pending at `7f627c6f`). Slice 2b part 2 (commit
resolution) delivered (working tree `7f627c6f+`, gate pending):
`ResolveCommit` with the `CommitResolutionMode` / `LocalFileRelation` /
`CommitResolution` / `CommitResolutionResult` public types (Rust
commit_resolution.rs parity) proves one exact attempted transaction
and nonce against both meta pages without validating either page
graph, in Live mode (shared main lifetime lock, ready reader-table
gate of the attempted database, writer-lease claim, slot scan,
classify-sync-classify stability proof, tail trim) and Immutable mode
(sidecar-absent, any-link identity rule); the bootstrap authority
gained `ResolveCommitAttempt` (exact two-meta transaction/nonce
classification) and the mapping owner gained the file-level
`ShrinkFileOrRetain` tail primitive; the Rust commit_lifecycle tests
are ported (live resolution facts, WriterBusy while the lease is
held, same/different local-file relation, superseded-unknown old
attempts, tail-only removal, invalid-attempt refusal). The parity
ledger flipped the commit-resolution row to present (73
present/remove-planned, 13 missing) and the gate test passes. Full
battery green (plain, v4work, vet, gofmt, race+checkptr) at the slice
commit. Slice 2b part 3 (live join sources) delivered (working tree
`9f0a9c93+`, gate pending): `DirectJoinSource` with the
`DirectJoinSourceImmutable` / `DirectJoinSourceLive` constructors
(Rust DirectJoinSource parity) lets `MembershipScope.JoinDirect` merge
a membership scope with the pinned generation of a live direct
provider; the variant resolution proves the live reader's open state
before any page is touched (Rust Source::new over LiveReader::core),
the zero source is refused as InvalidArgument, and the live-provider
test pins the exact report facts, result cells, closed-reader
WrongState, wrong-kind, and empty-source arms. The parity ledger
flipped the join row to present (74 present/remove-planned, 12
missing) and the gate test passes. Full battery green at the slice
commit. Slice 2b part 4 (membership import) delivered (working tree
`9f0a9c93+`, gate pending): `LiveWriter.BeginMembershipImport` with the
`MembershipImportSource` / `MembershipImportSourceImmutable` /
`MembershipImportSourceLive` constructors and the `MembershipImport`
handle (Rust live_writer/membership_import.rs parity) imports a
complete name-based membership database from an explicitly pinned live
or immutable reader into the preserved destination: the source
resolution and compatibility proof (membership kind, same family and
value tag, a different local file via the live writer's main identity),
the catalog sweep re-proving every source feed by name, the v4/v6
range sweeps with the last-translation fast path, the import cache
(the draft-private fixed tree with page types 240/241 and the aux
discriminator, Rust draft_store/import_cache.rs), the ordered
preserve-without-input union through `begin_import_merge` carrying the
already-translated membership pair in the merge record exactly like
Rust `Incoming<TranslatedMembership>` (draft_store/import_merge.rs
parity: the merge never re-resolves a translation - the ordered merge
input became generic over the policy's value type like the Rust
`OrderedMerge<K, V, P>`, with history/timestamp inputs staying u32),
the exact six-way classification
report, the empty-feed catalog change, and the full IPv6 space with
the exact 2^128 cardinality. All abort arms are atomic and leave the
writer reusable: cancellation, same-file InvalidArgument, incompatible
family/tag/kind, zero-budget TransactionAborted wrapping
InsufficientResourceBudget, and corrupted-source TransactionAborted
wrapping FormatInvalid (the reader cursor now maps every header
problem to the Corrupt class exactly like Rust slotted_page
parse_tree). The parity ledger flipped both membership-import rows to
present (76 present/remove-planned, 10 missing) and the gate test
passes; the full battery (plain, v4work, vet, gofmt, race,
checkptr=2, mmap trace, Rust membership_import authority) is green at
this state. With this commit slice 2b (live workflows) is complete;
slice 2c (remove/privatize the off-contract `Create`/`OpenWriter`/
`Writer` surface and regenerate fixtures through the live path)
follows.

Sub-state (2026-08-26): slice 2c (remove the off-contract writer surface)
delivered (working tree `5e458c37+`, gate pending): the public `Create`,
`OpenWriter`, and `Writer` types and every `Writer.*` method (direct, feed,
membership, and structured transactions; direct-replacement and first/last-
seen workflows; create/replace/rename/delete feed; project history; abort;
close; info) are removed; every operation's sole owner is the sidecar-bound
`LiveWriter` or `LiveDirectTransaction`. All warm-path, workflow, zero-alloc,
lifecycle, and subprocess/cross-open suites were migrated to
`CreateLive` + `OpenLiveWriter` (+ `OpenLiveReader` for read-backs and
`SnapshotTo` with `SnapshotSourceLive`, `PolicyFailIfExists`, and the proven
budget for publish), and the parity ledger flipped all 17 `remove-planned`
off-contract rows to `missing` (59 present, 27 missing); the gate now also
enforces that no public `Writer` symbol exists. The five Go conformance
fixtures are regenerated through the live writer path and re-verified by the
staging subprocess conformance and the Rust conformance suite. The live
projection-commit allocation pin was recalibrated to 400 with both measured
floors recorded (plain 233, race+checkptr 375); the abort pin stays 100. Full
battery green at this state: plain and v4work Go suites, vet, gofmt,
race+checkptr, cross-builds (windows/darwin/freebsd), mmap trace gate, Rust
source graph, full Rust livedb/capi suites, and Rust conformance on the
regenerated fixtures. Slice 2c is complete; slice 2d (platform boundary)
follows.

Sub-state (2026-08-26): slice 2d (platform boundary) part 1 delivered
(working tree `8f20e367+`, gate pending): the Windows housekeeping
surface is public (`ListWindowsHousekeeping` / `RemoveWindowsHousekeeping`
with `WindowsHousekeepingCandidateKind` / `WindowsHousekeepingEntry` /
`WindowsHousekeepingList` / `WindowsHousekeepingPayloadIdentity` /
`WindowsHousekeepingRemoval`, Rust maintenance.rs parity): the internal
live GC machine is wired through exported internal/publication arms with
the Rust control surface (sink Stop -> StoppedBySink, sink error ->
SinkFailed; cancellation-first at every entry like the rest of the
maintenance surface), the non-Windows arms refuse with the exported
OS-unsupported class, and the parity ledger flips both housekeeping rows
to present. The macOS creator-only machine is implemented in pure Go
(internal/security acl_darwin.go + acl_darwin_algo.go): the darwin arm
calls the raw fchmod_extended (283) and fstat_extended (281) syscalls the
libc filesec/fchmodx_np/acl_get_fd_np wrappers use (public XNU
syscalls.master/kauth.h evidence; no cgo), with the Rust apple.rs outcome
classes; the classification is unit-tested on every host and the syscall
glue cross-compiled for darwin amd64/arm64. `CreatorOnlySupported` now
reports true on darwin so the live and publication platform gates open
there; the ledger platform row keeps `missing` until the authorized
native macOS round proves the arm (the remaining slice 2d item is the
version-matched worker on every supported OS/architecture, which needs a
user decision because the POSIX SIGBUS containment machine is
assembly-based on linux/amd64 and each new target requires a new raw
trampoline or another native mechanism). Full battery green at this
state.

Sub-state (2026-08-27): slice 2d (platform boundary) part 2 - the portable
worker - delivered (working tree `1ac7dcdf+`, gate pending): every worker
file is retagged to the four supported OSes (`linux || darwin || freebsd ||
windows`); the package and cmd platform negations are updated to
`!linux && !darwin && !freebsd && !windows`. The mapped-control atomics
became per-target raw assembly (`atomic_{os}_{arch}.s`: plain aligned
32-bit ops on amd64, LDAR/STLR acquire/release plus LDAXR/STLXR CAS on
arm64) so the parent and the naked handler share one authority on every
target. Process reaping is portable (one-shot reaper goroutine + buffered
channel replacing wait4/GetExitCodeProcess), and the worker executable
candidates gained the darwin/freebsd `.exe`-suffix arm (exe_posix.go /
exe_windows.go).

The POSIX SIGBUS containment machine is now implemented on all six POSIX
targets with raw per-ABI syscalls in the naked handler, mirroring posix.rs
exactly: linux amd64 (existing) plus new linux arm64 (SVC, x8, generic
numbers; SA_RESTORER stub), darwin amd64/arm64 (SYSCALL plain numbers /
SVC #0x80 with the number in X16 - the macOS ABI proven on the M4 host;
4-byte sigset, si_addr@24, SA flags 0x40/0x1/0x10/0x4, SS_DISABLE 0x4, no
SA_RESTORER - the kernel sigtramp returns), and freebsd amd64/arm64 (SVC,
x8, FreeBSD 14 numbers 416/417/340/341/53; 128-bit sigset, sigaction
layout handler@0 flags@8 mask@12, si_addr@24, BSD SA flags). The Go-side
install surface (sigaltstack, sigaction query/set, handler address) is a
per-OS twin of the linux machine with the ABI-verified struct layouts
(macOS sigaction is 16 bytes with a 4-byte mask; FreeBSD sigaction is 32
bytes with a 16-byte mask at offset 12; both stack_t are sp@0 size@8
flags@16). Windows uses the pure-Go VEH machine (kernel32
AddVectoredExceptionHandler via system DLL, EXCEPTION_IN_PAGE_ERROR with
the documented parameter layout, same control record, TerminateProcess
with ownedFaultExit). The Linux/Windows amd64+arm64 and darwin/freebsd
amd64+arm64 worker binaries all cross-build; `go vet` passes on all eight
targets including the test files. The handler subprocess proofs
(owned-fault record + runtime chaining) are portable (sigbus_posix_test.go
for linux/darwin/freebsd, sigbus_windows_test.go for the VEH path); the
15-case previous-disposition matrix stays linux/amd64 v4work (its naked
symbols are linux/amd64-specific by design). Full battery green at this
state: plain and v4work Go suites, vet, gofmt, race+checkptr, eight-target
cross-builds and vets, mmap trace gate, Rust conformance on the generated
fixtures. Native rounds on the three authorized hosts follow this commit;
their results and any fixups are recorded here before the slice gate.
Evidence posture for the remaining targets: linux/amd64 is proven by the
committed suite; darwin/arm64, freebsd/amd64, and windows/amd64 are
proven by the authorized native rounds; linux/arm64, darwin/amd64,
freebsd/arm64, and windows/arm64 are cross-compile and vet proven only
(the authorized darwin host is arm64 and has no Rosetta, and no
authorized hosts exist for the remaining three; the darwin amd64 raw
`syscall` convention is the same classless ABI the macOS kernel accepts
from libc `syscall()`).

Sub-state (2026-08-27): slice 2d native round delivered (commits
`76765f21`, `87140582`, `42bee736`, `c162ad34`, `b444712b`, plus the
signed slice commit recorded at the end of this entry; gate pending the
five-reviewer round): the authorized native rounds on the approved hosts
(Option 1: costa-win11 windows/amd64 Go 1.26.5, plakam4mini darwin/arm64
Go 1.26.3, freebsd/amd64) are complete and green, with the following
correctness defects the rounds exposed and the lead fixed:

- Darwin SIGBUS delivery: the kernel stores the installed sigaction's
  `sa_tramp` and jumps every real-handler delivery through it (sendsig
  sets PC to SIGTRAMP), so zero/garbage tramp was fatal at delivery. The
  darwin install/restore arms now supply a 24-byte `__sigaction` input
  (handler@0, tramp@8, mask@16, flags@20; output 16 bytes handler@0
  mask@8 flags@12) with the worker's own sigtramp that calls the catcher
  and sigreturns the interrupted context; proven on the M4.
- Windows worker control sharing: Go unlinks the worker control on every
  platform (Rust's `remove_path` is `#[cfg(unix)]`, so this is a Go-side
  cleanup obligation), and Windows DeleteFileW therefore requires every
  later handle to advertise DELETE sharing while the creator-only
  FILE_ALL_ACCESS handle exists; `OpenWorker` now reopens the control
  through the platform arm (`openControlFile`) with
  read/write/delete share, and the executable-candidate fallthrough also
  accepts `exec.ErrNotFound` (CreateProcess ERROR_FILE_NOT_FOUND).
- Windows delete-blockers in tests: the cmd fixture leaked a plain
  `os.Open` observation handle (no FILE_SHARE_DELETE) that blocked
  TempDir removal, and the import-precondition test leaked a live writer
  (Rust drops it; the Go port now closes it) whose mapped main file
  blocked deletion with ACCESS_DENIED.
- Windows GC/wire kinds: the worker cleanup codecs validated the
  checkpoint security against the hardcoded unix kind (1), rejecting
  the Windows kind-2 wire facts with FormatInvalid; the kinds are now
  platform arms (kind_posix.go / kind_windows.go), the scratch-checkpoint
  fixture creates the artifact through the platform creator-only
  authority (the Windows GC observe proof compares the live
  creator-only commitment, so a bare OS file classified as a
  names-or-identities conflict), and the retained-residue fold uses the
  platform basename-encoding kind.
- Windows fault-record validation: the parent fault-record check
  hardcoded the POSIX rule (si_code > 0); Windows NTSTATUS codes are
  negative int32, so the check is now the Rust platform split
  (unix `code > 0`, windows `code != 0`).
- Windows amd64 mapped-CAS assembly: `mapAtomicCas32` loaded `old` into
  EDX while CMPXCHG compares EAX (left holding the base pointer), so the
  CAS always failed and the vectored handler never claimed an owned
  fault; all four amd64 atomic files were corrected (old into EAX, new
  into EDX, base into SI). The arm64 LDAR/STLXR arms were already
  correct.
- Windows in-page fault arming: hardware EXCEPTION_IN_PAGE_ERROR cannot
  be armed deterministically on Windows (CreateFileMapping refuses a
  maximum size above the file extent with ERROR_COMMITMENT_LIMIT,
  MapViewOfFile cannot exceed the section, and SetEndOfFile refuses
  while a section is open with ERROR_USER_MAPPED_FILE), so the Windows
  subprocess proofs deliver the in-page error synthetically
  (kernel32 RaiseException with the documented parameter layout:
  access type, the accessed page-2 address inside the armed region, and
  the NTSTATUS), exercising the real vectored dispatch, the real
  EXCEPTION_POINTERS record, the record write, and the owned-fault
  termination. The real-binary source-fault fixture truncates the
  source under the worker's open mapping, which Windows refuses by
  design, so that fixture stays POSIX-only exactly like the Rust
  authority (client_tests.rs is cfg(all(test, unix))); Windows proves
  the same record machinery through the synthetic subprocess proofs and
  the declared-page restart through the real-binary refusal test.
- Windows cleanup terminal: the POSIX discard arms always produce
  HousekeepingNone, while the Windows GC machine classifies a fully
  absent removal as CrashReappearancePossible (Rust
  resolver::finish_housekeeping does the same, because Windows
  unlinking is not power-loss durable); `EarlyDiscard.Clean()` and
  `ScratchCleanup.Clean()` now accept the crash-possible terminal when
  every residue and visible artifact is absent, which is the proved-
  absence class. This unblocked the checkpointed-scratch, real-binary
  discard, and fault-retry terminals on Windows.
- FreeBSD race zeroalloc: the FreeBSD -race runtime bills its
  shadow-arena growth through the Go GC (exactly 31 x 4KiB Go-heap
  blocks per publish/count iteration, deterministic on Go 1.26.5), so
  the publish-set page-copy evidence window skips under
  `raceEnabled && GOOS == freebsd`, with the plain FreeBSD build
  proving the SDK path; the same window stays green under race on
  linux and darwin.
- Windows alloc ceiling: the ProjectHistory commit floor is 407
  objects per run on windows/amd64 Go 1.26.5 (vs 233 on linux/amd64),
  so the pinned ceiling moved 400 -> 450 with the platform floors
  recorded; no per-record or per-insert allocation was introduced.
- Windows wire vectors: the worker wire is same-host and same-OS and
  the Rust wire kinds are platform constants (unix.rs 1, windows.rs 2),
  so the Rust-produced vector fixtures are now a per-OS pair; the
  Windows pair was generated on the authorized host with the unchanged
  Rust generator (its POSIX output verified byte-identical to the
  committed POSIX vectors first).

Native proof record at the slice commit: linux/amd64 full battery
(plain, v4work, vet, gofmt, race+checkptr) green; darwin/arm64 (M4,
Go 1.26.3) plain, v4work, and race+checkptr green; freebsd/amd64 plain,
v4work, and race+checkptr green; windows/amd64 (Go 1.26.5) plain,
v4work, and vet green, with race+checkptr on the Windows host blocked by
the host's broken mingw toolchain (mingw-w64-x86_64-gcc 16.1.0-5 exits 1
silently on trivial inputs; no clang present; Go -race on Windows needs a
working cgo CC; the code-level race/checkptr gate remains the committed
Linux battery). Rust suite green at the same tree; all eight targets
cross-build and vet clean including the final tree.

Review gate round 2 (2026-08-27, HEAD `82e022f1`): gate stayed open.
Verdicts: Go-idioms PASS (one P3 record-truth note: the worker row in
`parity_manifest.tsv` still carried the stale `linux/amd64-only`
clause, contradicting the round's own comment fixes); wire/integrity
PASS (P3-2 vector provenance carried); APIs/records PASS (round-1 P2-1
cleanup classification now recorded, comments restored); performance
FAIL with P2-1: the Windows VEH callback first-ever fault executed
`LazyProc.Call` for GetCurrentProcess/TerminateProcess, which inside
`golang.org/x/sys v0.35.0` takes the per-proc mutex, allocates, and
calls GetProcAddress on first resolve - contradicting the handler's
own allocation-free invariant and the Rust import-table authority (the
handler is only ever run once per process, on the terminal fault);
Rust-parity FAIL with P2-1: the Windows worker-control unlink/reopen
is the fourth accepted Go-shape divergence (recorded above) and the
`control_open_windows.go` / `control.go` parity comments omitted the
Rust `#[cfg(unix)]` split, plus P3-1 that the fixes-narrative should
name the unlink as the divergence driver. The fix commit resolves all
of them: `InstallHandler` now pre-resolves the two kernel32 addresses
in ordinary context and the callback terminates through pre-resolved
`syscall.SyscallN` addresses only (runtime-mediated, verified
allocation-, lock-, and panic-free on this path); the control comments
and the divergence
enumeration now disclose the Rust `cfg(unix)` split; the manifest row
lost the stale clause; the narrative names the unlink.

Review gate round 3 (2026-08-27, HEAD `cf07eff2`): all five reviewers
PASS on the same HEAD; slice 2d gate closed. The round-2 P2-1s and
record-truth notes are verified fixed: the Windows VEH callback now
terminates through pre-resolved kernel32 addresses only (performance
PASS; GetCurrentProcess retained by decision to mirror the Rust
authority, pseudo-handle substitution explicitly not required); the
Windows worker-control unlink/reopen is enumerated as the fourth
accepted divergence with the Rust `#[cfg(unix)]` split disclosed in
code comments and narrative (Rust-parity PASS); the manifest worker
row lost the stale clause (Go-idioms PASS); the wire surface is
untouched (wire/integrity PASS, P3-2 vector provenance carried); the
records verify against both trees (APIs/records PASS). Carried
non-blocking P3 notes, no failure scenario: alloc-ceiling sensitivity
at 450 vs the linux floor 233 (performance P3-1, a per-OS ceiling
would restore sensitivity), vector provenance resting on the recorded
generator process (wire P3-2), and the runtime-mediated SyscallN path
as the absolute terminal floor (performance P3-3, a naked-asm stub
would only remove the remaining cgocall entry, not required).

Sub-state (2026-08-27): milestone 3 (symmetric conformance proof, plan
step 6) started after slice 2d's gate closed. Slice plan:
- 3a symmetric corpus: Go-produced membership-ipv4 and membership-ipv6
  fixtures through the public `BeginMembershipTransaction` workflows
  (mirroring generate.rs membership_ipv4/membership_ipv6 op sequences
  exactly: 70 feeds with delete + feed-index reuse on IPv4, global +
  special feeds across the whole IPv6 space with the 1 MiB repeated-
  byte metadata on IPv6), published through the existing public live
  `SnapshotTo` path (regenPublish), added to cases.json and both
  reader inventories; both readers re-verify the extended corpus.
- 3b mixed live cooperation: cross-language subprocess tests cooperate
  on the same live database in both directions (Go parent spawns the
  Rust test binary, Rust parent spawns the Go test binary; children are
  env-gated entries of the other language's test binary, built at test
  time - `cargo test --no-run` / `go test -c` - exactly like the
  same-language subprocess pattern and the explicit regeneration
  entry). Coverage maps the acceptance list: registration/release,
  writer exclusion, reclamation, stale slots, sidecar replacement,
  transition/reservation states, publication inspection, and commit
  resolution. Gate: linux/amd64 with both toolchains available; runs
  documented in the conformance README (explicit env, like fixture
  regeneration) so plain suites stay fast.
- 3c commit-resolution + snapshot cross proofs (same-transaction /
  different-nonce attempts cross-language, compact-snapshot cross-open)
  and README/records updates (the v4/conformance README currently
  carries stale producer-path wording; the Go fixture publisher has
  been SnapshotTo-based since slice 2a).

Sub-state (2026-08-27): milestone 3 slice 3b (mixed live cooperation)
delivered in this commit, working tree green both directions. Child
entries and parents follow the same-language subprocess pattern: the Go
parent builds the Rust `mixed_live` test binary with incremental
`cargo test --no-run` and spawns `mixed_live_rust_child`; the Rust
parent builds the Go test binary with incremental `go test -c` and
spawns `TestMixedLiveGoChild` (cwd set to the Go module root because
the Go TestMain harness resolves the module from the working
directory). Each direction covers: generation read-back incl. the
generation-2 residue (publication inspection + sidecar replacement
across two commits), writer exclusion while the other language holds
the writer (Rust `Error::WriterBusy` / Go `ErrorWriterBusy`), and
pinned-read reclamation (the holding side's `Reclaim` returns NoChange
while the foreign-language reader is pinned and reclaims after its
release - stale-slot release and oldest-reader safety across
languages). The protocol is stdin-held for the pinned reader with a
READY line, 90 s deadline, unique temp paths, and full cleanup; the
battery is env-gated (`IPRANGE_V4_MIXED_LIVE=1`, linux/amd64-only,
both toolchains, documented in the conformance README) so plain suites
stay green and fast without the other toolchain. Plain Go (incl.
v4work) and Rust suites stay green at this state.

Observation recorded for the gate (APIs/records scope): the public live
open surface returns internal `*format.Error` values directly (e.g.
`OpenLiveWriter`), which external consumers cannot name or classify
through the exported `Error` type; the test convention `isPubCode`
works by matching the internal type from inside the module. Whether
the public surface must wrap live-opening failures into the exported
`Error` is deferred to the gate reviewers (acceptance criterion: public
failures must be accessible as the exported error type).

Sub-state (2026-08-27): milestone 3 (symmetric conformance proof, plan
step 6) delivered in this commit, slice by slice: 3a added the
Go-produced membership-ipv4/ipv6 fixtures through the public
membership transactions and the live SnapshotTo publish (13-fixture
corpus, both readers re-verify); 3b added the mixed live cooperation
battery in both directions (read-back, writer exclusion, pinned-read
reclamation); 3c added the cross-language commit-resolution attempt set
(committed / same-transaction different-nonce / superseded-unknown,
each SDK committing its own generation on the same live database and
classifying identically) and the compact-snapshot cross-open (each SDK
snapshots through its public snapshot path, the other reader opens and
verifies). Five modes per direction, all green with
IPRANGE_V4_MIXED_LIVE=1 on linux/amd64; plain suites stay green
without the env or the other toolchain. The public live-open error
shape observation recorded at 3b stays open for the gate reviewers.
Milestone 3 gate pending the five-reviewer round at this HEAD. Plan
steps 7-8 (matched update-ipsets release benchmark matrix; artifacts,
docs, and full-scope acceptance) remain after the gate.

Review gate round 1 for milestone 3 (2026-08-27, HEAD `58532bfc`):
gate stayed open. Verdicts: Rust parity PASS (generators and mixed
battery mirror generate.rs and the canonical live/commit tests
instruction-for-instruction; one P3 identity-kind observation recorded
above); Go idioms PASS (one P3 wording label for the IPv6 union range,
fixed in this commit); performance PASS (delta is test/corpus/records
only, no production hot-path surface); wire/integrity PASS (13-fixture
corpus verified by both readers, resolve classes and snapshot
cross-open proven in both directions; wording P3 fixed here).
APIs/records FAIL with P1-1: the public live surface (OpenLiveWriter,
LiveWriter facade, transactions, workflows) returned internal
`*format.Error` / `classedError` values raw, so external consumers
could not classify failures through the exported `Error` type - a
direct acceptance-criterion violation; plus P3-1 (README still said
eleven fixtures). The fix commit converts the seven live public files
through the existing `publicError` boundary (bare-error returns and
`&format.Error{...}` constructions become the exported `Error`), the
abort helpers return the public error, `publicError` preserves the
original error as `Cause` (classed wrappers keep their unwrap chain)
and passes through already-public errors, `isPubCode` classifies both
shapes, and `abortCauseCode` walks the public-classed-cause chain.
Gate pending the five-reviewer round 2 at the fix HEAD.

Review gate round 2 for milestone 3 (2026-08-27, HEAD `25ec4c84`):
gate stayed open. Verdicts: Rust parity FAIL P1 (the live result
surface still exposed internal error types: the `Abort()` incomplete
path returned `result.Cause` raw and `commitPrepared`'s `Err` plus
`publicCommitResult`/`publicAbortResult`/`publicCloseResult` `Cause`
fields carried unclassified `*format.Error`/`classedError` values,
`live_writer_public.go:219/245/727/738/749`, so external consumers
still could not classify those failures through the exported `Error`);
Go idioms FAIL P1 and APIs/records FAIL P2-1 with the same five sites;
performance PASS (remaining work is failure-path-only; P3: the
`Abort()` incomplete path returned the raw cause); wire/integrity PASS
(error typing only, zero on-disk or coordination change). The fix
converts all five sites through `publicError`, and `publicError` now
consumes the outermost classed class into the exported `Error` and
converts the rest of the chain recursively, so no internal error type
crosses the public boundary (the previous preserve-Cause passthrough
also let a `classedError` wrapping a public cancellation cause escape
unchanged). The test classifiers (`lifecycleCode`, `isPubCode`,
`pubCode`) classify only the top of the chain, so an inner class
never shadows the outer one (Rust abort_after_source nesting:
TransactionAborted -> CleanupInProgress -> original cause). Validation
at the fix HEAD: gofmt, vet, `go test ./...`, `go test -tags v4work
./...` (including the outcome-unknown fail-closed nested-class
chain), race/checkptr, the seven-target cross-build matrix, and the
env-gated mixed live battery on both sides are green. Gate pending
the five-reviewer round 3 at the fix HEAD.

Review gate round 3 for milestone 3 (2026-08-27, HEAD `085edea9`):
gate stayed open. Verdicts: Rust parity PASS (all-public chain mirrors
`abort_after_source` nesting, verified against sdk_error.rs), Go
idioms PASS (classifiers now document the boundary-authority
convention), performance PASS (failure-path-only conversion, bounded
recursion), wire/integrity PASS (zero on-disk or coordination change,
13-fixture corpus re-verified). APIs/records FAIL with P1-1 + P2-1:
the live-coordination lifecycle facade never used `publicError` -
`CreateLive`, `InitializeLive`, `ResetLiveCoordination`,
`ResolveLiveTransition`, `ResolveCreateLive`, and
`ResolveInterruptedLiveTransition` returned hard failures raw, and
`publicCreateResult`/`publicTransitionResult`/`publicResidueResult`
copied internal `*format.Error`/`classedError` values into the public
`Cause` fields (`lifecycle_public.go:163/180/351/366/382/397/205/234/
417`), so external consumers could not classify lifecycle failures
through the exported `Error`; the same scan also found the exported
`NewFeedName` returning a raw internal `*format.Error`
(`feed_name.go:23`). The round-2 closing claim that no internal error
type crosses the public boundary was scoped to the live-writer surface
and missed the lifecycle facade; this entry supersedes it. The fix
routes all nine lifecycle sites and `NewFeedName` through `publicError`
(the same boundary rule as the live-writer surface). Validation at the
fix HEAD: gofmt, vet, `go test ./...`, `go test -tags v4work ./...`,
race/checkptr, the seven-target cross-build matrix, and the env-gated
mixed live battery on both sides are green. Gate pending the
five-reviewer round 4 at the fix HEAD.

Review gate round 4 for milestone 3 (2026-08-27, HEAD `0e70e9b9`):
all five reviewers PASS. Rust parity: the lifecycle facade exposes
every failure as the exported `Error` with the Rust `live_lifecycle`
classes preserved (InvalidArgument, WrongStructureKind, WrongState,
CleanupConflict, IO, CleanupInProgress), no internal error type
crosses the public boundary. Go idioms: the boundary convention is
uniform across the live, lifecycle, workflow, reader, snapshot,
recovery, and publication surfaces; P3 noted that `NewFeedName`
blends the cleanest when it constructs the exported `Error` directly -
applied as the one-line cleanup included in the gate-close commit.
Performance: failure-path-only conversion on cold lifecycle surfaces,
no hot-path or success-path cost. Wire/integrity: error-typing only,
internal calls argument-identical, resolve paths never read `Cause`,
zero on-disk or coordination change. APIs/records: independent
same-defect scan of the root package found zero raw internal error
escapes (the remaining `format.Error` constructors feed the converting
abort helpers, internal-direction callbacks, or the `publicError`
machinery itself). Milestone 3 gate: CLOSED at the fix HEAD (after
the `NewFeedName` cleanup, re-validated: gofmt, vet, `go test ./...`,
`go test -tags v4work ./...`, race/checkptr, the seven-target
cross-build matrix, and the env-gated mixed live battery on both
sides are green). Plan steps 7-8 (matched update-ipsets release
benchmark matrix; artifacts, docs, and full-scope acceptance review)
remain for milestone 4.

Milestone 4 started (plan step 7): the Go `update-ipsets` benchmark
matrix port. Slice 4a/4b delivered: `v4/go/cmd/iprange-v4-bench/`
mirrors the Rust harness (v4/rust/iprange-livedb/benches/update_ipsets)
- CLI modes smoke/scale/local/ci/sample/case/header, subprocess-per-case
isolation, elapsed + allocation (MemStats Mallocs/TotalAlloc deltas) +
Linux RSS before/after/peak (VmRSS/VmHWM) + fd counts + logical/physical
file size + private-artifact refusal, the optional
IPRANGE_PERF_CONTROL/IPRANGE_PERF_ACK protocol, per-case validation of
every output database, and the accepted-baseline CI comparison with the
same CSV contracts. All 57 Rust scenario arms ported
(scenario_direct.go 6, scenario_membership.go 16, scenario_read.go 11,
scenario_structured.go 11, scenario_sdk.go 13), registered under the
exact Rust names. Matched smoke evidence on linux/amd64 (both harnesses,
same host, release builds): all 55 smoke cases run green on both sides
and the semantic facts (work units, emitted units, range records, feeds,
file logical bytes) are identical 55/55 after aligning the one deviating
scenario (live-membership-validation now ports membership::populated_rotating
exactly). Go runtimes at smoke scale measured 0.34x-88.5x of Rust in the
pre-fix pair (direct-replace 1000 at 0.34x, live-membership-validation
4000 at 88.5x from the then-unfixed external-poll ping-pong); at the
milestone-4 fix HEAD the smoke range (evidence/smoke-go-final.csv vs
the Rust reference) is 0.42x-7.59x: Go faster on direct-replace 1000
(0.42x) and snapshot/live-open-256 4000 (0.76-0.90x), Go slower on
structured-assign-random 4000 (7.59x) and every other case. The material
deltas need profiling at scale and actionable-waste removal before the
release evidence. Baseline CSV intentionally empty (status untracked)
until the matched scale run populates it.

Scale measurement runs (2026-08-27, linux/amd64, release builds, both
harnesses, under `nice`): the full scale matrix is about 90
subprocess-isolated cases at 10k-1M records per language; expected cost
is roughly 15-30 wall-minutes and 15-30 core-minutes per language
(single-core harness design, same as Rust). The runs produce the
matched release evidence: semantic facts, elapsed, allocations, RSS,
fds, file sizes. Material per-scenario deltas then get profiled with
pprof/cargo flamegraphs to separate actionable Go waste from designed
costs (for example the isolation-worker spawn in validation).

Milestone-4 scale catch (2026-08-27): the Go scale matrix exposed a
real structured-table defect that smoke never reaches (smoke caps
structured-build-random at 4,000 ids; the corruption starts at ids
>= 25,600). `structured-build-random` at 28k/421 validated with 2,401
ReasonStructureInvalid findings on the structure dictionary; Rust
passes the same case at 1M. Root cause (fixed): the Go validation and
recovery table walkers scaled a directory's child bases by
`StructureSpanOfLevel(level-1)` (the 50-id level-0 span) where the Rust
authority uses `table::coverage(level-1)` - the full 25,600-id span of
a level-2 child. The writer and reader already used the correct span;
the stored tables were always correct, and the two walkers mis-derived
every implied id above the first level-1 coverage. The bug was a
permanent-regression gate gap: level-2 roots (ID limits above 25,600)
were never walked by any Go test. Fix: validation
`structure_table.go` and recovery `structure_index.go` now scale
branch bases with `StructureSpanOfLevel(header.Level)`, the test
descender in validation/structure_test.go aligns to the same rule, and
two new regression tests pin the boundary:
`TestValidateStructureLevelTwoRootClean` and
`TestRecoveryStructureCountLevelTwoRoot` (synthetic three-level
generations with records at ids 1 and 25,600; both fail on the old
span and pass on the fixed span). The temporary validator
instrumentation used to localize the mismatch was removed. After the
fix: `structured-build-random 28000 421` validates clean (repro exit
0), and the full Go suite (`go test ./...`) is green. The complete
scale evidence below supersedes the partial 48-case run that stopped
at this defect.

Milestone-4 semantic probe catch (2026-08-27, after the walker fix):
cross-language membership probing of the matched scale artifacts found
a second real defect the validator cannot see: the Go name-based
membership import translated every source bitmap word with three or
more set bits wrongly. The Go `mapSourceWord` bit walk re-counted the
bit position from zero while shifting the word right, so bits at
positions 2 and above collapsed onto the second-lowest bit; every
imported membership lost feeds silently while staying internally
consistent (the Go validator passes the wrong file, because it proves
dictionary/hash/bitmap consistency, not import semantics). Repro at
membership-import 10k/421: the source's full 421-feed union imported as
a 14-bit bitmap ({0,1,64,65,...}) instead of 421 bits; the Rust import
of the identical source produces the exact 421-feed union. The Rust
authority (draft_store/import_cache.rs map_source_word) uses
`word.trailing_zeros()` with `word &= word - 1`; the Go port's shifting
variant was the only such loop in the codebase. Fix: rewrite the Go
walk with `math/bits.TrailingZeros64` and the same clear-lowest-bit
step (`v4/go/internal/writer/import_cache.go`). Regression test
`TestLiveImportPreservesWideMembershipBitmap` (public import of a
four-feed one-word union) fails on the old loop exactly at
"address 0 lost feed gamma" and passes on the fixed loop; the existing
two-feed import tests sat below the corruption threshold (a two-bit
word is handled correctly by both loops), which is why the defect
shipped unnoticed. After the fix, the repro destination reports
421/421 feeds at every probed address, byte-equivalent to the Rust
output's membership semantics. The matched scale matrices and their
fact comparisons were re-run at the fix HEAD and supersede the numbers
above for the affected membership-import cases.

Matched scale evidence at the fix HEAD (2026-08-27, linux/amd64,
release builds, both harnesses, under `nice`): the full Go scale
matrix is green 90/90 after the two fixes above (the earlier 48-case
partial run stopped at the structure-table defect). Go and Rust agree
on every semantic fact (work units, emitted units, range records,
feeds, file logical bytes) in all 90 cases; the remaining 29
differences are physical-only (unpublished tail granularity), matching
the authoritative commitment granularity difference already recorded.
Elapsed ratios Go/Rust (p50, same host, release, verified against the
matched CSVs): direct and membership lookups 1.9-4.0x (live-direct
100k=3.96x, live-membership 100k=3.41x, random variants 1.9-2.4x),
structured random lookups 1.6-2.0x (scalar 1M=1.98x, threat 1M=1.65x),
structured scan 3.6x, membership-import 8.8x, update-ipsets-workflow
5.4x, feed-replace 6.8x, nested-overwrite 8.9x, membership-cardinalities
1.9x, and the worker-mediated validation paths 445-640x
(live-validation 1M: Go 4.54 s for 4,113 checked pages vs Rust 10.2 ms;
live-membership-validation 1M: Go 9.88 s vs Rust 15.4 ms).
Milestone-4 perf root cause found and fixed (2026-08-27): the
445-640x validation ratios were not Go engine cost at all. The gdb
goroutine dump of a mid-validation isolation worker proved the real
cause: the worker's main goroutine was sleeping in
`Control.RequestExternalPoll`'s CancelPoll spin (control_wire.go:298)
from the checkpoint hook, called once per checked page (direct) and
once per structure slot (structured). External polling was enabled
because the public entries passed `cancellation.check` for a nil
token: `CancellationToken.check` is a nil-receiver method, and the
method value `(*CancellationToken)(nil).check` is non-nil, so
`RequiresExternalPoll` returned true and every checkpoint became a
~1-2 ms parent round trip (the "hang" was this ping-pong: 100k
structure slots x ~2 ms = minutes). Rust's fresh token has no poll
hook (`cancellation.rs requires_external_poll` = poll.is_some(), false)
and disables external polling. Fix: `CancellationToken.hook()` returns
nil for a nil token and the three worker-routed public entries
(`Validate`, `InspectRecoveryCandidates`,
`ValidateOfflineCandidate`) use it; the Recover arms already used
`internalCheck`, which handles nil correctly. Regression test
`TestNilCancellationHookIsNil`. Measured impact (release, same host,
under `nice`): live-validation 1M 4.54 s -> 24.2 ms (188x; Rust
10.2 ms, now 2.4x), live-membership-validation 1M 9.88 s -> 28.9 ms
(342x; Rust 15.4 ms, now 1.9x), live-structured-threat-random-lookup
100k case wall ~3.5 min -> 6.6 s total (measured op 5.88 s for 10M
lookups, ~1.25x Rust). The full Go suite is green at the fix. Profile
tooling note: IPRANGE_CPU_PROFILE now writes one pprof file per
process (PID-suffixed), so the bench case process and its worker each
produce their own profile instead of clobbering one path.

Accepted-baseline population completed at the fix HEAD (2026-08-27,
linux/amd64, release Go build, under `nice`): all 18 CI cases (1
warmup + 5 samples each) completed in about 8 wall-minutes
single-core (the pre-fix binary needed 50+ minutes to reach case
12/18 and was projected at 1.5-2 hours). The accepted-baseline.csv in
v4/go/cmd/iprange-v4-bench/ is populated from the p50 medians with
the disaster limits (2x p50 + 100 ms runner noise, 500 ms for the
complete workflow). The `ci` gate re-run at the fix HEAD (1 warmup +
3 samples, enforce mode) passes 18/18 within-limit; the observed CI
median ratio spans 0.957-1.109 across the 18 cases in the final gate
log (committed under v4/go/cmd/iprange-v4-bench/evidence/); the
highest limit utilization is 51.5% (live-structured-scalar-random-
lookup), so all cases hold with more than 2x disaster headroom.
Validation medians at the fix HEAD: live-validation 1M = 25.5 ms and
live-membership-validation 1M = 30.6 ms (Rust 10.2/15.4 ms), so the
CI gate additionally proves the nil-token fix holds under gate
conditions.

Final milestone-4 evidence at the fix HEAD (2026-08-27, linux/amd64,
release builds, under `nice`): re-run after the nil-token fix. Smoke
matrix: 55/55 semantic facts identical to the pre-fix run (work
units, emitted units, range records, feeds, logical file bytes), with
the validation rows now at 4-5 ms at the 4k scale (pre-fix these
rows carried the external-poll ping-pong). Scale validation rows:
live-validation 1M 28.8 ms, live-membership-validation 1M 29.8 ms,
immutable-validation 1M 29.7 ms (Rust 10.2/15.4 ms for the live
rows), i.e. the validation family went from 408-640x to 1.9-3.2x
(immutable-validation 1M 29.7 ms vs Rust 9.3 ms = 3.2x).
The remaining material Go/Rust p50 deltas at scale, with the pprof
attribution (IPRANGE_CPU_PROFILE over the bench `case` child, one
profile per process):
- live direct/membership lookups 1.9-4.0x and structured random
  lookups 1.25-2.0x: fixed-tree probe cost. The shared
  `internal/tree.lowerBoundBy` calls a keyAt closure per probe with
  bounds-checked codec cell decodes (profiles: lookupRange4 +
  lowerBoundBy + SlottedPage.Record); Rust's generic binary search
  inlines the comparator. The lookup path stays allocation-free (0-1
  allocs, 0-16 bytes, vs Rust 0).
- membership-import 1M 8.85x (scale Go 412 ms vs Rust 46.6 ms; the
  standalone profile run measured 496 ms): the same
  lowerBoundBy probes dominate the profiles, and the Go write seam
  allocates per record: 3,001,943 alloc calls / 1,536 MB vs Rust
  20 calls / 474 B (the per-range Mutate closures in the import
  loop; the import cache itself is draft-page backed like Rust).
- nested-overwrite 1M 8.9x, feed-replace 1M 6.8x,
  update-ipsets-workflow 1M 5.4x: profiles show the same two causes
  (lowerBoundBy/InsertIfLocalGap probes + per-operation mutation
  seam); the workflow additionally runs the full user pipeline
  (feeds, imports, publishes) so the delta is the sum of the same
  unit costs.
The single recurring hot symbol across all five profiled deltas is
the probe path of the one shared fixed-tree search (lowerBoundBy +
its generic comparator closure); the second is the per-record write
seam allocation. Both are explained designed costs of the Go
abstraction layer (one shared tree codec over four record layouts;
one lock-scoped Mutate per record like the Rust mutable brace, but
a closure escape per call). They are tracked as follow-up
optimizations below, not silent debt: the reader hot paths ship at
1.25-4.0x with zero allocations, and the write paths ship
subsecond-to-low-seconds at the 1M scale.
Accepted CI gate record: final gate run at the fix HEAD with the
accepted Go baseline identity (baselineID go-v4-local-20260827)
re-verifies 18/18 within-limit.

Milestone-4 extension (user decision 2026-08-28, in scope before
acceptance): (1) slice 4c specializes the fixed-tree search per
record codec (inline key comparators, no per-probe closure) - the
old external-review recommendation that now has profile evidence
across every remaining delta; (2) slice 4d batches the
membership-import mutation seam to cut the per-record allocation
class; (3) slice 4e re-measures, re-baselines, and re-gates the
accepted evidence. The accepted milestone-4 evidence remains valid
as the pre-optimization baseline for the 4c/4d deltas.

Milestone-4 five-reviewer gate round (2026-08-28, HEAD 81620b63):
scope reviews were PASS for Go idioms, performance, and wire/integrity,
and FAIL for records (two falsifiable P2 claims) and Rust parity (one
P2 harness guard). All findings fixed in the follow-up commit:
- Records P2-1: the CI median-ratio claim "0.98-1.09" was wrong for
  the final gate artifact; corrected to the committed final gate log
  (ratio range 0.957-1.109, highest limit utilization 51.5%), and the
  gate log is now committed as evidence. P2-2: the smoke ratio range
  claim ("0.4x-9.7x, none faster than noise") predated the aligned
  run; corrected to the measured pre-fix pair (0.34x-88.5x) and the
  fix-HEAD range from the committed final smoke CSV (0.42x-7.59x;
  earlier 0.66x-6.64x belonged to the 23:29 pre-batch-reuse run and
  is superseded). P3s: membership-import parenthetical now separates
  the scale ratio (8.85x, 412 ms) from the profile run (496 ms); the
  validation family range is 1.9-3.2x including immutable-validation;
  the baseline cost is stated as ~8 wall-minutes for the sample run.
- Rust parity P2: the bench source guard accepted count+phase up to
  2^32-1 where Rust require_address_space refuses above 2^30, so
  oversized user-invoked sample/case inputs wrapped the uint32
  address arithmetic instead of erroring. The Go guard is now
  (math.MaxUint32-1)/4+1 with the same refusal message; regression
  tests pin the boundary. Rust parity P3: the streaming sources now
  reuse one [batchCapacity] backing slice per source like Rust's
  [T;1024], removing the per-batch allocations from the measured
  region (feed-first-ascending 1M: 1,222 calls / 8,048,152 bytes in the
  first accepted-run sample artifact dropped to 227 calls / 40,048
  bytes in the final gate log); a test pins the reused backing
  buffers.
- Recording-only: the batch reuse improves the harness allocation
  facts for streaming scenarios, so the accepted baseline and the CI
  gate were re-run with the final harness and the accepted-baseline.csv
  and the committed evidence logs were refreshed at the final HEAD.
- Standing measurement caveats recorded (non-blocking): rssPeak uses
  VmHWM so setup-heavy cases overbound the measured region; rssBefore
  is sampled before the pre-baseline GC; worker-mediated rows exclude
  the worker subprocess's allocations (the only cross-language alloc
  comparison cited, membership-import, is in-process on both sides);
  Go allocation capture is GC + MemStats deltas (inherent Go shape);
  immutable-feed-random's measured region differs structurally
  (one-inode feed creation is a recorded pending gap) and is excluded
  from the CI 18; the worker pprof hook PID suffix noted by one
  reviewer was already present in the committed fix; scenario_sdk.go
  at 1,668 lines exceeds the soft 500-line guideline but is cohesive
  (one workflow per scenario, clear sections).

Milestone-4 performance mandate (user decision 2026-08-28): the
recorded 4-9x write-path deltas and 2-4x read-path deltas are NOT
acceptable as explained designed costs; Go must not be significantly
inferior to Rust. Both harnesses were re-verified as optimized
production builds (Rust bench profile opt-level 3, same binary that
produced the evidence; Go plain go build, gc, GOAMD64=v1; GOAMD64=v3
changes nothing). Milestone 4 extends with three optimization slices
before acceptance: 4c specialized key-only fixed-tree search probes
(no per-probe closure/interface dispatch; the #1 hot symbol in every
delta), 4d write-seam allocation removal (membership-import 3M
allocs / 1.5 GB vs Rust 20 / 470 B; batched mutation scopes, reused
buffers), 4e re-measure + re-baseline + re-gate + evidence refresh.
Targets: read paths ~1.2-1.6x Rust, validation ~1.5-2x, write paths
~2-3.5x; absolute parity is not promised (GC and Go generics are
real), the 4-9x class must be eliminated.

Slice 4c/4d implementation record (2026-08-28, commits 483fc37f and
follow-ups): the per-probe closure plus interface ReadKey plus
materialized 88-byte Key was the top CPU symbol in every remaining
delta. lowerBound is now closure-free; codecs whose fixed cells carry
the ordered key as a plain prefix opt into an inline width-specialized
probe (u32/u64/u128 little-endian or raw digest+suffix words) that
keeps the persistent slot-table read per probe; every codec implements
Codec.CompareKey without building a Key. The membership dictionary
transactions and the membership-import range seams bind one WriterEdit
per workflow instead of allocating a fresh DraftStore per record
(Rust borrowed-&mut-WriterEdit parity); the import loop also stores
the previous canonical range by value, removing the per-record
escape. fixedPositions writes into a bounded int16 scratch
([u16; MAX_SLOT_COUNT] parity) and branch cells encode into
caller-owned CellBufs, removing the truncate and split allocations.
Bench harness: heap-profile hook for the delta work and reused batch
buffers in the measured regions. Measured medians at 1M records,
same host, matched optimized release binaries (Go gc plain build,
Rust bench profile); every quoted median is reproducible from the
committed evidence rows (evidence/case-runs-4c4d-20260828.csv for
the headline case runs, evidence/ci-go-v4-local-20260828b.log for
the final gate):

- membership-import: 170.5 ms vs Rust 47.1 ms (3.6x; was 430.8 ms /
  9.1x with 3,001,958 allocs / 1,536.7 MB, now 2,311 allocs /
  0.7 MB; Rust has 20 allocs / 470 B).
- nested-overwrite: 1,932.9 ms vs Rust 301.7 ms (6.4x; was
  2,887.6 ms).
- update-ipsets-workflow: 4,878 ms vs Rust 1,112 ms (4.4x; was
  5,780.7 ms, then 5,038 ms at the 4c/4d commits; the delta-review
  value-return of the selected-ranges feed cut the run allocation
  volume from 21.2M calls / 752 MB to 10.7M calls / 369 MB).
- live-direct-random-lookup: 492.1 ms vs Rust 236.8 ms (2.1x; was
  596.5 ms).
- immutable-direct-random-lookup: 420.2 ms vs Rust 217 ms (1.9x).
- live-validation: 25.6 ms vs Rust 14.1 ms (1.8x; was 24.6 ms).

The 4-9x class is eliminated for the lookup, validation, and import
families; the remaining write-path outliers are nested-overwrite
(6.4x) and update-ipsets-workflow (4.4x), both deep in the generic
gap/replace machinery where the residual cost is Go bounds checks,
interface indirection, and GC, not structure (verified against the
Rust range_mutation assign paths). Slice 4e re-baselined and
re-gated the acceptance evidence at the improved state: the accepted
baseline identity is now go-v4-local-20260828 (rebuilt
accepted-baseline.csv from 18 CI cases x 5 samples; the pre-4c
20260827 samples stay in evidence/ as the improvement baseline), the
final CI gate run is 18/18 within-limit with ratios 0.940-1.106
(update-ipsets-workflow ratio 0.964 against the accepted 5,058 ms),
and the smoke pair range improved to 0.34x-5.92x. pprof head
evidence before/after is committed at
evidence/profiles-4c4d-summary.txt.

Milestone-4 delta review (2026-08-28, five level-1 reviewers of the
lead model, adversarial mode; scopes: Rust parity Linnaeus, Go idioms
Darwin, performance Hooke, wire/integrity McClintock, APIs/records
Plato). Verdicts: Darwin PASS (P3: keycmp.go hand-rolled compares
vs cmp.Compare; optional probe-set enumeration test), Hooke PASS
(P3-1: the u32 prefix codecs fell back to the record-decode compare
path - fixed by declaring PrefixKeyProbe on idCodec/indexCodec,
commit dbcf0008; P3-2 per-width probe specialization noted as
moderate optional; P3-3 workflow seam - fixed by the value-return
sweep, commit 6f66e32c). Three FAILs with actionable findings, all
fixed and re-tested:

- Linnaeus P2 (Rust parity): the raw prefix probe dropped the
  per-probe cell validation of the membership and structure hash
  codecs; corrupt cells (zero id, out of range word count) were
  compared blindly where Rust decode_hash refuses them. Fixed with
  an optional tree.ProbeValidator applied per probe only on the two
  hash codecs (nil for the plain numeric probes, keeping the hot
  u32/u64/u128 paths dispatch-free), commit 93f81997; regression
  tests corrupt the root page middle cell and require
  CodeFormatInvalid with healthy controls.
- McClintock P2 (wire/integrity): the 4c sweep wrapped the edit
  cause in publicError before abortEdit/abortAfter, hiding the
  internal class from isFatalClass; a CodeFormatInvalid mid-edit
  left the writer falsely healthy (Rust abort_after brands the
  writer unusable on Io/Format). The abort paths pass the raw
  internal error again with the public conversion at the boundary
  (the structured transaction already did), commit 90b4d885;
  regression test drives a v4work fault point inside
  deleteCurrentFeedMembership and asserts the writer fails closed.
- Plato P2-1 (records): the committed 20260828 CI log header still
  named the 20260827 identity. Fixed by committing the final gate
  log at the final identity as ci-go-v4-local-20260828b.log (the
  old file stays with an explanatory README note).
- Plato P2-2 (records): six quoted before/after medians were not
  reproducible from the preserved case runs. Fixed by correcting
  the table to the preserved artifact rows and committing them as
  evidence/case-runs-4c4d-20260828.csv.

The re-gate after the four fix commits is 18/18 within-limit at
ratios 0.940-1.106; all local tests, v4work, race, vet, and gofmt
pass at commits 6f66e32c..93f81997.

Independent external review (2026-08-28, single sol reviewer, read-only,
session 7ade436af6fb434ea2e25b78a806b02e): verdict NEEDS CHANGES with
four P1, three P2, and one P3 finding. Adjudication:

- P1 records: the Go-baseline CI gate (2x Go p50 limits) proves
  regression stability only, not Rust parity; milestone 4's mandate
  compares Go against Rust. Fixed by committing the separate
  Rust-ratio acceptance report: evidence/rust-ratio-acceptance-
  20260828.csv with matched fresh medians of the final release
  binaries (same host, both sides; raw samples committed alongside).
  Measured ratios: membership-import 3.5x (165.3 ms vs 47.1 ms),
  nested-overwrite 6.7x (1,931.5 ms vs 290.2 ms),
  update-ipsets-workflow 4.4x (4,895.1 ms vs 1,104.1 ms),
  live-direct-random-lookup 1.9x (466.3 ms vs 241.7 ms),
  immutable-direct-random-lookup 2.1x (436.0 ms vs 211.9 ms),
  live-validation 2.6x (28.9 ms vs 11.0 ms). Write-path ratios
  remain above the mandate's 2-3.5x target.
- P1 gap machinery copies: the 752-byte descent Path was passed by
  value into every gap probe, and the selector used value receivers.
  Fixed with pointer receivers and a *Path (nil keeps the vacuous
  edge semantics of the cached-leaf probe), commit fc9df1ea.
- P1 universal keys: verified - Go range records carry two 88-byte
  universal tree.Key values (184-byte decoded records) where Rust
  uses a u32 IPv4Key (12-byte records), plus interface dispatch per
  assignment. Structural fix (key-typed tree core or generated
  specializations) is high effort and high integration risk under
  the one-authoritative-implementation rule; NOT implemented -
  recorded for a user design decision.
- P1 transform segments: segmentAt returned pointer segments; fixed
  by value (compiler escape analysis had already kept them on the
  stack; measured impact noise-level, escape hazard removed).
- P2 rejected-neighbor decodes: the RequireReplacement validation
  volume is exact Rust parity (3 ReadLeaf + 4 ReadKey per 3-cell
  replacement); the selector's third neighbor decode is a real but
  ~2-4% upper-bound inefficiency that needs a generic LocalGap
  interface refactor - recorded, not implemented.
- P2 probe-width switch: implemented as monomorphized width compares,
  measured 1-3% REGRESSION (the generic-interface method call per
  probe is not inlined), reverted; the per-probe switch stays.
- P2 streaming facades: verified. The per-record string conversions
  measure at most one object per distinct delivered name (Go escape
  analysis stack-allocates the short-lived conversions under the
  current toolchain); the join direct-value pointers allocated one
  heap scalar per mapped cell. Fixed: one owned string per distinct
  feed name shared across batches, a per-batch value arena for the
  optional direct values (documented batch-lifetime contract), and
  slices.SortFunc in the join emitter (no reflect). Two allocation
  gates pin the classes (real-sink aggregate test with a names+96
  bound; internal emit test at one allocation per 300-cell run),
  commit fc9df1ea.
- P3 heap optionals: PrivateEdge pending first-key and RemovedRun
  following are inline values now (commit fc9df1ea).

Measured impact of the adjudicated fix set: all headline cases within
run noise of the pre-review state (the remaining ratio class is the
structural universal-key design). All tests, v4work, race, vet, and
gofmt pass at fc9df1ea.

## Requirements

### Purpose

Make the pure-Go v4 SDK a true public-behavior peer of the Rust v4 SDK under the
exact current unsigned v4 contract. The result must support the same logical
operations, live coordination, failure outcomes, recovery containment,
platform boundary, bounded-resource behavior, and cross-language evidence
needed by `update-ipsets` and downstream SDK consumers.

This work must correct implementation and evidence gaps rather than weaken the
normative specification to match the incomplete Go surface.

### User Request

Create a new SOW containing the verified static gap analysis of feature parity
between the Rust and Go v4 SDK implementations.

### Assistant Understanding

Facts:

- Rust was implemented first and is the reference implementation for v4
  semantics; the normative specifications and shared conformance contract are
  the cross-language authorities.
- The design requires completed Rust and Go SDKs to be public-behavior peers.
  Physical page placement, tree shape, internal IDs, and compressed bytes may
  differ when observable semantics agree.
- Both readers open and semantically check all eleven committed immutable
  conformance files. This is real file-format and reader-interoperability
  evidence for the cases represented by the corpus.
- Go's sidecar-bound `LiveWriter` exposes only information, one advanced direct
  transaction, and close. Most Go mutation operations are attached to the
  separate sidecar-free `Writer`, which blocks readers for its lifetime.
- The public sidecar-free mutable Go writer contradicts the exact v4 contract,
  which requires every public writer to be live and explicitly forbids offline
  in-place mutation.
- Go has no public membership-import, one-inode immutable-feed construction,
  exact commit-resolution, or bounded reader-safe reclamation operation.
- Go validation, candidate inspection, and recovery use the required worker
  process only on Linux/amd64. Other targets execute faultable mapped-file work
  in the application process.
- Go deliberately refuses creator-only artifact creation on macOS because its
  Darwin ACL machine is unavailable. Rust supports the specified macOS
  creator-only policy.
- The existing subprocess tests spawn another test process of the same language
  and inspect immutable files. They do not run cooperating Go and Rust
  processes on the same live database.
- SOW-0025 claimed the missing behavior and conformance evidence as completed,
  but its own delivery record states that the Go `LiveWriter` shipped only the
  direct transaction surface. The reviewed gap is an incorrect acceptance
  record, not evidence that later code removed formerly implemented behavior.
- SOW-0017 currently treats Go/Rust parity and platform completion as proven
  prerequisites for snapshot-signing design. This static review invalidates
  that prerequisite.

Inferences:

- The long-term-best correction is to converge Go on the Rust public contract,
  not to preserve two Go writer modes or narrow the specification after the
  fact.
- The existing Go mapped reader, writer core, live coordination, workflow,
  publication, validation, and recovery owners contain substantial reusable
  implementation. The correction should rewire and complete those owners
  rather than introduce another storage path.
- Static source presence is insufficient evidence of semantic parity. The
  repaired surface needs independent public-API tests, symmetric producer
  fixtures, true mixed-language live tests, native platform evidence, and
  matched release benchmarks.
- Snapshot signing should not proceed until this SOW restores the prerequisite
  unsigned SDK contract and evidence.

Unknowns:

- Whether macOS creator-only ACL removal and proof can be implemented under the
  existing pure-Go/no-cgo constraint without a new native boundary. Official
  Darwin interfaces and viable pure-Go mechanisms must be investigated before
  that platform chunk is designed.
- The smallest portable worker implementation that can preserve the exact
  POSIX `SIGBUS` and Windows in-page-exception containment rules on Darwin,
  FreeBSD, Windows, non-amd64 Linux, and supported architectures without
  violating the pure-Go boundary.
- The final Go/Rust performance delta after the public writer is consolidated
  and per-result adapter allocation is removed. Static inspection cannot
  establish the measured result.

### Acceptance Criteria

- The public Go writer contract exposes only the normative live writer. A
  sidecar-free immutable file cannot be opened for mutation, and no public
  `Create`/`OpenWriter` offline mutation path remains.
- Go `LiveWriter` exposes Rust-equivalent advanced direct, membership, and
  structured transactions; feed create/replace/rename/delete; exact direct
  replacement; first-seen and last-seen refresh; history projection; metadata
  mutation/read-your-writes; abort, close, commit, and bounded reclamation.
- Every advanced or high-level Go begin operation accepts and retains its own
  cancellation token through ingestion, normalization, metadata, commit, and
  cleanup checkpoints.
- Go implements name-based membership import from explicitly pinned immutable
  and live readers with the Rust compatibility, source-borrow, report,
  cancellation, and no-change semantics.
- Go implements one-inode immutable IPv4 and IPv6 feed construction with the
  Rust resource budget, normalization, metadata, publication, report, and
  cleanup contract.
- Go implements exact commit-outcome resolution for immutable and live modes,
  including same-file proof, superseded/unknown classification, and truthful
  cleanup evidence.
- Go implements bounded, transaction-grouped, oldest-reader-safe reclamation
  through the live writer and proves lowest-free-page reuse while readers are
  pinned.
- Go clean-writer metadata mutation and transaction metadata access provide
  the same staged read-your-writes semantics as Rust.
- Direct provider joins accept both immutable and live direct readers. Public
  mapped source adapters provide bounded reader-to-writer chaining equivalent
  to the Rust named-feed and direct-range sources.
- All public Go failures that belong to the stable SDK taxonomy are
  programmatically accessible as the exported Go error type. Public APIs never
  leak an unnameable `internal` error type.
- Invalid public Go inputs return typed errors rather than panic. Enum-like
  certifications reject undefined values before path access or mutation.
- Validation, candidate inspection, recovery, and retained-cleanup retry use a
  version-matched worker on every supported OS/architecture. No supported
  target silently executes faultable scans in the application process.
- Go creates and proves creator-only live and immutable publication artifacts
  on Linux, macOS, Windows, and the specified FreeBSD immutable boundary, or
  implementation stops for an explicit user decision if the pure-Go macOS
  contract is proven infeasible.
- Go publicly exposes the required Windows housekeeping list/removal APIs and
  their exact identity-bound evidence and terminal outcomes.
- Both implementations independently produce the required symmetric direct,
  membership, structured, metadata, cardinality, snapshot, and invalid-case
  corpus coverage through public normative paths. Go fixtures are produced
  through `CreateLive`, `LiveWriter`, close, and live `SnapshotTo`, not through
  an offline mutable writer.
- Mixed Go/Rust subprocess tests cooperate on the same live database in both
  directions and cover registration/release, writer exclusion, reclamation,
  stale slots, sidecar replacement, transition/reservation states,
  publication inspection, and resolution.
- Commit-resolution and compact-snapshot cases required by the normative
  conformance contract are cross-proven, including same-transaction/different-
  nonce attempts.
- Go aggregation/join adapters meet their documented bounded/allocation
  behavior. Public steady-state performance claims are backed by allocation
  pins and profiles of the actual public adapters.
- Go ports the representative Rust `update-ipsets` release benchmark matrix.
  Matched release measurements record throughput, allocation, RSS, file size,
  file descriptors, necessary work, and semantic results for both languages.
  Material differences are explained and accepted by the user.
- Current SDK and conformance documentation accurately describes implemented
  behavior, worker deployment, platform support, and evidence. Superseded
  milestone reports no longer appear to be active work records.
- The full Go codebase passes the mmap-only/page-copy/file-I/O adversarial gates,
  all supported compiler graphs, tests, race/checkptr analysis, static checks,
  native authorized platform tests, and independent final review with no open
  P0, P1, or P2 finding.
- SOW-0017 remains blocked until this SOW completes and the restored unsigned
  SDK prerequisite is accepted by the user.

## Analysis

Sources checked:

- `.agents/sow/specs/design-iprange-engine.md`
- `.agents/sow/specs/binary-format-v4.md`
- `.agents/sow/done/SOW-0025-20260811-pure-go-exact-v4-port.md`
- `.agents/sow/done/SOW-0026-20260825-v4-platform-completion.md`
- `.agents/sow/current/SOW-0017-20260717-v4-snapshot-signing-phase2.md`
- `v4/rust/iprange-livedb/src/lib.rs` and its exported reader, writer,
  lifecycle, workflow, query, publication, validation, recovery, and snapshot
  modules
- All compiled production Go files under `v4/go/`, including public facades and
  the internal writer, live-coordination, publication, routing, security,
  validation, recovery, and worker owners
- `v4/conformance/README.md`, `v4/conformance/cases.json`, Go/Rust fixture
  generators, conformance tests, and mixed-subprocess tests

Current state:

- **Substantial parity:** exact immutable format identity; reading committed
  direct, membership, and structured fixtures; metadata and cardinality
  observations; broad live-reader surface; membership queries, aggregation,
  global algebra, snapshots, lifecycle transitions, publication, validation,
  and recovery facades on the platforms where their required machinery exists.
- **Partial parity:** direct live transaction; direct-provider joins; public
  error translation; cancellation; platform creation; worker deployment;
  producer-fixture coverage; resource/performance evidence.
- **Not parity:** live membership/structured/feed/timestamp/history mutation;
  membership import; immutable single-feed creation; commit resolution;
  reader-safe reclamation; live writer metadata read-your-writes; public Windows
  housekeeping; real mixed-language live cooperation.

Evidence:

- Normative all-writers-live rule and offline-mutation prohibition:
  `.agents/sow/specs/binary-format-v4.md:1272-1279`.
- Explicit constructor contract:
  `.agents/sow/specs/binary-format-v4.md:2413-2421`.
- Cross-language public-behavior requirement:
  `.agents/sow/specs/design-iprange-engine.md:57-73,418-438`.
- Go's sidecar-free creation and mutation API:
  `v4/go/writer_public.go:52-97`.
- Go's real live writer is direct-only:
  `v4/go/live_writer_public.go:42-100`.
- Go workflows attached to sidecar-free `Writer`:
  `v4/go/direct_workflow_public.go:60-80`,
  `v4/go/feed_workflow_public.go:62-73,520-575`,
  `v4/go/membership_transaction_public.go:83`,
  `v4/go/structured_transaction_public.go:62`, and
  `v4/go/history_projection_public.go:64`.
- Rust `LiveWriter` equivalents:
  `v4/rust/iprange-livedb/src/live_writer/direct.rs:29`,
  `membership.rs:91`, `structured.rs:41`, `direct_workflow.rs:66-96`,
  `feed_workflow.rs:58-71`, `feed_lifecycle.rs:11-36`, and
  `history_projection.rs:57`.
- Missing-in-Go Rust operations:
  `v4/rust/iprange-livedb/src/live_writer/membership_import.rs:60-93`,
  `live_writer/reclaim.rs:24-37`, `commit_resolution.rs:86-99`, and
  `immutable_feed.rs:67-119`.
- Rust clean-writer metadata/read-your-writes:
  `v4/rust/iprange-livedb/src/live_writer.rs:143-189,211-229`.
- Go live direct operation omits cancellation:
  `v4/go/live_writer_public.go:68-79,270-282` and
  `v4/go/internal/live/live_writer.go:196-201`.
- Go direct joins accept only immutable sources:
  `v4/go/join_public.go:63-89`; Rust accepts immutable and live sources at
  `v4/rust/iprange-livedb/src/membership_query/join.rs:20-25,108-128`.
- Go public writer paths leak internal typed errors:
  `v4/go/writer_public_test.go:17-24`; the correct conversion boundary exists at
  `v4/go/reader_public.go:769-787`.
- Go public nil-input panic and unchecked certification paths:
  `v4/go/join_public.go:71-78,114-122`,
  `v4/go/membership_algebra_public.go:79-86`, and
  `v4/go/recovery_public.go:27-37,205-211`.
- Go aggregation/join adapter allocation contradicts its public comments:
  `v4/go/aggregation_public.go:1-10,80-108` and
  `v4/go/join_public.go:1-8,89-101,124-151`.
- Worker required for faultable operations:
  `.agents/sow/specs/binary-format-v4.md:3155-3206`.
- Linux/amd64 Go worker routing and other-platform in-process routing:
  `v4/go/internal/routing/routing_linux_amd64.go:1-40`,
  `v4/go/internal/routing/routing_other.go:1-50`, and
  `v4/go/cmd/iprange-v4-worker/main_other.go:1-15`.
- Go Darwin creator-only refusal:
  `v4/go/internal/security/acl_darwin.go:9-28` and
  `v4/go/internal/live/public.go:23-38`.
- Windows housekeeping is private in Go but public in Rust:
  `v4/go/internal/publication/maintenance_gc_windows.go:10-29`,
  `v4/go/publication_public.go:179-225`, and
  `v4/rust/iprange-livedb/src/publication.rs:36-47`.
- Normative mixed-language live conformance:
  `.agents/sow/specs/binary-format-v4.md:4145-4170`.
- Existing same-language immutable subprocess tests:
  `v4/go/subprocess_cross_open_test.go:27-171` and
  `v4/rust/iprange-livedb/tests/mixed_subprocess.rs:23-140`.
- Rust fixtures use live creation/writing/snapshot; Go fixtures use the
  sidecar-free writer:
  `v4/rust/iprange-livedb/tests/conformance_support/generate.rs:36-73` and
  `v4/go/conformance_generate_test.go:48-64,107-125,147-194,226-240,310-324`.
- Corpus asymmetry and stale snapshot statement:
  `v4/conformance/README.md:10-43`; Go `SnapshotTo` exists at
  `v4/go/snapshot_public.go:101`.
- Go has reader microbenchmarks but no port of the public Rust
  `update-ipsets` benchmark matrix:
  `v4/go/internal/reader/bench_test.go:25-141` and
  `v4/rust/iprange-livedb/benches/update_ipsets.rs:1-43`.
- SOW-0025 claimed complete parity while recording a direct-only live facade:
  `.agents/sow/done/SOW-0025-20260811-pure-go-exact-v4-port.md:2785-2817,7478-7484`.
- SOW-0026 records non-Linux worker refusal as accepted behavior:
  `.agents/sow/done/SOW-0026-20260825-v4-platform-completion.md:122-133`.
- SOW-0017 relies on the disproven parity prerequisite:
  `.agents/sow/current/SOW-0017-20260717-v4-snapshot-signing-phase2.md:7-25,114-118`.

Risks:

- Keeping the sidecar-free writer public would establish two incompatible Go
  mutation contracts and allow callers to build on behavior forbidden by the
  v4 specification.
- Consolidating the public writer affects every mutation facade, transaction
  handle, result type, test fixture, benchmark, and downstream caller. Partial
  migration could silently keep an off-contract path alive.
- Incorrect live workflow integration could publish partial drafts, reclaim
  pages still visible to readers, lose outcome-unknown evidence, or deadlock
  reader/writer coordination.
- In-process damaged-file scans can terminate or corrupt the consuming process
  on mapping faults. Worker portability is a correctness and containment
  requirement, not optional hardening.
- Incorrect ACL handling could publish artifacts with inherited access or make
  the macOS SDK unusable. A weakened creator-only policy is not acceptable.
- Removing exported Go APIs before all internal tests and callers migrate can
  cause build failures. The format is unreleased, so compatibility must not be
  preserved at the cost of the exact contract, but the migration must still be
  complete and atomic within this repository.
- Incomplete mixed-language testing can leave lock, sidecar, retirement, and
  resolver incompatibilities undiscovered even while immutable fixtures
  cross-open successfully.
- Benchmark work is potentially expensive. Every run expected to exceed two
  wall-minutes or ten core-minutes must be recorded with cost before execution
  and run under `nice`.

## Pre-Implementation Gate

Status: resolved (2026-08-26, user decision A on Open Decision 1)

Problem / root-cause model:

- SOW-0025 implemented a large Go semantic surface over the mapped writer core,
  then added a separate sidecar-bound live writer containing only direct
  transactions. The broader workflows were never migrated onto the live writer.
- The completion gate treated sidecar-free exclusive mutation as an accepted
  implementation mode even though the normative contract explicitly forbids
  it. The conformance fixtures and subprocess tests then exercised that
  incomplete architecture rather than the required live cross-language path.
- Several Rust exports, platform requirements, and conformance cases were
  recorded as completed without compiled Go public entry points or equivalent
  evidence.
- Later platform work preserved the accepted gaps, including non-Linux worker
  refusal, while SOW-0017 was unblocked on the resulting completion claims.

Evidence reviewed:

- Exact current design and binary-format specifications.
- Complete public Rust export inventory and the implementation modules owning
  every material operation.
- Complete compiled Go public facade inventory and targeted internal owners for
  writer/live coordination, security, routing, workers, publication,
  validation, and recovery.
- Shared corpus manifest, producer paths, immutable conformance tests, and
  subprocess tests in both languages.
- SOW-0025, SOW-0026, and the active SOW-0017 prerequisite record.
- Repository revision
  `07c0147bf18b4d1e976f460fc2624e90e91be8cc`; the working tree was clean before
  this SOW file was created.

Affected contracts and surfaces:

- Go public package API, public types, typed errors, cancellation, writer and
  transaction handles, lifecycle, recovery, validation, publication, and
  platform behavior.
- Internal Go writer core, live sidecar owner, mapped sources, immutable output,
  worker client/server, security, publication maintenance, and platform seams.
- Rust and Go fixture generators and conformance tests.
- Shared immutable corpus files and manifest.
- Mixed-process coordination harnesses.
- Go/Rust benchmarks, work counters, profiles, and release evidence.
- SDK README/package documentation, worker deployment documentation, platform
  support statements, specifications if investigation exposes a real contract
  ambiguity, runtime project skills, AGENTS.md gates, SOW-0017 prerequisites,
  and SOW lifecycle records.
- `update-ipsets` suitability and any downstream code compiled against the
  unreleased Go API.

Existing patterns to reuse:

- Rust `LiveWriter` and its direct, membership, structured, workflow, metadata,
  reclaim, and result modules are the semantic reference.
- Go `internal/writer.Core` and `internal/live.LiveWriter` are the existing
  physical and coordination owners; workflows must compose them rather than
  copy persistent logic.
- `HistoryProjectionSource` already demonstrates a Go tagged source that can
  bind immutable or live readers.
- `publicError` is the established root-package conversion boundary for typed
  errors.
- Rust public source enums demonstrate explicit immutable/live modes for
  membership import, joins, snapshots, validation, and recovery.
- Existing Go lifecycle/publication result translations demonstrate how
  platform-neutral public evidence should wrap internal owners.
- Rust fixture generation through live writer plus `snapshot_to` is the
  normative producer pattern.
- The project full-codebase mmap-only/page-copy/file-I/O reviewers and the
  `project-final-review` skill are the acceptance-review pattern.

Risk and blast radius:

- Behavioral/API compatibility: high inside the unreleased Go SDK because the
  off-contract public writer must disappear or become private.
- Data loss/corruption: high if commit, abort, reclaim, resolver, or workflow
  integration is incorrect.
- Concurrency: high for reader slots, gates, writer lease, close, and page reuse.
- Platform: high for signal/exception containment, ACLs, file identity, locks,
  durability, and Windows housekeeping.
- Security: high for creator-only access and untrusted damaged-file handling.
- Performance: high on writer, query adapter, worker, and publisher hot paths.
- Released compatibility: none for v4; the format and SDK remain unreleased.
- Existing legacy C CLI: expected to be unaffected; validation must confirm no
  build or packaging coupling changed.

Sensitive data handling plan:

- Use only synthetic database paths, ranges, feed names, metadata, identities,
  fault cases, and benchmark inputs in durable artifacts.
- Do not record operational FireHOL infrastructure, credentials, access tokens,
  production feed names, literal operational ranges, customer/community data,
  personal data, private endpoints, or identifying IP addresses.
- Real-use `update-ipsets` replays, if explicitly authorized, record only
  sanitized aggregate counts, sizes, timings, and profiles.

Implementation plan:

1. **Activate and freeze the parity ledger.** Resolve SOW sequencing, mark
   SOW-0017 blocked on SOW-0027, inventory every Rust public export and normative
   operation against a compiled Go symbol, owner, test, platform, and benchmark,
   and record the exact API removals/additions before code changes.
2. **Consolidate one live writer.** Extend `internal/live.LiveWriter` over the
   existing `internal/writer.Core`; bind all advanced transactions and
   high-level workflows to the live writer; carry per-operation cancellation;
   migrate result/cleanup translations; then remove or privatize public
   `Create`, `OpenWriter`, `Writer`, and their duplicate transaction surface.
3. **Complete missing semantics.** Port membership import, one-inode immutable
   feed construction, commit resolution, bounded reclaim, clean-writer metadata
   mutation/read-your-writes, immutable/live mapped source adapters, and live
   direct-provider joins through the existing owners.
4. **Repair the public Go contract.** Convert every stable SDK failure to the
   exported error type, validate nil and enum-like inputs, remove per-callback
   result/name allocation where the documented contract requires reuse, and add
   external-package compilation/behavior tests that cannot import Go `internal`
   packages.
5. **Complete platform containment and security.** Implement and package the
   version-matched worker for every supported platform/architecture, port exact
   POSIX/Windows fault ownership, implement the specified macOS creator-only
   policy within the approved pure-Go boundary, and expose Windows housekeeping.
   Stop for a user decision before introducing cgo, assembly, another native
   boundary, weakening creator-only security, or dropping a specified platform.
6. **Replace conformance evidence.** Regenerate symmetric Go fixtures through
   live creation/writer/snapshot, add the missing producer and invalid cases,
   and build true Go-to-Rust and Rust-to-Go live subprocess harnesses covering
   the complete section-21 contract.
7. **Prove bounded performance.** Port the representative Rust benchmark matrix,
   add public-adapter allocation pins and necessary-work counters, profile every
   material delta, remove actionable waste, and record matched release evidence.
8. **Close the full artifact and review gate.** Update current SDK docs,
   conformance docs, affected specs/skills/instructions, retire misleading
   pending milestone reports, run the complete Go and Rust gates, obtain native
   platform evidence with explicit authorization, run the required four
   full-codebase mmap/I/O reviewers plus independent semantic/platform/API
   reviewers, resolve all P0-P2 findings, and complete SOW lifecycle records.

Validation plan:

- Generate a machine-checkable Rust-export/normative-operation to Go-symbol
  parity manifest and fail CI for missing or extra prohibited public writer
  operations.
- Run all Go tests under `nice`, with and without `v4work`, plus `-race`,
  `checkptr=2`, vet, formatting, supported cross-builds, mmap storage/runtime
  gates, worker protocol/crash gates, and external-package API tests.
- Run Rust architecture, mmap storage/runtime, source-graph, complete workspace
  tests, Clippy, formatting, documentation, MSRV, and affected sanitizer/Miri
  gates under the project-v4-rust workflow.
- Regenerate all Go and Rust conformance artifacts only through explicit public
  fixture-generation commands; verify both readers open and semantically compare
  every file and reject missing/extra/stale/malformed entries.
- Run true mixed-language live processes in both directions for the complete
  normative coordination/resolution matrix.
- Add deterministic scalar/property models for every newly live-bound Go
  transaction/workflow and compare results with Rust authority vectors.
- Add crash/fault matrices for commit outcome, reclaim, transitions,
  reservations, publication, validation, recovery, worker identity/protocol,
  SIGBUS, and Windows in-page errors.
- Add same-failure searches for sidecar-free public mutation, leaked internal
  errors, unvalidated enum conversions, nil dereferences, in-process faultable
  routing, unexported maintenance operations, and per-result adapter allocation.
- Run authorized native tests on Windows, macOS, and FreeBSD only after the user
  explicitly approves access to those systems. Cross-compilation is recorded as
  compilation evidence only.
- Before each performance command expected to exceed two wall-minutes or ten
  core-minutes, record the exact command, purpose, expected cost, and stop
  criteria here; run every build/test/benchmark under `nice`.
- At each implementation milestone, run four complete Go-codebase adversarial
  reviews: two for complete-page copies into/out of mappings and two for
  persistent content I/O outside mappings. Run independent API/semantic,
  platform/security, conformance, and performance reviews at the final gate.
- Final acceptance uses `.agents/skills/project-final-review/SKILL.md` and
  requires no open P0, P1, or P2 finding.

Artifact impact plan:

- AGENTS.md: update only if this work establishes a new durable parity,
  conformance, platform, or benchmark guardrail not already recorded.
- Runtime project skills: update `project-v4-rust` if the symmetric Go/Rust
  conformance or worker workflow becomes reusable project procedure; add no
  generic skill solely for framework completeness.
- Specs: preserve the current contract when implementation is brought into
  conformance; update specs only for a user-approved contract decision or a
  real ambiguity discovered during implementation.
- End-user/operator docs: update Rust README, Go package documentation, worker
  installation, platform support, public APIs, and conformance instructions to
  describe the delivered present state.
- End-user/operator skills: none currently exist; record whether any are added
  before close.
- SOW lifecycle: keep SOW-0027 open in `pending/` until SOW-0017 is paused,
  completed, or superseded by user decision. Preserve SOW-0025 and SOW-0026 as
  historical evidence of incorrect acceptance rather than rewriting their
  narratives. When activated, SOW-0017 must be marked blocked/paused on this
  prerequisite. Remove or relocate the unnumbered SOW-0025 milestone reports
  from `pending/` only with explicit user approval because deletion is outside
  this SOW's automatic authority.

Open-source reference evidence:

- No external open-source repository was used for this static parity finding.
  The exact local normative specifications and the two implementations are the
  authoritative evidence. Platform implementation planning must review official
  OS/runtime documentation and relevant open-source language-runtime patterns
  before code changes.

Open decisions:

1. **SOW sequencing — RESOLVED (2026-08-26, user decision A).**
   Pause SOW-0017 and activate SOW-0027. Signing decisions and implementation
   wait; no signed-format work builds on a false SDK premise. Options B and C
   recorded but not selected.

2. **Worker port scope — RESOLVED (2026-08-27, user decision 1-Option 1).**
   Full worker port to every supported OS/architecture: linux amd64+arm64,
   darwin amd64+arm64, freebsd amd64+arm64, windows amd64+arm64. New
   assembly is approved for the POSIX SIGBUS containment machines (the
   SOW stop condition is satisfied by this decision); the Windows arm uses
   the pure-Go vectored exception handler path (kernel32
   AddVectoredExceptionHandler through LazyDLL, no assembly).

3. **Native validation hosts — RESOLVED (2026-08-27, user decision 2).**
   Authorized: `costa-win11` (Windows housekeeping public surface + GC
   machine + worker in-page-exception containment), `plakam4mini` (darwin
   creator-only arm + live suites + worker SIGBUS containment),
   `freebsd` (immutable/offline reads + publication + worker SIGBUS
   containment). Native rounds run only over pushed commits.

Resolved decisions:

- The user requested a new SOW rather than modifying SOW-0025. SOW-0027 is the
  corrective tracker because the evidence shows an incorrect acceptance at the
  time of SOW-0025 completion, not a later removal of previously delivered
  behavior.
- The recommendation class is long-term-best: match the Rust authority and
  current specification; do not retain off-contract public compatibility for
  an unreleased SDK.
- The Go implementation remains pure Go with no cgo. If platform investigation
  proves the exact contract infeasible within that boundary, implementation
  stops and presents evidence-backed options to the user.

## Implications And Decisions

1. **New corrective tracker** — resolved by user request on 2026-08-26.
   SOW-0027 records the complete gap and preserves SOW-0025/SOW-0026 as
   historical acceptance evidence.
2. **Implementation order** — pending user selection from Open Decision 1.
   No production code may change under SOW-0027 until this is resolved and
   recorded.
3. **Contract direction** — resolved as long-term-best. Go converges on Rust and
   the normative specifications; specifications are not weakened to legitimize
   incomplete implementation.
4. **Pure-Go boundary** — resolved from SOW-0025. No cgo or new native boundary
   is introduced without a new explicit user decision supported by feasibility
   evidence.

## Plan

1. Resolve SOW sequencing and activate SOW-0027 without running two SOWs.
2. Freeze a complete public API/semantic/platform/evidence parity ledger.
3. Consolidate Go mutation behind the one sidecar-bound live writer.
4. Port every missing operation and repair public cancellation/error/input
   contracts.
5. Complete portable worker isolation, macOS creator-only security, and Windows
   housekeeping.
6. Replace asymmetric/off-contract fixtures and same-language subprocess smoke
   tests with the complete symmetric conformance proof.
7. Prove resource and performance parity on public `update-ipsets`-shaped
   workloads.
8. Update all affected artifacts and pass independent full-scope acceptance
   review before closing.

## Execution Log

### 2026-08-26

### Status (2026-08-26) - milestone 1 (parity ledger freeze) delivered

- Delivered in this commit: the parity ledger and its enforcing gate.
- `v4/go/parity_manifest.tsv` (81 curated rows, tab-separated:
  class, Rust reference, Go symbol, status, note) records every material
  Rust public operation and its Go symbol or missing status, organized by
  class: writer-offcontract (19 rows, remove-planned or missing), live-writer
  (21 rows), live-direct (7), reader (8), live-reader (4), cancellation (4),
  join (1), publication/scratch maintenance (7), snapshot (1), validation (1),
  recovery (4), worker (1), platform (1), cards/types (1). Statuses:
  present = the Go symbol exists and is the correct owner; remove-planned =
  the symbol exists but is off-contract and must disappear when the live
  writer consolidation lands; missing = the operation has no public Go
  equivalent (or only a wrong-owner off-contract one, recorded in the note).
- `v4/go/parity_gate_test.go`: the machine-checkable tripwire (SOW-0027
  acceptance: "fail CI for missing or extra prohibited public writer
  operations"). It parses the compiled root-package surface (go/parser, no
  subprocess) and fails when: a present/remove-planned symbol is absent; a
  missing row's symbol appears; or any public `Writer.*` method exists that
  is not recorded in the ledger (the off-contract writer surface is closed).
- `v4/go/parity_rust_public.tsv`: the regenerable raw Rust public export
  inventory (507 symbols) that the curated manifest maps; the uncurated
  long-tail rows are explicitly out of the gate's scope until later
  milestones record them.
- Ledger facts confirmed by the gate at this revision: 54 present/
  remove-planned, 32 missing. The gate additionally exposed two acceptance
  records that did not match reality, now recorded truthfully: the Go public
  surface has no `RemoveCheckpointedScratch` export (the internal recovery
  machine exists; SOW-0026's D record claimed the public export), and
  `Writer.DeleteFeed`/`Writer.RenameFeed` exist on the off-contract writer
  (live-owner equivalents absent).
- Battery: root plain and `-tags v4work` suites green (the gate runs in both),
  `go vet ./...` clean, `gofmt -l` empty for the new files, under `nice`.
- Next milestone 1 item: extend the ledger to the full 507-row Rust export
  inventory with name-variant resolution (the long tail), then milestone 2
  starts the live-writer consolidation with the removal list fixed by this
  ledger.


- User decision: activate SOW-0027 (option A), pause SOW-0017 as blocked on the
  unsigned-SDK prerequisite; recorded in Open Decision 1 and in SOW-0017.
- Lead re-verification of the static audit: every material claim confirmed
  against the code and the normative specs; evidence list recorded in the
  status sub-state above.
- Performed a read-only static parity audit at
  `07c0147bf18b4d1e976f460fc2624e90e91be8cc`.
- Reviewed the normative specifications, Rust public authority, all Go public
  production facades, relevant Go internal owners, shared corpus, producer
  paths, subprocess tests, completed SOW-0025/SOW-0026, and active SOW-0017.
- Created this pending corrective SOW at the user's request.
- No production code, specification, SDK documentation, skill, or active SOW
  was changed. No test or benchmark was run because the request was to record
  the completed static analysis.
- Ran `nice .agents/sow/audit.sh`. It validates SOW-0027 as `open` under
  `pending/`, the pre-implementation gate, regression-section placement,
  sensitive-data guardrail, and project-skill structure. The overall audit
  remains partial because the pre-existing SOW-0025 document has no canonical
  top-level status block and is reported as `Status: ###` in `done/`; this SOW
  records that historical artifact but does not rewrite it.
- Checked the new file for template placeholders, personal names, and trailing
  whitespace with `rg`; none was found.

## Validation

Acceptance criteria evidence:

- Pending implementation. The static gap evidence and intended proof conditions
  are recorded above.

Tests or equivalent validation:

- Static source/specification analysis only. No dynamic test was required to
  create this tracker, and no test result is claimed.
- `nice .agents/sow/audit.sh`: SOW-0027 status/directory and required gates pass;
  repository verdict remains partial only for the pre-existing SOW-0025 status
  layout described in the execution log.
- New-file hygiene scan for template placeholders, personal names, and trailing
  whitespace: pass.

Real-use evidence:

- Pending. No runtime path was exercised for SOW creation.

Reviewer findings:

- The originating static review returned FAIL. Material findings are preserved
  in Requirements, Analysis, and the Pre-Implementation Gate.

Same-failure scan:

- The static audit searched the complete compiled Go public production surface
  for Rust-equivalent writer, import, immutable-feed, commit-resolution,
  reclamation, metadata, join-source, worker, platform, housekeeping, and
  conformance operations. Implementation-time same-failure scans remain
  pending.

Sensitive data gate:

- This SOW contains repository paths, public API names, synthetic contract
  descriptions, and a public source revision only. It contains no secrets,
  credentials, bearer tokens, SNMP communities, community member names,
  customer names, personal data, customer-identifying IP addresses, private
  endpoints, or proprietary incident details.

Artifact maintenance gate:

- AGENTS.md: unchanged; SOW creation does not change project-wide workflow.
- Runtime project skills: unchanged; `project-v4-rust` already contains the
  relevant authority and proof workflow.
- Specs: unchanged; the current specs are evidence for the implementation gaps,
  not the source of the discrepancy.
- End-user/operator docs: unchanged at SOW creation; affected stale docs are
  explicitly required implementation artifacts.
- End-user/operator skills: none exist.
- SOW lifecycle: SOW-0027 is `Status: open` under `pending/`; SOW-0017 remains
  the sole in-progress SOW pending the sequencing decision; SOW-0025 and
  SOW-0026 remain historical records.

Specs update:

- No update at SOW creation: implementation must conform to the current specs.

Project skills update:

- No update at SOW creation: no new proven reusable workflow exists yet.

End-user/operator docs update:

- Pending implementation. Rust README, Go package docs, worker deployment,
  platform support, and conformance documentation are in scope.

End-user/operator skills update:

- None currently exist; reassess before close if documentation changes create a
  downstream operator workflow.

Lessons:

- Completion evidence must be generated through the normative public path. An
  immutable file that cross-opens does not prove that live mutation,
  coordination, recovery, or failure semantics match.
- A parity ledger must map every required public operation to a compiled symbol,
  test, platform proof, and benchmark before a port can be accepted.

Follow-up mapping:

- All verified parity gaps are owned by this SOW.
- Snapshot signing remains tracked by SOW-0017 and must remain blocked until
  this SOW completes.
- No untracked deferred implementation item is accepted at SOW creation.

## Outcome

Pending.

## Lessons Extracted

Pending implementation and final review.

## Followup

- SOW-0017: snapshot signing remains dependent on accepted completion of this
  unsigned SDK parity SOW.

## Regression Log

None yet. This SOW records an incorrect prior acceptance, not a later regression
of behavior that was previously implemented and proven.
