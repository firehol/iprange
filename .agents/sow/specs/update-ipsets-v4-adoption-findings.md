# update-ipsets Adoption Evidence for iprange v4

**Status:** Non-normative consumer analysis
**Evidence checked:** `firehol/update-ipsets @ e593366f7b0a`
**Last updated:** 2026-07-21

This document explains how the unsigned Phase-1 v4 contracts fit the current
`update-ipsets` workflow. It does not define the wire format; the normative
contract is [`binary-format-v4.md`](binary-format-v4.md).

## Established current behavior

### One scheduler run may contain several independent feeds

The scheduler drains queued work into one run and passes the selected names to
the engine:

- `pkg/scheduler/processing_loop.go:47-74`

Source workers execute independently and the final report can contain a mixture
of updated, skipped, and failed feeds:

- `pkg/engine/run_pipeline.go:40-119`
- `pkg/engine/run_pipeline.go:121-136`

This is why v4 intentionally does not make a scheduler run one atomic
multi-feed transaction. Successful replacements commit independently; a failed
feed keeps its previously committed membership.

### Each feed currently publishes its own durable artifacts

Finalization writes a per-feed binary `latest` artifact before promoting the
canonical text output:

- `pkg/engine/finalize.go:41-61`

The binary writer uses a temporary file, sync, close, and rename sequence:

- `pkg/engine/binary_write.go:11-51`

The v4 integration must retain at least this failure isolation and atomic
publication behavior. It must not expose a partially replaced feed or generate
a missing public artifact in a request handler.

### Retention currently rebuilds state from cohort files

Retention enumerates a feed's saved cohorts, opens batches of historical files,
compares each cohort with the complete current set, and rewrites or deletes the
surviving cohorts:

- `pkg/engine/retention_update.go:303-335`
- `pkg/engine/retention_update.go:440-463`
- `pkg/engine/retention_update.go:493-550`

History-window derivatives similarly reopen and union the current feed plus
eligible saved snapshots:

- `pkg/engine/feed_body_stage.go:400-462`

This is the direct fit for a `direct` v4 database tagged `retention`: keep each
continuously present address's original timestamp, add only new addresses with
the refresh timestamp, and remove addresses missing from the complete new
download.

### Pairwise comparisons scale quadratically in feed count

The current comparison path enumerates every unordered feed pair:

- `pkg/engine/output_comparison.go:205-230`

A named-feed membership database changes the data model: one scan can aggregate
membership combinations because every interval already says which cataloged
feeds contain it. This removes repeated independent range searches, but the
replacement must be benchmarked at real feed and range counts before old
comparison machinery is retired.

## Phase-1 v4 adoption contract

### Membership state

- Use a separate membership database per address family.
- The database's in-file feed catalog is authoritative for name-to-bit mapping.
- The caller identifies feeds by name; it never persists or assigns a bare bit.
- `CreateFeed`, `ReplaceFeed`, `DeleteFeed`, and `RenameFeed` are exact
  feed-lifecycle transactions.
- An empty feed remains cataloged with no member addresses; deletion removes the
  catalog entry and makes its index reusable.
- Feed replacements are serialized through one writer, even if download and
  parsing run concurrently. The v4 SDK normalizes each feed's unordered batches
  inside its own replacement workflow.
- After the selected replacements finish, aggregate work pins one reader so the
  catalog, dictionary, ranges, and JSON are from one generation.

`update-ipsets` uses these high-level one-feed workflows; it does not manipulate
feed indexes, membership IDs, or bitmap combinations. The Phase-1 advanced
membership layer may update several named feeds atomically for other callers,
and the dedicated name-based import copies a multi-feed membership file while
translating all source indexes and membership IDs inside the SDK.

### Retention state

Each retained feed uses a direct database whose immutable tag is exactly
`retention`.

For a complete downloaded set at refresh value `tN`:

- addresses in both old and new snapshots preserve their old value;
- addresses only in the new snapshot receive `tN`;
- addresses only in the old snapshot are removed;
- partial overlaps split so the rule applies per address;
- an address removed earlier and later reappearing receives the later value.

The refresh requires a clean writer and a private draft. Any failure before
commit discards the complete draft, so unreadable input cannot be published as
mass deletion.

### Application metadata

The engine-defined catalog is structural. Publisher annotations, source facts,
licenses, descriptions, and similar application data belong in the optional
opaque JSON payload.

The engine preserves the exact uncompressed bytes but does not understand or
merge them. `update-ipsets` owns their JSON schema and must update any
application-level feed-name references when renaming a feed.

### Phase-1 snapshot proof

The live database and its `.readers` sidecar are private producer state. The
Phase-1 v4 integration path is:

1. finish the selected independent feed commits;
2. pin the exact committed generation used for evaluation;
3. invoke explicit `Validate` only when the test/operator intentionally requests
   a full proof; normal SDK and snapshot paths do not invoke it implicitly;
4. use `SnapshotTo` to compact away unpublished, free, retired, unreachable, and
   deleted bytes into the one private final-output inode;
5. atomically publish that unsigned snapshot only to the Phase-1 test/integration
   destination through the durable namespace reservation protocol; and
6. reopen, compare, and benchmark it through both SDK implementations.

Phase 1 does not replace update-ipsets' current public distribution path and
does not introduce signing keys, signature verification, trust rotation, or
replay policy. Authenticated v4 public publication is pending SOW-0017 and starts
only after the unsigned SDK passes reliability and performance gates.

## Failure and recovery behavior

- A failed feed replacement leaves that feed's prior committed membership
  intact and does not roll back unrelated successful feed commits.
- A structured commit result tells the caller whether retry is safe, forbidden,
  or requires reopen and exact database/transaction/commit-nonce resolution.
  A later transaction number alone never proves that the caller's unknown
  attempt committed.
- Main-file bootstrap is O(1); live open additionally scans the configured
  reader table in O(reader_capacity). Neither proves the complete database
  healthy.
- Ordinary access performs no general page-CRC scan. A writer verifies each
  committed allocator page at most once per transaction only when that metadata
  is about to authorize destructive page reuse.
- When corruption is suspected, the caller runs explicit detailed validation.
- Recovery writes a new database ID and copies only fully verifiable reachable
  content; the report distinguishes verified address coverage from rejected or
  structurally unknown gaps.
- Recovery defaults only to a recovery-readable meta independently proven to be
  the actual current generation. A previous or generation-order-ambiguous
  candidate is available only from an immutable copy or caller-certified offline
  source; `update-ipsets` must select it explicitly and review its independent
  report. Ambiguity is never promoted into live recovery.
- The caller chooses whether to retain the old published artifact, retry the
  source, or publish a reviewed recovered subset. Recovery is never silently
  treated as a normal feed refresh.

## Deliberate scope boundaries

V4 does not replace these `update-ipsets` responsibilities:

- HTTP download/cache, parsing, DNS expansion, and source configuration;
- canonical text `.ipset`/`.netset` output required by existing consumers;
- application JSON/CSV reports and append-only operational history;
- publisher key management, trusted-key rotation, or replay state;
- public artifact routing and HTTP cache policy.

Those surfaces may consume v4 results, but they remain application behavior and
must be migrated and tested separately.

## Adoption order

The long-term-best order is:

1. exact retention refresh, because its current cohort-file loop is isolated and
   I/O-heavy;
2. named-feed create/replace/delete plus compact unsigned snapshot proof;
3. membership-based comparison/query reads after scale benchmarks prove the
   replacement;
4. history/merge composition where semantic parity is demonstrated;
5. broader direct-value uses such as ASN or geography only when each consumer's
   provider and overlap model fits one value per address.

Authenticated public snapshot publication is the separate Phase 2 tracked by
SOW-0017 after this sequence proves the SDK reliable and measures its behavior.

Old artifacts and paths should be removed only after side-by-side semantic,
durability, recovery, and performance evidence is clean.
