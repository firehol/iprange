# update-ipsets Adoption Evidence for iprange v4

**Status:** Non-normative consumer analysis
**Evidence checked:** `firehol/update-ipsets @ f299ee780dc0ce09a15a46d0ee660611399c2d48`
**Last updated:** 2026-08-10

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

### Historical state currently rebuilds from cohort files

Retention enumerates a feed's saved cohorts, opens batches of historical files,
compares each cohort with the complete current set, and rewrites or deletes the
surviving cohorts:

- `pkg/engine/retention_update.go:303-335`
- `pkg/engine/retention_update.go:440-463`
- `pkg/engine/retention_update.go:493-550`

History-window derivatives similarly reopen and union the current feed plus
eligible saved snapshots:

- `pkg/engine/feed_body_stage.go:400-462`

This requires two direct v4 timestamp databases per feed. `first_seen` keeps
each continuously present address's original timestamp, adds only new addresses
with the refresh timestamp, and removes addresses missing from the complete new
download. `last_seen` timestamps current addresses, retains absent addresses
newer than a caller cutoff, and removes older absent addresses. One last-seen
map can project every configured history window.

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

### First-seen and last-seen state

Each feed uses two direct databases whose immutable tags are exactly
`first_seen` and `last_seen`.

For a complete downloaded set at refresh value `tN`:

- addresses in both old and new snapshots preserve their old value;
- addresses only in the new snapshot receive `tN`;
- addresses only in the old snapshot are removed;
- partial overlaps split so the rule applies per address;
- an address removed earlier and later reappearing receives the later value.

The refresh requires a clean writer and a private draft. Any failure before
commit discards the complete draft, so unreadable input cannot be published as
mass deletion.

For the same complete downloaded set at refresh value `tN`, last-seen also
takes a cutoff:

- current addresses receive `max(old_value, tN)`, or `tN` when new;
- absent addresses newer than the cutoff remain; and
- absent addresses at or below the cutoff are removed.

All configured history windows are membership projections from one last-seen
scan, not independent timestamp databases or repeated scans.

### Application metadata

The engine-defined catalog is structural. Publisher annotations, source facts,
licenses, descriptions, and similar application data belong in the optional
opaque JSON payload.

The engine preserves the exact uncompressed bytes but does not understand or
merge them. `update-ipsets` owns their JSON schema and must update any
application-level feed-name references when renaming a feed.

### Implemented Rust SDK capability

The current Rust candidate supplies the complete format-side operations needed
for the intended publisher flow without changing `update-ipsets` itself:

- one-pass unordered construction of the downloaded current-feed v4 file;
- mapped current-feed sources for first-seen, last-seen, create/replace, and
  immutable-output workflows;
- exact first-seen removals and monotonic last-seen expiration reports;
- one last-seen scan projecting every configured history feed;
- reusable named scopes, point matches, feed cardinalities, selected/all-pair
  overlaps, and direct/membership provider joins;
- same-name global multi-file count and comparison; and
- union, intersection, and exclusion published directly as immutable v4 in
  preserved-feed or flat mode, always with exact statistics.

Provider joins are analytical because a generic direct value has no universal
set-output conflict rule. When the required result is address coverage, the
algebra publisher creates the v4 result. No operation creates a temporary
combined feed, external sorting file, or complete-feed heap image.

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
6. reopen, compare, and benchmark it through the Rust SDK; repeat the same
   cross-open proof after the accepted Rust result is ported independently to
   Go.

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

The Rust format-side primitives above must first pass their final acceptance
gate. Consumer migration then proceeds in independently verifiable slices:

1. create the immutable current-feed file and refresh its first-seen and
   last-seen files from the same downloaded coverage;
2. replace that name in the central all-feeds membership file and project all
   history windows;
3. build the critical-infrastructure, ASN-provider, and geography-provider
   multi-feed databases through the same named-feed lifecycle;
4. replace pairwise application loops with the proven aggregation and provider
   joins while comparing exact old/new reports; and
5. switch every set-producing comparison/artifact path to v4 algebra output,
   then remove superseded cohort and temporary-format paths only after parity.

The SDK remains the owner of interval normalization, named-feed mapping,
membership combinations, timestamp transitions, ordered joins, algebra, and v4
publication. `update-ipsets` remains the owner of download/extraction, schedule,
application metadata schemas, text/website artifacts, and cross-file workflow
coordination.

Authenticated public snapshot publication is the separate Phase 2 tracked by
SOW-0017 after this sequence proves the SDK reliable and measures its behavior.

Old artifacts and paths should be removed only after side-by-side semantic,
durability, recovery, and performance evidence is clean.
