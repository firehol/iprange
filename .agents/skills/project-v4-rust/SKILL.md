---
name: project-v4-rust
description: Verify, benchmark, review, or change the Rust v4 database engine, its Rust-provided C ABI, and the shared v4 conformance corpus. Use for work under v4/rust or v4/conformance, Rust v4 portability and resource claims, generated ABI checks, or update-ipsets suitability evidence.
---

# Verify Rust v4

Keep Rust v4 work inside the approved exact-v4 contract and prove claims with
the compiled implementation. Do not infer current behavior from obsolete source
files, historical SOW claims, or aspirational Phase-2 work.

## Establish the boundary

1. Read the sole active SOW under `.agents/sow/current/`.
2. Read `.agents/sow/specs/binary-format-v4.md` and
   `.agents/sow/specs/design-iprange-engine.md`. Also read
   `.agents/sow/specs/c-abi-v4.md` when the C boundary is affected.
3. Inspect `git status --short`. Preserve unrelated changes and stage files
   explicitly.
4. Keep Phase 1 unsigned and exact. Do not restore v3, add signing, start Go,
   or invent high-level set algebra without explicit authorization.
5. Keep validation explicit. Ordinary open and read paths must not perform a
   whole-file validation.

## Measure the compiled implementation

Tracked `v4/rust` files include historical uncompiled sources. Derive the
active core module graph from Cargo instead of counting or deleting every
tracked Rust file:

```bash
CARGO_TARGET_DIR=/tmp/iprange-v4-graph \
  cargo check --manifest-path v4/rust/Cargo.toml -p iprange-livedb --lib
```

Read the generated `iprange_livedb-*.d` dependency file. Measure
`iprange-capi` separately. Never delete historical files without user approval.

## Run the Rust gates

Use these baseline gates:

```bash
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

Use a different `CARGO_TARGET_DIR` for each toolchain. Reusing one directory
between current Rust and Rust 1.74.1 can produce incompatible cached metadata:

```bash
CARGO_TARGET_DIR=/tmp/iprange-v4-msrv \
  cargo +1.74.1 test --manifest-path v4/rust/Cargo.toml \
  --workspace --all-features --all-targets
```

For a C ABI change, additionally prove:

- generated header and manifest equal the committed files;
- all 136 frozen symbols are exported exactly;
- the header compiles as C11 and C++17;
- all native C behavior programs pass;
- numeric constants, layouts, callbacks, ownership, and error contracts did not
  drift.

## Maintain conformance evidence

Keep normal conformance tests read-only. They must:

- open committed snapshots with the public reader;
- invoke explicit full validation;
- compare declared semantics independently;
- reject missing, extra, stale, malformed, or duplicate corpus entries.

Fixture regeneration must remain an explicit ignored test. Generate through the
public live writer and `snapshot_to`; do not add a test-only encoder. Until the
Go port exists, describe the corpus as Rust-first and never claim
bidirectional cross-language proof.

## Prove bounded update-ipsets workloads

Run the public-SDK benchmark:

```bash
cargo bench --manifest-path v4/rust/Cargo.toml --bench update_ipsets -- smoke
cargo bench --manifest-path v4/rust/Cargo.toml --bench update_ipsets -- scale
```

Record elapsed time, records per second, counted allocations and bytes, peak
RSS, open file descriptors, logical and physical file size, page counts, and
temporary residue. Check scaling, not only one result. Ordinary unordered
ingestion and snapshot construction must not use an external sorting file.

## Report proof precisely

- Cross-compilation proves compilation only. Run native platform tests only
  with explicit authorization.
- State the exact FreeBSD boundary: immutable reading and publication can be
  supported while live reader/writer coordination remains unsupported.
- Search for the same failure class before declaring a repair complete.
- Update the active SOW and every affected spec, SDK document, project skill,
  and `AGENTS.md` entry.
- Separate facts from working theories. A non-reproduced transient failure is
  not proof of a defect or proof of correctness.
- Do not declare Rust accepted or start the Go port before the user accepts the
  final Rust evidence.
