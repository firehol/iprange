# iprange v4 C ABI Generation 1

**Status:** Normative Phase-1 binding rules and frozen generation-1 symbol,
numeric, prototype, and layout surface
**ABI generation:** 1
**Last updated:** 2026-08-10

This document defines the stable C boundary exported by the Rust v4 engine. The
semantic behavior and durable file contract remain normative in
[`binary-format-v4.md`](binary-format-v4.md); the product/language architecture
is defined in [`design-iprange-engine.md`](design-iprange-engine.md).

## Purpose and authority

The C ABI exposes the complete Phase-1 Rust behavior without exposing Rust
layout, panics, allocators, or physical v4 storage identifiers. It is a semantic
binding, not a second engine or a lower-level page API.

The ABI has three checked artifacts:

1. one generated C/C++ header;
2. one machine-readable manifest freezing exported symbols, numeric values,
   structure size/alignment/offsets, callback signatures, and ownership; and
3. native C compile, link, and behavior tests against the built Rust library.

The checked header and manifest are generated from the implemented Rust surface
and are the exact mechanical authority. Before the first v4 release, an approved
incompatible correction replaces this unreleased generation-1 contract rather
than adding compatibility. After release, an incompatible change requires a new
ABI generation and symbol prefix.

## ABI identity and calling convention

- Every exported symbol begins with `iprange_v4_abi1_`.
- `uint32_t iprange_v4_abi1_version(void)` always returns `1`.
- The generated header defines one visibility macro and one platform calling-
  convention macro. Windows exports use `__cdecl`; declarations on every
  platform use the macro rather than relying on compiler defaults.
- Public integers use exact-width `<stdint.h>` types. ABI lengths, capacities,
  and counts are `uint64_t` unless the v4 semantic contract fixes a narrower
  type. `size_t`, C enums, bitfields, `bool`, `long`, and compiler-dependent
  packing are not part of the ABI.
- Every versioned input/output structure begins with `uint32_t abi_version`
  and `uint32_t struct_size`. Generation 1 accepts exactly its generated size;
  the unreleased ABI has no historical smaller layouts to preserve.
- Reserved fields and padding named by the header must be zero on input and are
  written as zero on output.

## Stable value layouts

The manifest freezes at least these layouts:

```c
typedef struct iprange_v4_abi1_cardinality129 {
    uint8_t bit128;
    uint8_t reserved[7];
    uint64_t hi;
    uint64_t lo;
} iprange_v4_abi1_cardinality129;
```

Its offsets are `0`, `8`, and `16`; its alignment is 8 and its size is 24.
`bit128` is only 0 or 1 and every reserved byte is zero.

An IP value contains a numeric family plus a fixed 16-byte network-order array.
IPv4 uses the first four bytes and requires the remaining twelve to be zero;
IPv6 uses all sixteen. Ranges contain two such addresses and never depend on
native integer endianness or alignment.

Input byte/unit slices are `{pointer, uint64_t length}`. A null pointer is legal
only when length is zero. Nonzero lengths require a readable, correctly aligned
range whose conversion to the host address space succeeds. Mutable output
slices have the same rule and must not overlap engine-owned storage.

Paths are explicit tagged slices:

- POSIX accepts raw nonempty pathname bytes with no implicit NUL terminator;
- Windows accepts nonempty well-formed UTF-16 code units and rejects unpaired
  surrogates; and
- locale strings, lossy conversion, embedded platform terminators, and implicit
  current-working-directory substitution are forbidden.

Feed names and metadata are byte slices under their exact v4 semantic limits.
No ABI operation treats arbitrary input as a NUL-terminated string.

## Status and error ownership

Every fallible function returns one frozen nonzero numeric status on failure and
zero on success. A function may also return one owned opaque typed-error handle
through an explicit output pointer. There is no thread-local or process-global
"last error".

On a platform without the live sidecar protocol, every live constructor, open,
transition, resolver, validation, recovery, and snapshot source mode returns
typed error `LIVE_COORDINATION_UNSUPPORTED` (44) before path access or artifact
mutation. `OS_UNSUPPORTED` (58) remains the generic classifier for unrelated
unsupported operating-system operations.

The error handle exposes stable numeric category/code, optional OS code, cause
chain inspection, and two-call UTF-8 diagnostic copying. Diagnostic text is not
a stable classifier. Error/report destroy accepts null as success. If the handle
still owns an untaken cleanup guard, destroy returns `HANDLE_BUSY` and leaves the
handle intact; otherwise it is infallible.

All output pointers are initialized to null/zero before work that can fail. A
function documents which factual result handles remain present with a nonzero
status, including `Committed`, `Published`, `OutcomeUnknown`, partial validation
counters, cleanup obligations, and housekeeping. A generic I/O status never
hides those facts. When a nonzero status returns a factual report and an opaque
cleanup guard, the report owns that guard; the accompanying typed error owns the
guard only when no factual report is returned. The guard is never duplicated.

## Opaque handles and lifetime

Stateful and variable-size values are opaque handles. Generation 1 includes
opaque families for readers, writers, cursors, writer feed references,
membership views/references/builders, variable operation results, typed errors,
cleanup guards, residue inspection, reusable named membership scopes, and
reusable multi-source membership algebra. Reader feed enumeration/name lookup
copies `{name,index}` entries and does not require a reader feed-reference
handle. No declaration exposes a Rust struct or trait object.

- Memory allocated by Rust crosses the boundary only as an opaque handle and is
  released only by its matching ABI function.
- Borrowed buffers, batches, names, and records are valid only for the documented
  call or callback. C never frees them and must not retain their pointers.
- Reader children retain their parent. Reader Close returns `HandleBusy` while a
  cursor, membership view, membership scope, or algebra-retained scope exists and
  admits no new child after Close begins.
- Transaction-bound feed/membership references become invalid after commit,
  abort, failed whole-draft cleanup, or writer reopen even when the same logical
  feed exists later. Invalidated wrappers still count as writer children until
  destroyed. Writer Close checks for any feed reference, membership reference,
  or builder first and returns `HandleBusy` without aborting or changing state;
  after children are destroyed, pending Close performs its normal abort.
- Writer, cursor, view, builder, result, cleanup, and residue handles are caller-
  serialized. A fail-fast non-reentrant gate returns `HandleBusy`; it never
  silently waits. Different handles may operate concurrently subject to v4
  database locks.
- Reader point lookups and independent scans do not take a per-call ABI lock.
  The caller must not race Reader Close with any reader call.

Close and destroy are distinct where cleanup can fail. Reader/writer Close and
cleanup-guard/residue completion return status and do not free an unresolved
handle. Pending healthy writer Close runs Abort and then clears the writer lease;
it never commits. Finalizers and destructors in wrappers never begin a slot,
lease, abort, resolver, or namespace cleanup transition. An infallible destroy
is provided only for handles whose obligations are already resolved; otherwise
it returns the exact refusal status and leaves the handle owned by the caller.

## Callbacks and cancellation

The mandatory streaming boundary is synchronous and batched:

- a pull-source callback receives one engine-owned mutable batch and returns
  `Batch(nonzero_count)`, `End`, or `Error`;
- a sink callback receives one nonempty borrowed batch and returns `Continue`,
  `Stop`, or `Error`;
- neither callback may retain the batch or reenter the originating handle; and
- callback context is an opaque caller pointer passed through unchanged.

Every source/sink callback also receives a mutable fixed
`iprange_v4_abi1_callback_failure` with `abi_version`, `struct_size`,
`caller_code:u64`, `message_ptr`, and `message_length:u64`. On callback `Error`,
the engine copies `caller_code` and at most 4,096 message bytes before the
callback returns; null message is legal only at zero length. The message is
required to be well-formed UTF-8 and is not a stable engine classifier. Invalid
UTF-8 is replaced by the fixed diagnostic `callback supplied invalid UTF-8`
while preserving `caller_code`; it never crosses into the typed-error UTF-8
surface. On `Batch`, `End`,
`Continue`, or `Stop`, every failure field must remain zero. The callback owns
its original memory and the engine-owned typed cause owns the copy.

`Stop` maps to the exact `StoppedBySink` semantics in the binary-format spec.
Callback `Error` maps to `SinkFailed`/source failure with an optional caller error
code carried in the typed cause. A callback must not throw, unwind, `longjmp`, or
otherwise escape its C frame.

Potentially long operations take an explicit cancellation callback/context.
Cancellation is checked at the same bounded checkpoints as the Rust/Go APIs. A
callback outcome already in progress wins for that invocation. Cancellation
never disguises a factual commit/publication outcome and never abandons cleanup
authority. A cancellation callback/context stored by `Begin` remains caller-owned
and must stay valid until that operation terminates through no-change
`FinishInput`, Commit, Abort, or whole-draft failure. It is never called
afterward. It may not reenter the originating writer; the same fail-fast gate
makes any nested writer call, including Close/destroy, return `HandleBusy`
without mutation.

## Required semantic surface

The checked generation-1 symbol manifest must cover the complete Phase-1 Rust
surface in these groups:

- version/error inspection and exact handle close/destroy;
- `CreateLive`, `InitializeLive`, `ResetLiveCoordination`, and their inspection,
  resolution, and exact residue-cleanup operations;
- explicit immutable reader, live reader, and live writer opens;
- direct lookup, membership lookup/view, forward/backward range cursors, feed
  enumeration/name lookup, and ordered named-feed cursors;
- exact metadata presence/length query and caller-buffer copy, including writer
  read-your-writes;
- advanced direct begin/assign/clear and advanced membership begin, feed
  ensure/lookup/enumerate/rename/delete, membership building, and
  Replace/Union/Difference/Intersection/Xor range application;
- high-level named-feed create/replace/delete/rename, direct replacement,
  first-seen/last-seen refresh, and name-based multi-feed membership import;
- repeated bounded range ingestion, `FinishInput` statistics, one metadata stage,
  `Commit`, `Abort`, and pending-writer Close;
- bounded `Reclaim`;
- explicit validation modes, recovery-candidate inspection, all three recovery
  modes, compact `SnapshotTo`, their sinks/cancellation/resource budgets, and
  their factual result inspection;
- commit, publication, creation, and live-transition resolution; and
- exact cleanup-ledger, coordination-cleanup, access-state, and Windows
  housekeeping inspection/removal.

The update-ipsets SDK surface additionally includes:

- one-inode immutable single-feed creation from unordered batched coverage;
- exact one-pass projection from one `last_seen` direct database into several
  named history feeds prepared for the normal writer commit;
- reusable all-feed or named-feed scopes, point matching, exact cardinality and
  overlap aggregation, and ordered direct/membership provider joins; and
- reusable global-name algebra over retained scopes, including analytical
  count/compare and direct immutable publication in preserved-feed or flat mode.

The ABI does not expose page numbers, roots, COW paths, free/retirement storage,
feed bit positions as mutation authority, membership IDs, bitmap ownership,
dictionary hashes/refcounts, mmap pointers, Rust allocation, or a second
binding-specific algebra implementation.

Explicit validation and recovery may internally use the version-matched
SDK-provided fault worker required by the binary-format contract. The worker is
not a public handle and receives no caller function pointer. It returns mapped
protocol records to the parent library, and the parent alone invokes C sinks and
constructs ABI reports/errors. Missing/incompatible worker or failed handler
ownership is detected before source scanning or destination mutation. An owned
physical mapped-source fault is recovery/validation damage (`IO_ERROR` in the
report), while an unclassified worker failure is never relabeled as source
damage.

The native SDK distribution includes `iprange-v4-worker` with the platform's
normal executable suffix. The embedding application installs it beside its own
executable; the library does not search `PATH` or accept an environment
override. This helper is an installation requirement, not a public ABI handle.

## Metadata and variable output

Metadata follows the stable two-call contract: query presence and exact
decompressed length, then copy into one caller buffer. Too-small storage returns
the required length and no partial bytes. Go/Rust allocation conveniences are
not additional C ABI semantics.

`writer_set_metadata_json` and `writer_clear_metadata_json` include an optional
cancellation callback/context pair. On a clean writer the pair is required
(a null callback explicitly means never-cancel), starts or attempts the
metadata-only transaction, and becomes its stored token. During an active
advanced/high-level operation both fields must be null and the stored Begin
token is used; a second token is `WRONG_STATE` before mutation.

Each `writer_add_*_ranges` and advanced range-mutation symbol receives one source
callback/context and drains it to `END`. Repeated calls concatenate their records
in callback order. `END` ends only that call; `writer_finish_input` ends the
complete high-level input. Native slice/iterator overloads do not change this C
contract.

Variable result collections are inspected through count plus indexed getters or
the mandatory batched sink. Indexed getters copy fixed records or use two-call
byte copying; they never return a pointer whose lifetime depends on an internal
vector reallocation. Cleanup and housekeeping ledgers remain available until the
owning result is successfully destroyed.

Membership scan callbacks receive a range plus a borrowed-membership-view handle
valid only until that callback returns. Membership cursors expose the same
borrowed-view kind valid only until the next cursor movement or Close. Point
lookup instead returns an owned persistent `MembershipView`. Neither path
materializes the complete bitmap or exposes its internal membership ID.

## Panic, OOM, and pointer safety

The Rust ABI crate is built with unwind enabled at this boundary. Every export
catches a Rust panic and maps it to the frozen panic status/typed error; no unwind
crosses into C. The wrapper validates nullability, alignment, lengths, checked
integer conversions, enum discriminants, reserved fields, output aliasing, and
handle kind/state before semantic mutation.

Allocator abort and process OOM are documented as non-recoverable. The ABI does
not claim to convert an aborting allocator failure into a normal error.

## Generation-1 acceptance gate

Before the ABI is considered implemented:

- header generation is deterministic and CI fails on an unreviewed diff;
- the manifest lists every exported symbol and rejects unexpected exports;
- C and C++ compilation checks assert every fixed layout and numeric value;
- native C tests exercise success, malformed arguments, callback Stop/Error,
  panic containment, handle-state errors, pending Close/Abort, factual
  commit/publication outcomes, and cleanup retry;
- Rust and C run the same semantic conformance cases for every required group;
  and
- sanitizers or equivalent boundary tooling cover invalid pointers where the
  platform can test them without invoking unavoidable C undefined behavior.

## Frozen numeric registry

All names below are generated as `#define` constants with the stated `uint32_t`
value; they are not C enums. Zero is reserved for invalid/unset unless explicitly
listed.

- status: `OK=0`, `ERROR=1`;
- address family: `IPV4=4`, `IPV6=6`;
- path kind: `POSIX_BYTES=1`, `WINDOWS_UTF16=2`;
- value kind: `DIRECT=1`, `MEMBERSHIP=2`;
- source outcome: `BATCH=1`, `END=2`, `ERROR=3`;
- sink outcome: `CONTINUE=1`, `STOP=2`, `ERROR=3`;
- cursor direction: `FORWARD=1`, `BACKWARD=2`;
- open/source mode: `IMMUTABLE=1`, `LIVE=2`, `OFFLINE=3`;
- destination policy: `FAIL_IF_EXISTS=1`, `REPLACE_EXISTING=2`,
  `REPLACE_EXISTING_NO_ROLLBACK=3`;
- live reset policy: `ROLLBACK_SAFE=1`, `DISCARD_PREVIOUS=2`;
- resolver action: `COMPLETE=1`, `REMOVE=2`;
- membership operation: `REPLACE=1`, `UNION=2`, `DIFFERENCE=3`,
  `INTERSECTION=4`, `XOR=5`;
- validation mode: `LIVE_CURRENT=1`, `IMMUTABLE_CURRENT=2`,
  `OFFLINE_CANDIDATE=3`;
- recovery candidate: `NEWEST=1`, `PREVIOUS=2`, `UNORDERED_META0=3`,
  `UNORDERED_META1=4`;
- cleanup state: `CLEAN=1`, `RESIDUE_POSSIBLE=2`;
- coordination cleanup: `NONE=1`, `CLEANUP_GUARD=2`,
  `RETAINED_READER_CLOSE_REQUIRED=3`, `RETAINED_WRITER_CLOSE_REQUIRED=4`;
- housekeeping: `NONE=1`, `CRASH_REAPPEARANCE_POSSIBLE=2`, `VISIBLE=3`;
- Windows housekeeping transition: `MOVE_PENDING=1`, `MOVE_AMBIGUOUS=2`,
  `INERT=3`, `CONFLICT=4`;
- access: `ABSENT=1`, `CREATOR_ONLY=2`, `CHANGED_OR_UNPROVEN=3`,
  `UNCLASSIFIED=4`;
- meta selection: `PROVEN_CURRENT=1`, `SOLE_META0=2`, `SOLE_META1=3`;
- commit durability: `NOT_COMMITTED=1`, `COMMITTED=2`,
  `OUTCOME_UNKNOWN=3`;
- creation: `NOT_CREATED=1`, `CREATED=2`, `OUTCOME_UNKNOWN=3`;
- live transition: `NOT_INITIALIZED=1`, `OLD_COORDINATION_RETAINED=2`,
  `LEFT_IMMUTABLE=3`, `INITIALIZED=4`, `OUTCOME_UNKNOWN=5`;
- publication: `NOT_PUBLISHED=1`, `PUBLISHED=2`, `OUTCOME_UNKNOWN=3`;
- destination content: `DESIRED=1`, `PREVIOUS=2`, `ABSENT=3`, `OTHER=4`,
  `UNCLASSIFIED=5`;
- later canonical owner: `NONE=1`, `RESERVATION_OR_TRANSITION=2`,
  `READY_LIVE_SIDECAR=3`;
- live lineage: `SAME_GENERATION_EXACT_BYTES=1`,
  `SAME_GENERATION_PHYSICAL_BYTES_CHANGED=2`, `ADVANCED_GENERATION=3`;
- local file relation: `SAME_LOCAL_FILE=1`, `DIFFERENT_LOCAL_FILE=2`;
- commit resolution: `COMMITTED=1`, `NOT_COMMITTED=2`,
  `SUPERSEDED_UNKNOWN=3`, `UNRESOLVABLE=4`;
- abort outcome: `ABORTED=1`, `ABORT_INCOMPLETE=2`;
- close outcome: `CLOSED=1`, `INCOMPLETE=2`;
- live-transition operation: `INITIALIZE=1`, `RESET=2`;
- live-coordination location: `ABSENT=1`, `CANONICAL=2`, `PRIVATE=3`,
  `UNCLASSIFIED=4`;
- live-residue status: `ABSENT=1`, `READY=2`, `COMPLETED=3`, `REMOVED=4`,
  `OUTCOME_UNKNOWN=5`;
- live-residue kind: `CANONICAL=1`, `PRIVATE_RESET=2`;
- artifact kind: `PRIVATE_OUTPUT=1`, `PRIVATE_RESERVATION=2`,
  `OWNED_COORDINATION=3`, `AUTHORIZED_SCRATCH=4`,
  `OWNED_MAIN=5`, `UNPUBLISHED_MAIN_TAIL=6`;
- artifact presence: `ABSENT=1`, `PRESENT=2`, `UNCLASSIFIED=3`;
- artifact record kind: `AUTHORIZED_SCRATCH=1`, `PUBLICATION_TEMP=2`,
  `PUBLICATION_RESERVATION=3`;
- scratch authentication: `UNAUTHENTICATED=0`, `VALIDATION=1`, `RECOVERY=2`;
- abandoned-reservation phase: `PREPARED=1`,
  `MAIN_MAY_HAVE_BEEN_ATTEMPTED=2`;
- Windows housekeeping candidate: `ENVELOPE=1`, `INERT_PAYLOAD=2`;
- directory role: `DESTINATION=1`, `SCRATCH_DIRECTORY=2`, `MAIN_FILE=3`;
- local identity kind: `POSIX=1`, `WINDOWS=2`;
- creation-security kind: `POSIX=1`, `WINDOWS=2`;
- graph/object kind: `FILE_GEOMETRY=1`, `META=2`, `RANGE_TREE=3`,
  `CATALOG_NAME_TREE=4`, `CATALOG_INDEX_TREE=5`,
  `MEMBERSHIP_DICTIONARY=6`, `MEMBERSHIP_REVERSE_INDEX=7`,
  `MEMBERSHIP_BLOB=8`, `METADATA=9`, `FREE_BITMAP=10`,
  `FEED_USED_BITMAP=11`, `MEMBERSHIP_USED_BITMAP=12`,
  `RETIREMENT_TREE=13`, `RETIREMENT_BLOB=14`;
- logical change: `CHANGED=1`, `NO_CHANGE=2`;
- direct semantic: `NOT_APPLICABLE=0`, `GENERIC=1`, `FIRST_SEEN=2`,
  `LAST_SEEN=3`;
- workflow: `CREATE_FEED=1`, `REPLACE_FEED=2`, `DIRECT_REPLACEMENT=3`,
  `FIRST_SEEN_REFRESH=4`, `LAST_SEEN_REFRESH=5`,
  `MEMBERSHIP_IMPORT=6`; and
- report kind: `SCAN=1`, `FINISH_INPUT=2`, `COMMIT=3`,
  `COMMIT_RESOLUTION=4`, `ABORT=5`, `CLOSE=6`, `RECLAIM=7`, `CREATE=8`,
  `LIVE_TRANSITION=9`, `CREATE_RESOLUTION=10`,
  `LIVE_TRANSITION_RESOLUTION=11`, `PUBLICATION=12`, `VALIDATION=13`,
  `RECOVERY_CANDIDATES=14`, `RECOVERY=15`, `RESIDUE=16`,
  `LIVE_RESIDUE=17`;
- residue operation: `INSPECT_PUBLICATION=1`, `REMOVE_PUBLICATION=2`,
  `LIST_ABANDONED_SCRATCH=3`, `REMOVE_ABANDONED_SCRATCH=4`,
  `LIST_ABANDONED_PUBLICATION_TEMPS=5`,
  `REMOVE_ABANDONED_PUBLICATION_TEMP=6`,
  `LIST_ABANDONED_RESERVATION_ARTIFACTS=7`,
  `REMOVE_ABANDONED_RESERVATION_ARTIFACT=8`,
  `LIST_HOUSEKEEPING_ARTIFACTS=9`, `REMOVE_HOUSEKEEPING_ARTIFACT=10`,
  `SNAPSHOT_PREPARATION_FAILURE=11`;
- residue coordination: `ABSENT=1`, `PUBLICATION_RESERVATION=2`,
  `LIVE_SIDECAR=3`, `UNSELECTABLE=4`; and
- residue main content: `V4=1`, `OTHER=2`.

Validation and recovery share one stable reason-code namespace:

| Value | Stable reason |
|---:|---|
| 1 | `META_UNAVAILABLE` |
| 2 | `META_INVALID` |
| 3 | `META_STATIC_MISMATCH` |
| 4 | `FILE_GEOMETRY_INVALID` |
| 5 | `ROOT_COUNT_INVALID` |
| 6 | `IO_ERROR` |
| 7 | `ARITHMETIC_OVERFLOW` |
| 8 | `PAGE_OUT_OF_BOUNDS` |
| 9 | `PAGE_HEADER_INVALID` |
| 10 | `PAGE_CRC_MISMATCH` |
| 11 | `PAGE_TYPE_MISMATCH` |
| 12 | `PAGE_BORN_TXN_INVALID` |
| 13 | `PAGE_RESERVED_NONZERO` |
| 14 | `TREE_CYCLE` |
| 15 | `PAGE_ALIAS` |
| 16 | `TREE_LEVEL_INVALID` |
| 17 | `TREE_ORDER_INVALID` |
| 18 | `TREE_FENCE_INVALID` |
| 19 | `RANGE_REVERSED` |
| 20 | `RANGE_OVERLAP` |
| 21 | `RANGE_NOT_COALESCED` |
| 22 | `CATALOG_NAME_INVALID` |
| 23 | `CATALOG_BIJECTION_INVALID` |
| 24 | `CATALOG_BITMAP_INVALID` |
| 25 | `MEMBERSHIP_BITMAP_INVALID` |
| 26 | `MEMBERSHIP_HASH_INVALID` |
| 27 | `MEMBERSHIP_REVERSE_INDEX_INVALID` |
| 28 | `MEMBERSHIP_REFCOUNT_INVALID` |
| 29 | `MEMBERSHIP_ACTIVE_FEED_INVALID` |
| 30 | `BLOB_INVALID` |
| 31 | `METADATA_ZLIB_INVALID` |
| 32 | `METADATA_LENGTH_INVALID` |
| 33 | `BITMAP_SUMMARY_INVALID` |
| 34 | `ALLOCATION_PARTITION_INVALID` |
| 35 | `RETIREMENT_ORDER_INVALID` |
| 36 | `RETIREMENT_LIST_INVALID` |
| 37 | `CATALOG_INVALID` |
| 38 | `MEMBERSHIP_MISSING` |
| 39 | `MEMBERSHIP_INVALID` |
| 40 | `METADATA_INVALID` |

Values 1-36 are validation findings. Recovery unknown envelopes use the subset
named by the binary contract plus values 37-40; shared names keep the same value.

The typed-error code registry is:

| Value | Stable code |
|---:|---|
| 1 | `INVALID_ARGUMENT` |
| 2 | `NULL_POINTER` |
| 3 | `MISALIGNED_POINTER` |
| 4 | `INVALID_LENGTH` |
| 5 | `INVALID_ENUM` |
| 6 | `RESERVED_NONZERO` |
| 7 | `BUFFER_TOO_SMALL` |
| 8 | `WRONG_HANDLE_KIND` |
| 9 | `HANDLE_CLOSED` |
| 10 | `HANDLE_BUSY` |
| 11 | `WRONG_STATE` |
| 12 | `WRONG_ADDRESS_FAMILY` |
| 13 | `WRONG_VALUE_KIND` |
| 14 | `WRONG_VALUE_TAG` |
| 15 | `RANGE_REVERSED` |
| 16 | `NAME_INVALID` |
| 17 | `NAME_EXISTS` |
| 18 | `NAME_NOT_FOUND` |
| 19 | `STALE_REFERENCE` |
| 20 | `FOREIGN_REFERENCE` |
| 21 | `NO_PENDING_TRANSACTION` |
| 22 | `TRANSACTION_ABORTED` |
| 23 | `ABORT_INCOMPLETE` |
| 24 | `INSUFFICIENT_RESOURCE_BUDGET` |
| 25 | `PAGE_SPACE_EXHAUSTED` |
| 26 | `WORK_LIMIT_TOO_SMALL` |
| 27 | `CANCELLED` |
| 28 | `SOURCE_FAILED` |
| 29 | `SINK_FAILED` |
| 30 | `STOPPED_BY_SINK` |
| 31 | `IO` |
| 32 | `FORMAT_INVALID` |
| 33 | `NOT_V4` |
| 34 | `DURABILITY_UNSUPPORTED` |
| 35 | `PUBLICATION_UNSUPPORTED` |
| 36 | `ACCESS_POLICY_UNSUPPORTED` |
| 37 | `CONFLICT` |
| 38 | `UNRESOLVABLE` |
| 39 | `WRITER_BUSY` |
| 40 | `DIRECTORY_IDENTITY_MISMATCH` |
| 41 | `DESTINATION_NAME_MISMATCH` |
| 42 | `CLEANUP_CONFLICT` |
| 43 | `COORDINATION_SEQUENCE_EXHAUSTED` |
| 44 | `LIVE_COORDINATION_UNSUPPORTED` |
| 45 | `LIVE_COORDINATION_CLEANUP_REQUIRED` |
| 46 | `LIVE_COORDINATION_MALFORMED_REQUIRES_RESET` |
| 47 | `LIVE_OPEN_CLEANUP_REQUIRED` |
| 48 | `LIVE_RECOVERY_COORDINATION_UNAVAILABLE` |
| 49 | `LIVE_RECOVERY_CURRENT_GENERATION_UNPROVABLE` |
| 50 | `LIVE_RECOVERY_CURRENT_GENERATION_UNREADABLE` |
| 51 | `RECOVERY_CANDIDATE_CHANGED` |
| 52 | `RECOVERY_PREPARATION_FAILED` |
| 53 | `SNAPSHOT_PREPARATION_FAILED` |
| 54 | `TRANSITION_SUPERSEDED` |
| 55 | `CURRENT_GENERATION_UNPROVABLE` |
| 56 | `FORKED_HANDLE` |
| 57 | `PANIC` |
| 58 | `OS_UNSUPPORTED` |
| 59 | `TRANSACTION_ID_EXHAUSTED` |
| 60 | `ARITHMETIC_OVERFLOW` |
| 61 | `FEED_INDEX_EXHAUSTED` |
| 62 | `MEMBERSHIP_ID_EXHAUSTED` |
| 63 | `READER_CAPACITY_EXHAUSTED` |
| 64 | `CLEANUP_IN_PROGRESS` |
| 65 | `FAULT_WORKER_UNAVAILABLE` |
| 66 | `FAULT_WORKER_FAILED` |

Factual `Committed`, `Published`, `OutcomeUnknown`, invalid validation content,
and recovery damage are report fields, not error codes. Future ABI-1 additions
may append new nonzero codes and report kinds but never renumber or redefine an
existing value.

## Frozen opaque type and callback manifest

The generated header declares exactly these opaque tags:

```text
iprange_v4_abi1_error
iprange_v4_abi1_report
iprange_v4_abi1_reader
iprange_v4_abi1_writer
iprange_v4_abi1_cursor
iprange_v4_abi1_writer_feed_ref
iprange_v4_abi1_membership_view
iprange_v4_abi1_borrowed_membership_view
iprange_v4_abi1_membership_builder
iprange_v4_abi1_membership_ref
iprange_v4_abi1_cleanup_guard
iprange_v4_abi1_residue
iprange_v4_abi1_membership_scope
iprange_v4_abi1_membership_algebra
```

It declares exact callback typedefs named:

```text
iprange_v4_abi1_cancel_fn
iprange_v4_abi1_coverage_source_fn
iprange_v4_abi1_direct_source_fn
iprange_v4_abi1_coverage_sink_fn
iprange_v4_abi1_direct_sink_fn
iprange_v4_abi1_first_seen_removal_sink_fn
iprange_v4_abi1_membership_sink_fn
iprange_v4_abi1_feed_sink_fn
iprange_v4_abi1_validation_finding_sink_fn
iprange_v4_abi1_recovery_unknown_sink_fn
iprange_v4_abi1_artifact_sink_fn
iprange_v4_abi1_housekeeping_sink_fn
iprange_v4_abi1_feed_name_sink_fn
iprange_v4_abi1_feed_cardinality_sink_fn
iprange_v4_abi1_feed_overlap_sink_fn
iprange_v4_abi1_direct_join_sink_fn
iprange_v4_abi1_membership_cross_sink_fn
iprange_v4_abi1_uncovered_feed_sink_fn
```

The source callbacks receive context, mutable engine-lent record array/capacity,
output count, and callback-failure output, and return the frozen source outcome.
Sink callbacks receive context, const borrowed record array/nonzero count, and
callback-failure output, and return the frozen sink outcome. Cancellation
receives only context and returns `uint8_t` zero or one; other values are treated
as one. Exact C declarations and offsets are emitted in the checked generated
header and machine manifest before the first ABI-bearing build.

A first-seen removal record contains one normal ABI range, the old
`first_seen:u32`, zero reserved bytes, and exact `Cardinality129` interval
length. `writer_finish_first_seen_with_removals` is valid only for an active
first-seen workflow and emits bounded batches during its required merge; sink
failure or `Stop` aborts the unpublished draft.

## Frozen generation-1 symbol manifest

No other `iprange_v4_abi1_` function symbol may be exported. Snapshot always
takes an explicit destination policy; generation 1 has no policy-defaulting
overload. Writer-owned state is the sole public transaction state, so there is no
transaction-handle family.

### ABI, errors, and reports

```text
iprange_v4_abi1_version
iprange_v4_abi1_error_code
iprange_v4_abi1_error_os_code
iprange_v4_abi1_error_message_query
iprange_v4_abi1_error_message_read
iprange_v4_abi1_error_cause
iprange_v4_abi1_error_cleanup_artifact_count
iprange_v4_abi1_error_cleanup_artifact_get
iprange_v4_abi1_error_take_cleanup_guard
iprange_v4_abi1_error_destroy
iprange_v4_abi1_report_kind
iprange_v4_abi1_report_get_scan
iprange_v4_abi1_report_get_finish_input
iprange_v4_abi1_report_get_history_projection
iprange_v4_abi1_report_get_history_window
iprange_v4_abi1_report_get_commit
iprange_v4_abi1_report_get_commit_resolution
iprange_v4_abi1_report_get_abort
iprange_v4_abi1_report_get_close
iprange_v4_abi1_report_get_reclaim
iprange_v4_abi1_report_get_create
iprange_v4_abi1_report_get_live_transition
iprange_v4_abi1_report_get_create_resolution
iprange_v4_abi1_report_get_live_transition_resolution
iprange_v4_abi1_report_get_live_residue
iprange_v4_abi1_report_get_publication
iprange_v4_abi1_report_get_validation
iprange_v4_abi1_report_get_recovery_candidates
iprange_v4_abi1_report_get_recovery
iprange_v4_abi1_report_get_residue
iprange_v4_abi1_report_cleanup_artifact_count
iprange_v4_abi1_report_cleanup_artifact_get
iprange_v4_abi1_report_housekeeping_artifact_count
iprange_v4_abi1_report_housekeeping_artifact_get
iprange_v4_abi1_report_recovery_candidate_count
iprange_v4_abi1_report_recovery_candidate_get
iprange_v4_abi1_report_take_cleanup_guard
iprange_v4_abi1_report_take_residue
iprange_v4_abi1_report_cause
iprange_v4_abi1_report_destroy
```

### Creation, transition, open, and lifecycle

```text
iprange_v4_abi1_create_live
iprange_v4_abi1_create_immutable_feed
iprange_v4_abi1_initialize_live
iprange_v4_abi1_reset_live_coordination
iprange_v4_abi1_resolve_create_live
iprange_v4_abi1_resolve_live_transition
iprange_v4_abi1_resolve_interrupted_live_transition
iprange_v4_abi1_open_immutable_reader
iprange_v4_abi1_open_live_reader
iprange_v4_abi1_open_live_writer
iprange_v4_abi1_reader_close
iprange_v4_abi1_reader_destroy
iprange_v4_abi1_writer_close
iprange_v4_abi1_writer_destroy
iprange_v4_abi1_cleanup_guard_retry
iprange_v4_abi1_cleanup_guard_close
iprange_v4_abi1_cleanup_guard_destroy
iprange_v4_abi1_residue_close
iprange_v4_abi1_residue_destroy
```

### Reader, cursor, membership view, and metadata

```text
iprange_v4_abi1_reader_database_info
iprange_v4_abi1_reader_lookup_direct
iprange_v4_abi1_reader_lookup_membership
iprange_v4_abi1_reader_enumerate_feeds
iprange_v4_abi1_reader_lookup_feed
iprange_v4_abi1_reader_scan_direct
iprange_v4_abi1_reader_scan_membership
iprange_v4_abi1_reader_scan_feed
iprange_v4_abi1_reader_open_direct_cursor
iprange_v4_abi1_reader_open_membership_cursor
iprange_v4_abi1_reader_open_feed_cursor
iprange_v4_abi1_cursor_next_direct
iprange_v4_abi1_cursor_next_membership
iprange_v4_abi1_cursor_next_coverage
iprange_v4_abi1_cursor_close
iprange_v4_abi1_cursor_destroy
iprange_v4_abi1_membership_view_word_count
iprange_v4_abi1_membership_view_word
iprange_v4_abi1_membership_view_read_words
iprange_v4_abi1_membership_view_contains_index
iprange_v4_abi1_membership_view_close
iprange_v4_abi1_membership_view_destroy
iprange_v4_abi1_borrowed_membership_view_word_count
iprange_v4_abi1_borrowed_membership_view_word
iprange_v4_abi1_borrowed_membership_view_read_words
iprange_v4_abi1_borrowed_membership_view_contains_index
iprange_v4_abi1_reader_metadata_query
iprange_v4_abi1_reader_metadata_read
iprange_v4_abi1_writer_metadata_query
iprange_v4_abi1_writer_metadata_read
iprange_v4_abi1_writer_set_metadata_json
iprange_v4_abi1_writer_clear_metadata_json
iprange_v4_abi1_reader_all_feeds_scope
iprange_v4_abi1_reader_named_feeds_scope
iprange_v4_abi1_reader_matching_feeds
iprange_v4_abi1_membership_scope_feeds
iprange_v4_abi1_membership_scope_aggregate
iprange_v4_abi1_membership_scope_join_direct
iprange_v4_abi1_membership_scope_join_membership
iprange_v4_abi1_membership_scope_close
iprange_v4_abi1_membership_scope_destroy
iprange_v4_abi1_membership_algebra_create
iprange_v4_abi1_membership_algebra_feeds
iprange_v4_abi1_membership_algebra_count
iprange_v4_abi1_membership_algebra_compare
iprange_v4_abi1_membership_algebra_publish_set
iprange_v4_abi1_membership_algebra_close
iprange_v4_abi1_membership_algebra_destroy
```

### Advanced logical operations

```text
iprange_v4_abi1_writer_begin_direct
iprange_v4_abi1_writer_direct_assign_ranges
iprange_v4_abi1_writer_direct_clear_ranges
iprange_v4_abi1_writer_begin_membership
iprange_v4_abi1_writer_feed_ensure
iprange_v4_abi1_writer_feed_lookup
iprange_v4_abi1_writer_feed_enumerate
iprange_v4_abi1_writer_feed_rename
iprange_v4_abi1_writer_feed_delete
iprange_v4_abi1_writer_feed_ref_info
iprange_v4_abi1_writer_feed_ref_destroy
iprange_v4_abi1_writer_membership_builder_create
iprange_v4_abi1_membership_builder_add_feed
iprange_v4_abi1_membership_builder_finish
iprange_v4_abi1_membership_builder_destroy
iprange_v4_abi1_membership_ref_destroy
iprange_v4_abi1_writer_membership_apply_ranges
```

### High-level workflows, transaction termination, and maintenance

```text
iprange_v4_abi1_writer_begin_create_feed
iprange_v4_abi1_writer_begin_replace_feed
iprange_v4_abi1_writer_delete_feed
iprange_v4_abi1_writer_rename_feed
iprange_v4_abi1_writer_begin_direct_replacement
iprange_v4_abi1_writer_begin_first_seen_refresh
iprange_v4_abi1_writer_begin_last_seen_refresh
iprange_v4_abi1_writer_begin_membership_import
iprange_v4_abi1_writer_project_history
iprange_v4_abi1_writer_add_coverage_ranges
iprange_v4_abi1_writer_add_direct_ranges
iprange_v4_abi1_writer_finish_input
iprange_v4_abi1_writer_finish_first_seen_with_removals
iprange_v4_abi1_writer_commit
iprange_v4_abi1_writer_abort
iprange_v4_abi1_writer_reclaim
iprange_v4_abi1_resolve_commit
```

### Validation, recovery, publication, and exact cleanup

```text
iprange_v4_abi1_validate
iprange_v4_abi1_inspect_recovery_candidates
iprange_v4_abi1_recover_live
iprange_v4_abi1_recover_immutable
iprange_v4_abi1_recover_offline
iprange_v4_abi1_snapshot_to
iprange_v4_abi1_resolve_publication
iprange_v4_abi1_inspect_publication_residue
iprange_v4_abi1_remove_publication_residue
iprange_v4_abi1_list_abandoned_scratch
iprange_v4_abi1_remove_abandoned_scratch
iprange_v4_abi1_list_abandoned_publication_temps
iprange_v4_abi1_remove_abandoned_publication_temp
iprange_v4_abi1_list_abandoned_reservation_artifacts
iprange_v4_abi1_remove_abandoned_reservation_artifact
iprange_v4_abi1_list_housekeeping_artifacts
iprange_v4_abi1_remove_housekeeping_artifact
```

The exact prototype/layout manifest remains the committed mechanical authority.
It freezes all 158 functions, 14 opaque handle families, 18 callback types,
numeric constants, structure layouts, offsets, and function parameter order.
Tests regenerate and compare both artifacts, inspect the shared-library exports,
compile the header as C11 and C++17, and execute the native C behavior programs.
An ABI-bearing build cannot pass acceptance when any generated declaration or
manifest entry differs.
