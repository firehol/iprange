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

## `metadata_cases.json` — v4.1 metadata cases

A JSON array of cases exercising the v4.1 metadata system (§C): the scope table and the
per-scope / per-file KV trees, including overflow-spanning values and a branchy scope
table. Each case is built through the **Writer** and then read back **through the Reader**
(the consumer path being certified — never the Writer). Schema per case:

```jsonc
{
  "name": "meta_basic",
  "family": "v4",                  // "v4" | "v6"
  "scope_width": 1,                // bytes of opaque scope per IP record (0..=255)
  "ip_ops": [                      // optional; applied to the IP tree (coexists with metadata)
    {"op": "set", "from": "100", "to": "200", "scope": [1]}
  ],
  "scopes": [                      // IN ORDER — the i-th gets scope_id = i+1 via ScopeDefine
    {
      "name": "feed-a",
      "set_version": 5,            // optional → ScopeSetVersion
      "set_type": 1,               // optional → ScopeSetType
      "kv": [                      // → MetaSet(scope_id, key, type, value)
        {"key": "license", "type": 0, "value_hex": "4d4954"}
      ]
    }
  ],
  "file_kv": [                     // → MetaSet(0, …) — the FILE target (scope_id 0)
    {"key": "dataset", "type": 0, "value_hex": "66697265686f6c"}
  ],
  "expect_scopes": [               // ScopeList (FILE 0 excluded), kv ascending by key bytes
    {"id": 1, "name": "feed-a", "version": 5, "type": 1,
     "kv": [{"key": "license", "type": 0, "value_hex": "4d4954"}]}
  ],
  "expect_file_kv": [{"key": "dataset", "type": 0, "value_hex": "66697265686f6c"}]
}
```

- A KV `value` is encoded as **exactly one of** `value_hex` (a hex string) or `value_fill`
  (`{"byte": N, "len": M}` ⇒ `M` copies of byte `N`). `value_fill` keeps the corpus small
  for the large overflow-spanning values (e.g. `huge`/`bin` in `meta_overflow`).
- `expect_scopes[*].kv` and `expect_file_kv` are sorted ascending by key bytes (the order
  `MetaList` returns).
- One programmatic case, `meta_many`, is **not** in the JSON (it is built by the same
  deterministic loop in both `tests/metadata_conformance.rs` and
  `metadata_conformance_test.go`): 25 scopes overflow the 14-record scope-leaf capacity,
  forcing a **multi-level scope tree** — proving branchy-scope-table cross-read.

### Bidirectional metadata goldens (`*.r.iprdb` / `*.go.iprdb`)

Unlike the IP-tree goldens (Rust writes the only `<name>.iprdb`, both read), each writer's
KV / scope-table encoding is independent, so the metadata goldens are **two-sided**:

- Rust writes `files/<name>.r.iprdb`;
- Go writes `files/<name>.go.iprdb`;
- **each reader opens BOTH** (via the Reader API) and verifies the same expectations —
  Rust reads `.go.iprdb`, Go reads `.r.iprdb`. A missing golden ⇒ skip (clean bootstrap).

This certifies a metadata-bearing file written by *either* language is read identically by
*both*. Reads go through the Reader's `scope_list` / `scope_name` / `scope_version` /
`scope_type` / `meta_list` / `meta_get` (Go: `ScopeList` / `ScopeName` / `ScopeVersion` /
`ScopeType` / `MetaList` / `MetaGet`).

Regenerate the goldens (the `*.r`/`*.go` pair per case) in this order — each writer's
goldens must exist before the *other* reader can cross-read them:

```bash
# 1) Rust writes *.r.iprdb (Go goldens absent ⇒ Rust skips read-Go this run)
REGENERATE_GOLDENS=1 cargo test --manifest-path v4/rust/Cargo.toml --test metadata_conformance
# 2) Go writes *.go.iprdb AND reads the now-present *.r.iprdb (Go-reads-Rust verified)
REGENERATE_GOLDENS=1 go -C v4/go test -run Metadata ./...
# 3) Rust no-regen: *.go.iprdb now present ⇒ Rust-reads-Go verified
cargo test --manifest-path v4/rust/Cargo.toml --test metadata_conformance
```
