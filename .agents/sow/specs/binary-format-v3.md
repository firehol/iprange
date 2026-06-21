# iprange binary format v3 — byte-level specification (DRAFT)

**Status:** Draft for review. This is the gate before any code (per the design
review). It defines the on-disk bytes precisely so the Rust and Go libraries
produce **byte-identical** files. Decisions here implement
`.agents/sow/pending/SOW-0001-...md` (Design-review outcomes & decisions).

Legacy formats `v1.0` (IPv4) and `v2.0` (IPv6) are the existing C `iprange`
formats; this is **v3** (a fresh container). Legacy files are read-only-supported
for migration (§14).

---

## 1. Goals and non-goals

Goals: architecture-neutral; memory-mappable; cheap metadata read (front of file);
self-describing + extensible (skip-unknown sections); reusable across C/Rust/Go;
safe against malformed/hostile input; ready to carry a signature later.

Non-goals (v3, now): compression (may add as a section kind later); the merged
multi-feed file (phase 2, §13); the actual signing/key management (deferred — only
the *slot* is reserved, §11).

## 2. Why a custom format (not FlatBuffers / Cap'n Proto / MMDB)

- **MMDB** maps IP→one record with no set algebra and a heavyweight typed data
  section; multi-feed membership is awkward; no integrity/signing. We borrow its
  good ideas (offset-relative addressing, fixed endianness, mmap-friendliness) but
  not its model.
- **FlatBuffers / Cap'n Proto** are general object-graph serializers (IDL, codegen,
  vtables/pointers). Our payload is a flat array of fixed records plus a few blobs;
  their machinery is overhead and a parser/security surface we don't need. We do
  borrow: a front section directory, alignment, ignore-unknown evolution, and
  bounds-check-before-trust.
- We need a **tiny, fixed, fully-specified** layout we can make byte-identical
  across three languages and verify exhaustively. A custom format is the smaller,
  safer long-term contract here.

## 3. Conventions (normative)

- **Scalar integers** (`u8/u16/u32/u64`) are **little-endian**. (Native on x86/ARM;
  big-endian hosts byte-swap scalars — a small, documented penalty.)
- **IP keys are stored as fixed-width little-endian integers and compared
  numerically:** IPv4 = one `u32`; IPv6 = two `u64` — `hi` then `lo`, where `hi` is
  the most-significant 64 bits. A 128-bit comparison is "compare `hi`, then on a tie
  compare `lo`" — **two plain integer comparisons, no 128-bit type required**
  (handles Go's missing `u128`), and byte-identical across C/Rust/Go. (128-bit
  *arithmetic* — only producer-side, for unique-IP counts — uses `u128` in Rust and
  a `{hi,lo}` helper in Go.)  ← **KEY DECISION (confirmed 2026-06-21: integer-pair
  compare, not bytewise — per Costa).**
- **One family per file; IPv4 is NEVER widened to 128-bit.** The header
  `ip_version` flag fixes the family: an IPv4 file uses 4-byte keys throughout, an
  IPv6 file uses 16-byte keys. "Unify v4/v6" means the *algorithm* is written once
  and specialized per width by the compiler (Rust/Go generics) — it does **not**
  mean storing IPv4 as 128-bit. The common IPv4 case stays compact and cache-dense
  (this mirrors the legacy C `ipset` vs `ipset6` split).
- **Ranges are inclusive** on both ends `[start, end]` (matches legacy C
  `network_addr_t{addr,broadcast}`). A single IP is `start==end`.
- **Alignment:** every section begins at an offset that is a multiple of its
  required alignment (8 bytes default; the index section uses 16). Padding bytes
  between sections are `0x00` and are **not** covered by section hashes (they are
  covered by the file-size/offset checks).
- **No reliance on native struct layout.** All records are defined field-by-field
  with explicit offsets below; readers parse fields, they do not cast a byte slice
  to a language struct (a reader MAY fast-path a cast only when it has verified
  alignment + endianness match, but the *contract* is the byte layout).
- All offsets are **absolute from the start of the file**, `u64`.

## 4. Overall layout

```
[ HEADER            ] fixed 64 bytes, offset 0
[ SECTION DIRECTORY ] array of fixed entries (near front)
[ SECTION: feed-meta] identity/operational strings (near front, cheap to read)
[ SECTION: index    ] the interval map (the hot path; 16-byte aligned)
[ SECTION: values   ] interned value table (membership sets etc.)
[ SECTION: metrics… ] optional per-update computed metrics (ASN/geo/…)  [later]
[ SECTION: prose…   ] optional descriptive text                         [later]
[ SECTION: strings  ] string table referenced by the above
[ SECTION: signature] RESERVED, empty in v3 (signing deferred, §11)
```

Header + directory + feed-meta are at the front so a consumer can read identity
and locate sections by touching only the first pages.

## 5. Header (fixed 64 bytes at offset 0)

| Offset | Size | Type | Field | Notes |
|---:|---:|---|---|---|
| 0 | 8 | bytes | `magic` | ASCII `IPRANGE3` |
| 8 | 2 | u16 | `version_major` | 3 |
| 10 | 2 | u16 | `version_minor` | 0 |
| 12 | 2 | u16 | `header_size` | 64 (allows growth; readers use this, not a constant) |
| 14 | 2 | u16 | `flags` | bit0 ip_version (0=v4,1=v6); bit1 optimized; others reserved=0 |
| 16 | 8 | u64 | `file_size` | total bytes; must equal real size |
| 24 | 8 | u64 | `directory_offset` | start of the directory |
| 32 | 4 | u32 | `directory_count` | number of directory entries |
| 36 | 4 | u32 | `reserved0` | =0 |
| 40 | 8 | u64 | `entry_count` | number of ranges in the index |
| 48 | 8 | u64 | `generation_unixtime` | seconds since epoch (build time) |
| 56 | 8 | u64 | `unique_ip_count_lo` | low 64 bits of unique-IP count (see §9 overflow) |

Rationale: only fixed scalars live here. Variable-length identity (name, category,
maintainer, …) lives in the **feed-meta** section (§7) so the header stays fixed.

## 6. Section directory

At `directory_offset`, `directory_count` entries, each **fixed 56 bytes**:

| Offset | Size | Type | Field | Notes |
|---:|---:|---|---|---|
| 0 | 4 | u32 | `kind` | section kind id (§8) |
| 4 | 4 | u32 | `flags` | bit0 `must_understand` (reject file if unknown & set) |
| 8 | 8 | u64 | `offset` | absolute file offset of the section |
| 16 | 8 | u64 | `length` | section length in bytes |
| 24 | 8 | u64 | `align` | required alignment of `offset` |
| 32 | 8 | u64 | `reserved` | =0 |
| 40 | 16 | bytes | `hash` | first 16 bytes of SHA-256 of the section bytes (integrity; full 32 only in the signed-directory variant later) |

Unknown `kind`: skip if `must_understand`=0; reject the file if `must_understand`=1.
Directory entries are **sorted by `offset` ascending**; no two sections overlap.

## 7. feed-meta section (kind = 1)

Length-prefixed UTF-8 fields (each: `u32 length` + bytes), in this fixed order:
`name`, `category`, `maintainer`, `maintainer_url`, `source_url`,
`license` (a short token), then a `u32 license_flags` (bit0 `dont_redistribute`).
More fields may be appended later (older readers stop at the count they know;
the section carries a leading `u32 field_count`).

## 8. Section-kind registry

| id | name | status |
|---:|---|---|
| 0 | reserved/none | — |
| 1 | feed-meta | v3 |
| 2 | index (interval map) | v3 |
| 3 | values (interned value table) | v3 |
| 4 | strings (string table) | v3 |
| 5 | signature | v3 (reserved, empty) |
| 16+ | metrics (asn/geo/overlap/retention/…) | later |
| 64+ | prose/descriptive | later |
| 1024+ | vendor/experimental | open |

Ids are allocated centrally (this table is the registry). Unknown ids follow the
skip-unknown rule (§6).

## 9. index section (kind = 2) — the interval map

16-byte aligned. A header then a packed array of fixed records.

Index sub-header (32 bytes): `u32 record_size`, `u32 key_width` (4 or 16),
`u64 record_count`, `u64 value_table_section_kind` (=3), `u64 reserved`.

Each record (**IPv4: 12 bytes; IPv6: 40 bytes**) — IPv4 keys are 32-bit, never
widened:

| Field | v4 size | v6 size | Notes |
|---|---:|---:|---|
| `start` | 4 | 16 | little-endian; v4 = `u32`; v6 = `u64 hi` then `u64 lo` |
| `end` | 4 | 16 | same encoding; inclusive |
| `value_id` | 4 | 4 | index into the values table (`0xFFFFFFFF` = no value / "present, no label") |
| `pad` | 0 | 4 | v4 needs none (4-byte aligned); v6 pads to 8-byte alignment |

Normative invariants (a reader MUST verify; a writer MUST guarantee):
- records sorted by `start` ascending; **disjoint** (no overlap); for the
  single-feed file, coalesced (adjacent ranges with the **same `value_id`** are
  merged). Tie-break/secondary sort: by `end` then `value_id` (deterministic →
  byte-identical output across implementations).
- `start <= end` for every record.
- a point lookup = numeric binary search (compare `hi` then `lo` for v6) for the
  record whose `[start,end]` contains the key.

**Unique-IP count overflow (IPv6):** the true count can reach `2^128`, which does
not fit in 64 bits. The header carries `unique_ip_count_lo` (low 64 bits) plus a
flag; full 128-bit count, when needed, is recomputed or stored in feed-meta as a
decimal string. (Matches legacy C saturation behavior, but made explicit.)

## 10. values section (kind = 3) — interned values

A table of distinct values, referenced by `value_id` (= index). Decision O4:
**interned sorted id-lists** (good for sparse membership). Layout: `u32 count`,
then `count` entries; each entry: `u32 type_id`, `u32 byte_length`, then bytes.
For a membership set the bytes are a sorted list of `u32` feed-ids.

Interning is **content-addressed**: two values are the same iff their bytes are
equal. The writer dedups by exact bytes (a hash may index the dedup map, but
equality is by full bytes — **no hash-collision risk**). Entries are written in a
deterministic order (first-occurrence during the sweep) so output is reproducible.
Unknown `type_id`: a reader that doesn't understand it can still return the raw
bytes / skip — same skip-unknown principle.

## 11. signature section (kind = 5) — RESERVED, deferred

v3 reserves the slot but **does not implement signing**. When added: a detached
signature over `(header || directory)` — because the directory carries each
section's hash, signing the directory transitively authenticates the whole file,
and a consumer can verify one section against its directory hash. Header gets
`algo_id` + `key_id` fields (in the reserved space) at that time. Key
distribution/rotation/revocation is a separate later design. **Until then, files
are unsigned; readers must not imply authenticity.**

## 12. Versioning & compatibility

- `version_major` bump = incompatible; a reader rejects unknown major.
- `version_minor` bump = additive; older readers ignore new sections (skip-unknown)
  and new trailing header fields (bounded by `header_size`).
- Mandatory new sections use the directory `must_understand` bit.

## 13. Multi-feed merged file (phase 2 — outline only)

Adds: a `catalog` section (feed-id → name/category + dossier offset) and a
**merged index** whose `value_id` points to a membership set (which feeds cover
that range) → one lookup returns all memberships. Same record/values machinery.
Open: feed-id registry vs. interning feed **names** (avoids a global registry);
build cost; cold-lookup speed at full scale — **measure before committing** (the
plain-binary-search lookup at ~tens of millions of ranges is µs-scale cold, so an
accelerator is likely required, not optional).

## 14. Legacy read (migration)

The reader also accepts legacy `v1.0`/`v2.0` (ASCII pseudo-header + native-endian
record dump). Big-endian-written legacy files were already rejected by the C tool;
v3 reader inherits that limit (documented). Legacy support is read-only and may be
deferred to a later step.

## 15. Safety & integrity (normative — even with signing deferred)

**No artificial size limits.** The format supports files as large as the user
wants — that is the point of mmap. There is **no maximum** on IPs, ranges,
sections, or file size. Safety comes from **consistency with the real file size,
not from arbitrary caps.**

A reader MUST, before trusting anything:
- `fstat` the file; refuse non-regular files; require the header `file_size` to
  equal the actual size.
- Validate every `offset`/`length`/count with **overflow-safe arithmetic**, each
  required to fit within the **actual file size**: `offset + length` must not
  overflow and must be ≤ real size; `directory_count × 56 ≤ size`;
  `entry_count × record_size == index_length`. A claim the bytes don't back up is
  rejected — this bounds everything to reality with **no fixed ceiling**.
- **Never pre-allocate from an unverified claimed count.** In mmap mode nothing is
  allocated for the data (the file is mapped and accessed in-bounds), so a genuine
  huge file is fine and a lying header is harmless. In owned-mutable mode, size
  allocations only from counts already validated against the real file size, and
  grow incrementally.
- Reject overlapping sections; check alignment.
- Only after structural checks pass, map/read sections. (When signing lands:)
  verify the whole file once at download/install, then trust the local immutable
  file. Publishers MUST publish atomically (temp file → fsync → rename).
- A malformed file must **never** crash, over-allocate, or read out of bounds
  (fuzzed in tests).

A consumer MAY set an **optional** self-imposed ceiling for its environment (e.g.
a small IoT device), but the **default is unlimited**.

**Platform note (not a format limit):** on 32-bit hosts the virtual address space
(~2–3 GB) limits how much can be mmap'd at once; such hosts use a pread/windowed
fallback for very large files. On 64-bit hosts (the norm) there is no practical
limit.

## 16. Reader modes (recap)

- **metadata-only:** header + directory + feed-meta (front pages only).
- **mmap read-only:** map index (+values), numeric binary-search lookups
  (compare `hi` then `lo` for v6), zero allocation on the hot path.
- **owned-mutable:** parse into in-memory structures for editing/rewriting.

## 17. Open items (need a decision or measurement)

- IP keys as little-endian integer pairs, compared numerically (§3) — **confirmed
  (per Costa, 2026-06-21).**
- Size caps (§15) — **resolved (per Costa): no artificial caps; bounded only by
  the real file size; optional per-consumer ceiling, default unlimited.**
- Legacy read timing — **resolved (default): deferred to last (sub-step 1D).**
- Phase-2 feed identity: numeric registry vs interned names (§13).
- **Index layout: records (array-of-structs) vs columns (struct-of-arrays).** SoA
  = separate `starts[]`, `ends[]`, `value_ids[]` arrays, so a binary search scans
  only the dense `starts[]` (16 IPv4 starts per 64-byte cache line) and reads
  `end`/`value_id` once at the hit. Better cache use (esp. IPv4) at the cost of a
  slightly more complex layout. Decide via the early benchmark (1B/1C).
