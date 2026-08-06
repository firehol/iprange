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

Invalid content is a completed `ValidationResult`; an operational failure is a
`ValidationFailure` with partial progress, cleanup facts, and any retained
source-cleanup guard. When that guard is present, the caller must retry its
cleanup before discarding the obligation.

## Build and verify

The minimum supported Rust version is 1.74.

```bash
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

The source-graph gate cross-checks every Rust source against fresh Linux,
Windows, macOS, and FreeBSD compiler dependency graphs. It also rejects
compiler warnings and dead-code suppression. The native C panic shim is the
one explicit exception because its integration test compiles it directly at
runtime.

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
library exports, compile all layouts as C11 and C++17, and run five native C
behavior programs.

## Measured publisher workloads

Run the small matrix routinely and the production-shaped matrix explicitly:

```bash
cargo bench --manifest-path v4/rust/Cargo.toml \
  --bench update_ipsets -- smoke
cargo bench --manifest-path v4/rust/Cargo.toml \
  --bench update_ipsets -- scale
```

The final local Linux scale run for this Rust milestone on 2026-08-06, pinned
to one performance core, produced:

| Scenario | Work | Time | Rate | Counted allocations | Peak RSS |
|---|---:|---:|---:|---:|---:|
| Direct replacement, dispersed input | 1,000,000 ranges | 0.561 s | 1,783,860/s | 22 | 67.5 MiB |
| Retention refresh | 1,000,000 ranges | 0.536 s | 1,865,200/s | 22 | 67.7 MiB |
| Compact snapshot | 1,000,000 ranges | 0.061 s | 16,443,190/s | 31 | 67.5 MiB |
| Direct point lookup | 100,000 lookups | 0.187 s | 533,399/s | 0 | 67.5 MiB |
| Membership point check, 421 feeds | 100,000 checks | 0.338 s | 296,271/s | 0 | 67.6 MiB |
| Direct ordered scan | 100,000 ranges | 0.0073 s | 13,757,452/s | 0 | 67.5 MiB |
| Named-feed ordered scan, 421 feeds | 100,000 ranges | 0.0078 s | 12,831,571/s | 0 | 67.5 MiB |

All scale cases kept file descriptors stable and left zero private artifacts.
The million-range write workflows retained constant allocation counts. The
benchmark deliberately authorizes a 64 MiB heap budget, which the optional page
cache uses; this explains the roughly 67.5 MiB process peak. Smaller caller
budgets reduce the cache rather than violating the bound. Every output is fully
validated after timing. These are one-machine baselines, not portable timing
guarantees.

The point-lookup rows are independent queries: every call starts at the tree
root and readers have no page cache. In this fixture, each direct lookup makes
three positional page reads and each membership check makes five. Ordered
cursors are the bulk-read path; they retain traversal state, and the named-feed
cursor also reuses the result for consecutive ranges with the same membership.
The read rows' peak RSS includes database construction before timing; the timed
read operations themselves allocate nothing.

The current flat update-ipsets set remains faster for simple lookup and scan,
but it does not provide COW publication, live readers, direct values, named
feeds, retention refresh, recovery, or portable snapshots. The Rust SDK proves
the required publisher primitives and bounded resource shape; final product
acceptance and consumer integration remain separate gates.

## Platform evidence

- Linux: native tests, process/crash tests, C/C++ callers, AddressSanitizer, and
  Valgrind have passed.
- macOS and Windows: the implementation cross-compiles without warnings, but
  native runtime and crash execution have not yet been authorized or performed.
- FreeBSD 14: immutable reading and durable immutable publication are in scope
  and cross-compile; live reader/writer coordination is explicitly unsupported.
  Native runtime and crash execution have not yet been performed.

Cross-compilation is compilation evidence only. It is not native platform
proof.
