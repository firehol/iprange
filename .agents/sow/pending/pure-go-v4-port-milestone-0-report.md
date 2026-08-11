# SOW-0025 — Milestone 0 Report: exact gap and replacement map

Date: 2026-08-11. Status: read-only planning complete. No production edit made.
Owns: SOW-0025 (`.agents/sow/pending/SOW-0025-20260811-pure-go-exact-v4-port.md`).

## 1. Executive summary

- The current Go tree is a self-contained, mutually consistent experiment, not a
  conforming current-format implementation. Numbers measured here match the SOW
  planning claims exactly: 50 production files / 44,088 newline-counted lines,
  59 test files / 37,403 lines, `go test ./...` + `go vet ./...` + `gofmt -l .`
  all green at HEAD `972960a`.
- The old tree has no conformance coupling (no `cases.json` / `.iprdb` fixture
  reference in any Go file), no structured values, no public reader/writer/
  workflow constructors, a stale `retention` tag, a stale 1 MiB metadata limit,
  and a rejected positional-I/O + in-heap-page architecture (3 files with
  prohibited content-transfer calls, 24 files with complete 4 KiB page arrays).
- Exactly five production files carry scalar logic worth transferring after
  literal-vector verification: `errors.go` (with one documented code-46
  divergence and codes 65–69 to add), `key.go`, `cardinality.go`, `page.go`,
  `name_binding.go` (plus four test files). `process_identity.go` was
  initially kept and is corrected here to delete: it implements a
  PID/process-start stale-slot recovery model that the current format spec
  explicitly excludes.
- Projected pure-Go production size: a rough estimation prior of ~32–44k
  newline-counted lines (~37k midpoint), budgeting only. It is NOT a target
  and the actual size is unknown until implementation: `iprange-livedb` alone
  is 82,516 production lines, and every area must justify its real size from
  implemented, measured code at its milestone close.
- Two decisions are requested from the user at the end of this report: the
  exact deletion set (100 tracked files) and the fault-worker native-boundary
  policy.

## 2. Baseline commands and factual results (HEAD 972960a, clean tree)

```
go test ./...            exit 0   (root cached, internal/exactv4 5.600s)
go vet ./...             exit 0
gofmt -l .               (empty)
git status               clean
```

Tracked `v4/go`: 111 files = 3 root production + 47 `internal/exactv4/`
production + 59 test + `go.mod` + `go.sum`. `go.mod`:
`module github.com/firehol/iprange/v4/go`, `go 1.23.0`,
`require golang.org/x/sys v0.35.0` (preserve as the support floor per SOW).

Untracked residue: `v4/go/exactv4.test` (15.8 MB compiled test binary,
gitignored by `.gitignore:43`); empty directory `v4/go/exactv4/`.
The `allocator_reader_alloc_linux.go` file named in earlier planning does not
exist (only its `_test.go` sibling).

## 3. Read-only inventory

### 3.1 Go tree (all backed by file:line citations in the inventory)

- Package-level exported declarations: 199 total across 7 files (rule:
  top-level `type/func/var/const` declarations plus exported const-block
  members). Root public package: `errors.go` 66, `types.go` 22 (the only true
  public SDK surface). Internal `exactv4` package: `contract.go` 42,
  `bootstrap.go` 39, `page.go` 18, `cardinality.go` 9, `key.go` 3. 43 of 50
  production files export nothing.
- Missing public surfaces: no reader, writer, workflow, query, validation,
  recovery, snapshot, publication, or live-lifecycle constructor exists.
- Wire constants: current magics `IPRANGE4`, `IP4P`, `IPR4RSV1`
  (`reservation.go:9`, `binary-format-v4.md:3569`); the sidecar magic
  `IPR4RDRS` is stale (spec: `IPRDRS4\0`); zero v3 references anywhere in
  `v4/go`.
- Stale contract facts: `RetentionTag()` at `contract.go:98-101` +
  `types.go:34`; `MaxMetadataUncompressed = 1_048_576` (1 MiB) at
  `contract.go:17` (current contract: 20 MiB); `ValueKind` has only
  Direct/Membership (no Structured); no `StructureKind`, no structure meta
  fields, no `NetworkEnrichment*`, `threat`, or `structured` identifiers.
- Error codes: `errors.go` defines codes 1–64 whose names match the current
  Rust codes 1–64 in every position except code 46: Go
  `ErrorLiveCoordinationDomainMismatchRequiresReset` (`errors.go:54`) vs Rust
  `LiveCoordinationMalformedRequiresReset` (`sdk_error.rs:58`). Codes 65–69
  (FaultWorkerUnavailable, FaultWorkerFailed, UnsupportedStructure,
  WrongStructureKind, StructureIdExhausted) are missing. Transfer requires
  resolving code 46 to the Rust name/semantics and adding 65–69.
- Prohibited content-transfer calls: 3 files — `os_linux.go:503` `ReadAt`,
  `os_linux.go:565` `WriteAt`, `page_source_linux.go:27` `unix.Pread`,
  `page_source_other.go:26` `File.ReadAt`. `Truncate`/`Sync` are required
  lifecycle/durability syscalls, NOT prohibited content I/O
  (`os_linux.go:1495-1497/1855-1857/2267-2269`, `live_writer_linux.go:650-658`).
  Complete-page arrays in 24 files (e.g. `os_linux.go:544` two whole meta
  pages in a heap array, `private_page_pool.go:99`, `bitmap_cow.go:261-262`,
  `retirement_writer.go` 8 sites). Everything reads/writes through a
  `committedPageSource → [PageSize]byte` copy contract plus an in-heap private
  page pool — precisely the architecture the format contract prohibits.
- Sidecar layout is stale: Go `sidecarMagic = "IPR4RDRS"` with 64-byte slots
  carrying `processID`/`processStart` (`sidecar.go:11,15,394-396`) vs spec
  `IPRDRS4\0` with 16-byte dual-txn-complement slots and explicit "no PID,
  process-start token, thread ID, claim nonce, transition state, or slot
  checksum" (`binary-format-v4.md:2104,2130-2138`).
- Tests: all 59 are self-contained (temp files); none reference
  `v4/conformance/cases.json` or any committed `.iprdb`.
- The 47 internal production files group into: wire foundation (7),
  bitmap/allocator family (8), page pool/index/range tree (13),
  retirement/sidecar/reservation/live (12), writer fixed-point contracts (7).

### 3.2 Rust reference surface (accepted authority)

- Crate-root re-export list is fully enumerated (73 `pub use` lines, ~190
  public names across 27 modules; exact list recorded in the inventory).
  Reader surface (ImmutableReader/LiveReader), writer surface (LiveWriter +
  Direct/Membership/Structured transactions), 7 workflow entry points +
  Prepared/Finished convergence, membership queries/joins/algebra, structured
  values (NetworkEnrichmentV1), metadata, snapshot, validation (13 public
  names), recovery (20+ public names), publication (~35 public names),
  lifecycle (14 public names), commit resolution, worker contract.
- Production size: `iprange-livedb/src` = 82,516 newline-counted lines
  excluding `*test*` files (99,415 including them); `iprange-capi` = 15,391.
- Semantic tests that pin behavior: 40+ unit test files (arrival-order
  normalization, zero-alloc lookups, crash injection, literal codec vectors,
  metadata exactness, dictionary/blob/retirement edge cases) and 34 integration
  tests incl. `tests/conformance.rs` (corpus verify + `#[ignore]`d regeneration).
- Accepted benchmark baseline (i9-12900K, Rust 1.91.1): full table quoted in
  the inventory; headliners: direct replacement 1M ranges 0.262 s,
  publisher-shaped workflow (13.6M units) 1.217 s, random direct point
  0.223 s live / 0.202 s immutable, structured scalar lookup 2.214 M/s live /
  2.945 M/s immutable.

### 3.3 Spec and conformance facts

- `binary-format-v4.md` (4,105 lines) fully inventoried into 48 numbered
  normative contracts (constants, meta layout with exact offsets, page header
  at CRC offset 28, 21 page types, range/catalog/membership/structured/blob/
  metadata/bitmap/retirement codecs, sidecar layout, strong durability rules,
  validation/recovery/worker contracts, publication/reservation, §21
  conformance). `design-iprange-engine.md` (519 lines) inventoried into 16
  architecture requirements. `c-abi-v4.md`: C ABI is Rust-exported only —
  out of scope for Go; same error/reason codes remain the parity surface.
- Conformance corpus: `cases.json` schema 2, five committed Rust fixtures
  (16,384 / 16,384 / 36,864 / 40,960 / 53,248 bytes — all page multiples,
  mode 0600) + three invalid mutations (wrong-magic, short, unaligned) all
  expecting `ErrorCode::FormatInvalid`. Rust `verify.rs` performs 9 exact check
  groups: corpus inventory, open+info, metadata states (absent/empty/text/
  repeat), explicit validation zero-findings, canonical range/membership/
  structure checks, full-IPv6 cardinality strings, boundary±1 point probes,
  semantic (not ID) value comparison, invalid-case rejection.
- `v4/conformance/README.md` defines the cross-language gate: Go-produced
  files added to the same manifest; both readers open and verify both producer
  sets; malformed transformations produce equivalent public errors; mixed
  Rust/Go subprocess tests in both directions for reader slots, writer
  exclusion, reclamation, stale-slot cleanup, sidecar replacement, and
  reservation/live-transition resolution.

## 4. Parity matrix: Rust public surface → proposed Go API

Status legend: **T** = transfer scalar content, re-verify with literal vectors
before use; **R** = rewrite (fresh implementation of the same semantics);
**N** = new (no antecedent in the Go tree). Go naming is idiomatic; semantics
are exact. Cancellation: Go `context.Context` with explicit checkpoints mapping
to `ErrorCancelled` (27). Sinks: narrow Go interfaces/callbacks whose returned
error supports the `ErrStoppedBySink` contract.

| # | Rust surface (accepted names) | Proposed Go API | Location | Status |
|---|---|---|---|---|
| 1 | `Ipv4Key`, `Ipv6Key` (hi/lo) | `IPv4`, `IPv6` (existing types, numeric hi/lo order) | root | T |
| 2 | `Cardinality129`, `CardinalityOverflow`, `ipv4/ipv6_inclusive` | `Cardinality129`, `ErrCardinalityOverflow`, `IPv4Inclusive`, `IPv6Inclusive` | root | T |
| 3 | `ValueTag`, `AddressFamily`, `ValueKind`, `StructureKind`, `DirectSemantic`, `MembershipOperation`, `MAX_METADATA_UNCOMPRESSED` | same names; `ValueKindStructured=3`, `StructureKindNetworkEnrichmentV1=1` added | root + `internal/format` | R (constants from spec; only scalar tag logic transfers) |
| 4 | `ErrorCode` 1–69, `Error`, `Result` | `ErrorCode uint32` 1–69: 1–64 transfer except code 46 (Go `ErrorLiveCoordinationDomainMismatchRequiresReset` → Rust `LiveCoordinationMalformedRequiresReset`), add 65–69; `Error{Code,Detail,Cause}`; Go `error` | root | T+R |
| 5 | `MetaSelection` (`ProvenCurrent`, `SoleMeta0/1`) | `MetaSelection` enum | `internal/format` | R |
| 6 | `DatabaseInfo` (11 fields + `meta_selection`, `direct_semantic()`) | `DatabaseInfo` struct + `DirectSemantic()` | root | N |
| 7 | `ImmutableReader`: `open`, `info`, `lookup_direct_v4/v6`, `direct_cursor_v4/v6` (`seek`, `next_range`), `lookup_feed`, `feed_cursor`, `feed_range_cursor_v4/v6`, `named_feed_source_v4/v6`, `direct_range_source_v4/v6`, `lookup_membership_v4/v6`, `lookup_network_enrichment_v1_v4/v6`, `network_enrichment_v1_cursor_v4/v6`, `membership_query`, `metadata_json_len/read_metadata_json/metadata_json` | `ImmutableReader` with the same method set (family parameterized or `...V4/V6` variants per Rust) | root facade over `internal/reader` | N |
| 8 | `LiveReader`: same surface + `open(path, cancellation)`, `close() -> ReaderCloseResult` | `LiveReader` + `Close() (ReaderCloseResult, error)` | root facade | N |
| 9 | `MembershipView`: `word_count`, `word`, `read_words`, `contains_index` | `MembershipView` methods | root/internal | N |
| 10 | `RangeDirection {Forward, Reverse}` | `RangeDirection` enum + `DirectRange{From,To,Value}` | root | N |
| 11 | `RangeSource` trait, `SliceSource`, `DirectRangeSourceV4/V6`, `FeedRangeSourceV4/V6` | generic `RangeSource[R]` interface, `SliceSource[R]`, source adapters | root | N |
| 12 | `LiveWriter`: `open(path, budget, cancellation)`, `commit`, `close`, `abort`, `reclaim`, metadata get/set/clear | `LiveWriter` same surface; `TransactionBudget` struct | root facade over `internal/writer` | N |
| 13 | `create_live(path, family, value_kind, structure_kind, value_tag, reader_capacity, cancellation) -> CreateResult` | `CreateLive(...)` + `CreateResult`/`CreationState` | root | N |
| 14 | `DirectTransaction`: `assign_v4/v6`, `clear_v4/v6`, metadata, `commit/abort` | `DirectTransaction` | root facade | N |
| 15 | `MembershipTransaction`: `feed_cursor`, `empty_membership`, `add_feed`, `apply_v4/v6`, `lookup_feed`, `ensure_feed`, `rename_feed`, `delete_feed`, metadata, `commit/abort` | `MembershipTransaction` + `FeedRef`/`MembershipRef` opaque generation-bound handles | root facade | N |
| 16 | `StructuredTransaction`: membership ops + `intern_network_enrichment_v1(value, membership) -> StructureRef`, `assign_v4/v6`, `clear_v4/v6` | `StructuredTransaction` + `StructureRef` + `InternNetworkEnrichmentV1` | root facade over `internal/structured` | N |
| 17 | `begin_direct_replacement`, `begin_first_seen_refresh`, `begin_last_seen_refresh` (incl. `finish_input_with_removals_v4/v6`) | `DirectReplacement`, `FirstSeenRefresh`, `LastSeenRefresh` | root facade | N |
| 18 | `begin_create_feed`, `begin_replace_feed` | `CreateFeed`, `ReplaceFeed` | root facade | N |
| 19 | `begin_membership_import(source, cancellation)` (`MembershipImportSource::{Immutable,Live}`) | `MembershipImport` + `MembershipImportSource` | root facade | N |
| 20 | `project_history(source, cutoffs, cancellation)`; `HistoryWindow`, `HistoryWindowReport`, `HistoryProjectionReport` | `ProjectHistory` + windows/reports | root facade | N |
| 21 | `FinishedWorkflow`, `PreparedWorkflow`, `PreparedFeedChange`, `PreparedHistoryProjection` (report/set/clear metadata/commit/abort), `WorkflowReport`, `LogicalChange`, `WorkflowKind`, `FirstSeenRemoval` + sink | same convergence with Go names | root | N |
| 22 | `delete_feed`, `rename_feed` (direct on LiveWriter) | `DeleteFeed`, `RenameFeed` | root | N |
| 23 | `FeedName`, `FeedEntry`, `FeedCursor` | `FeedName` (validated ≤255), `FeedEntry`, `FeedCursor` | root | N |
| 24 | `MembershipQuery`, `MembershipScope`, `matching_feeds_v4/v6`, `feed_count`, `feeds`, `aggregate`, `join_direct`, `join_membership` + sinks/reports | `MembershipQuery`, `MembershipScope`, matching/aggregate/join with sink interfaces; reports structs | root facade over `internal/query` | N |
| 25 | `MembershipAlgebra::new/count/compare/publish_set`, `FeedSelection`, `AlgebraSetOperation`, budgets, reports | `MembershipAlgebra` (`Count`, `Compare`, `PublishSet`) | internal/query | N |
| 26 | `NetworkEnrichmentV1`, `NetworkEnrichmentV1Location`, `NetworkEnrichmentV1View` (`value()`, `threat_membership()`), cursors/ranges | same types; `Location *NetworkEnrichmentV1Location`; `View.Value()` + `ThreatMembership()` | root + `internal/structured` codec | N |
| 27 | `snapshot_to(mode, budget, policy, cancellation)` | `SnapshotTo(...)`, `SnapshotBudget`, `SnapshotPublicationPolicy`, `SnapshotResult` | root facade | N |
| 28 | `create_immutable_feed_v4/v6` | `CreateImmutableFeed(..., family, ...)` | root facade | N |
| 29 | `validate`, `ValidationMode`, `ValidationBudget`, `ValidationFinding`, `ValidationSink`, `ValidationResult/Failure`, 47 `ValidationReason`, 17 `ValidationObject` | `Validate(...)`, same names; exact reason/object codes | root facade over `internal/validation` | N |
| 30 | `inspect_recovery_candidates`, `recover_immutable/live/offline`, `RecoveryBudget`, `RecoveryCandidate(Inspection)`, sinks/reports, `RecoverySourceCleanupGuard`, scratch maintenance | same semantics | root facade over `internal/recovery` | N |
| 31 | `resolve_publication`, `inspect/remove_publication_residue`, `PublicationPolicy/Result/Status`, abandoned-artifact listing/removal | `ResolvePublication`, residue + maintenance | root facade over `internal/publication` | N |
| 32 | `initialize_live`, `reset_live_coordination`, `resolve_create_live`, `resolve_live_transition`, `resolve_interrupted_live_transition`, `resolve_commit` | same entry points | root facade over `internal/live` | N |
| 33 | worker binary `iprange-v4-worker`, build-ID handshake, control map, POSIX/Windows fault handlers | `cmd/iprange-v4-worker` building exactly `iprange-v4-worker`; same handshake/fault contract | `cmd/` + `internal/worker` | N |
| 34 | `CommitDurability`, `CommitResult`, `CommitCleanupArtifacts`, `AbortOutcome/Result`, `CloseOutcome/Result`, `ReclaimResult` | same outcome/evidence structs (whole-draft-or-abort; OutcomeUnknown + exact resolution) | root | N |
| 35 | `CommitResolutionMode/Result`, `LocalFileRelation` | `ResolveCommit` + result | root | N |

Non-goals (parity excluded): the Rust C ABI (`iprange-capi`, 168 symbols), v3
compatibility, signing (SOW-0017), byte-identical file output.

## 5. Proposed Go API/module graph

Module path and public package identity stay: `github.com/firehol/iprange/v4/go`,
package `iprangedb` at module root. One internal reader core, one internal
writer core, separate narrow owners for validation/recovery, live
coordination, and publication — matching the design spec's authority map.

```
v4/go/
  go.mod, go.sum                     (keep; floor go 1.23, x/sys v0.35.0)
  doc.go                             (rewrite: SDK doc)
  errors.go                          (transfer + codes 65-69)
  types.go                           (rewrite: scalars, ValueKind+Structured,
                                      StructureKind, tags, key/cardinality types)
  reader.go  writer.go  workflow.go  (public facades: types + thin sequencing)
  query.go   metadata.go  source.go  budget.go
  snapshot.go validation.go recovery.go publication.go lifecycle.go
  internal/format     constants, meta, page header+CRC32C, 21 page types,
                      literal encoders/decoders, value tags      [~5-7 files]
  internal/mapping    Mapping owner: file-backed views, flush/sync/truncate/
                      tail, identity, symlink rejection, fault-region
                      registration; platform files linux/darwin/windows/freebsd
  internal/reader     ReaderCore + GenerationReader, cursors, feed catalog,
                      membership lookup, structured lookup      [~6-9 files]
  internal/writer     DraftStore + WriterCore: final-offset COW mutation,
                      free/used bitmaps (lowest-take), retirement extents,
                      commit-time CRC sealing, meta publication, abort/reclaim
  internal/dict       membership dictionary manager (interning, hash tree,
                      refcounts, blobs)
  internal/structured common structured manager + NetworkEnrichmentV1 codec
                      (lazy threat membership)
  internal/workflow   advanced transactions + typed workflows (sequencing only)
  internal/query      membership queries, aggregation, joins, algebra
  internal/metadata   chunked zlib chain (absent/empty/present states, 20 MiB)
  internal/live       sidecar codec (IPRDRS4, 16-byte slots), OFD/handle locks,
                      reader table, lifecycle transitions, namespace identity,
                      process identity; freebsd gate returns code 44 before
                      path access on every live entry
  internal/publication private outputs, reservation, resolver, residue,
                      maintenance (per-spec private names)
  internal/validation validation engine + worker client; 47 reasons, 17 objects
  internal/recovery   candidate inspection, classify, build, scratch, external
                      sort (bounded, authorized only)
  internal/worker     control map, POSIX/Windows fault handlers, wire codecs,
                      launcher, build-ID handshake
  cmd/iprange-v4-worker/   main.go (exact binary name)
```

Rules carried from the design spec: no page view outlives its operation; no
complete page in heap/stack/cache; mmap-only persistent content; open never
validates; budgets everywhere; `name_binding` content transfers after
verification; any process-identity or slot-state code is fresh work under the
current no-PID-in-slot contract (`binary-format-v4.md:2130-2138`).

## 6. Current-file classification

### 6.1 Production files

| File | Bucket | Evidence / rationale |
|---|---|---|
| `errors.go` (root) | T | Codes 1–64 match current Rust names except code 46 (`ErrorLiveCoordinationDomainMismatchRequiresReset` vs Rust `LiveCoordinationMalformedRequiresReset`); add 65–69. No I/O, no page arrays. |
| `key.go` | T | Numeric hi/lo IPv6 key order + Next/Previous match current `Ipv6Key`/`Ipv4Key`. No I/O. |
| `cardinality.go` | T | 129-bit arithmetic matches spec §17. No I/O. |
| `page.go` | T | PageHeader codec + CRC-32C at offset 28, current `IP4P` magic; verify offsets against literal vectors. |
| `name_binding.go` | T | SHA-256 `IPR4NAME` commitment matches spec §3.8; verify encoding_kind 1/2. |
| `key_test.go`, `cardinality_test.go`, `page_test.go`, `name_binding_test.go` | T | Cover only transferable scalar logic; re-verify. |
| `contract.go` | D | Stale `RetentionTag`, stale 1 MiB limit (`contract.go:17`), no Structured kind/fields, page-array encoder — rewrite from spec (constants only). |
| `process_identity.go` | D | Implements PID/process-start stale-slot recovery (`process_identity.go:15`, `activeSlot` with `processID`/`processStart` at `sidecar.go:394-396`), a model the current spec explicitly excludes: slots carry no PID, process-start token, thread ID, claim nonce, transition state, or slot checksum; ownership is the lifetime byte-range lock (`binary-format-v4.md:2130-2138`). Its parser test goes with it; any process-identity need of the new worker design is fresh code. |
| `bootstrap.go`, `types.go`, `doc.go` (root) | D | Rewrite in new layout; concepts transfer, files do not. |
| `contract_test.go`, `types_test.go` | D | Pin stale semantics (RetentionTag, 1 MiB, 2-kind enum). |
| `page_source.go`, `page_source_linux.go`, `page_source_other.go` | D | Positional `unix.Pread`/`ReadAt` + whole-page copy contract — prohibited architecture, replaced by `internal/mapping`. |
| `bitmap_page.go`, `bitmap_reader.go`, `bitmap_insert.go`, `bitmap_cow.go`, `bitmap_finalize.go`, `bitmap_reservation.go` | D | In-heap COW ledger + `[PageSize]byte` arrays; replaced by mapped writer bitmaps. |
| `blob_page.go`, `blob_reader.go` | D | Readers over the removed page-source contract. |
| `private_page_pool.go` | D | Complete pages in heap — the exact prohibited pattern (`:99`). |
| `page_number_index.go`, `range_builder.go`, `range_ownership_walk.go`, `range_page.go`, `range_payload.go`, `range_pool_sink.go`, `range_reader.go`, `range_root_proof.go`, `range_staging.go` | D | In-heap range-tree build/read machinery; codec knowledge feeds new `internal/format` modules. |
| `retirement_page.go`, `retirement_plan.go`, `retirement_reader.go`, `retirement_writer.go` | D | In-heap private arena + page arrays; replaced by mapped retirement in writer core. |
| `sidecar.go`, `sidecar_transition.go`, `reservation.go` | D | Stale slot protocol: magic `IPR4RDRS` vs spec `IPRDRS4` (`sidecar.go:11`), 64-byte slots with PID/process-start vs spec 16-byte dual-txn-complement slots; reservation magic `IPR4RSV1` is current but serves the stale active-slot design. |
| `live_reader_linux.go`, `live_writer_linux.go`, `os_linux.go` | D | Linux-only positional I/O + flock architecture (prohibited `ReadAt`/`WriteAt` content calls, whole meta pages in stack; `Truncate`/`Sync` themselves are required lifecycle ops, but the file's architecture is still replaced by the mapping owner). |
| `sequential_assignment.go` | D | Serves the removed pool architecture. |
| `writer_cleanup_contract.go`, `writer_resource_contract.go`, `writer_transaction_core.go`, `writer_workspace.go`, `writer_fixed_point.go`, `writer_fixed_point_aggregate.go`, `writer_fixed_point_authority.go`, `writer_result_contract.go` | D | Fixed-point/private-pool writer architecture; superseded by `WriterCore` over `DraftStore`. |
| remaining 55 test files (incl. `process_identity_test.go`) | D | Tied to deleted internals or the stale slot model; behavioral models (arrival-order normalization, codec vectors, crash points) are re-created in new tests. |

### 6.2 Same-failure / duplicate-authority searches

- Prohibited content-transfer calls: 3 files; complete-page arrays: 24 files;
  stale-format constants: `retention` ×2 files, 1 MiB limit ×1, v3 ×0;
  structured/threat/NetworkEnrichment references ×0; conformance/corpus
  references in any Go file ×0; stale-slot process model: `process_identity.go`
  + `activeSlot` (`sidecar.go:394-396`, PID/process-start at slot offsets
  16/24) — rejected by the current spec. Each class is searched
  repository-wide in the future port gates, not just at the cited examples.
- Authority within the old Go tree: one read boundary (`committedPageSource`)
  and one write machinery (fixed-point + private pool) exist, so the old tree
  is internally single-authority — but that authority design is the rejected
  one. The new tree adds a `check-go-architecture.sh` (mirror of the Rust
  `check-architecture.sh`) to enforce owner boundaries mechanically.

## 7. Deletion approval list (exact tracked set)

Proposed tracked deletions: **100 files** = 45 production + 55 test files.

- Production (45): every §6.1 file marked D — `contract.go`, `bootstrap.go`,
  `process_identity.go`, root `types.go`, `doc.go`; `page_source.go`, `page_source_linux.go`,
  `page_source_other.go`, `bitmap_page.go`, `bitmap_reader.go`,
  `bitmap_insert.go`, `bitmap_cow.go`, `bitmap_finalize.go`,
  `bitmap_reservation.go`, `blob_page.go`, `blob_reader.go`,
  `private_page_pool.go`, `page_number_index.go`, `range_builder.go`,
  `range_ownership_walk.go`, `range_page.go`, `range_payload.go`,
  `range_pool_sink.go`, `range_reader.go`, `range_root_proof.go`,
  `range_staging.go`, `retirement_page.go`, `retirement_plan.go`,
  `retirement_reader.go`, `retirement_writer.go`, `sidecar.go`,
  `sidecar_transition.go`, `reservation.go`, `live_reader_linux.go`,
  `live_writer_linux.go`, `os_linux.go`, `sequential_assignment.go`,
  `writer_cleanup_contract.go`, `writer_resource_contract.go`,
  `writer_transaction_core.go`, `writer_workspace.go`,
  `writer_fixed_point.go`, `writer_fixed_point_aggregate.go`,
  `writer_fixed_point_authority.go`, `writer_result_contract.go`.
- Tests (55): every `*_test.go` tracked file except the four transfer tests —
  i.e. all of: `allocator_reader_alloc_linux_test.go`,
  `bitmap_cow_adversarial_test.go`, `bitmap_cow_scoped_test.go`,
  `bitmap_cow_test.go`, `bitmap_finalize_preview_test.go`,
  `bitmap_finalize_released_free_test.go`, `bitmap_finalize_test.go`,
  `bitmap_page_test.go`, `bitmap_reader_test.go`,
  `bitmap_reservation_all_bound_compat_test.go`,
  `bitmap_reservation_insert_test.go`,
  `bitmap_reservation_late_binding_test.go`, `blob_page_test.go`,
  `blob_reader_test.go`, `bootstrap_test.go`, `contract_test.go`,
  `live_reader_linux_test.go`, `live_writer_linux_test.go`,
  `os_linux_test.go`, `page_number_index_test.go`, `page_source_test.go`,
  `private_page_compat_test.go`, `private_page_pool_binding_test.go`,
  `private_page_pool_test.go`, `process_identity_test.go`,
  `race_disabled_test.go`,
  `race_enabled_test.go`, `range_builder_test.go`,
  `range_ownership_walk_test.go`, `range_page_test.go`,
  `range_payload_test.go`, `range_pool_sink_test.go`,
  `range_reader_test.go`, `range_root_proof_test.go`,
  `range_staging_test.go`, `reservation_test.go`,
  `retirement_blob_index_test.go`, `retirement_fixed_point_test.go`,
  `retirement_page_test.go`, `retirement_reader_test.go`,
  `retirement_writer_scope_test.go`, `retirement_writer_test.go`,
  `sequential_assignment_test.go`, `sidecar_test.go`,
  `sidecar_transition_test.go`, `terminal_journal_test.go`,
  `writer_cleanup_contract_test.go`, `writer_fixed_point_aggregate_test.go`,
  `writer_fixed_point_authority_test.go`, `writer_fixed_point_core_test.go`,
  `writer_fixed_point_test.go`, `writer_resource_contract_test.go`,
  `writer_result_contract_test.go`, `writer_transaction_core_test.go`,
  `types_test.go`. (This includes the linux-tagged tests and the
  `race`/`!race` gate pair, both tied to the removed architecture.)
- Kept tracked (11): `go.mod`, `go.sum`, `errors.go`, `key.go`,
  `cardinality.go`, `page.go`, `name_binding.go` + their four tests
  (`key_test.go`, `cardinality_test.go`, `page_test.go`, `name_binding_test.go`).
  These are transfer candidates: content is re-verified against literal
  vectors and the current contract in Milestone 1 *before* it is used; paths
  may move into the new package layout.
- Untracked cleanup (listed for approval together with the deletion set —
  repository rules require explicit consent before deleting any file,
  tracked or not): `v4/go/exactv4.test` (15.8 MB compiled test binary),
  empty directory `v4/go/exactv4/`.

Transfer content is never discarded: it moves into the new layout only after
verification. Deletion here removes obsolete tracked paths, not evidence.

## 8. Risk register

1. **Pure-Go worker fault ownership (highest).** Go's `signal.Notify` delivers
   SIGBUS without `si_addr`; the runtime's own handler cannot be chained to
   without runtime internals (prohibited). Exact in-region claiming + prior-
   disposition chaining may require a minimal project-owned assembly sigaction
   shim (not cgo). Decision requested in §10; Milestone 1 must produce a
   feasibility proof either way.
2. **Windows fault isolation.** Vectored `EXCEPTION_IN_PAGE_ERROR` via
   `syscall.NewCallback` appears pure-Go feasible but is unproven; failure
   mode is a crash-contract gap, not data loss.
3. **Mapped-view lifetime in Go.** No borrow checker: escaping page views or a
   racing `Close`/remap can read stale pages. Mitigation: enforcement by API
   shape (no view outlives its operation), checked offsets, and runtime gates;
   documented in the mapping owner contract from Milestone 1.
4. **Boundary reversal during reset.** Deleting 100 files and rebuilding is
   the intended reset, but the milestone-1 immutable reader must land first
   with the corpus cross-open passing, so a buildable conforming state exists
   before further milestones.
5. **Zero-allocation hot paths under GC.** Warmed lookups/cursors must not
   allocate; Go requires preallocated cursor/buffer plumbing and custom hash
   tables over mapped memory (avoiding per-op map/GC traffic). Verified with
   `-benchmem` + allocation counters from the start; not deferred.
6. **5–10% performance band.** Dominant costs (range-tree descent, CRC32C,
   memcpy, binary search) map to format work and Go's stdlib CRC32C is
   hardware-accelerated; the band is plausible but must be measured per
   operation with matched profiles before any CI gate.
7. **FreeBSD live gate.** Every live entry must return error 44 before any
   path access; implemented as dedicated `*_freebsd.go` files so the gate
   cannot be omitted by a build tag mistake.
8. **Error/reason-code drift.** Already materialized once: old Go code 46
   `ErrorLiveCoordinationDomainMismatchRequiresReset` vs Rust
   `LiveCoordinationMalformedRequiresReset`. 69 error codes, 47 validation
   reasons, 17 objects must stay 1:1 with Rust; one table per language +
   literal-vector tests, both cross-checked in conformance.
9. **Big-endian proof.** Little-endian-only hosts; a big-endian target
   (s390x cross-compile + emulator, or authorized hardware) is required by the
   SOW validation plan — needs environment authorization later, not now.
10. **Test-only counters compiling out.** Go has no `cfg(test)`; use build
    tags (e.g. `//go:build iprange_workcounters`) with release binaries
    verified counter-free.
11. **Corpus regeneration discipline.** Go-produced fixtures are generated
    only through public APIs, verified against `cases.json`, opened by Rust,
    then committed; regeneration is never a test-only wire encoder.
12. **Scope containment.** Signing (SOW-0017), v3, the C ABI, and
    update-ipsets parser changes stay out; the worker binary name,
    `iprange-v4-worker`, is the only new artifact.

## 9. Projected size (estimate only, not a target)

The projection below is an estimation prior for budgeting and milestone
planning only. It is NOT a target: the design reference remains "smallest
coherent implementation" (directional 5k line guide), and every area must
justify its actual size from implemented, measured code at its milestone
close. The final count is reported evidence, not a commitment.

Rust reference (production, excludes tests): `iprange-livedb` 82,516 lines
(99,415 incl. tests); `iprange-capi` 15,391 (out of scope for Go).

| Area | Rust LOC (prod) | Go projection | Basis |
|---|---|---|---|
| publication | 14,022 | 7,000–9,000 | ~0.55–0.65 factor; no trait/error boilerplate |
| recovery | 10,870 | 5,500–7,500 | external sort + scratch states stay |
| validation | 5,284 | 2,800–3,600 | reasons/objects tables, worker client |
| worker (binary+control) | 5,099 | 2,800–3,500 | handshake/control map identical obligations |
| membership queries/joins/algebra | 4,533 | 2,400–3,000 | sinks as interfaces |
| live_writer + draft_store + writer_core | 7,999 | 4,000–5,200 | COW/allocation/sealing/retirement |
| trees (fixed_tree, range_mutation) | 4,691 | 1,800–2,400 | Go generics reduce codec duplication |
| live_lifecycle + sidecar + locks/namespace | 2,854 | 1,800–2,400 | platform split per OS |
| structured manager + NEV1 codec | 1,775 | 1,000–1,300 | one codec module |
| immutable output + snapshot + feeds | 1,753 | 900–1,300 | publication reuse |
| membership dictionary | 959 | 700–1,000 | interning/hash/refcount |
| reader core | 848 | 600–900 | cursors |
| workflows + feed catalog | 561 | 400–600 | sequencing only |
| bitmaps (used/free) | 1,629 | 900–1,300 | writer-owned |
| top-level remaining (format, meta, mapping, bootstrap, error, contract, ranges, slotted, blobs, checksum…) | ~20,000 | 9,000–12,000 | codecs dominate |
| **Total** | **≈ 82,500** | **≈ 32,000–44,000** | midpoint ≈ 37k |

- The directional 5,000-line guide exists to force lean design; Rust itself
  exceeded it ~16x and documented that honestly. Whether the Go peer exceeds
  it is unknown until implementation. Engineering constraints that DO apply:
  files under ~500 lines preferred, no file over ~700, one owner per
  persistent concern, and no dead code or duplicate authority.
- Tests: Rust carries ~17k test lines in `src` + 34 integration files; the Go
  test suite is projected at 15–25k lines including literal vectors, property
  models, crash/fault tests, and the mixed-process matrix.

## 10. Decisions requested from the user

Background: the SOW requires explicit user approval of the exact deletion set
and forbids adding a new native boundary without a user design decision. Both
are presented now so Milestone 1 can start without a mid-work stop.

**Decision 1 — deletion set.** Approve deleting the exact 100 tracked files
listed in §7 (45 production + 55 test), plus the 2 untracked leftovers
(`v4/go/exactv4.test`, empty `v4/go/exactv4/`), keeping `go.mod`/`go.sum` and
the 9 kept source files (5 transfer production + 4 transfer tests), whose
content is re-verified in Milestone 1 before reuse. Timing note: approved
deletion never precedes the replacement — it executes atomically with the
compiling, tested Milestone 1 commit (immutable reader cross-opening all Rust
fixtures + transfer verification).
- A. Approve the set now, with the atomic-with-M1-timing rule above.
- B. Approve with exceptions (name them).
- C. Decide after Milestone 1 evidence (recommended — matches the SOW's
  controlled-handoff checkpoint model: the deletion commit is also the
  verified replacement commit, so approval is informed by the evidence it
  gates; nothing else in the repo references these files and the Rust
  conformance suite is unaffected, but the evidence-first order removes all
  residual doubt).

**Decision 2 — fault-worker native boundary.** Milestone 1 must prove whether
pure Go can satisfy the exact SIGBUS in-region claiming + prior-disposition
chaining contract (with `si_addr`) without cgo or runtime internals. If it
cannot, the fallback is a minimal project-owned assembly sigaction shim (a new
native boundary; no cgo, no runtime linkname). How should the implementer
proceed?
- A. Wait for the Milestone 1 feasibility evidence, then decide
  (recommended — the SOW requires the design decision after feasibility
  evidence: "If a new boundary appears necessary, implementation stops for a
  user design decision before adding it"; no boundary is added without your
  approval and the evidence is reviewed in the milestone report).
- B. Pure-Go only, unconditionally: if the exact contract cannot be met in
  pure Go, implementation stops at the worker milestone (pre-commits to
  stopping without seeing the evidence).
- C. Pre-authorize the minimal assembly shim now, conditional on documented
  pure-Go failure (grants the boundary before the evidence exists; legal only
  because you explicitly authorize it, but deviates from the SOW's
  evidence-first wording).

## 11. Milestone 1 readiness

Safe to start. Milestone 0 produced zero production edits; baseline gates
pass; the target (specs + accepted Rust behavior + corpus) and the exit
criteria (transfer-file verification against literal vectors, mapping/lifetime
owner, immutable reader cross-opening all five Rust fixtures, malformed
bootstrap rejection, zero-allocation lookup evidence, worker feasibility
report) are fully specified. Tracked deletions and the fault-worker boundary
decision remain pending user decisions: no tracked file is deleted and no new
native boundary is added before those decisions. The first act of Milestone 1
implementation is moving SOW-0025 to `.agents/sow/current/` with
`Status: in-progress` (per the AGENTS.md status/directory rule); it stays in
`pending/` with `Status: open` until implementation begins.
