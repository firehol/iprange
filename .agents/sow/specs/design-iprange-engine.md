# iprange Engine and SDK Architecture

**Status:** Current unsigned Phase-1 target architecture
**Last updated:** 2026-07-25

This document defines the product and language architecture for the next
`iprange` engine. The exact portable file contract is defined by
[`binary-format-v4.md`](binary-format-v4.md). The active SOW records approved
work and temporary implementation evidence; completed SOWs are historical and
are not normative.

The stable Rust-provided C boundary is defined by
[`c-abi-v4.md`](c-abi-v4.md).

## Purpose

`iprange` is the high-performance IP interval engine used by FireHOL tooling.
The new engine makes `update-ipsets` a reliable publisher of portable,
self-contained threat-intelligence databases and gives consumers ready-to-use
SDKs for real-time address annotation.

Netdata is the first consumer:

- Rust consumers, including netflow, use the Rust library.
- Go consumers, including topology/network-viewer integrations and
  `update-ipsets`, use the pure-Go library.
- C consumers use the stable C ABI exported by the Rust implementation.

The existing C CLI remains the released `iprange` implementation and the
behavioral oracle for legacy set algebra while the new engine is developed. It
is not a third native implementation of the v4 database.

## Fit-for-purpose outcome

The engine is successful when it provides all of the following:

- exact IPv4 and IPv6 interval semantics;
- one portable, architecture-neutral v4 database contract;
- native Rust and pure-Go implementations with equivalent public behavior;
- a stable Rust-provided C ABI for C consumers;
- bounded-memory processing of databases larger than RAM;
- low-latency, allocation-free steady-state lookups;
- one writer with concurrent snapshot readers for live databases;
- explicit validation and recovery, without implicit full-file scans;
- compact unsigned snapshots for SDK correctness and performance proof;
- shared semantic conformance and comparable performance measurements.

Authenticated public snapshots are intentionally excluded from Phase 1. They
are tracked by pending SOW-0017 and begin only after this core SDK is reliable
and measured.

Correctness, crash durability, bounded resources, security, and recovery are
release requirements. They are not optional hardening phases.

## Architecture

### Rust first, then equivalent SDK surfaces

Rust implements and proves the complete v4 behavior first: creation, lookup,
scans, advanced logical changes, exact high-level replacement/import workflows,
feed lifecycle, first-seen and last-seen refresh, validation, recovery, reclamation, and
unsigned snapshotting. It must then be benchmarked on realistic
`update-ipsets` workloads and accepted as a material architectural improvement
before the Go port begins.

After that gate, the Go implementation is a pure-Go semantic port and must not
require cgo. The C SDK is a thin boundary over the proven Rust implementation,
not a separate implementation or a driver of core design.

The completed Rust and Go implementations are peers for public behavior.
Neither implementation's physical tree layout, page allocation order, or zlib
output is the wire oracle. The normative specification and shared corpus are
the oracle.

### Two architectural levels

The implementation has two authority levels over one COW format:

- The **public SDK level** exposes logical operations only. Its advanced direct
  and membership transactions support callers that need exact low-level logical
  control; its typed workflows provide named-feed lifecycle, direct-map and
  timestamp refresh, immutable construction, history projection, queries,
  provider joins, algebra, snapshot, validation, and recovery. Membership
  callers use SDK-issued feed and membership references. They never control
  physical indexes, IDs, pages, or bitmap combinations.
- The **internal format level** implements each persistent read, mutation,
  allocation, retirement, encoding, sealing, and publication operation exactly
  once. Public Rust workflows and the C adapter compose those operations; they
  do not contain another tree, merge, page, or publication implementation.

Unordered, duplicate, and overlapping range input becomes one canonical map
inside the same caller-facing operation. Public APIs expose logical state
changes, never physical storage operations. Rust defines and proves the
semantics first. The later Go port may use idiomatic names, while the
Rust-provided C ABI freezes its generated symbol and layout manifest.

### Authoritative internal ownership

Every operation that reads or changes persistent state has one authoritative
low-level implementation. Healthy selected-generation reads go through one
reader core. Healthy COW mutations, allocation, retirement, page sealing, and
main-file generation publication go through one writer core over the shared
mapped tree and page codecs. Advanced transactions, exact workflows, public
Rust handles, and the C adapter contain only logical sequencing,
reference/lifetime handling, and contract translation; they do not inspect
mappings, roots, page numbers, allocators, or physical dictionary state.

Untrusted validation and recovery remain a separate mapped-inspection boundary
because damaged input cannot assume a healthy selected generation. They reuse
the canonical byte codecs and final mapped-output builders without adding
recovery checks to ordinary reads. The external reader table and filesystem
namespace/publication artifacts likewise have separate owners. This is one
owner per persistent concern, not one object combining unrelated files and OS
state.

The dependency direction is enforced by a source gate. Test-only necessary-work
accounting pins deterministic tree, page, range, mapping, and durability costs;
it must compile out of release binaries. Representative release profiles must
show that every retained dominant hot-path cost maps to required format work.

### One v4 format, two operating modes

There is one exact v4 main-file format:

- A **live database** uses the main file plus a local external reader table.
- An **immutable snapshot** uses the same main-file bytes without that sidecar.

The sidecar is coordination state, not database content. It is never
distributed or embedded. Opening mode is explicit because the main
file alone cannot say whether local live coordination is required.

All persistent SDK content is accessed through file-backed mappings. This
includes the main file, live-reader sidecar, immutable/private outputs,
publication-control artifacts, recovery input/output, and authorized recovery
scratch. The SDK does not transfer those bytes through positional, buffered, or
stream file-I/O APIs. OS calls for file/handle lifecycle, identity, geometry,
mapping, sparse extension, truncation, locking, namespace changes, and
durability remain required.

The public constructors are distinct: immutable reader, live reader, and live
writer. They never auto-create, repair, reset, initialize, or switch mode.
`CreateLive` creates only the canonical empty live pair and returns a creation
result; acquiring a writer is a separate open. Direct live rename/relink is not
a Phase-1 operation: relocation uses snapshot, explicit live initialization,
and an application-controlled switch.

Linux, macOS, and Windows support the live sidecar protocol. FreeBSD 14 supports
immutable reading, explicit immutable/offline validation and recovery, and
durable immutable publication, but not live reader/writer coordination. On
FreeBSD every live constructor, open, transition, resolver, validation,
recovery, and snapshot source mode returns `LiveCoordinationUnsupported` before
accessing or changing the supplied paths. This boundary does not change the
portable main-file bytes.

The compact `SnapshotTo` operation streams one pinned committed generation into an
ordinary v4 file, excluding free, retired, unreachable, unpublished, and deleted
bytes. Phase 1 uses these unsigned artifacts for SDK conformance, durability,
and update-ipsets-shaped integration proof. Authenticated public publication is
not part of this phase.

There is no parallel snapshot format and no conversion between two current
formats.

### One address family and value model per file

Every database has immutable static identity:

- IPv4 or IPv6 address family;
- `direct` or `membership` value kind;
- a 16-byte value tag;
- a nonzero random 128-bit database ID.

Every stored interval has a fixed `value: u32`:

- In a direct database, the value is caller-defined and opaque. Zero is valid.
- In a membership database, the value is an internal `membership_id` for a
  canonical in-file feed bitmap. ID zero means absence and is not stored.

Membership databases contain their own structured feed catalog. The engine
owns feed indexes; callers use names or snapshot/transaction-bound handles and
never assign bare bits. Application annotations belong in the optional opaque
file-level JSON payload, not in the structural catalog.

The exact direct tags `first_seen` and `last_seen` select specialized
full-snapshot refreshes. First-seen preserves continuously present addresses,
timestamps new addresses, and removes absent addresses. Last-seen timestamps
current addresses, retains recently absent addresses above a caller cutoff, and
removes older absent addresses. Every other direct tag remains generic and
opaque; ASN and geography data do not require new physical value kinds.

## Processing model

### Interval map core

The logical model is a canonical map of non-overlapping inclusive address
ranges. Mutations split and coalesce ranges as required. Adjacent ranges with
equal values coalesce; absent membership is represented by no range.

Generic direct assignments are applied exactly in arrival order, within and
across input batches. A later assignment overwrites only its own inclusive
interval; earlier values survive on uncovered sides. Implementations may batch
or reorder only work proven not to change that per-address result.

High-level feed/direct/timestamp ingestion normalizes unordered input directly
into operation-private COW state in the destination. It never requires the
caller to presort input and never creates an external sorting file.

Public cardinalities use the exact fixed-size 129-bit type, which represents
the full IPv6 space and combined-family counts without wrapping or saturation.

### Transactions and durability

Mutations are copy-on-write and publish through double metadata pages. A public
operation either leaves the pending transaction untouched because it failed
before mutation, or aborts the entire pending transaction after any possible
mutation. Cleanup failure poisons the writer.

Every COW destination page is allocated at its final offset in a writable
file-backed mapping and built there. A complete page is never staged in a stack
buffer, heap buffer, application cache, or anonymous mapping. Source records are
decoded as bounded scalar values or copied directly from mapped source bytes to
mapped destination bytes. Transaction capacity is checked, sparsely extended,
and mapped before the hot mutation path; remap invalidates every prior internal
view.

Exact high-level replacements/imports, feed lifecycle operations, and timestamp
refreshes use clean writers and private transactional drafts. `SnapshotTo` instead
owns an isolated destination output and may coexist with source writes by pinning
one reader generation. None exposes partial work as a successful output.
Namespace publication distinguishes rollback-safe replacement from explicit
no-rollback replacement. Strict replacement requires an atomic exchange and
fails before mutation when the platform/filesystem lacks it. The no-rollback
policy may atomically discard the previous name only when the caller explicitly
selects it; no API silently downgrades.
`Commit` reports the attempted transaction, its random 128-bit commit nonce, and
one of `NotCommitted`, `Committed`, or `OutcomeUnknown`. Reopen resolves an
unknown attempt only by the exact database/transaction/nonce tuple; callers are
never asked to infer publication from a generic I/O error or a later
transaction number.

A writer owns at most one active advanced transaction or high-level workflow.
An ordinary transaction may stage at most one metadata set/clear alongside its
range or feed changes. `Abort` discards the whole draft; explicit `Close` on a
healthy pending writer runs that abort protocol and never commits. Automatic
destructors/finalizers never perform file I/O. `Reclaim` is a separate bounded
clean-writer maintenance operation that commits itself only when it actually
reclaims pages.

### Readers and reclamation

Readers pin one committed generation. A live database supports concurrent
readers and one writer through a strict external reader table. Retired pages
remain protected until no registered reader can observe them, then flow into a
persistent hierarchical free-page bitmap.

Ordinary commit always retires replaced committed pages. Explicit bounded
`Reclaim` holds the operation gate from its stable reader-table scan through
publication and moves only complete reader-safe transaction groups into the
free bitmap. Later allocation takes the lowest eligible free page before tail
growth. The existence of unrelated active readers does not create an
append-only allocation fallback and does not require all readers to quiesce.

There is no permanent transaction history. Open and normal operation must not
materialize allocator state proportional to file size or past transaction
count.

The writer copies a committed page at most once: after its parent points to the
transaction-private copy, later changes update that private page in place.
Retired committed page numbers stream into same-file retirement batches rather
than a heap list. A small fixed reserve in the selected meta supplies pages for
COW changes to the free bitmap itself, avoiding recursive allocation machinery.
Abort discards private roots; reused free pages remain free according to the
committed meta, while aligned appended growth is truncated immediately or by
the next writer open.

Live mappings expose checked raw page views, not ordinary Rust slices or
references whose validity assumes an unvalidated pointer cannot name a reused
page. Page views check mapped bounds and required local header/offset arithmetic,
decode bounded values, and copy records mapped-to-mapped. No page view survives
its operation, unmap, or remap.

### Validation and recovery

Main-file bootstrap performs only O(1) identity, geometry, alignment, and
memory-safety checks. A live open additionally performs O(reader_capacity)
sidecar coordination checks. Normal access uses checked bounds and arithmetic
but does not calculate data-page checksums. The narrow exception verifies each
committed allocator page at most once per transaction before it authorizes
destructive reuse.

`Validate` is an explicit streaming graph and integrity audit. Recovery is a
separate operation that creates a new database ID and copies only fully
verifiable reachable content into a new file. It reports verified content and
reason-coded rejected or unknown coverage; it never guesses from damaged or
unreachable bytes. A proven current, recovery-readable meta is labelled
`Newest`, is the default, and is the only live-recovery candidate. Previous or
generation-order-ambiguous candidates require explicit selection from an
immutable copy or caller-certified offline source; candidates are never merged.

Explicit validation, recovery-candidate inspection, and recovery run their
faultable mapped-source scans in a version-matched SDK worker process. On POSIX,
the worker installs `SIGBUS` handling only inside that process. It claims a
kernel-generated signal only while a probe is armed and `si_addr` lies inside
the exact registered SDK mapping; every other signal chains to the saved prior
disposition with the correct ABI. An owned fault records fixed facts in mapped
control state and terminates the worker. It never returns to the faulting access,
unwinds or `longjmp`s through Rust, allocates, locks, calls user code, or performs
content I/O in the handler. Windows applies the equivalent rule with
`EXCEPTION_IN_PAGE_ERROR`; unowned exceptions continue searching.

The parent accepts a physical-unreadability result only when worker identity,
control generation, active mapping, fault offset, and exit state agree. It
restarts from the last sealed mapped-scratch checkpoint, never from partial tree
mutation, and never publishes partial output. Ordinary lookup and mutation do
not install a process-global fault handler or pay worker-process overhead.

The SDK distribution includes the exact platform executable name
`iprange-v4-worker` and applications install it beside the consuming process
executable. It does not search `PATH` or accept an environment override. The
only secondary candidate is the parent of a directory literally named `deps`,
which supports Cargo's integration-test layout and is not an application
deployment contract. A build-ID and protocol handshake rejects a missing or
different worker before source scanning or destination mutation.

Validation mode is explicit: current live, current immutable, or a selected
offline recovery candidate. When bootstrap damage prevents selecting a normal
generation, validation reports the trustworthy bootstrap findings and unknown
extent instead of hiding the corruption behind a generic open error.

## Resource and performance contract

Files may be much larger than available RAM. "Bounded application working
memory" means engine-owned heap and explicit scratch; it excludes caller-owned
output, mapped virtual address space, kernel page cache, page faults, and
one-time runtime initialization. Resource use follows these rules:

- a warmed successful lookup or cursor step allocates nothing through the
  language allocator and returns borrowed data or writes into caller storage;
- no persistent-content path calls read/write/seek APIs, and no complete
  database page exists outside a file-backed mapping;
- an advanced range mutation is allocation-free only while the existing
  mapping, free pages, and preallocated transaction scratch suffice; a growth
  path remains bounded and must fail before mutation if its declared budget
  cannot suffice;
- `Commit` and open use working memory independent of database page count and
  transaction history;
- open performs no page-graph scan and allocates no file-sized structures;
- catalog/dictionary work, metadata compression, normalization, validation,
  recovery, and snapshotting use explicit memory budgets plus documented fixed
  overhead;
- normal ingestion, metadata staging, transactions, commit, abort, snapshot
  construction, and ordered algebra create no external scratch files. They use
  bounded heap plus unpublished same-file COW pages or the private final output
  inode. Only explicit validation and recovery graph-safety work may use a
  caller-authorized bounded external scratch file;
- open file descriptors, authorized scratch, decompression output, arithmetic,
  and persisted lengths have explicit checked limits;
- sparse page numbers must not produce page-number-sized heap structures;
- virtual address use from a full-file mmap is proportional to file size and is
  measured separately from heap and resident memory;
- algorithms must be measured separately for time, heap allocation, mapped
  virtual bytes, RSS, page faults, descriptors, persistent-content syscalls, and
  scaling shape, not only happy-path throughput.

Every potentially long operation has cancellation checkpoints with bounded
engine work between them. Cancellation never disguises a factual committed,
published, or outcome-unknown result and cannot interrupt an OS call already in
progress. Reader lookups and independent scans may run concurrently without a
per-call mutex, atomic, or active counter; Go/C callers must not race `Close`
with reader work.

Every engine-created artifact starts creator-private (`0600` on POSIX and the
equivalent protected user-only Windows DACL), independent of process defaults.
Applications deliberately widen or change ownership only after publication.

The Rust implementation is measured first against the current update-ipsets
workflows. After acceptance and the Go port, Rust and Go are compared operation
by operation. A 5–10% performance band is a target where the runtimes permit it,
not permission to trade correctness or bounded resources for a benchmark. Any
material exception needs measured, documented cause.

## Cross-language conformance

After the Rust acceptance gate and Go port, conformance is semantic, not
byte-identical:

- both implementations must open current Phase-1 v4 files produced by the other;
- both expose identical ranges, direct values, feed names and indexes,
  memberships, cardinalities, and exact decompressed JSON bytes;
- both reject the same invalid bootstrap and structural cases through the
  appropriate explicit API;
- both implement equivalent transaction, recovery, and unsigned snapshot
  outcomes;
- both implement the same advanced logical operations, high-level workflows,
  handle invalidation, metadata read-your-writes, batched source/sink, and
  cancellation semantics;
- mixed Go/Rust subprocesses must coordinate on the same live database in both
  directions, including OS-held reader/writer locks, pinned transaction IDs,
  sidecar/database identity, and reclamation;
- mutable tree shape, membership IDs, page placement, and zlib byte streams may
  differ when the observable committed state is the same.

The Rust-provided C ABI is generation 1 under the
`iprange_v4_abi1_` symbol prefix. Its generated header and frozen manifest are
normative for symbols, numeric statuses, structure sizes/offsets, path and IP
encoding, ownership, callback behavior, and panic containment. Native C tests
compile, link, and exercise that exact contract. The binding rules and required
semantic surface are specified in [`c-abi-v4.md`](c-abi-v4.md).

The committed corpus must contain files produced independently by both writers,
and every reader must actually open both producer sets. A JSON manifest alone
does not prove cross-read compatibility.

## update-ipsets integration

`update-ipsets` remains responsible for downloading, parsing, application
metadata, scheduling, and text artifacts. The engine owns durable IP interval
state and exact transformations.

The Phase-1 SDK integration is:

1. download and parse feed streams, potentially in parallel, without requiring
   caller-side sorting or normalization, directly into one immutable current-feed
   v4 file;
2. refresh that feed's first-seen and last-seen direct databases from the same
   current-feed coverage through mapped batched sources;
3. serialize the independent replacement of its named feed in the central
   membership database, preserving the prior committed feed on failure;
4. project every configured history window from one last-seen scan in one
   membership transaction;
5. pin the chosen generations and run one-scan overlap aggregation, ordered
   provider joins, and global-name algebra without temporary merged feeds; and
6. publish each set result as an immutable v4 file with exact statistics, then
   produce compact unsigned snapshots where a live database is the source.

The high-level update-ipsets workflow has no general atomic multi-feed batch.
One failed feed does not roll back unrelated feeds that already committed
successfully. This does not restrict the advanced logical membership layer,
which may intentionally update several feeds in one transaction.

Phase 1 implements the format-facing primitives, named-feed/direct/timestamp
workflows, one-inode immutable feed construction, one-pass history projection,
named membership aggregation, ordered provider joins, exact snapshot/copy,
membership multi-feed import, and global multi-file algebra. A result feed is a
published v4 file, never an in-memory feed object. Same-named feeds across input
files form one virtual global feed through ordered enumeration rather than a
temporary combined file. Provider-value joins are analytical; address-set
results use the algebra publisher rather than inventing ambiguous direct-value
conflict semantics.

Adoption should proceed only from proven behavior. Existing text outputs and
released operational workflows remain until the v4 path and later high-level
operations prove semantic and performance parity.

## Compatibility boundaries

- Readers and writers accept one exact current `v4` layout only. Until the first
  v4 release, an approved incompatible correction replaces the experimental
  bytes rather than adding a compatibility mode.
- No earlier experimental layout, alias, importer, exporter, or golden is part
  of the supported product.
- The released C v1.0 IPv4 and v2.0 IPv6 formats remain documented separately
  as legacy C behavior; they are not v4 compatibility modes.
- After the first v4 release, a future incompatible on-disk contract is `v5`.

## Non-goals

- Parsing, validating, normalizing, or merging the opaque JSON payload.
- Caller-assigned feed bits or feed-index aliases.
- Public membership IDs, bitmap words, page numbers, roots, allocator state, or
  other physical storage controls.
- Implicit full validation during open, lookup, scan, or mutation.
- Recovering data by guessing from checksum-failed or unreachable pages.
- Shipping live coordination state.
- Maintaining unreachable membership combinations or permanent page history.
- Guaranteeing byte-identical files from Rust and Go writers.
- External sorting files for ordinary ingestion or snapshot construction.
- Automatic open-mode detection or direct live-file rename/relink in Phase 1.
- Snapshot signing, verification, key handling, or authenticated public
  publication during Phase 1; pending SOW-0017 owns that later work.
