# Design: iprange v4 scope-aware API (uncapped scope, multi-feed / categorical)

## Status

Settled design (2026-06-23 discussion). The v4 **core file format**
(`design-iprange-v4-livedb.md`) is reused **unchanged** — COW B+tree, double-meta
two-fsync commit, mmap reader, advisory `flock`, derived in-memory allocator, per-page
CRC32C. This document specifies the **scope-aware engine** on top of that core and the
**v4.1 additions** (cursor, standard SDK helpers, metadata system); the detailed v4.1
layout is in the final section.

**Guiding principle — mechanism vs policy.** iprange ships *mechanism*: the generic
`[from,to,scope]` interval map (`set`/`delete`/`lookup`), an ordered **cursor**
(`seek`/`next`/`prev`/read) with a mutate-during-open-cursor contract, and a small set of
**standard SDK helpers** (selectors, coalescing scan, `query{Ranges,CIDRs}[Merged]`,
on-demand `countIPs`/`countCIDRs`). All *policy* — retention, multi-feed comparison, geo —
is built by **callers** on top; none of it lives in iprange, and `scope` stays opaque to
the engine. Per-scope counts are **on-demand** helpers — a caller that needs instant counts
maintains its own.

Implementation: **SOW-0008** (cursor + helpers — read-only, no format change) and
**SOW-0009** (metadata system — the additive v4.0 → v4.1 minor bump).

## Purpose

Evolve v4 from a single **opaque-scope** interval map into a **scope-aware** engine that
serves two membership shapes with one core:

- **Set membership (feeds / blocklists):** an IP range can belong to *many* scopes at
  once (`{A, B, bogons}`); total scopes are small (~hundreds). Mutations are
  *union / remove a member*. This is the update-ipsets retention + multi-feed-bundle
  + indexed-comparison use case.
- **Categorical (ASN / country / city / retention-age):** an IP range belongs to
  *exactly one* value per dimension; the number of *distinct* scopes is **uncapped**
  (ASNs ~100k+, cities up to millions). Mutations are *replace the value*. This is the
  geo/threat-intel-source use case ("all ASNs in one file").

Caller goal (locked): a multi-feed file must behave, **per scope**, like an independent
single-scope file — the caller operates on one scope without knowing how the others are
multiplexed. Multi-feed is a **bonus** (fast cross-scope comparison), never a
**constraint**. Single-scope ops on a multi-feed file MAY be slower; cross-scope
comparison is significantly faster.

## Terminology

- **scope** — a label attached to IP ranges (a feed, an ASN, a country, a retention
  window, a comparison index).
- **scope value** — the per-record opaque, fixed-width bytes (`scope_width` per file).
  Interpreted **only by the caller** (a categorical value, a membership bitmap, …); the
  engine never interprets it.
- **scope_id** — a scalar scope identity from the registry (`≥ 1`; `0` = the FILE target).
- **selector** — a **caller-supplied predicate** `match(scope: &[u8]) -> bool` over the
  opaque scope bytes, passed to `count*` / `query*`. The engine never interprets scope;
  SDK convenience constructors build common predicates (exact value, any-of-set, all).

## Decisions — LOCKED

- **D-A — Uncapped scope.** The number of distinct scopes per file is **not** bounded.
  This is achieved by scope-as-**value** (opaque fixed-width bytes), not scope-as-bit.
  A bitmap is one *optional* interpretation for bounded set-membership (capped at
  `scope_width × 8` *co-occurring* members — fine for ~hundreds of feeds; never used for
  the categorical/geo case).
- **D-B — Scope = opaque fixed-width value per file; coalescing is byte-equality.**
  Adjacent ranges merge iff their scope bytes are byte-equal (correct for both "same
  ASN" and "same feed-set"). `scope_width` fixed per file (D1 from livedb spec
  preserved); for a multi-dimension categorical file it is a fixed tuple, e.g.
  `asn(4)·country(2)·city(4)` (a caller layout choice).
- **D-C — Lifecycle.**
  - `create`, `open(read|write)`, `close`.
  - `close` with an open writer does `commit(flush=false)` (persists, no fsync).
  - `commit(flush bool)` — persist buffered changes; does **not** close the writer;
    fsync/fdatasync only when `flush=true`.
  - `verify` — explicit, caller-invoked; **no implicit verification** ever; **errors**
    if a transaction is open.
- **D-D — No transactions / no rollback.** Operations take effect in the working tree
  immediately and irrevocably. `commit` is purely a flush/durability checkpoint, not a
  rollback boundary. The CoW core still gives crash-*consistency* for free (on-disk meta
  only advances on commit; uncommitted pages auto-reclaimed by the derived allocator on
  reopen). The **only** way to discard uncommitted work is to terminate the process
  before `close` (which would otherwise commit). No `abort` API.
- **D-E — Errors on every call.** Every API returns `Result<T, Error>` (Rust) /
  `(T, error)` (Go). Typed shared error enum: `InvalidInput`, `Corruption`, `Io`,
  `Locked`, `Full`, `State`. Because verification is not implicit (D-C), corruption
  surfaces **lazily, per operation, including on reads** — so reads are fallible too.
  - **Unconditional bounds-safety:** per-access bounds checks (pgno < npages, offsets
    within page, against the *mapped length*) are always on. A corrupt-but-in-bounds
    file may return `Corruption`, never segfault; a short/truncated file yields
    `Io`/`Corruption`, never SIGBUS. (Concurrent truncation while the writer holds the
    lock is a `flock`-contract violation — documented, not defended.)
  - **Streaming error reporting:** `query*` use a **visitor callback** and return
    `Result<(), Error>`; the visitor is called per emitted element and may signal
    stop/error; the call returns the first error (visitor's, or internal
    corruption/io). Idiomatic in both languages, zero-alloc, every call stays fallible.
  - **`close` returns an error** (its `commit(false)` can fail). Rust `Drop` does a
    best-effort flush and swallows errors (mirrors `std::fs::File`); document that
    relying on `Drop`/deferred-close loses error visibility.
- **D-F…D-K — operation layer (registry, selectors, mutations, counting, extraction).**
  Specified authoritatively in **§v4.1** below; corrected from earlier drafts on four
  points the reviewers flagged:
  - the **registry** is the per-scope header in the scope table (§v4.1.C);
  - **selectors are caller-supplied predicates** over the opaque `scope` bytes — the
    engine never interprets `scope` (§v4.1.B);
  - **per-scope mutation policy** (add/remove a feed, replace, move) is **caller-side**,
    composed over the cursor + the core `set`/`delete`/`lookup` (§v4.1.A); the engine
    exposes no `range-add`/`range-del`/`rangeMove`;
  - **counts are on-demand** helpers, **not maintained** format state (no counters region,
    no per-op/per-commit counter upkeep) — §v4.1.B.
- **D-M — `commit(flush=false)` crash semantics.** Safe across a **process crash** (page
  cache survives); can **corrupt on power loss / kernel panic** (writeback may reorder
  meta before data). Durable+consistent checkpoints require `flush=true`. Caller's
  choice — **documented**.

## Resolved (the questions deferred to the update-ipsets investigation)

The 5-agent investigation and the design discussion resolved all of these — and the
resolution in every case is the same principle: iprange ships mechanism, callers own policy.

- **P1 — feed-set representation:** a **caller** concern. iprange stores opaque `scope`
  bytes; a caller that needs multi-membership chooses its own encoding (bitmap, interned
  set-id, …). Not an engine decision.
- **P2 — `rangeMove`:** **dropped from the engine** — no current use case, and it is a
  caller-side composition over `set`/`delete` + cursor if ever needed.
- **P3 — file granularity (tuple vs per-dimension):** a **caller** decision (geo is a
  caller module); the engine supports either layout unchanged.
- **P4 — counters:** **on-demand** SDK helpers, not maintained format state (§v4.1.B). A
  caller needing instant counts maintains its own.

## Relationship to existing artifacts

- `design-iprange-v4-livedb.md` — the core v4 format, **LOCKED & implemented**
  (SOW-0007). This document reinterprets D11 and adds the scope-aware operation layer on
  top of that unchanged core.
- `design-iprange-engine.md` — the multi-language engine/SDK target direction.
- Implementation SOWs: **SOW-0008** (cursor + helpers) and **SOW-0009** (metadata system).

---

# v4.1 additions: cursor, standard helpers, and the metadata system

Resolved in the 2026-06-23 design discussion. The **cursor + helpers** are read-only
(no format change) — **SOW-0008**. The **metadata system** is a format change
(new meta field + new on-disk structures), an additive **v4.0 → v4.1** minor bump —
**SOW-0009**. Both build on the unchanged locked v4 core (COW B+tree, double-meta commit,
derived allocator, mmap, flock, CRC32C).

## A. Cursor (read-only; no format change) — SOW-0008

- **API (as implemented, Rust reference — Go mirrors exactly):** `seek(key)`, `first()`,
  `last()`, `next()`, `prev()` (each returns `bool` = "positioned at a record"), and
  `current() -> Option<(from, to, scope)>`. Bounds-safe on hostile input (never OOB/panic),
  like the reader. The cursor is `Copy` (a `&Reader` + a fixed `[Frame; TREE_HEIGHT_MAX]`
  path stack + a 1-byte state), so a position can be snapshotted by value.
- **Position state:** `Empty` (no records) · `BeforeFirst` (initial; `next`→first,
  `prev`→stays) · `At` (a record; `current` yields it) · `AfterLast` (`prev`→last,
  `next`→stays).
- **`seek(key)` = successor:** positions at the **first record with `from >= key`**; `true`
  if one exists, else `AfterLast`/`false`. The record **covering** `key` (greatest
  `from <= key`, present iff `key <= to`) is obtained by: `seek(key)`; if
  `current().from != key`, `prev()` and test `to >= key`.
- **No sibling pointers (D3):** the cursor holds the root→leaf **path stack** (one `Frame`
  = `(pgno, idx)` per level); `next`/`prev` step within the leaf, else pop and re-descend
  leftmost/rightmost to the adjacent leaf.
- **Snapshot model (the mutate-during-open contract), made explicit:**
  - A cursor binds to the **committed root** (`root_pgno` from the active meta) at open and
    reads **only committed pages**.
  - It is safe to `set`/`delete` while a cursor is open: those allocate new pages from the
    **free set**, and **reclaim-after-commit (D7)** guarantees the committed pages the cursor
    is reading are **not** freed or reused until the *next* commit. So the cursor's view is a
    stable snapshot for the whole write transaction. This dependency on D7 is **normative** —
    if the allocator ever reused freed pages within a txn, the cursor guarantee would break.
  - **`commit` invalidates open cursors:** the committed root has advanced and prior pages may
    be reclaimed by the following txn, so a caller MUST re-`seek` to continue. Using a cursor
    across a commit is a `State` error, not undefined behavior.
- This is what lets a caller walk the *old* tree to compute a delta while writing the *new*
  one — the basis of caller-side retention/comparison/geo.

## B. Standard SDK helpers (on the cursor; cross-language conformance) — SOW-0008

A small, **standard** set so callers do not re-implement the same loops. **All take a
caller-supplied selector predicate** and stream via a visitor; the engine never interprets
`scope`.

- **Selector = a caller predicate `match(scope: &[u8]) -> bool`.** The helper provides the
  *mechanism* (ordered traversal + coalescing + CIDR decomposition + counting); the caller's
  closure provides the *policy* (which scopes match — bitmap overlap, value-set membership,
  whatever the caller's encoding is). This is how the helpers stay generic while `scope`
  remains **opaque** to the engine. (Convenience constructors — "this exact value", "any of
  this set", "all" — are SDK sugar that build the closure.)
- **Visitor:** `visit(item) -> ControlFlow<()>` (`Continue`/`Break`); `Break` is a clean
  early-exit. The helper returns `Result<(), Error>` (`Ok` for the validated-tree cursor; the
  `Result` reserves a future `Corruption`/`Io` path). A caller that must surface its own error
  captures it in the closure and returns `Break`. Zero-alloc.
- **coalescing scan** — the open-run primitive: one accumulator per matched run, flushed on
  discontinuity/end; single key-order pass, scratch `O(open runs)`.
- `queryRanges` / `queryRangesMerged` / `queryCIDRs` / `queryCIDRsMerged`. `[from,to]` may be
  a **single IP** (`from==to`, the `O(log n)` point case), a range, or **everything**
  (`[family_min, family_max]`).
- `countIPs` (returns `u128`, **saturating at `u128::MAX`** — only a fully-covered IPv6 space,
  `2^128`, would exceed it) / `countCIDRs` (returns `u64`; counts the **merged** runs'
  CIDRs — the netset entry count) — **on-demand** (cursor scan), **not** maintained format
  state (no counters region, no per-op/per-commit upkeep). A caller that needs instant counts
  maintains its own. `u128` counts serialize as 16 LE bytes (Go `math/bits` Add64/Sub64;
  Rust native `u128`) — value-identical across languages.
- **CIDR decomposition (normative, for cross-language identity):** `[from,to]` is emitted as
  the **canonical minimal cover** — repeatedly take, from the current position, the **largest
  aligned prefix** whose block fits within the remaining range, advance past it, repeat. This
  yields a unique, ordered CIDR list in both implementations.

## C. The metadata system (format change; v4.1) — SOW-0009

Multi-feed files are the norm, so **all per-feed metadata is per-scope**; the file header
stays purely structural.

**Handle placement of the API (decision 2026-06-24).** The metadata **read** API
(`scopeList`/`scopeName`/`scopeVersion`/`scopeType`, `metaGet`/`metaList`) is exposed on
**both** the read-only `Reader` and the `Writer`. The `Reader` (shared-lock `LOCK_SH`, the
concurrent consumer path) reads by descending the **on-disk committed tree** (validated at
open) — this is what makes a v4.1 file self-describing for read-only consumers, mirroring the
cursor placement (§A). The `Writer` (exclusive `LOCK_EX`) serves the same reads from its
**in-memory registry** during an open txn (so it reflects uncommitted mutations). All
**mutating** metadata calls (`scopeDefine`/`scopeDrop`, the header setters,
`metaSet`/`metaDelete`) are `Writer`-only.

### C.1 Header change (one field)

```
@90  scope_table_root  u32   0 = no metadata; else root of the scope table
```
`meta_size` 90 → 94; `version_minor` 0 → 1. Nothing descriptive in the header.

### C.2 Scope table = registry + per-scope header

A **new fixed-record B+tree keyed by `scope_id`** — a *separate* tree from the IP tree. It
shares the generic page / COW / allocator / commit machinery but has its **own node and
value layout**; it is **not** a reuse of the IP-tree code. Each entry IS the per-scope
header:

```
scope_id   u32      (B+tree key)
version    u64
type       u8       opaque caller hint; 0=unspecified, 1=ipset, 2=netset by convention
name_len   u16      0..256
name       u8[256]  UTF-8 (RFC 3629); name_len bytes used; remaining bytes MUST be zero
kv_root    u32      0 = no KV; else this scope's KV tree
```

- **`name` / `version` / `type` are SEEK get/set:** look up / CoW-update the entry,
  `O(log scopes)` — **no KV scan, no KV rewrite.** Fixed `name[256]` makes the seek a fixed
  offset.
- **`type` here is `u8`** (the per-scope header enum) — **distinct** from the per-scope KV
  `type`, which is `u32` (§C.4). Implementers must not conflate them.
- **Registry API (signatures):**
  - `scopeDefine(name) -> Result<scope_id>` — assigns the next `scope_id ≥ 1` (`0` is
    reserved for FILE, never returned); `version` starts at `0`; names need not be unique
    (`scope_id` is the identity).
  - `scopeDrop(scope_id) -> Result<Changed>` — removes the scope's **metadata only** (its
    scope-table entry + KV tree). It does **not** touch IP records carrying that scope —
    clearing the scope from records is caller policy (cursor + `set`/`delete`). `scopeDrop(0)`
    (FILE) → `InvalidInput`.
  - `scopeName(scope_id) -> Result<Option<name>>`; `scopeList() -> Result<list<(scope_id,
    name)>>` (ascending by `scope_id`).
- **`scope_id` and the record `scope` bytes are independent namespaces.** `scope_id` is the
  metadata key; the record `scope` is opaque membership the engine never decodes. The caller
  chooses any correlation (e.g. `scope_id` = a bitmap bit position, or = a categorical
  value); the engine relates the two only through caller-supplied helper predicates (§v4.1.B).
- **File/dataset-level metadata = reserved `scope_id 0`** (the `FILE` target): it has no
  `name`/`type`/`version`, only a `kv_root` holding dataset KV (dataset name, **signature**,
  provenance, …). `scope_id 0` always exists logically.

### C.3 `version` semantics (caller-bumped, not engine-auto)

The engine does **not** auto-increment `version`: it treats record `scope` as opaque, so
it cannot know which feeds a data mutation touched without decoding it (which would break
the opacity that keeps retention/comparison caller-side). Standard helpers, called by the
caller when it has updated a feed:

- `scopeBumpVersion(scope_id)` → read `+1` write (an `O(log scopes)` seek-to-set).
- `scopeSetVersion(scope_id, v)` → copy/preserve a version ("copy the last version").

Default policy (when/whether to bump) is the caller's.

### C.4 Per-scope KV (behind `kv_root`) — everything else

License, maintainer, info, URL, category, listing/unlisting notes, structured binary, …
**Unbounded** in both entry count and value size.

- **Entry = `(key, type, value)`:**
  - `key` — UTF-8 (RFC 3629), **1..1024 bytes**, no NUL (`0x00`); empty rejected.
  - `type` — **u32**. `0` = text (engine validates the value is valid UTF-8 (RFC 3629) +
    no NUL); any non-zero = **caller-defined binary** (opaque; e.g. 1=json, 2=cbor, or
    `format<<16 | version`). The engine interprets `type` **only** to decide
    text-validation; it never interprets the value bytes.
  - `value` — bytes, **any size** (incl. 0), opaque, **no limit**.
- **On-disk = a bulk-loaded B+tree** behind `kv_root` (variable-length keys; values inline
  when small, in **overflow page chains** when large). This keeps a scope's metadata
  **unbounded** and reads `O(log n_kv)`:
  - **read** (`metaGet`/`metaList`): normal descent / ordered scan. `metaGet` on a missing
    key returns **`Ok(None)`**, not an error.
  - **write** (`metaSet`/`metaDelete`): the txn buffers the scope's KV changes **in memory**;
    at **commit** the scope's KV tree is **bulk-rebuilt** from the sorted entries into fresh
    pages, `kv_root` is switched, the old pages are freed. So it is **full-rewrite per scope
    per commit** (many `metaSet` in one txn → one rebuild), with **no incremental
    split/merge** — bulk-load builds it balanced by construction. Large values' overflow
    pages are rewritten with it.
  - **Encoding & conformance:** the **page/entry encoding** is normative (§D) so either impl
    can parse the other's KV pages; the **tree shape** (fanout, the inline-vs-overflow
    threshold, bulk-load order) is **implementation-defined** and conformance is **cross-read
    behavioral** — Rust and Go MAY lay the tree out differently but MUST return identical
    entries (matching the v4.0 IP-tree model; **not** byte-identity). The leaf entry's value
    descriptor self-describes inline-vs-overflow (§D), so a reader handles either regardless
    of the writer's threshold.
  - **`type==0` (text) validation covers the *entire reassembled* value** (inline +
    overflow), not just the inline portion.
- **API:** `metaSet` / `metaGet` / `metaDelete` / `metaList(target, …)`, where
  `target = scope_id | FILE (scope_id 0)`.

### C.5 CoW, memory management & validation (all v4.1 structures)

- **Page-uniform (4096).** Whole-page **and whole-path** CoW; **copy-on-first-touch per
  txn** (a dirty-page set), in-place after that; per-**commit** not per-item; batching
  amortizes the path cost (upper tree levels copied once per commit).
- **One derived allocator for the whole file** (D4): at writer-open it walks `root_pgno`
  **+** `scope_table_root` **+** every entry's `kv_root` (its KV tree) **+** all overflow
  pages → a single free-page set. No on-disk freelist; **uniform pages ⇒ no external
  fragmentation, no free-slot merging**; reclaim-after-commit (D7).
- **Bounds-safety / never-panic (normative, mirroring the v4.0 core):** the reader MUST
  range-check `scope_table_root`, every `kv_root`, every internal child pgno, and every
  overflow pgno to `0` or `[2, total_pages)`; enforce **`TREE_HEIGHT_MAX = 32`** for the
  scope-table and each KV tree (reject deeper — adversarial-depth / stack-overflow defense);
  run a recursive `validate` on the default full-validate path (sorted + disjoint keys,
  height bound, page-type and self-pgno checks, per-page CRC32C); and reject `type==0` KV
  values that are not valid UTF-8 (whole reassembled value). **Overflow chains are read by
  count, not to a terminator:** the reader consumes exactly `ceil(value_total_len /
  overflow_payload)` pages and rejects a chain that revisits a page, cycles, or
  over/under-shoots → `Corruption`, never an infinite loop. A hostile but checksum-valid file
  may yield `Corruption`, never OOB/panic.
- **Grows + reuses, does not auto-shrink.** Churn reuses freed pages; reclaiming physical
  size needs a future `compact` (rebuild into a fresh file, like `export_v3`).

### C.6 Forward-compat & the v4.0 → v4.1 upgrade path

Additive minor bump, per §5.1 of the livedb spec:
- A **v4.0 reader** reads the IP tree correctly and skips `[meta_size, page_size)` — it never
  sees `scope_table_root` or the metadata.
- A **v4.0 writer MUST refuse** a v4.1 file — otherwise its allocator would treat the
  metadata pages as free and overwrite them. Normative check at writer-open:
  `version_minor <= supported_minor`, else refuse (or open read-only).
- A **v4.1 writer opening a v4.0 file** (no `scope_table_root`) treats it as no-metadata and
  may keep it v4.0; on the **first** metadata write it sets `version_minor = 1`, allocates
  the scope table, and writes `scope_table_root`. A file to which no metadata is ever written
  stays byte-compatible v4.0.

### C.7 Metadata API error mapping

- `metaGet` / `scopeName` on a missing key/scope → **`Ok(None)`** (absence is not an error).
- `key` violating the rules (empty / >1024 / contains NUL / not UTF-8), or a `type==0` value
  that is not valid UTF-8 → **`InvalidInput`**.
- `scopeDrop` / `metaDelete` of something absent → **`Ok(Unchanged)`** (no-op success).
- structural damage on any path → **`Corruption`**; I/O failure → **`Io`**; using a cursor
  across a commit, or `verify` with an open txn → **`State`**.

## D. v4.1 on-disk page formats (normative encoding; shape implementation-defined)

This extends livedb §5 (page types) and §7 (the derived-allocator walk) for v4.1. The common
16-byte page header is unchanged; `page_type ∈ {1,2,3}` remain meta / IP-branch / IP-leaf. A
**v4.0 reader never reaches** the new types (they hang off `scope_table_root`, which it does
not walk); a **v4.1 reader MUST reject an unknown `page_type`**. New types:

```
4 = scope-table branch
5 = scope-table leaf
6 = kv branch
7 = kv leaf
8 = overflow
```

**Scope-table leaf (`page_type 5`).** Common header + a sorted array of the fixed **275-byte**
per-scope records (§C.2), ascending by `scope_id`; `entry_count` in the header; capacity
`floor((page_size-16)/275)`; unused tail MUST be zero.

**Scope-table branch (`page_type 4`).** As livedb §5.2 with `key_width = 4`: child pointers +
separator `scope_id`s (u32).

**KV leaf (`page_type 7`).** A **slot-directory** page (variable-length entries): a `u16`
slot array (byte offsets) grows from the front, the entry heap from the back, entries sorted
by `key`. Each entry:
```
key_len      u16            1..1024
key          u8[key_len]    UTF-8, no NUL
type         u32
value_kind   u8             0 = inline, 1 = overflow
  if inline:   value_len u32 · value u8[value_len]
  if overflow: first_pgno u32 · value_total_len u64
```
`value_kind` makes inline-vs-overflow **self-describing**, so the threshold is the writer's
choice and any reader parses either.

**KV branch (`page_type 6`).** Slot-directory of variable-length separators:
`sep_len u16 · sep_key u8[sep_len] · child_pgno u32` (sorted by `sep_key`) + the leftmost
child pgno.

**Overflow (`page_type 8`).** Common header + `next_pgno u32` (0 = last) + payload
(`overflow_payload = page_size - 16 - 4`). A value is the concatenation of payloads along the
chain from `first_pgno`, truncated to `value_total_len`. The chain is read **by count**
(§C.5): exactly `ceil(value_total_len / overflow_payload)` pages; a revisit, cycle, or
length mismatch → `Corruption`.

**Conformance.** This page/entry encoding is normative so either implementation can read the
other's pages (**cross-read**). Tree **shape** — fanout, split points, inline/overflow
threshold, bulk-load order — is **implementation-defined** (as for the v4.0 IP tree);
conformance is **behavioral + cross-read**, not byte-identity.
