# iprange v4 — conformance corpus

Language-neutral conformance for the v4 live-DB format (`design-iprange-v4-livedb.md`,
§12). Both the Rust reference (`v4/rust/iprange-livedb`) and the Go port run this corpus
and MUST produce identical results.

## `cases.json` — behavioral cases

A JSON array of cases. Each case applies an op-sequence to a fresh DB, commits, then
checks the full scan and point lookups:

```jsonc
{
  "name": "split_middle",
  "family": "v4",              // "v4" | "v6"
  "scope_width": 1,            // bytes of opaque scope per record (0..=255)
  "ops": [                     // applied in order
    {"op": "set",    "from": "1",  "to": "100", "scope": [1]},
    {"op": "delete", "from": "40", "to": "50"}
  ],
  "expect_scan":   [["1","39",[1]], ["51","100",[1]]],   // [from, to, scope] in key order
  "expect_lookup": [["45", null], ["51", [1]]]           // [ip, scope|null]
}
```

- **Keys are decimal strings** — a `u32` value for `v4`, a `u128` value for `v6` — so the
  corpus is safe against JSON-number precision limits and parses trivially in Rust
  (`u32`/`u128`) and Go (`strconv` / `math/big`).
- **Scope** is an array of byte values (length `scope_width`).
- `set` is unconditional and coalesces byte-equal adjacent neighbours; `delete` removes a
  range (splitting/trimming); a wholly-absent `delete` is a no-op (§8).

Run (Rust): `cargo test -p iprange-livedb --test conformance`.

## Byte-level cross-read (added with the Go port)

The mandatory cross-read check (§12) — one implementation reading the other's file — is
provided by committed binary goldens (`files/*.iprdb`) produced by the Rust writer; both
implementations open each and must return the case's `expect_scan`. (Mutable-tree files
are **not** byte-identical across implementations, so cross-read tests results, not
bytes.)
