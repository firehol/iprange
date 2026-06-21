# iprange binary format v3 — byte-level specification

**Status:** Normative contract (revised through four external review rounds on
2026-06-21). This is the gate before any code: it defines the on-disk bytes
precisely so the Rust and Go libraries produce **byte-identical** files. Decisions
D1–D6 (recorded in `.agents/sow/pending/SOW-0002-...md`) are folded in.

Conformance language: **MUST / MUST NOT / SHOULD / MAY** are normative. A reader
that accepts a file violating a MUST is non-conforming; a writer that emits one is
non-conforming. Where a check is labelled "reader MUST reject", a conforming
reader discards any partial state, returns a typed error, and exposes nothing from
the file.

**Byte-identical contract (scope):** two conforming writers given **identical
logical inputs** MUST emit the same file bytes. Identical logical inputs means: the
same set of ranges; the same per-range **value bytes** (the caller supplies the exact
value content — the format treats it as opaque, see §10); and the same
externally-supplied metadata *bytes* (feed-meta field bytes, `license_flags`,
`generation_unixtime`). All other on-disk values (`entry_count`, `unique_ip_count_*`,
`value_id` assignment, section offsets/padding, hashes) are **derived
deterministically** by the rules below. The format does **not** own any *semantic*
mapping (e.g. which feed a membership-set id denotes): that is the engine's (Step 2) /
phase-2 concern; a stable cross-producer feed-id registry is a phase-2 deliverable
(§13). A **writer library MUST NOT** normalize, trim, case-fold, re-encode, or
otherwise transform caller-supplied value or metadata bytes — it emits them verbatim
(validating only structure, §7/§10) or rejects invalid input; its public API MUST
document this.

Legacy formats `v1.0` (IPv4) and `v2.0` (IPv6) are the existing C `iprange`
formats; this is **v3** (a fresh container). Legacy files are read-only-supported
for migration (§14).

---

## 1. Goals and non-goals

Goals: architecture-neutral; memory-mappable; cheap metadata read (front of file);
self-describing + extensible (skip-unknown sections); reusable across C/Rust/Go;
**byte-identical across independent implementations**; safe against
malformed/hostile input; ready to carry a signature later.

Non-goals (v3, now): compression (may add as a section kind later); the merged
multi-feed file (phase 2, §13); the actual signing/key management (deferred — only
the *slot* is reserved, §11); a lookup accelerator (kind 6 is reserved, layout
deferred, §8/§17).

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
  across languages and verify exhaustively. A custom format is the smaller, safer
  long-term contract here.

(Rationale for individual decisions lives in SOW-0001/SOW-0002, not here.)

## 3. Conventions (normative)

- **Scalar integers** (`u8/u16/u32/u64`) are **little-endian**. `MaxUint64` denotes
  `2^64 − 1` (`u64::MAX` / `math.MaxUint64` / `UINT64_MAX`).
- **IP keys are stored as fixed-width little-endian integers and compared
  numerically:** IPv4 = one `u32`; IPv6 = two `u64`, field order **`hi` then
  `lo`**, where `hi` is the most-significant 64 bits. Each `u64` is serialized
  little-endian. **Ordering:** compare `hi`; on a tie, compare `lo` — two plain
  integer comparisons, no 128-bit type on the lookup hot path (so Go needs no
  `u128` there). **Equality:** two v6 keys are equal iff `hi_a == hi_b AND
  lo_a == lo_b`. This numeric order is **not** raw-byte order, and the hi-then-lo
  layout is **not** a native `u128` in memory (a native LE `u128` would put `lo`
  first); a v6 key MUST NOT be cast to a native 128-bit integer. Readers compare by
  value, never by `memcmp`.
  - **Exact IPv6 key byte layout (16 bytes):** bytes 0–7 (inclusive) = `hi`
    little-endian, bytes 8–15 (inclusive) = `lo` little-endian. Example `2001:db8::1`:
    `hi = 0x2001_0db8_0000_0000`, `lo = 0x1` →
    `00 00 00 00 b8 0d 01 20  01 00 00 00 00 00 00 00`.
  - **IPv4 key (4 bytes):** `192.0.2.1` = `0xC0000201` → LE bytes `01 02 00 c0`.
- **128-bit arithmetic scope:** the producer needs 128-bit
  **add / subtract / compare / count** (coalescing `end+1`, unique-IP
  `Σ(end−start+1)`). The `unique_ip_count` sum uses 128-bit carry-add with the same
  carry rule as `u128_inc` generalized to two operands (`lo' = a.lo + b.lo;
  carry = lo' < a.lo; hi' = a.hi + b.hi + carry`), matching legacy `src/uint128.h`
  `u128_add`; Rust and Go MUST implement this identically. A *reader's* lookup hot
  path and its **mandatory** validation
  walk (sortedness, disjointness, `start ≤ end`) use only hi-then-lo `u64`
  comparisons — **no 128-bit arithmetic**. Only a reader that **optionally** verifies
  the v6 coalescing invariant (§9) needs a single 128-bit addition `end + 1`, using
  `u128_inc` semantics (legacy `src/uint128.h`): `lo' = lo + 1; hi' = hi + (lo ==
  UINT64_MAX ? 1 : 0)`. Such a reader MUST pre-check the family maximum (`hi == lo ==
  UINT64_MAX`) and skip the check on that record before computing `end + 1` (mirroring
  the writer rule, §9 — `end + 1` is undefined at the all-ones value). Rust uses
  `u128`; Go uses a `{hi,lo}` helper set (carry-add, borrow-subtract) — required for
  the producer, optional for the reader.
- **One family per file; IPv4 is NEVER widened to 128-bit.** Header `flags` bit0
  (`ip_version`) fixes the family; the algorithm is width-specialized at compile
  time (Rust monomorphization / Go generics) — no per-lookup width dispatch
  (mirrors legacy C `ipset` vs `ipset6`).
- **All comparisons are unsigned.** Every IP-key, size, count, offset, and
  `end + 1` comparison uses unsigned integer semantics (in Go, use `uint32`/`uint64`
  — not the default signed `int` — to avoid sign-related bugs).
- **Ranges are inclusive** `[start, end]`. A single IP is `start == end`.
- **All reserved and pad bytes everywhere MUST be written as zero** (`0x00`); a
  reader MUST reject any non-zero reserved/pad: header `flags` bits 1–15, header
  `license_flags` bits 1–31, directory-entry `flags` bits 1–31, directory-entry
  `reserved`, index sub-header `reserved`, and the v6 record `pad` (one per record).
  (Trailing header bytes `[72, header_size)` are NOT zero-checked: in a
  `version_minor > 0` file they may be defined fields a v3.0 reader does not know, so
  zero-checking them would break additive forward-compatibility — see §12. For
  `version_minor == 0` there are no trailing bytes because `header_size == 72`.)
- **Inter-region padding** (the alignment gap before/between sections, including the
  gap between the directory and the first section) MUST be written `0x00` by the
  writer and is **not** covered by any section hash. A reader MUST reject non-zero
  padding. The scan is `O(number_of_sections × max_align)`, bounded by the file
  size.
- **Deterministic offsets:** the directory immediately follows the header
  (`directory_offset == header_size`). Each section's `offset` =
  `align_up(prev_end, section_align)` where
  `align_up(x, a) = (x + a − 1) & ~(a − 1)` and `prev_end` is the previous section's
  `offset + length`, or `directory_offset + directory_count × 72` for the first
  section (and for a zero-length section). No over-padding. The writer MUST compute
  `align_up` overflow-safe (a file whose `prev_end > MaxUint64 − (align − 1)` is
  unrepresentable), mirroring the reader's check (§15 step 6).
- **Packed, no compiler padding.** Every multi-field structure is packed: the byte
  offsets in the tables below are exact, with no implementation-inserted padding. A
  reader MAY use a cast-based fast path **only** when (a) host endianness matches,
  (b) the struct is declared packed (`#[repr(C, packed)]` in Rust) with matching
  field order/types, and (c) the section's `align` is sufficient for the widest
  scalar it casts (≥ 8 for any `u64`); otherwise it parses field-by-field.
  - **Go note:** `encoding/binary.Read`/`Write` over a Go **struct** uses Go's native
    layout (with alignment padding) and would mis-parse/mis-emit this format. Go
    readers and writers MUST NOT pass a multi-field struct to `encoding/binary`; they
    MUST read/write each field individually (or via an explicit byte buffer). The
    offsets here are exact and no Go struct alignment may be assumed; the conformance
    corpus verifies each section's exact byte length (so stray padding is caught).
- **Section bodies MUST NOT contain absolute file offsets.** Only the directory
  carries offsets; each section is independently hashable and relocatable.
- All directory offsets are **absolute from the start of the file**, `u64`.
- **Portability note:** big-endian hosts byte-swap **every** little-endian scalar
  they interpret — header/directory/sub-header fields, the index records, AND the
  values section scalars (`count`, `type_id`, `byte_length`, membership-set ids). The
  IPv6 comparator swaps up to four `u64` per compare on the hot path. BE readers
  SHOULD swap the index (and values) arrays once at load (owned-mutable) — but only
  **after** verifying the section hash — or use a `bswap64`-inlined comparator (mmap). Most targets are LE; this is a
  documented penalty, not a correctness issue. A BE-written-without-swap file is
  non-conforming and fails the §15 structural checks.

## 4. Overall layout (canonical order — normative)

```
[ HEADER            ] fixed 72 bytes, offset 0
[ SECTION DIRECTORY ] array of fixed 72-byte entries; offset == header_size (72)
[ SECTION: feed-meta] kind 1; align 8
[ SECTION: index    ] kind 2; align 16 (the hot path)
[ SECTION: values   ] kind 3; align 8; present iff used
[ SECTION: (7–15)…  ] future core sections, ascending kind                  [later]
[ SECTION: metrics… ] kinds 16–63, ascending kind                          [later]
[ SECTION: prose…   ] kinds 64–1023, ascending kind                        [later]
[ SECTION: signature] kind 5; align 8; RESERVED, empty in v3 (§11)
```

Ordering rules (a writer MUST follow; a reader MUST verify):
- `directory_offset == header_size`; the directory immediately follows the header.
- Sections appear in this total order: by **canonical band** — core (kinds 1–6:
  feed-meta 1, index 2, values 3, and reserved 4 and 6 — by ascending kind, between 3
  and 7, if a future version ever uses them), future-core (7–15), metrics (16–63),
  prose (64–1023), vendor (1024+), then the signature (kind 5) **last** (kind 5 is the
  sole exception to ascending-kind order — it is placed last because, once signing
  lands, it signs the directory and so must be finalized after every other section).
  Within a band, sections are ordered by **ascending `kind`**; multiple sections of
  the **same** kind keep the caller-supplied relative order (part of the
  byte-identical contract only when the caller's input is identical). This pins the
  canonical position of every kind (incl. reserved 4/6) so a future version cannot
  diverge.
- Each section's `offset` = `align_up(prev_end, align)` (§3). No over-padding.
- A zero-length section (the v3 signature, `length=0`) still occupies its canonical
  position; its `offset = align_up(prev_end, align)`, MUST be ≤ `file_size`, and it
  contributes no bytes.
- `file_size` MUST equal the end of the highest-offset section (`offset + length`;
  in v3.0 that is the signature section); there is no trailing padding. A reader
  MUST reject trailing bytes.

**Required vs optional sections:**
- Required: feed-meta (1), index (2), signature (5, `length=0` in v3).
- Required if used: values (3) — a writer MUST emit it iff any index record has
  `value_id != 0xFFFFFFFF`. A reader does NOT require the reverse: a values section
  present but unreferenced is wasteful, not invalid (the "used ⇒ present" direction is
  enforced by the `value_id` bounds check, §9/§15). When present, `count` ≥ 1.
- Optional/later: future-core (7–15), metrics (16–63), prose (64–1023),
  vendor (1024+). Optional kinds MAY repeat.
- A reader MUST reject a file missing a required section or containing more than one
  section of any mandatory kind (1, 2, 3, 5).

## 5. Header (fixed 72 bytes at offset 0)

| Offset | Size | Type | Field | Notes |
|---:|---:|---|---|---|
| 0 | 8 | bytes | `magic` | ASCII `IPRANGE3` |
| 8 | 2 | u16 | `version_major` | 3 |
| 10 | 2 | u16 | `version_minor` | 0 |
| 12 | 2 | u16 | `header_size` | 72 in v3.0; multiple of 8; ≥ 72 (readers use this, §12) |
| 14 | 2 | u16 | `flags` | bit0 `ip_version` (0=v4,1=v6); bits 1–15 reserved=0 |
| 16 | 8 | u64 | `file_size` | total bytes; MUST equal the real file size |
| 24 | 8 | u64 | `directory_offset` | MUST equal `header_size` |
| 32 | 4 | u32 | `directory_count` | number of directory entries (≥ 3 in v3) |
| 36 | 4 | u32 | `license_flags` | bit0 `dont_redistribute`; bits 1–31 reserved=0 |
| 40 | 8 | u64 | `entry_count` | index record count after sort+coalesce (computed; backpatched) |
| 48 | 8 | u64 | `generation_unixtime` | seconds since Unix epoch UTC; `0` = unknown. **Externally supplied** |
| 56 | 8 | u64 | `unique_ip_count_lo` | low 64 bits of the unique-IP count (computed; backpatched) |
| 64 | 8 | u64 | `unique_ip_count_hi` | high 64 bits (computed); MUST be 0 for IPv4 |

Total = 72 bytes. Variable-length identity lives in feed-meta (§7).

- **Writing computed fields (backpatch):** `entry_count` and `unique_ip_count_*`
  are derived from the finalized index, which is written **after** the header.
  A writer MUST therefore write these fields only after the index is finalized —
  typically by streaming the body then seeking back to overwrite the header
  (backpatch), or by buffering the header until all sections are complete. A writer
  that emits these fields before the index is finalized is non-conforming.
- `license_flags` is in the header so a consumer can gate on `dont_redistribute`
  without reading any section.
- **`generation_unixtime` is a writer input, not auto-generated.** It is **POSIX
  time** (`time(2)` semantics — seconds since 1970-01-01 UTC, leap seconds not
  counted). For byte-identical output the writer MUST use the caller-supplied value
  (the conformance harness supplies a fixed value); two systems may disagree on "now"
  (clock skew, leap-second smearing), so a convenience "default to current time" path
  MUST NOT be used when deterministic output is required.
- **`entry_count` (computed):** the number of index records after sort and coalesce
  (= index sub-header `record_count`); a reader MUST verify equality (§15).
- **`unique_ip_count` (computed):** the full 128-bit value `(hi << 64) | lo`,
  defined as `Σ (end_i − start_i + 1)` over all records. For IPv4 both the per-range
  size `(end − start + 1)` and the sum MUST be computed in `u64` — the full-v4-range
  size `2^32` overflows `u32` — and the whole sum fits in `u64` (≤ `2^32`); `hi` MUST
  be 0 and `lo ≤ 2^32` — the **full IPv4 space (`lo = 2^32`) is valid and accepted**
  (no v4 rejection for size). IPv6 uses overflow-checked 128-bit arithmetic, and only
  **IPv6** has an unrepresentable case: **the single per-range size `(end − start +
  1)` can itself overflow** — the full-space range `[0, 2^128−1]` has size `2^128`,
  which does not fit in `u128`. An **IPv6** writer MUST therefore detect that single
  range **structurally** — `start_hi == 0 AND start_lo == 0 AND end_hi == UINT64_MAX
  AND end_lo == UINT64_MAX` — **before** computing the size, and reject it (no real
  feed covers all of IPv6). This applies to the final on-disk (coalesced) records;
  the case where several ranges together cover the whole space is caught by the same
  rule: any IPv6 accumulation that would exceed `2^128−1` is a writer error → reject.
  (A v6 sum of exactly `2^128−1` is representable and allowed.)
- In an unsigned v3 file the header fields (including `unique_ip_count_*` and
  `generation_unixtime`) are not authenticity-protected; consumers MUST NOT rely on
  them for security decisions until signing lands (§11, §15 threat model). A reader
  **SHOULD** recompute `unique_ip_count`/`entry_count` from the index and reject on
  mismatch (a stronger corruption check matching the legacy C loader; the conformance
  corpus includes a mismatched-count file that MUST be rejected); at minimum it MUST
  treat these
  fields as **unverified metadata** — never as input to memory allocation or bounds
  decisions. **Reader-side overflow rule:** a reader that recomputes
  `unique_ip_count` MUST use overflow-checked 128-bit addition and MUST reject the
  file on overflow. (This is **not** the same as the writer's structural full-space
  pre-check: two non-full-space ranges can sum to `2^128` and overflow, which only the
  checked addition catches — a structural `[0, 2^128−1]` check would miss it.) The
  mandatory per-record safety walk
  (§9) does not recompute the count and is unaffected — the full-space range is
  structurally valid for lookups (it simply matches every key).
- A reader MUST reject if `magic != "IPRANGE3"` (compared **bytewise** — 8 ASCII
  bytes, not interpreted as a scalar, so it is endianness-independent),
  `version_major != 3`, `header_size % 8 != 0`, or `header_size < 72`. For
  `version_minor == 0`, `header_size` MUST equal 72 (reject otherwise). A reader MUST
  accept any `version_minor` (`u16`), applying skip-unknown to sections and to
  trailing header fields `[72, header_size)` it does not recognize (those bytes are
  NOT zero-checked — they may be defined fields of a newer minor version; §3, §12).

## 6. Section directory

At `directory_offset` (== `header_size`), `directory_count` entries, each **fixed
72 bytes**:

| Offset | Size | Type | Field | Notes |
|---:|---:|---|---|---|
| 0 | 4 | u32 | `kind` | section kind id (§8); MUST NOT be 0 |
| 4 | 4 | u32 | `flags` | bit0 `must_understand`; bits 1–31 reserved=0 |
| 8 | 8 | u64 | `offset` | absolute file offset of the section |
| 16 | 8 | u64 | `length` | section length in bytes |
| 24 | 8 | u64 | `align` | required alignment of `offset` |
| 32 | 8 | u64 | `reserved` | =0 |
| 40 | 32 | bytes | `hash` | full SHA-256 of the section bytes `[offset, offset+length)` |

- Directory entries MUST be **sorted by `offset` ascending**; on a tie (two
  zero-length sections at the same offset — cannot occur in v3.0 but possible once
  future minor versions add zero-length sections) they MUST be ordered by **canonical
  position (§4)**, not by raw `kind` number (the signature, kind 5, sorts *last* by
  canonical position despite its low kind), so the sort is total and byte-identical.
  Non-overlap is a single O(N) pass (`offset[i] ≥ offset[i−1] + length[i−1]`). No
  section overlaps another, the directory region, or the header. A reader processes
  sections in **directory order** (which, by these rules, equals the §4 canonical
  order) — pinning the processing order even for repeated optional kinds in a future
  version.
- `align` MUST be **one of** `{8,16,32,64,128,256,512,1024,2048,4096}` (each a power
  of two — that property is informational; the set membership is the normative check)
  and `offset % align == 0`; a reader MUST reject any value outside this set. For a
  **known, defined** kind, `align` MUST equal the §8 canonical value; for a
  reserved/unknown kind, only the set-membership and `offset % align == 0` checks
  apply.
- **`flags` is fully determined by `kind` (byte-identical):** for every known kind
  the `flags` value is fixed — `must_understand = 1` for the required core sections
  (kinds 1 feed-meta, 2 index, 3 values) and `must_understand = 0` for the signature
  (kind 5); all other `flags` bits are 0. A writer MUST emit exactly these values; a
  reader MUST reject a known-kind entry whose `flags` differ. (This removes the only
  writer freedom in the directory-entry bytes; without it two writers could diverge.)
- Unknown **or reserved** `kind`: treated as unknown — skip if `must_understand=0`;
  reject the file if `must_understand=1`. (A v3.0 writer never emits unknown or
  reserved kinds.) A *known* kind with `must_understand=0` whose content a reader
  chooses not to interpret may also be skipped (how a v3.0 reader treats a future
  signed signature section — §11).
- `hash` is the full 32-byte SHA-256 of the section's bytes — exactly the bytes
  `[offset, offset+length)`, **including any pad field that lives inside the section**
  (e.g. the v6 record `pad`). v3.0 sections contain no *unspecified* intra-section
  padding: every byte of a section is a defined field, and all pad/reserved fields are
  zero (§3) — so the hashed content is fully determined. The digest is stored in the
  **standard SHA-256 digest byte order** (the digest's natural output order, byte 0
  first) — it is a 32-byte string, **not** a scalar, so the little-endian scalar rule
  does NOT apply and it is never byte-swapped (a BE host compares its native
  `[u8; 32]` digest byte-for-byte against this field; the lowercase-hex constant below
  is this byte sequence). For a **zero-length section** the hash is `SHA-256` of the
  empty input (`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`);
  it MUST NOT be written as all-zeros. The hash algorithm is SHA-256 for all of v3;
  changing it requires a `version_major` bump. Verification policy: §15.
- The **header is not a section** and has no per-section hash; in an unsigned v3 file
  it is unauthenticated (a future signature over `header || directory` covers it).
- Directory entries are fixed at 72 bytes for all v3.x; the `reserved` field can be
  repurposed only in a `version_major` bump.

## 7. feed-meta section (kind = 1, align 8)

```
u32 field_count                  ; number of length-prefixed string fields that follow
repeat field_count times:
    u32 length                   ; byte count of the UTF-8 value (0 = empty)
    u8  bytes[length]            ; UTF-8 (NOT NUL-terminated; embedded NUL allowed)
```

For v3.0, `field_count` MUST be **6**, fields in this fixed order: `name`,
`category`, `maintainer`, `maintainer_url`, `source_url`, `license`. The section
`length` MUST equal exactly `4 + Σ(4 + length_i)` — no trailing padding. Every
field is length-prefixed, so a reader that knows fewer fields skips unknown trailing
fields by reading their lengths. Future minor versions append fields and raise
`field_count` (a v3.N reader reads the 6 fields it knows by 0-based index 0..5, then
skips extras by length). A reader MUST reject if `field_count < 6` (for
`version_minor == 0`, reject if `!= 6`) or if any declared length runs past the
section.

Field semantics: `name` (human-readable feed name); `category`;
`maintainer`/`maintainer_url`; `source_url` (the original threat-intel source URL);
`license` (a short token, SPDX preferred). The machine-readable redistribution gate
is `license_flags` in the **header** (§5). **For v3.0 (unsigned) files
`license_flags.dont_redistribute` is advisory only** — the header is not
authenticity-protected, so an attacker can flip the bit; it MUST NOT be the sole
redistribution-enforcement mechanism. Enforcement requires signed files (§11) or an
out-of-band trust channel.

**Encoding / determinism:** field bytes are written **verbatim** as supplied. A
**writer library MUST NOT** normalize, trim, case-fold, or re-encode; it emits valid
input verbatim or rejects invalid input — it MUST NOT transform/replace bytes. A
writer MUST emit **well-formed UTF-8** (RFC 3629 / the Unicode "well-formed code unit
sequence" definition — no overlong encodings, surrogate code points, or values
> U+10FFFF) for every field; invalid UTF-8 input is a writer-side error, and its
public API MUST document the no-normalization rule. A reader **MUST** validate that
each field is well-formed UTF-8 and, on invalid UTF-8, **MUST reject the file** (a
single, deterministic behavior — a conforming writer never emits invalid UTF-8, so
this only fires on a corrupt/non-conforming file; the conformance corpus asserts
rejection). A reader MAY additionally offer a lenient display mode (replace invalid
sequences with U+FFFD) as an explicit caller opt-in, not the default. Independently,
renderers (web UIs etc.) MUST treat feed-meta as untrusted content and escape it
appropriately (HTML/JS) — even well-formed UTF-8 can carry markup; it is
attacker-influenced.

## 8. Section-kind registry

| id range | name | status | align (v3.0) |
|---:|---|---|---:|
| 0 | reserved/none | invalid (reject) | — |
| 1 | feed-meta | v3 | 8 |
| 2 | index (interval map) | v3 | 16 |
| 3 | values (interned value table) | v3 | 8 |
| 4 | reserved (was "strings"; never emitted in v3) | reserved | — |
| 5 | signature | v3 (reserved, empty) | 8 |
| 6 | lookup-accelerator (DIR-24-8 / Poptrie / SoA) | reserved (layout deferred, §17) | TBD |
| 7–15 | reserved for future core sections | reserved | TBD |
| 16–63 | metrics (asn/geo/overlap/retention/…) | later | TBD |
| 64–1023 | prose/descriptive | later | TBD |
| 1024+ | vendor/experimental | open | TBD |

The ranges are **disjoint**; a kind belongs to exactly one band. A writer MUST set
each known section's `align` to the value above; a reader MUST reject a mismatch for
known kinds. Within a `version_major`, a kind's canonical `align` MUST NOT change
(a different alignment requires a new kind or a major bump) — this keeps the
cast-based fast path's alignment guarantee stable across minor versions. Unknown/
reserved ids follow skip-unknown (§6). Future kinds' canonical position (§4) and
alignment are defined when the kind is specified — and the spec that allocates a
future kind MUST also pin its `must_understand` value, so two future writers cannot
disagree (until then the kind is never emitted). Sections in the metrics (16–63),
prose (64–1023), and vendor (1024+) bands MUST carry `must_understand = 0` (they are
optional/skip-on-unknown; anything genuinely required needs a `version_minor`/major
that pins it per §12), and a reader MUST reject a section in those bands whose
`must_understand = 1`. Kind 4 was reserved (not a separate "strings" table — feed-meta
and values are inline length-prefixed, so no string table is needed) and was never
emitted by any v3 file, so it is safe to repurpose in a future version.

## 9. index section (kind = 2, align 16) — the interval map

Index sub-header (32 bytes):

| Offset | Size | Type | Field | Notes |
|---:|---:|---|---|---|
| 0 | 4 | u32 | `record_size` | 12 iff `key_width`=4; 40 iff `key_width`=16; reader MUST reject any other pairing |
| 4 | 4 | u32 | `key_width` | reader MUST reject if not 4 or 16; MUST match header `ip_version` |
| 8 | 8 | u64 | `record_count` | number of records; MUST equal header `entry_count` |
| 16 | 16 | bytes | `reserved` | =0 |

(The values section is located via the directory.)

**IPv4 record (12 bytes):** `start` u32@0, `end` u32@4 (inclusive), `value_id`
u32@8 (`0xFFFFFFFF` = "present, no value").

**IPv6 record (40 bytes):** `start_hi` u64@0, `start_lo` u64@8, `end_hi` u64@16,
`end_lo` u64@24 (inclusive), `value_id` u32@32, `pad` u32@36 (=0). The 4-byte `pad`
makes each record a multiple of 8 bytes, so (LE host, section 16-aligned) every
record's `u64` fields are 8-aligned, enabling the cast-based fast path; it MUST be
zero and is covered by the section hash.

**Writer input & invariants.** The format writer's input is an already-disjoint set
of `(range, value)` pairs — resolving overlapping input ranges that carry conflicting
values is the **engine's** responsibility (Step 2), not the byte format's. A format
writer **MUST reject** (typed error, no file emitted) input it cannot reduce to a
sorted, disjoint, coalesced form — i.e. input containing overlapping ranges — rather
than silently emit a file the reader would reject. Given a consistent input set, the
writer produces the on-disk records via this order: (1) sort by `start`; (2) coalesce
adjacent same-value neighbours (below); (3) assign `value_id`s by the §10 sweep. A
writer MUST guarantee all of:
- Records sorted by `start` **numerically** ascending (compare `hi` then `lo` for
  v6 — not raw-byte order) and **disjoint** (no overlap). Because the index is
  disjoint, every `start` is unique, so `start` alone is a total order — there is no
  secondary sort key. (In particular `value_id` is **not** a sort input: it is
  assigned afterward by sweeping the already-sorted, coalesced records, §10.) Two
  records sharing a `start` violate disjointness and are invalid.
- `start <= end` for every record.
- **Coalesced (single-feed file):** in a **single forward pass** over the sorted
  records, an adjacent pair `(a, b)` is merged iff their **values are byte-identical**
  (content equality per §10 interning — equivalently, they would receive the same
  `value_id`; coalescing is decided on value **content**, since `value_id`s are not
  assigned until §10, after coalescing) AND `start_b == end_a + 1`. For v6,
  `end_a + 1` uses `u128_inc` (§3); for v4 it is a
  plain `u32` add. If `end_a` is the family maximum (`0xFFFFFFFF` v4 / all-ones v6),
  `end_a + 1` is undefined and the pair is **not** mergeable — the writer MUST
  pre-check the maximum before computing `end_a + 1`. After the pass, no mergeable
  neighbours remain. (Mirrors legacy C `src/ipset.h` / `src/ipset_binary.c`, which
  exclude the all-ones value; a single forward pass suffices because the records are
  already sorted and a merge only extends the current run.) The pass is **transitive
  via the running record**: after a merge the merged record's `end` becomes the
  current run's end, against which the next record is tested — so A, B, C contiguous
  with equal values collapse to one record `[start_A, end_C]`. For large values a writer
  MAY decide content-equality via the §10 content-addressed dedup map (built
  incrementally during the sweep) instead of a full byte compare per adjacent pair.
  Two records both carrying the sentinel `value_id = 0xFFFFFFFF` are treated as having
  equal values for coalescing (both mean "present, no value") and MUST merge when
  contiguous, exactly as two records with the same real value would.
- `value_id == 0xFFFFFFFF` OR `value_id < values.count` (absent values section ⇒
  `values.count == 0`). All `reserved`/`pad` zero.

**Reader validation & lookup:**
- **Safety walk (mandatory for any file not trusted via verify-once).** Before
  reading or looking up records, a reader MUST perform the per-record safety checks:
  `value_id` in range (`== 0xFFFFFFFF` or `< values.count`, with `count = 0` when no
  values section is present); `pad == 0` per v6 record; `start ≤ end`; and — before
  any lookup — sortedness and disjointness. **Section-hash verification does NOT
  substitute for this walk on an untrusted file:** in an unsigned file an attacker
  controls both the bytes and the directory hash, so a matching hash proves nothing
  about these invariants. Hash verification is corruption detection (and, for signed
  files, authenticity) — complementary to, not a replacement for, the safety walk.
- **Lookups require a sorted, disjoint index.** Disjointness predicate for
  consecutive sorted records: `end_i < start_{i+1}` (numeric, hi-then-lo for v6);
  adjacency (`end_i + 1 == start_{i+1}`) is itself disjoint and allowed. This is a
  comparison only — **no 128-bit arithmetic** (unlike the coalescing `end+1`). If the
  walk finds the index unsorted or non-disjoint, the reader MUST reject the file
  (binary search over such data is undefined; the reader MUST NOT return a
  possibly-wrong result).
- A reader operating under the **verify-once** opt-in (integrity established
  out-of-band at install, §15) MAY skip both the safety walk and hash verification at
  load — it is explicitly trusting the install-time check.
- A point lookup is a numeric binary search whose **post-condition** is total and
  unambiguous on a sorted+disjoint index: it returns the unique record whose
  `[start,end]` contains the key if one exists, otherwise **not found**. Containment
  for v6 (numeric, never `memcmp`): `K ≥ start` iff `K.hi > start.hi || (K.hi ==
  start.hi && K.lo ≥ start.lo)`; `K ≤ end` iff `K.hi < end.hi || (K.hi == end.hi &&
  K.lo ≤ end.lo)`. The binary-search midpoint MUST be computed as `lo + (hi − lo) / 2`
  (not `(lo + hi) / 2`, which can overflow for a very large `record_count`). Any
  total-order-correct binary search satisfying this post-condition is conforming.
- Section length MUST satisfy `32 + record_count × record_size ==
  directory[index].length` (overflow-safe; §15). An empty index is valid
  (`record_count = 0`, length 32).

**Unique-IP count (IPv6 overflow):** the header carries the full 128-bit count
(§5). (This removes a legacy ambiguity: the legacy C in-memory IPv4 counter is a
`uint64` and never overflows for v4's `2^32` space (`src/ipset.h`), the v4 binary
loader rejects on `u64` overflow (`src/ipset_binary.c`), and the IPv6 in-memory
counter saturates at `2^128−1` (`src/ipset6.h`); v3 computes the exact value and
rejects only the unrepresentable full-IPv6-space case.)

## 10. values section (kind = 3, align 8) — interned values

Present iff the index uses `value_id != 0xFFFFFFFF`, and then `count` ≥ 1 (§4).

```
u32 count                        ; reader MUST reject if the section is present and count == 0
repeat count times:              ; entries tightly packed (no inter-entry padding)
    u32 type_id                  ; value type (registry below); reader MUST reject type_id == 0
    u32 byte_length              ; length of the value bytes (≤ 2^32-1)
    u8  bytes[byte_length]       ; type-specific; scalars little-endian per §3
```

`count` is `u32`; the maximum assignable `value_id` is `count − 1 ≤ 0xFFFFFFFE`,
which never collides with the sentinel `0xFFFFFFFF`. A writer MUST reject input that
would require more than `0xFFFFFFFF` distinct values (unrepresentable in the `u32`
`count`). The section `length` MUST equal exactly `4 + Σ(8 + byte_length_i)` — no
trailing padding.

**value_id** is the positional index `0..count-1`.

**Deterministic ordering:** the writer assigns `value_id`s by sweeping the sorted,
coalesced index in ascending record order; the first time a distinct value is seen
it gets the next id (`0,1,2,…`). Records carrying the sentinel
`value_id = 0xFFFFFFFF` are **skipped** during assignment (they reference no entry).
Two writers building the same index thus produce identical ids and bytes.

**Interning is content-addressed over the full entry tuple:** two values are equal
(share one `value_id`) iff their serialized `(type_id, byte_length, bytes)` are
byte-for-byte identical — the same `bytes` under two different `type_id`s are two
distinct entries. Equality is by full bytes (a hash MAY index the dedup map, but
equality is by full content — no hash-collision risk).

**type_id registry** (append-only and backward-compatible: an existing `type_id`'s
encoding and structural rules **never** change across `version_minor`s; new value
types receive new `type_id`s — so an older reader's validation of a known `type_id`
stays correct for every v3.x file):

| type_id | meaning |
|---:|---|
| 0 | reserved (invalid — reader MUST reject) |
| 1 | membership set: ≥ 1 strictly-ascending little-endian `u32` feed-ids; `byte_length` is a non-zero multiple of 4 |
| 2+ | reserved for later (ASN, geo, severity, …) |

`byte_length == 0` is structurally **valid** for any `type_id` **except** those whose
type-specific rule forbids it (e.g. `type_id == 1`, where an empty membership set is
invalid) — an empty opaque value means "present, empty value" and is interned like
any other distinct value.

A reader that does not understand an entry's `type_id` **MUST return its raw bytes**
as the canonical **"present, value of unknown type"** lookup result (the IP is
present; the value is opaque) — it MUST NOT silently drop the value. (Skip-unknown
applies at the *section* level — an entire unknown section kind, §6 — not to a value
inside the known values section.) Symmetrically, a writer **MUST intern a value of a
`type_id` it does not specifically handle verbatim** (no normalization), validating
only structural bounds (`byte_length` within the section) — never type-specific
content — so two writers cannot diverge on future types. For `type_id == 1` a reader
MUST reject the entry if `byte_length == 0`, `byte_length % 4 != 0`, or the ids are
not strictly ascending (an **empty membership set is invalid** — a range in no feeds
carries no value and uses the `0xFFFFFFFF` sentinel instead). A writer MUST likewise
reject such input.

In v3.0 (single-feed), value **bytes are interned exactly as supplied by the
caller** for any `type_id` (the byte-identical contract — see the intro — requires
identical caller input). **Writer obligation:** a writer MUST validate a
`type_id == 1` value before emitting it (`byte_length % 4 == 0` and ids strictly
ascending) and reject invalid input as a writer-side error — mirroring the §7 UTF-8
rule, so two writers cannot diverge (one rejecting, one interning verbatim). The
*meaning* of a `type_id == 1` membership set — i.e. a stable feed-id registry — is a
phase-2 concern (§13) and is not defined or required by v3.0.

## 11. signature section (kind = 5, align 8) — RESERVED, deferred

v3 reserves the slot but does not implement signing. In a `version_minor == 0` file
the signature section MUST be present with `length = 0` and `must_understand = 0`
(its hash is `SHA-256("")`, §6). This is the **complete normative v3.0 contract for
this section.**

> **The rest of this section is a NON-NORMATIVE forward sketch of the planned signing
> approach — it is OUT OF SCOPE for v3.0 and does NOT bind a v3.0 implementation.**
> The normative signed-file contract (exact signed preimage and its byte order,
> `algo_id`/`key_id` layout, the multi-pass zero-the-hash signing/verification
> procedure, replay/nonce handling, key management) will be specified, and reviewed,
> when signing is implemented (§17). v3.0 conformance does not depend on any of it.

When added (a future `version_minor`): a detached signature over
`(header || directory)`. Because the directory carries each section's full 32-byte
SHA-256, signing the directory transitively authenticates the file. To break the
circular dependency (the directory contains the signature section's own `hash`), the
signed payload is computed with the signature section's directory `hash` field set
to all-zeros; after signing, the real hash (`SHA-256(signature bytes)`) is written
back, and a verifier zeroes that field before checking. The signed variant adds
`algo_id` + `key_id` via a `header_size` growth (§12); for **signed** files,
section-hash verification is MUST-at-load. Forward-compat: because the signature
section has `must_understand = 0`, a v3.0 reader reads such a file as **unsigned**
(it skips the section). **Security note for the signing design:** so a v3.0 reader
cannot silently present an unverifiable *signed* file as a plain *unsigned* one (a
downgrade), the signed variant SHOULD signal "this file claims a signature" through a
channel a v3.0 reader **fails closed** on — e.g. a header `flags` bit (currently
reserved=0, so a v3.0 reader rejects it) or a `version_major` bump — rather than
relying solely on the skippable signature section. A reader that *does* implement
signing MUST verify the signature when it recognizes `algo_id`; if it does not
recognize `algo_id` it SHOULD warn and MAY treat the file as unsigned, but MUST NOT
report the file as authenticated. A `version_minor > 0` file MAY carry an **empty** signature section,
meaning "new format, unsigned" — valid, read as unsigned. (A nonce/timestamp in the
signed payload to bound replay is part of the signing design; `generation_unixtime`
is in the signed header.) For the byte-identical contract to extend to signed files,
the chosen signature algorithm MUST be **deterministic** (e.g. Ed25519, RFC 8032); a
randomized scheme (e.g. ECDSA with random `k`) would make two signed files of the
same content differ. A future signed-file spec SHOULD also tighten the values rule
to `count > 0` **iff** at least one record uses a non-sentinel `value_id` (the
reverse of v3.0's "used ⇒ present"), so an attacker cannot smuggle authenticated data
into unreferenced value entries (a covert channel the signature would otherwise
cover). **Until signing lands, files are unsigned; readers MUST NOT imply
authenticity** (§15 threat model).

## 12. Versioning & compatibility

- `version_major` bump = incompatible; a reader MUST reject a `version_major` it
  does not implement (v3 readers require `== 3`).
- `version_minor` bump = **additive only**: it MAY add new sections and new
  **trailing** header fields; it MUST NOT change the meaning, size, or position of
  any field defined in an earlier minor version, MUST NOT repurpose reserved
  fields/bits, and MUST NOT change a kind's canonical `align`. (Repurposing a
  reserved field or changing an existing field/align requires a `version_major`
  bump; older readers fail closed on non-zero reserved values.)
- A `version_minor` bump MUST NOT introduce a new section that is **required for
  correct interpretation** (i.e. one an older reader must understand): any
  minor-added section MUST be safely ignorable by an older reader (`must_understand =
  0`). A genuinely required new section needs a `version_major` bump. Rationale: an
  older reader silently *accepts* a file that simply omits a section kind it has never
  heard of, so a new minor-version "required" section could be dropped undetected.
- `header_size` is fixed per `(version_major, version_minor)`, is always a multiple
  of 8, and grows only by appending trailing fields; any reserved or pad bytes
  *within* a version's own defined header (e.g. to keep a later field aligned) MUST be
  zero. (`header_size` itself is already a multiple of 8, so there is never trailing
  pad between the last field and `header_size`.) Each minor version's spec defines its
  header fields and the minimum `version_minor` at which each appears; a reader
  accesses a trailing field only when the file's `version_minor` ≥ that field's
  introduction (otherwise the field is treated as absent). Readers MUST use
  `header_size` (not the constant 72) and MUST require `header_size >= 72`; for
  `version_minor == 0` it MUST be exactly 72. Trailing header bytes `[72, header_size)` belonging to a newer minor
  version are **skipped, not zero-checked** — they may be defined fields the reader
  does not know, so zero-checking them would break additive forward-compatibility.
- The header carries only small, fixed-width, machine-readable fields. Large or
  variable-length data (certificates, key material beyond small identifiers) MUST go
  in sections, keeping `header_size` within the `u16` range.
- Directory entries and index records are fixed-size for all v3.x; growing them
  requires a `version_major` bump.

## 13. Multi-feed merged file (phase 2 — NON-NORMATIVE outline)

> **This section is a non-normative outline of phase 2 — OUT OF SCOPE for v3.0.** The
> normative multi-feed contract (including the byte-identical rules for overlapping
> input feeds and the feed-id mapping) is specified when phase 2 is designed; v3.0
> conformance does not depend on it.

Adds a `catalog` section and a **merged index** whose `value_id` points to a
membership set (type_id 1) — one lookup returns all memberships. Same machinery.
New kinds are allocated from §8 at design time. For byte-identical multi-feed output
the caller will need to supply a stable feed-id mapping and a defined rule for
overlapping input feeds (both deferred to the phase-2 design). Open: feed-id
registry vs interning names; build cost; cold-lookup speed (the kind-6 accelerator
is likely required — measure first).

## 14. Legacy read (migration)

The reader also accepts legacy `v1.0`/`v2.0` (ASCII pseudo-header + native-endian
record dump). **Dispatch:** a reader distinguishes v3 from legacy by the 8-byte
magic — bytes `[0,8) == "IPRANGE3"` → v3; otherwise → the legacy parse path. Legacy
files have no v3 directory, so the §15 directory/section checks do not apply to them;
the legacy path has its own bounds checks (below). The legacy body begins with the
`0x1A2B3C4D` marker `u32` (legacy C `src/ipset_binary.c`); a v3 reader reads it as a
little-endian `u32` — if it reads back `0x4D3C2B1A` the file was written big-endian
and the reader MUST reject it (as the C tool did). Legacy parsing MUST apply the same §15 structural bounds checks. A
legacy v2.0 file whose ranges cover the entire IPv6 space (legacy saturates the
count to `2^128−1`, `src/ipset6.h`) is rejected by the v3 migration path with a
clear error, since `2^128` is unrepresentable (§5). **Migration note (for 1D):**
legacy v2.0 stores each IPv6 address in the C `uint128_t` in-memory layout, which is
**not** v3's `hi`-then-`lo` little-endian pair (§3); converting a legacy v2.0 record
to a v3 record requires the documented hi/lo transposition, to be specified with the
legacy reader. Legacy support is read-only and MAY be deferred to a later step
(SOW-0002 sub-step 1D); its full byte layout is specified when implemented.

## 15. Safety & integrity (normative — even with signing deferred)

**Threat model (unsigned v3):** the structural checks below prevent crashes,
out-of-bounds reads, and over-allocation on malformed/hostile input. **The
structural and safety steps (1–11 and 13) are mandatory for ALL files, signed or
unsigned, before any section content is trusted or any signature is verified** — a
valid signature authenticates content but does not make a malformed structure safe to
parse (the FlatBuffers/Cap'n-Proto CVE class). Step 12 (integrity verification) is
the one exception to "mandatory for all": it is **MUST-at-load for signed files** and
**SHOULD-at-load for unsigned files** (skippable under verify-once). Section hashes
(and, later, the signature) provide **corruption detection**; for an **unsigned**
file they do **not** provide tamper resistance (an attacker who rewrites the file can
rewrite the directory and its hashes). Tamper resistance requires signing (§11) or an
external integrity reference (trusted TLS download + atomic rename, or a known-good
out-of-band hash).

**No artificial size limits.** The only hard ceiling is `u64` `file_size` (16 EiB);
safety comes from consistency with the real file size, not caps.

Reader validation order:

1. Open with `openat2(RESOLVE_NO_SYMLINKS)` where available, else `O_NOFOLLOW` on
   the final component (the caller is responsible for ensuring no intermediate path
   component is an attacker-controlled symlink — or uses a trusted install path;
   note neither flag stops an attacker-controlled **hardlink** to a substituted
   inode — a trusted install directory is the defense there).
   `fstat` the **already-open fd** (never re-open by path — TOCTOU); refuse
   non-regular files. Let `real_size = fstat.size`; if `real_size < 72`, reject (too
   small to contain a header).
2. Read the first 8 bytes; if `magic != "IPRANGE3"` (bytewise), reject. If
   `version_major != 3`, reject. If `header_size % 8 != 0` or `header_size < 72`,
   reject; for `version_minor == 0`, reject if `header_size != 72`. If
   `real_size < header_size`, reject (file shorter than its own header). Verify header
   `flags` bits 1–15 and `license_flags` bits 1–31 are zero. (Trailing header bytes
   `[72, header_size)` of a newer-minor file are skipped, not zero-checked — §3/§12.)
3. If header `file_size != real_size`, reject. If IPv4, verify
   `unique_ip_count_hi == 0` and `unique_ip_count_lo ≤ 2^32`. (This is a consistency
   check on *untrusted* metadata — rejecting an inconsistent value is safe, but these
   fields are never used for allocation or bounds decisions, §5.)
4. Validate the directory region: `directory_offset == header_size`; 8-aligned;
   `directory_offset + directory_count × 72 ≤ real_size` (overflow-safe);
   `directory_count ≥ 3` (preliminary floor; step 6 enforces the exact set); no
   overlap with the header.
5. Read directory entries (each `kind != 0`; reserved bits/field zero; `align` in
   the §6 set — a power of two in `{8..4096}` — equal to the §8 canonical value for a
   known kind, `offset % align == 0`; `offset + length` overflow-safe and
   `≤ real_size`). Verify non-overlap in one O(N) pass (entries are offset-sorted),
   and no overlap with the directory/header. If a `kind` is unknown/reserved AND
   `must_understand = 1`, reject.
6. Enforce canonical order + bands; for each section verify
   `offset == align_up(prev_end, align)` (compute `align_up` overflow-safe — reject
   if `prev_end > MaxUint64 − (align − 1)`); verify required-section presence and
   no-duplicate-mandatory (§4). Verify all inter-region padding (including the gap
   before the first section) is zero, and that the highest-offset section ends
   exactly at `file_size`. (The "values present iff used" linkage needs index data
   and is enforced by the per-record `value_id` bounds check in step 11, not here.)
7. feed-meta (kind 1): `field_count` (≥ 6; `== 6` for `version_minor == 0`); each
   declared length within the section; section length equals the exact computed size.
8. index (kind 2): sub-header `reserved` zero; `key_width ∈ {4,16}`; `record_size`
   matches `key_width` (12↔4, 40↔16) and header `ip_version`;
   `record_count == header.entry_count`. Reject if `record_count > (MaxUint64 − 32)
   / record_size`, then verify `32 + record_count × record_size == length`.
9. values (kind 3): if present, `count ≥ 1`, walk entries (`type_id != 0`;
   `byte_length` within section; overflow-safe), section length equals the exact
   computed size. (The "values present iff used" linkage is enforced by the
   per-record `value_id` bounds check in step 11 — with `count = 0` when the section
   is absent, any non-sentinel `value_id` is rejected, so "used ⇒ present" needs no
   separate full-index scan here and is skipped under verify-once. A values section
   present but unreferenced is wasteful, not invalid.)
10. signature (kind 5): if `version_minor == 0`, `length` MUST be 0; if
    `version_minor > 0`, an empty section means "unsigned" and a non-empty section
    is handled per §11.
11. **Record-level safety walk** (skippable only under the verify-once opt-in):
    before trusting record contents, perform the §9 per-record walk in a **single
    forward pass** — the safety checks (`value_id` range; v6 `pad == 0`;
    `start ≤ end`) and, before any lookups, sortedness + disjointness; also verify the
    number of records walked equals `header.entry_count`; reject on any failure. The
    walk MUST traverse **every** record to completion; any mid-walk failure (I/O
    error, OOM, short read) MUST cause rejection — partial verification is
    non-conforming. This walk is **mandatory** for an untrusted file and is **not**
    replaceable by hash verification (a matching hash does not prove these invariants
    on an unsigned file, §9).
12. **Integrity / authenticity** (complementary to step 11, not a substitute):
    section-hash verification is corruption detection — SHOULD-at-load for unsigned
    files, covering **every section present** (each directory entry: feed-meta, index,
    values, signature in v3.0, and any future-version section), or skipped under
    verify-once. For **signed** files, signature verification plus section-hash
    verification is MUST-at-load (§11) and authenticates the content. **On any
    section-hash mismatch (or signature-verification failure) the reader MUST reject
    the file** and MUST NOT return results from a section whose hash did not verify.
13. Only after the above, map/read section bytes.

The numbered order is a logical dependency order, not a rigid execution sequence: an
implementation MAY reorder data-dependent checks (e.g. evaluate the step-9
values-iff-used cross-check during the step-8 index scan) **provided no untrusted
bytes are trusted before the structural check that governs them**.

**Verify-once opt-in:** a reader library MUST default to verifying section hashes
(or performing the per-record walk) at load. The verify-once-at-install
optimization — skipping steps 11 **and 12** because integrity was established
out-of-band — MUST be an explicit caller opt-in (e.g. a `trust_verified` flag), and
the library's docs MUST state the caller's responsibility to verify at install. That
install-time verification **MUST itself have performed full section-hash (or, for
signed files, signature) verification** plus the structural+record checks — skipping
those at load is only sound because install did them. "Install" = the point the file
is atomically renamed into place; the caller MAY cache the result, e.g. in a sidecar
or xattr, and skip re-verification on later opens. Any such cache MUST be
integrity-protected: it MUST be keyed by the data file's `(st_dev, st_ino, st_mtime,
st_size)`, MUST be stored only where the loading process's trust boundary already
covers (the same directory as the data file, or a platform-conventional cache
location, readable/writable only by the same user — mode `0600` or equivalent), and a
cache an attacker can write MUST NOT be trusted (it would silently disable all
integrity checking). When verify-once is active, a library MUST re-check the file's
`(st_dev, st_ino, st_mtime, st_size)` against the cached trust record before skipping
validation; if they differ (the file changed since install), **or if the cache is
missing, unreadable, or fails its own integrity check**, it MUST revert to full
validation (fail-safe — a bad cache never weakens checking). (`mtime` is
attacker-settable by anyone who can write the file, so it is not a cryptographic
boundary — the binding's strength comes from the cache being unwritable by an
attacker, not from the key; a library **SHOULD** additionally store a content hash of
the file (the install-time verification already computed the section hashes, so this
is nearly free) and re-check it before trusting the cache — closing the
in-place-overwrite gap where an attacker rewrites the file keeping the same inode and
size with a backdated `mtime`.)

**Overflow rule:** every `count × size` and `offset + length` MUST use
checked/widened arithmetic; any overflow ⇒ reject. (Go silently wraps `u64`: before
`a × b` check `a > MaxUint64 / b`; before `a + b` check `a > MaxUint64 − b`. Applies
to `directory_count × 72`, `record_count × record_size`, and the values walk.)

**Allocation rule:** never pre-allocate from an unverified count. mmap mode
allocates nothing for data. owned-mutable sizes only from counts validated against
`real_size`, grows incrementally, and discards on a later failure. Because every
count is validated against the real file size, **there is no amplification** — a
small file cannot force a large allocation; the only way to make a reader allocate
~N bytes is to hand it a ~N-byte file. A reader library MAY expose an optional
caller-configurable size ceiling that returns a typed "resource limit exceeded"
error (and MUST NOT reject a file solely for exceeding it without such an error); the
default is **unlimited** (per the no-artificial-caps model — a deliberate decision,
since the format targets feeds of any size via mmap).

**Publishing / mmap safety:** publishers MUST write atomically (temp → fsync →
rename), treat the file as immutable, and **fully allocate** it (no sparse holes — a
hole in a mapped section SIGBUSes despite the bounds check). Readers mmap the open
fd. **Sparse-hole defense (so the "never crash" invariant holds):** an mmap-mode
reader on a platform that supports hole detection (`SEEK_DATA`/`SEEK_HOLE` on
Linux/macOS/FreeBSD, or `FIEMAP`) **MUST** verify the mapped range has no holes
before mapping; **if a hole is found in the mapped range, the reader MUST NOT mmap —
it MUST either reject the file or fall back to the `pread`/windowed path.** On a
platform without hole detection — including a filesystem where `SEEK_HOLE` returns
`ENXIO` (e.g. some tmpfs) — a reader **MUST** use the `pread`/windowed path
instead of mmap (an unguarded mmap over a possibly-sparse untrusted file would risk
SIGBUS, which §15 forbids). A reader SHOULD also `fstat` before mmap and again
**immediately after** mmap, rejecting (and `munmap`) if `st_size`/inode changed, and
MUST then "probe" the last mapped byte (`pread(fd, &b, 1, file_size − 1)`) so that
any residual fault surfaces at a single known, catchable point rather than on a hot
lookup later, **and MUST check the probe's return value: `1` = OK; `0` (EOF — the
file was truncated after the size check) or `−1` (I/O error) ⇒ `munmap` and reject**
(`pread` past EOF returns `0` rather than raising SIGBUS, so the return value, not a
signal, is what catches a truncated file here). For
environments without atomic-rename guarantees (untrusted producers, NFS, shared
container volumes), readers SHOULD prefer the `pread`/windowed path (also the 32-bit
fallback) over mmap; a SIGBUS handler (Unix) / structured-exception handler (Windows)
converting the fault to a typed error is a last-resort backstop. An SDK consumed by
third parties MUST NOT crash the host on a torn read.

A malformed file MUST never crash, over-allocate, or read out of bounds (fuzz +
ASan/Miri).

**Platform note (not a format limit):** 32-bit hosts use a pread/windowed fallback
(or metadata-only mode) for very large files; 64-bit hosts have no practical limit.

## 16. Reader modes (recap)

- **metadata-only:** header + directory + feed-meta (steps 1–7); it MAY defer the
  values-iff-used and record-level checks (steps 8–11) since it does not read the
  index.
- **mmap read-only:** map index (+values), numeric binary-search lookups, zero
  allocation on the hot path (after the §15 validation / verify-once decision).
- **owned-mutable:** parse into in-memory structures for editing/rewriting.

## 17. Open items (deferred — additive, do not affect the v3.0 byte contract)

- **Lookup accelerator (kind 6).** v3.0 uses AoS + numeric binary search (D3). AoS
  gives no record-level 16-byte alignment for v6 (40-byte stride) and is not the
  most cache-dense layout for v4; an SoA / DIR-24-8 / Poptrie accelerator will be
  added under kind 6 (additive) once benchmarked (1B/1C).
- **Compression** (a §1 non-goal for v3.0) may be added as a section kind later.
- **Phase-2 feed identity** (§13): registry vs interned names; the stable feed-id
  mapping the byte-identical contract requires.
- **Legacy read timing** (§14): deferred to sub-step 1D.
- **Signed variant details** (§11): `algo_id`/`key_id` header growth, the
  zero-the-hash signing procedure, replay/nonce handling, key management.
- **Huge-page alignment:** the `align` set caps at 4096; huge-page/TLB tuning is via
  `madvise(MADV_HUGEPAGE)`, not file offsets.
