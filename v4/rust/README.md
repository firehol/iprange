# iprange v4 Rust SDK

This workspace contains the Rust implementation of the exact unsigned Phase-1
v4 database and its Rust-provided C ABI. It is unreleased and has not yet passed
the user's final acceptance gate. The post-acceptance Go port is outside this
milestone.

Only the current v4 contract is supported. There is no v3 compatibility,
importer, exporter, or alternate v4 layout. Snapshot signing is separate
Phase-2 work.

## Components

- `iprange-livedb` is the native Rust SDK and the one format implementation.
- `iprange-capi` is a thin C boundary over that Rust engine.
- [`../conformance/`](../conformance/) contains the current Rust-produced
  semantic corpus.
- [`binary-format-v4.md`](../../.agents/sow/specs/binary-format-v4.md) defines
  the wire and durability contract.
- [`design-iprange-engine.md`](../../.agents/sow/specs/design-iprange-engine.md)
  defines the product and SDK architecture.
- [`c-abi-v4.md`](../../.agents/sow/specs/c-abi-v4.md) defines the frozen C
  boundary.

## Internal ownership

Healthy selected-generation reads have one owner (`ReaderCore`). Healthy COW
mutation, allocation, retirement, page sealing, and committed-generation
publication in the main file have one owner (`WriterCore` over `DraftStore`).
Exact workflows, advanced transactions, and the C adapter sequence logical
operations over those owners; they cannot access mappings, roots, pages, or
allocators directly. Validation and recovery retain a separate untrusted
mapped-inspection boundary and reuse the canonical codecs and mapped output
builders. Sidecar coordination and filesystem namespace/publication are
separate persistent concerns with separate owners.

[`check-architecture.sh`](check-architecture.sh) enforces those dependency
directions. The 2026-08-09 production-only audit counts 74,375 physical lines
and 67,949 code lines across 282 inventoried Rust files. Of those physical
lines, 13,870 implement cross-platform namespace/publication, 10,012 the frozen
C ABI, 9,762 explicit recovery, 5,303 the isolated fault worker, and 5,089
explicit validation. The public Rust writer/workflow adapters account for
3,691 lines and the public reader adapters for 262; mapped storage, trees,
codecs, reader/writer cores, output construction, and shared logical types make
up the remainder.

At a 15-line/100-token threshold, exact clone detection finds eight small
shapes totaling 154 lines (0.21%). They are C entry-point forms, public
typestate wrappers, and separate direct/membership recovery policies—not
duplicate persistent-format operations. Across 4,117 production functions,
the averages are 13.6 lines and cyclomatic complexity 3.39. The largest is a
191-line recovery-attempt state machine whose branches retain distinct cleanup
and factual-outcome obligations. These measurements do not prove every line is
intrinsically required; they make the remaining size and review boundary
explicit.

## Database model

There is one main-file format with two explicit operating modes:

- A live database uses the main file plus a local `<filename>.readers`
  coordination sidecar. The sidecar is never distributed.
- An immutable snapshot contains the same database bytes without a sidecar.

Each file has one address family, one value kind, a random database identity,
and a value tag containing up to 15 non-NUL bytes plus its required NUL.

- `direct` ranges contain opaque caller-defined `u32` values. The exact
  `retention` tag enables the separate retention-refresh workflow.
- `membership` ranges refer to canonical in-file feed combinations. The SDK
  owns feed indexes, bitmap combinations, and membership IDs; callers use feed
  names and generation-bound references.

The optional file-level metadata is one opaque payload intended to contain a
JSON object. The engine reads and writes the exact bytes but does not parse or
normalize JSON. The uncompressed limit is 1 MiB; the committed representation
is compressed.

## Writer behavior

The writer accepts unordered, duplicate, adjacent, and overlapping ranges and
normalizes them directly into destination COW pages. Direct assignments apply
in arrival order: a later range overwrites only the addresses it covers.
Ordinary ingestion never presorts input and never creates an external sorting
file.

The public Rust SDK exposes:

- advanced direct and membership transactions;
- named-feed create, replace, rename, and delete workflows;
- complete direct-map replacement;
- exact retention refresh, where surviving addresses keep their original
  value, new addresses receive the new refresh value, and missing addresses are
  removed;
- name-based multi-feed membership import;
- one optional metadata replacement in the same committed generation;
- compact immutable snapshot construction.

An operation either publishes its complete draft or aborts it. A commit reports
`NotCommitted`, `Committed`, or `OutcomeUnknown` with exact resolution
evidence. Normal replacement and retention deliberately have no best-effort
flag because a read failure cannot safely mean deletion.

Normal ingestion, import, commit, queries, and snapshot construction use no
external scratch files. Only explicit validation or recovery graph work may
use caller-authorized bounded scratch space. A private snapshot output is the
prospective final database, not sorting scratch.

## Open, validation, and recovery

Opening is explicit through immutable-reader, live-reader, and live-writer
constructors. Open performs only the checks required for safe bounded access;
it does not scan or checksum the complete database.

Full validation is an explicit operation. Recovery is also explicit: it
inspects bounded candidates and returns typed evidence so the caller can choose
whether and how to revive data. Ordinary readers and writers do not repair,
reset, initialize, validate, or switch modes automatically.

Validation, candidate inspection, recovery, and retained-cleanup retry use the
SDK's isolated fault worker. Install the exact platform executable name
`iprange-v4-worker` beside the final application executable. The SDK does not
search `PATH` or accept an environment override. A build-ID handshake rejects a
missing or different worker before source scanning or destination mutation.
The worker is built and included by the `iprange-livedb` Cargo package; an
application installer must install both executables together. The only
secondary lookup is Cargo's exact integration-test layout: a process under a
directory literally named `deps` may use the worker in that directory's parent.
Applications must not depend on that test convention.

Invalid content is a completed `ValidationResult`; an operational failure is a
`ValidationFailure` with partial progress, cleanup facts, and any retained
source-cleanup guard. When that guard is present, the caller must retry its
cleanup before discarding the obligation.

## Build and verify

The minimum supported Rust version is 1.74.

```bash
./v4/rust/check-architecture.sh
./v4/rust/check-mmap-storage.sh
./v4/rust/check-mmap-runtime.sh
./v4/rust/check-source-graph.sh
cargo test --manifest-path v4/rust/Cargo.toml \
  --workspace --all-features --all-targets
cargo test --manifest-path v4/rust/Cargo.toml \
  --workspace --no-default-features --all-targets
cargo clippy --manifest-path v4/rust/Cargo.toml \
  --workspace --all-features --all-targets -- -D warnings
cargo fmt --manifest-path v4/rust/Cargo.toml --all -- --check
RUSTDOCFLAGS='-D warnings' cargo doc --manifest-path v4/rust/Cargo.toml \
  --workspace --all-features --no-deps
```

The static storage gate rejects persistent-content I/O and owned production
page images. The Linux runtime gate traces representative database, sidecar,
snapshot, recovery, publication, reservation, and scratch paths and requires
mapped access with zero persistent-content transfer syscalls. The source-graph
gate cross-checks every Rust source against fresh Linux, Windows, macOS, and
FreeBSD compiler dependency graphs. It also rejects compiler warnings and
dead-code suppression. The native C panic shim is the one explicit exception
because its integration test compiles it directly at runtime.

Use a separate target directory for Rust 1.74.1 so Cargo does not reuse
incompatible current-toolchain metadata:

```bash
CARGO_TARGET_DIR=/tmp/iprange-v4-msrv \
  cargo +1.74.1 test --manifest-path v4/rust/Cargo.toml \
  --workspace --all-features --all-targets
```

Normal conformance tests are read-only and explicitly validate every committed
fixture. Fixture regeneration is an ignored, manually selected test:

```bash
cargo test --manifest-path v4/rust/Cargo.toml \
  -p iprange-livedb --test conformance -- --nocapture
```

The corpus is Rust-first evidence. Bidirectional Rust/Go cross-open becomes
mandatory only after the accepted Rust result is ported independently to Go.

## C ABI

The committed boundary has 136 generation-1 functions. Its generated public
artifacts are:

- [`iprange_v4.h`](iprange-capi/include/iprange_v4.h)
- [`iprange_v4_abi1_manifest.json`](iprange-capi/include/iprange_v4_abi1_manifest.json)

The Rust tests regenerate and compare both artifacts, inspect exact shared
library exports, compile all layouts as C11 and C++17, and run seven native C
behavior programs.

## Measured publisher workloads

Run the small matrix routinely and the production-shaped matrix explicitly:

```bash
cargo bench --manifest-path v4/rust/Cargo.toml \
  --bench update_ipsets -- smoke
cargo bench --manifest-path v4/rust/Cargo.toml \
  --bench update_ipsets -- scale
```

Five local Linux runs on 2026-08-09, each pinned to one performance core,
produced these medians and observed ranges. Each case processes at least one
million inputs or timed reader operations; setup, snapshot construction, open,
close, and explicit validation are outside the reader timer.

| Scenario | Work | Median | Observed range | Median rate |
|---|---:|---:|---:|---:|
| Direct replacement, dispersed input | 1,000,000 ranges | 0.3464 s | 0.3356-0.6895 s | 2.89 million/s |
| Retention refresh | 1,000,000 ranges | 0.4769 s | 0.4738-0.5337 s | 2.10 million/s |
| Nested arrival-order overwrite | 1,000,000 ranges | 0.3026 s | 0.2977-0.4333 s | 3.30 million/s |
| Exact feed replacement, 421 feeds | 1,000,000 ranges | 0.4315 s | 0.3462-0.7210 s | 2.32 million/s |
| Membership import, 421 feeds | 1,000,000 ranges | 0.0575 s | 0.0538-0.0769 s | 17.38 million/s |
| Compact snapshot | 1,000,000 ranges | 0.0588 s | 0.0560-0.0853 s | 17.02 million/s |
| Live direct point lookup | 1,000,000 lookups over 100,000 ranges | 0.0832 s | 0.0709-0.0875 s | 12.02 million/s |
| Immutable direct point lookup | 1,000,000 lookups over 100,000 ranges | 0.0629 s | 0.0589-0.0798 s | 15.89 million/s |
| Live membership point check, 421 feeds | 1,000,000 checks over 100,000 ranges | 0.1172 s | 0.1157-0.1283 s | 8.53 million/s |
| Immutable membership point check, 421 feeds | 1,000,000 checks over 100,000 ranges | 0.1141 s | 0.0951-0.1268 s | 8.76 million/s |
| Live direct ordered scan | 1,000,000 ranges | 0.00835 s | 0.00821-0.00866 s | 119.76 million/s |
| Immutable direct ordered scan | 1,000,000 ranges | 0.00682 s | 0.00652-0.00834 s | 146.71 million/s |
| Live named-feed ordered scan, 421 feeds | 1,000,000 ranges | 0.01174 s | 0.00844-0.01483 s | 85.14 million/s |
| Immutable named-feed ordered scan, 421 feeds | 1,000,000 ranges | 0.00892 s | 0.00847-0.01340 s | 112.12 million/s |

All scale cases kept file descriptors stable, left zero private artifacts, and
explicitly validated every output after timing. Direct, retention, nested,
feed-replacement, import, and snapshot construction respectively made
21/21/21/22/20/31 fixed setup allocations totaling
406/418/406/436/474/939 bytes. Those counts are constant rather than per
range. The timed lookup and scan paths allocate nothing.

Readers access mapped tree pages directly. Independent point queries restart at
the mapped root, while ordered cursors retain their bounded traversal state.
There is no application page cache and no positional page transfer. Peak RSS
includes mapped pages faulted into the process and fixture construction; it is
not an engine-owned heap measurement. These are one-machine baselines, not
portable timing guarantees.

Live handles automatically reject inherited use after `fork`. On supported
Linux kernels this ownership check reads a private `MADV_WIPEONFORK` control
mapping and performs no per-operation process-ID syscall. Unsupported kernels
and other platforms retain the process-ID fallback.

The current flat update-ipsets set remains faster for simple lookup and scan,
but it does not provide COW publication, live readers, direct values, named
feeds, retention refresh, recovery, or portable snapshots. The Rust SDK proves
the required publisher primitives and bounded resource shape; final product
acceptance and consumer integration remain separate gates.

## Platform evidence

- Linux: native tests, process/crash tests, C/C++ callers, AddressSanitizer, and
  Valgrind have passed.
- macOS: both native feature matrices, process/crash tests, SIGBUS chaining,
  live lifecycle, publication, conformance, validation/recovery, Rust C-boundary
  tests, and C11/C++17 header checks pass on Apple ARM64.
- Windows with the GNU Rust target: both native feature matrices, mapped-reader
  tail retention/reuse, live lifecycle/crash resolution,
  publication/housekeeping, conformance, validation/recovery, C11/C++17 header
  checks, and an external C caller using the Windows calling convention and a
  non-ASCII UTF-16 path pass on local NTFS.
- FreeBSD 14: both native feature matrices pass for immutable reading,
  validation/recovery, and durable fail-if-exists/no-rollback publication.
  Strict replacement rejects before destination mutation. Live coordination is
  explicitly unsupported and every live entry returns error code 44 before path
  access or artifact mutation.

Cross-compilation is compilation evidence only. It is not native platform
proof.
