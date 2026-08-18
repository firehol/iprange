# iprange v4 conformance corpus

The normative wire contract is
[`../../.agents/sow/specs/binary-format-v4.md`](../../.agents/sow/specs/binary-format-v4.md).
The former unreleased and incompatible experimental fixtures were deleted; they
are not compatibility inputs.

## Rust-first foundation

`cases.json` is the language-neutral semantic manifest. The current foundation
contains six compact immutable snapshots produced through the public Rust live
writer and public snapshot operation:

- `rust/direct-ipv4.iprdb`: arrival-order direct assignments and clearing;
- `rust/first-seen-ipv6.iprdb`: full IPv6 first-seen coverage and empty metadata;
- `rust/membership-ipv4.iprdb`: 70 named feeds, index reuse, and memberships
  crossing the 64-bit boundary;
- `rust/membership-ipv6.iprdb`: full IPv6 membership and a 1 MiB compressed
  metadata payload;
- `rust/structured-ipv4.iprdb`: typed network enrichment, named threat feeds,
  arrival-order overwrites, clearing, lazy membership, and exact metadata; and
- `rust/structured-ipv4-nothreat.iprdb`: structured values without threat
  feeds (membership id zero), pinning the canonical absence result in both
  readers.

The Rust test actually opens and explicitly validates every listed file. It
compares every direct or structured range, typed enrichment field, feed
name/index, resolved membership, named-feed projection, exact decompressed
metadata state and bytes, and exact 129-bit cardinality. It also performs true
point lookups and derives temporary wrong-magic, short, and unaligned inputs from
the valid corpus to prove their rejection. Normal tests never modify the
committed corpus.

Run the read-only proof:

```bash
nice cargo test --manifest-path v4/rust/Cargo.toml --test conformance
```

Regenerate only the Rust-produced files explicitly:

```bash
nice cargo test --manifest-path v4/rust/Cargo.toml \
  --test conformance regenerate_rust_fixtures -- --ignored --exact
```

Regeneration has no test-only wire encoder. It creates live files with the
public writer, builds compact snapshots with `snapshot_to`, verifies all staged
outputs against `cases.json`, and only then replaces the committed Rust files.

## Cross-language gate

The corpus is currently Rust-first. The accepted Rust result authorized the
pure-Go port under pending SOW-0025. When that Go implementation exists:

- Go adds independently produced files to this same manifest;
- both readers must open and semantically verify both producer sets;
- malformed transformations must produce equivalent public errors; and
- mixed Rust/Go subprocess tests must prove external reader-slot locks, writer
  exclusion, reclamation, sidecar identity/replacement handling, and live
  transition/publication resolution in both directions.

Byte-identical files are not required. Page placement, mutable tree shape,
membership IDs, and compressed bytes may differ when observable data is equal.
At the cross-language gate, a missing required producer or skipped cross-open is
a failure.

Reachable empty leaves are malformed. An empty logical tree uses root page zero.
Live reader coordination is external to the database and uses OS-held locks; it
does not store process tokens in the database or sidecar.

Snapshot signing is intentionally absent from Phase 1. Pending SOW-0017 adds
authenticated artifacts only after the unsigned SDK is accepted and measured.
