# iprange v4 — live mutable on-disk DB format (design spec)

> Status: **LOCKED (2026‑06‑22, round 9).** The external reviewer panel reached
> consensus READY TO IMPLEMENT — **R1: zero findings of any severity**; R2,
> R3, R4: READY with only cosmetic P3s. Locked after 9 review rounds (the byte
> layout, COW/double‑meta crash model, derived allocator, flock concurrency, mmap
> safety, recursive `validate_node`, behavioral/cross‑read conformance, and the §13
> v3‑export delegation are all settled and specific enough for independent,
> interoperable Go + Rust implementations that reject hostile input). Implementation
> is a follow‑up SOW. Target‑direction spec for a *new*
> container (v4), distinct from the sealed v3 snapshot format
> (`binary-format-v3.md`). v4 is the **live working store**; v3 remains the sealed,
> byte‑identical, signable **published/interchange snapshot** that v4 exports to.

Conformance language: **MUST / MUST NOT / SHOULD / MAY** are normative (RFC 2119).
A reader that accepts a file violating a MUST is non‑conforming; a writer that emits
one is non‑conforming. "Reader MUST reject" = discard all partial state, return a
typed error, expose nothing from the file (the v3 reject contract).

---

## 0. Why v4 exists (problem statement)

`update-ipsets` keeps, per feed, a per‑IP attribute — e.g. **when each IP was
added** (retention), or **which interleaved feed** an IP belongs to. The format
stores it as a `scope` on each IP range. Two on‑disk models were tried and both
fail at scale:

- **Attribute‑partitioned files** (today): the attribute is encoded by *which file*
  an IP lives in. Removing an IP must **scan thousands of files** to find it
  (`update-ipsets/pkg/engine/retention_update.go:381-419` compares the current set
  against *every* cohort) — a **CPU** bottleneck. No index by IP.
- **One sealed v3 snapshot with `scope` inline**: gives the IP index, but v3 is
  **immutable** → every small change rewrites the whole file (**I/O**).

The dataset across all feeds exceeds RAM, so an in‑memory index is not an option;
**mmap is mandatory**. Production pains are **CPU** (the scans) and **I/O** (full
rebuilds), not memory.

**v4 is the resolution:** a portable, **mmap'd, on‑disk, mutable ordered index**
keyed by IP — a copy‑on‑write B+tree of fixed‑size `[from, to, scope]` records. A
point query is `O(log n)` (no scan); a change rewrites only the `O(log n + k)` pages
on the modified path(s) (no full rebuild). `scope` is **opaque** — the DB never
interprets it (§4, D11).

## 1. Goals / non‑goals

Goals:
- One **portable** on‑disk format, read+written natively by **Go** (reference
  writer = the daemon) and **Rust** (reference; also the C‑facing library — **no
  separate native C**, decision O3). A file written by one MUST be read identically
  by the other (**cross‑read**, §12).
- **mmap** read path; only hot pages resident; file may exceed RAM.
- **In‑place mutation** without full rewrite: `set` / `delete` a range, touching
  `O(log n + k)` pages.
- **Crash safety**: a torn write never corrupts the file; recovery is automatic on
  next open. Never `SIGBUS`/loop/OOB on a corrupt or hostile file (§9, §10).
- **Best read performance**: zero per‑access bookkeeping in the file; no MVCC.

Non‑goals (v4.0): concurrent readers during a write (§6); crypto/signing (v3's job;
v4 has a corruption checksum only); variable‑width scope (§4); network filesystems
and Windows (the live store is a **local** file; the shareable artifact is the v3
snapshot — Windows is future via `LockFileEx`, NFS unsupported, §11).

## 2. Terminology

- **Page**: fixed‑size I/O/alloc unit (`page_size`). `page_size = 4096` for v4.0
  (D10). All structures are page‑aligned.
- **pgno**: `u32` page index; byte offset `= pgno · page_size`. pgno 0/1 are the two
  meta pages; data pages are pgno ≥ 2.
- **Record**: fixed‑size `[from, to, scope]` (§4); records in a file are disjoint
  and sorted by `from`.
- **Meta page**: pgno 0/1; static identity + committed dynamic state + `txn_id`; the
  checksum‑valid one with the higher `txn_id` is **active** (§5.1, §6.3).
- **Transaction (txn)**: one writer mutation, committed by the meta flip (§6.3).
- **COW**: a modified page is copied to a free page and the parent re‑pointed, up to
  a new root.
- **Reachable set**: pages reachable from the active meta's `root_pgno`.

## 3. Design decisions

**DECIDED (locked for v4.0):**

- **D1 Fixed‑size records.** `record_size = 2·key_width + scope_width`. `key_width ∈
  {4,16}`; `scope_width ∈ [0,255]`.
- **D2 B+tree, page‑fanout.** Records live in **leaves**; branches hold separators +
  child pgnos.
- **D3 No leaf sibling pointers.** Scans re‑descend (cursor stack); avoids COW
  cascades on split (LMDB). No `O(1)` leaf‑to‑leaf.
- **D4 No persistent allocator.** The free set is **derived by the writer from the
  reachable set at open** (§7); no on‑disk bitmap/freelist; allocator
  crash‑recovery is automatic.
- **D5 Crash safety = COW + double‑meta‑page atomic commit** (§6.3). No WAL.
- **D6 No MVCC / no concurrent read+write.** Whole‑file advisory `flock` (§11):
  `LOCK_SH` readers / `LOCK_EX` writer, mutually exclusive. Readers do pure mmap
  traversal with zero in‑file bookkeeping. Short‑lived critical sections = API
  contract (§11).
- **D7 Reclaim‑after‑commit.** Pages freed by a txn are reusable only by the next
  txn (§6.4).
- **D8 Little‑endian, architecture‑neutral** for every multi‑byte field (incl.
  checksum). Keys are LE integers compared numerically (v3 §3): IPv6 = two `u64` `hi`
  then `lo`; no native `u128` cast; a big‑endian host byte‑swaps on read/write.
  **Unaligned access (port of v3):** multi‑byte fields are **not** guaranteed
  naturally aligned (e.g. meta `u64`s at offsets 42/58/66; branch keys follow a `u32`
  child pgno; `record_size` at offset 38). A reader/writer MUST read/write each field
  with explicit little‑endian byte access (`read_unaligned` / `binary.LittleEndian` /
  byte assembly) and MUST NOT cast a packed struct pointer over the mmap'd bytes.
- **D8a Family bounds.** `family_min = 0`; `family_max = 2^32−1` (IPv4) or `2^128−1`
  (IPv6). `scope_width` (hence `record_size`) is **fixed for the whole file** (from
  the meta); every record carries exactly `scope_width` scope bytes.
- **D9 Per‑page checksum = CRC32C (Castagnoli).** `checksum_algo = 1`. Parameters:
  polynomial `0x1EDC6F41` (reflected `0x82F63B78`), `init = 0xFFFFFFFF`, `refin =
  refout = true`, `xorout = 0xFFFFFFFF` (the iSCSI/Intel CRC32C; test vector:
  `crc32c("123456789") = 0xE3069283`). The 32‑bit result is stored in the **low 4
  bytes** of the `u64` checksum field (LE); the **high 4 bytes MUST be 0** and a
  reader MUST reject a non‑zero high half. The checksum covers the whole `page_size`
  bytes with the 8‑byte checksum field taken as zero. A `checksum_algo` field allows
  future algorithms.
- **D10 `page_size = 4096` for all v4.x.** Stored in the meta for self‑description,
  but **fixed across every v4 minor** — a reader MUST reject any `page_size != 4096`
  for `version_major == 4`. Changing `page_size` moves meta‑B's offset and the
  checksum span, so it requires a **major** bump (not a minor). This pins meta‑B to
  byte offset 4096, completing bootstrap.
- **D11 `scope` is opaque.** The DB never interprets, normalizes, or invents policy
  for `scope`. The caller owns all meaning. `set` is unconditional (§8).
- **D12 v4 complements v3.** v4 = live store; v3 = sealed published snapshot; v4
  compacts/exports to v3 (§13).

**MEASURED (perf tuning, not interop — SOW‑0005 harness):** `scope_width` per
deployment (M2); split/fill/underflow thresholds (M3 — implementation‑defined;
conformance is behavioral, §12); `fsync` vs `fdatasync` (M4 — barriers MUST exist).
(`page_size` is **not** a v4 tunable — D10.)

**RESOLVED (were O1–O3):** O1 → behavioral + cross‑read (§12), no byte‑identity. O2
→ dissolved (`set` is unconditional, D11). O3 → C via the Rust library.

## 4. Record & key format

- **Key**: unsigned integer, **little‑endian**, fixed `key_width` (4 = IPv4, 16 =
  IPv6). Numeric compare (IPv6 = `hi` then `lo`; exact bytes per v3 §3).
- **Record** (`record_size` bytes, LE): `from` (key_width) · `to` (key_width) ·
  `scope` (scope_width, opaque — D11). `to ≥ from`.
- **Invariants (reader MUST enforce):** records across the tree are sorted ascending
  by `from` and disjoint — in key order `a` before `b`: `a.from ≤ a.to < b.from`.
- **Canonical form (writer SHOULD):** adjacent records with **byte‑equal `scope`**
  and `a.to + 1 == b.from` are coalesced. Coalescing changes the record *count*,
  never query results → it is a SHOULD; **intra‑leaf** coalescing is expected,
  **cross‑leaf** MAY be deferred to compaction (§13).
- `scope_width = 0` is valid (presence map; all scopes equal → all contiguous ranges
  coalesce).
- **Boundary arithmetic.** `to + 1` / `from − 1` at the family max/min use the v3
  `u128_inc` / `u128_dec` rules: `inc`: `lo' = lo+1; hi' = hi + (lo == U64_MAX ? 1 :
  0)`; `dec`: `lo' = lo−1; hi' = hi − (lo == 0 ? 1 : 0)`. IPv4 uses ordinary `u32 ±
  1`. A writer MUST pre‑check the family boundary (`from == family_min` ⇒ no `from−1`
  left‑trim; `to == family_max` ⇒ no `to+1` right‑trim) before computing them (§8).

## 5. File & page layout

```
pgno 0 : META-A   ┐ two meta pages, double-buffered (§6.3)
pgno 1 : META-B   ┘
pgno 2.. : data pages: BRANCH | LEAF, allocated from the derived free set (§7)
```
After a clean commit the file is exactly `total_pages · page_size` bytes. A reader
MUST reject a file whose size is not a multiple of `page_size`, or `< total_pages ·
page_size`. Trailing pages beyond `total_pages` MAY exist after a crashed growth; a
reader ignores them (unreachable); the next writer reclaims/truncates them (§6.4).

**Common 16‑byte page header (every page, LE):**
```
@0  page_type   : u8    (1=meta, 2=branch, 3=leaf; reader MUST reject unknown — §12)
@1  reserved    : u8    (=0; reader MUST reject non-zero)
@2  entry_count : u16   (records in a leaf / separators in a branch; MUST be 0 for meta)
@4  pgno        : u32   (this page's own number — reader MUST verify it matches)
@8  checksum    : u64   (D9; whole page with this field zeroed)
```
All page bytes not otherwise defined — the tail after the last entry, unused entry
slots, reserved fields — **MUST be zero**. A reader MUST reject non‑zero reserved
header bytes always, and on the full‑validate pass (§9, the default) MUST reject any
non‑zero tail or unused‑slot byte (in the trusted/lazy mode it MAY skip the tail
scan). Writers zero‑fill all such bytes.

### 5.1 Meta page (pgno 0 / 1) — exact offsets

Static identity (set at creation; **identical in both metas**), then dynamic state.
v4.0 byte offsets (within the page; `page_size = 4096`):
```
# common page header [0,16)
@16  magic            : u8[8] = "IPRANGE4"
@24  version_major    : u16   = 4
@26  version_minor    : u16   = 0
@28  meta_size        : u16   = 90   (offset just past the last defined field)
@30  page_size        : u32   = 4096 (reader rejects != 4096 at version_minor 0, D10)
@34  checksum_algo    : u8    = 1    (CRC32C)
@35  flags            : u8    (bit0: 0=IPv4 1=IPv6; bits 1-7 reserved=0, reject if set)
@36  key_width        : u8    (4 | 16; MUST satisfy flags.bit0 ⇒ 16 else 4)
@37  scope_width      : u8
@38  record_size      : u32   (MUST == 2*key_width + scope_width)
@42  created_unixtime : u64   (static; identical in both metas)
# --- dynamic state (differs per commit) ---
@50  root_pgno        : u32   (0 = empty tree)
@54  tree_height      : u32   (0 = empty; else levels, leaf level = 1)
@58  total_pages      : u64   (logical page count; 2 ≤ total_pages < 2^32; bound is u32-storable)
@66  record_count     : u64   (UNVERIFIED hint; reader MUST NOT allocate/bound from it)
@74  txn_id           : u64   (monotonic; higher valid = active)
@82  updated_unixtime : u64   (caller-supplied per commit; for deterministic tests)
# @90 .. page_size : reserved, MUST be zero
```
The **static region** is `[16, 50)` plus the (static) `created_unixtime` `[42,50)`,
i.e. bytes `[16,50)`. The **dynamic region** is `[50,90)`. `meta_size = 90` is the
v4.0 value (a reader uses the file's `meta_size`, not the constant 90, but MUST
require `meta_size ≥ 90` and, at `version_minor == 0`, `meta_size == 90`).

**Bootstrap (resolves the page_size circularity; tolerates a torn meta‑A).** Because
`page_size == 4096` is fixed for all v4 (D10), **both** meta pages are at fixed byte
offsets 0 and 4096. A reader MUST first require the file is `≥ 2·4096` bytes, then
read **both** 4096‑byte candidates **independently** (it does **not** need meta‑A
valid to locate meta‑B). Each candidate falls into exactly one of three classes,
which are handled **differently** (do not conflate "torn" with "incompatible"):
1. **Torn / not a meta** — CRC32C fails (incl. high‑4‑bytes‑nonzero), or
   `page_type != 1`, `reserved != 0`, `entry_count != 0`, `self‑pgno` mismatch, or
   `magic != "IPRANGE4"`. The candidate is **discarded** (e.g. a torn inactive meta
   after a killed commit); it does **not**, by itself, reject the file. This keeps
   recovery robust when the active meta is B and A was torn.
2. **Intact but incompatible** — CRC32C + `magic` are valid (so it is a genuine,
   undamaged iprange‑v4 meta) **but** `version_major != 4`, or (at this reader's
   minor) `page_size != 4096`, `checksum_algo != 1`, an unknown `flags` bit, or any
   other unsupported/fail‑closed condition (§5.1 forward‑compat). The reader MUST
   **reject the whole file** (fail closed) — an undamaged meta announcing an
   unsupported format is authoritative; the reader MUST NOT fall back to the other
   meta and expose stale data.
3. **Valid v4.0 meta** — CRC32C valid and all static fields well‑formed (below).
A reader proceeds to selection only if no candidate is class 2; if any class‑2
candidate exists, reject. (Conforming files never produce a class‑2 candidate — both
metas share identical static fields written at creation, §6.3.)

**Meta validation & selection.** A meta is **valid** iff its checksum verifies
(including the high‑32‑bits‑zero rule) and its header + static fields are
**well‑formed** — `page_type == 1`, header `reserved == 0`, `entry_count == 0`,
`magic == "IPRANGE4"`, `version_major == 4`, `checksum_algo == 1`, `page_size ==
4096`, `flags` reserved bits zero, `key_width ∈ {4,16}` agreeing with `flags.bit0`,
`record_size == 2·key_width + scope_width`, and `90 ≤ meta_size ≤ page_size` (exactly
`90` at `version_minor == 0`; a future minor declares its own larger size and an
older reader skips `[meta_size, page_size)`). The reader compares the static
region `[16,50)`
**only among checksum‑valid metas**; if two valid metas disagree on any static
field, reject (corrupt) — **except `version_minor` `[26,28)` and `meta_size`
`[28,30)`**, which an in‑place minor upgrade (e.g. v4.0→v4.1, scope‑api §C.6)
legitimately leaves differing between the two metas during the transition (the new
minor is written into the inactive meta first, so the older meta still carries the
old minor until it is next overwritten). Those two fields stay per‑meta
CRC‑protected and range‑validated (`90 ≤ meta_size ≤ page_size`, and `meta_size ==
90` at `version_minor == 0`), and the active = higher‑`txn_id` meta is authoritative,
so excluding them from the cross‑meta identity check opens no hole; the rest of
`[16,50)` (i.e. `[16,26)` and `[30,50)`) MUST still match byte‑for‑byte. A
checksum‑*invalid* meta is ignored (e.g. a torn inactive
meta after a killed commit), **not** a reason to reject the file. Active = the valid
meta with the higher `txn_id`; on an (illegal) tie, pgno 0; if neither is valid,
reject. `entry_count` MUST be 0 for a meta page.

**Forward‑compat (mirrors v3 §12).** A `version_major` bump = incompatible (reader
rejects an unknown major). A `version_minor` bump is **additive only** and MUST:
append **trailing** dynamic meta fields (growing `meta_size`); **not** change any
existing field's meaning/size/offset; **not** repurpose a reserved field/bit; **not**
add a new `flags` bit or `page_type` that an older reader must understand (those
require a major bump — an older reader **fails closed**: it MUST reject an unknown
`flags` bit or an unknown `page_type`). An older reader reads fields up to its own
`meta_size` and **skips `[meta_size, page_size)` without zero‑checking**. A writer
MUST NOT write a file whose `version_minor` it does not fully implement (it would
drop trailing fields it does not know); it either refuses or operates read‑only.

### 5.2 Branch page (internal node)

`entry_count = s` separators and `s+1` child pgnos, after the header:
```
@16 child_pgno[0]                       : u32
    repeat s: sep_key[i] : key_width ; child_pgno[i+1] : u32
```
Descent for key `K`: smallest `i` with `K < sep_key[i]`, else the last child.
**Invariants (reader MUST enforce):** `1 ≤ s ≤ branch_max`; separators **strictly
increasing**; **every branch (root included) has ≥ 2 children** (a 1‑child branch is
degenerate — reject; a single‑child root is never produced because root‑collapse
promotes the child, §8); every `child_pgno ∈ [2, total_pages)` and the `s+1`
`child_pgno`s are **pairwise distinct** (reject duplicates). The branch's separators
partition its **inherited** key bound `[lo, hi]` (passed from the parent; the root's
bound is `[family_min, family_max]`) into the children's bounds — see the recursive
`validate_node` in §9, which inherits `lo`/`hi` rather than re‑deriving them from the
family (so nested branches are bounded by their ancestor, not by `family_min/max`).
Separators MUST satisfy `lo < sep_key[0] < … < sep_key[s-1] ≤ hi` (strictly
increasing and within the inherited bound); this guarantees `sep_key[i] − 1 ≥ lo` is
always representable and that the unrepresentable `family_max + 1` is never computed
(the rightmost child's upper bound is the inherited `hi`, not a separator). `branch_max
= ((page_size − 16 − 4) / (key_width + 4))` (children = separators + 1).

### 5.3 Leaf page

`entry_count = r` records, sorted by `from`, after the header. **Invariants (reader
MUST enforce):** a reachable leaf has `1 ≤ r ≤ leaf_max` (a reachable empty leaf is
degenerate — reject); records sorted+disjoint within the leaf, all keys within the
threaded inclusive `[lo, hi]`; and **disjoint across leaves** — the last `to` of the
previously‑visited leaf (in‑order) `<` the first `from` of this leaf. `leaf_max =
((page_size − 16) / record_size)`.

**Balance, occupancy & height.** The tree is a **balanced B+tree**: **all leaves are
at the same depth**, equal to `tree_height` (the root is at depth 1, its children at
depth 2, …; leaves at depth `tree_height`). Depth is **not** a stored field — it is
the recursion depth tracked during validation (§9). A reader MUST reject a page whose
type disagrees with its validation depth: a page at depth `tree_height` MUST be a leaf
(`page_type 3`), a page at depth `< tree_height` MUST be a branch (`page_type 2`).
Reader‑enforced minimums: every branch ≥ 2 children; reachable leaf ≥ 1 record; an
empty tree is `root_pgno = 0` (no root page). A writer SHOULD keep non‑root nodes
≥ half‑full (standard B+tree, M3) for efficiency; the reader does **not** reject a
non‑empty underfull node (conformance is behavioral). **`tree_height` MUST be ≤
`TREE_HEIGHT_MAX = 32`** — a conservative hard cap (`u32` pgno ⇒ < 2^32 pages, so even
at the degenerate minimum branch fanout of 2 the height cannot exceed ~32; a balanced
tree at realistic fanout is ≤ 6). A reader MUST reject `tree_height > 32` and MUST
treat descending deeper than `tree_height` as a hard error (cycle defense, §9); a
writer MUST refuse an operation that would grow the tree beyond `TREE_HEIGHT_MAX`
(unreachable at realistic fanouts).

## 6. Transactions, COW, crash safety

### 6.1 Read transaction
1. `flock(LOCK_SH)`. 2. Bootstrap + select active meta. 3. Validate per §9 (default:
**full‑validate the reachable tree before exposing any result**; a *trusted* caller
MAY opt into lazy per‑page verification — §9). 4. Traverse, copy out. 5. release.

A cursor (path stack) is valid **only while `LOCK_SH` is held**; continuing past
release requires re‑acquiring the lock and re‑selecting the active meta. A reader
MUST NOT hold the lock during downstream processing (§11).

### 6.2 Write transaction (single exclusive writer)
1. `flock(LOCK_EX)`. 2. Select active meta; **validate the reachable tree with the
   same §9 checks a reader uses** (the writer also reads untrusted on‑disk bytes —
   reject a corrupt file rather than allocate against it), then build the in‑memory
   free set (§7). 3. Apply the op (§8) by transaction‑local COW: the first write to a
   committed page copies it to a transaction‑private page and marks the old version
   **freed‑by‑this‑txn** (not reusable until commit, §6.4); repeated writes to that
   transaction‑private page in the same transaction mutate it in place. 4. Commit
   (§6.3). 5. release.

### 6.3 Commit protocol (the only durability mechanism)
Target the **inactive** meta (lower `txn_id`).
1. `pwrite` every new/COW'd data page (correct CRC32C + self‑pgno). If the file must
   grow, extend it (`ftruncate`) and place new pages in the growth.
   **(v4.1)** "data page" here includes the scope‑table and per‑scope KV pages, which the
   writer *builds* at commit time (bulk‑rebuild, §C.2/§C.4). They MUST be constructed
   **before** the dirty‑page set for this step is collected, so they are pwritten and made
   durable at Barrier 1 like any other page. The Barrier‑2 meta must never reference a page
   that was not written in step 1 — else reopen sees a missing / bad‑CRC / out‑of‑range page
   and the file is rejected as corrupt.
2. **Barrier 1:** `fsync` (data durable before the meta references it).
3. Construct the new meta **in memory** (the whole `page_size` bytes, tail
   `[meta_size, page_size)` zero‑filled) and `pwrite` it to the inactive meta page in
   **one** call: new `root_pgno'`, `tree_height'`, `total_pages'`, `record_count'`,
   `txn_id = T+1`, `updated_unixtime`, valid checksum. (Whole‑page write so the
   per‑page checksum guards the update — never field‑by‑field.)
4. **Barrier 2:** `fsync`.

**Acknowledged only after Barrier 2 returns success.** On any error in steps 1–4
(incl. `ENOSPC`/`EIO`/short write/`fsync` failure) the writer aborts with a typed
error and **no acknowledged commit**; recovery is automatic on next open. Crash
analysis:
- Crash **before** Barrier 2 completes ⇒ the new meta is either absent / bad‑checksum
  (⇒ active = old meta = old tree) **or** already fully written with a valid checksum
  and higher `txn_id` (⇒ active = new tree — also complete, since its data was made
  durable at Barrier 1). The surviving state is therefore **old or new, never torn**;
  the commit is simply **not acknowledged**. Orphan pages are reclaimed next open
  (§7).
- Crash **after** Barrier 2 ⇒ new tree, durable.
- A **post‑commit** torn page referenced by the new meta is detected by checksum and
  the file is **rejected, not recovered** (COW‑no‑WAL limit, same as LMDB/bbolt; v4
  assumes a device honoring `fsync`). Double‑torn‑meta is the unrecoverable mode —
  documented, probabilistically negligible.
- A meta page is written in one `pwrite` of the whole 4096 bytes, so a *partially*
  written meta is a torn page whose CRC32C (over all 4096 bytes) will not validate; it
  is therefore "checksum‑invalid" and ignored. A torn page coincidentally producing a
  valid CRC32C is ~2^−32 and out of scope (as for any checksummed store).

**File creation.** A new file = 2 pages: META‑A and META‑B with identical static
fields, `root_pgno = 0`, `tree_height = 0`, `total_pages = 2`, `record_count = 0`,
`created_unixtime` set; META‑A `txn_id = 1`, META‑B `txn_id = 0`; both written and
`fsync`ed (distinct `txn_id` avoids the tie; META‑A active). A writer MUST refuse to
commit if `txn_id` would reach `u64::MAX` (unreachable in practice).

### 6.4 Allocation & reclaim (no persistent allocator)
- **Free set = `[2, total_pages)` − reachable(active root) − freed‑by‑this‑txn**
  (in memory; never stored). On open the writer reclaims any **trailing pages beyond
  `total_pages`** (from a crashed growth) — it includes them in the free set or
  `ftruncate`s them away; either way the committed `total_pages` is authoritative.
- New allocations draw from the free set; if empty, the file grows (`total_pages +=
  k`, capped at `2^32` — a writer MUST refuse to grow past it) within the same
  commit.
- `freed‑by‑this‑txn` pages back the current tree until the meta flips → reusable
  only by the **next** txn (D7). A crash needs **no allocator recovery**: allocated =
  reachable from the active root; everything else is free.

## 7. Allocator (derived, in‑memory)
No on‑disk free structure (D4). On open the writer validates (§9) and walks the
reachable set (root → branches → leaves), marks those + the two metas allocated, the
rest free, then maintains the set in memory across txns. Allocation = a derived
property of the tree → no bitmap/tree desync, no self‑allocation, no growth
chicken‑and‑egg. The walk is `O(pages)` once per writer open. Allocation order is
implementation‑defined (M3).

## 8. Range operations (generic `set` / `delete` — D11)
A small, policy‑free interval‑map API; `scope` semantics are the caller's. Input
contract: `from ≤ to` else reject (`InvalidInput`); `len(scope) == scope_width` else
reject; the key family MUST match the file's; keys are full‑width for the family.

- **`lookup(ip) → scope | none`**: descend to the leaf; the record with greatest
  `from ≤ ip`; hit iff `ip ≤ to`. `O(log n)`.
- **`set(from, to, scope)`**: make every address in `[from,to]` equal `scope`,
  **unconditionally** (D11). Mechanically: trim/split existing records at `from−1` /
  `to+1` (boundary pre‑check, §4), remove records fully inside `[from,to]`, insert
  `[from,to,scope]`, coalesce with byte‑equal‑`scope` contiguous neighbours. Spans
  `k` leaves ⇒ `O(log n + k)`. Examples:
  - `{[1,10]=A}` · `set([3,6],B)` ⇒ `{[1,2]=A,[3,6]=B,[7,10]=A}`
  - `{[1,10]=A}` · `set([5,15],B)` ⇒ `{[1,4]=A,[5,15]=B}`
  - `{[1,5]=A,[6,10]=A}` · `set([3,7],A)` ⇒ `{[1,10]=A}`
  - `{[1,10]=A}` · `set([1,10],A)` ⇒ `{[1,10]=A}` (idempotent)
  - `set([family_min, family_max], A)` on empty tree ⇒ one record covering the family.
- **`delete(from, to)`**: make `[from,to]` absent; trim/split (a delete strictly
  inside one record splits it into two with the same scope). `O(log n + k)`. A
  `delete` of a range that is wholly absent is a **no‑op** (success, no change).

Edge cases (normative): `set(family_min, family_max, s)` replaces the whole tree with
a single record `[family_min, family_max] = s`; `delete(family_min, family_max)`
empties it (`root_pgno = 0`). On a single‑level tree (`tree_height == 1`, the root is
a leaf) descent is the root leaf itself with bound `[family_min, family_max]`.
Coalescing is **transitive** — after an op the writer SHOULD leave no two adjacent
intra‑leaf records with byte‑equal scope and `a.to + 1 == b.from`.

**Scope reduction is a `set`, and may merge (caller guidance).** Because `scope` is
opaque (D11), the DB has no "remove one member from a scope" operation. A caller that
logically *removes* something — e.g. drops one feed from a membership scope `{A,B}` →
`{A}` — performs a **read‑modify‑write**: read the range's current scope, compute the
reduced scope, and call `set(range, reduced_scope)`. `set`'s coalescing then merges
the now‑equal‑scope neighbours. Example: `{[1,10]={A}, [11,12]={A,B}, [13,20]={A}}`,
reducing `[11,12]` to `{A}` via `set([11,12], {A})` ⇒ `{[1,20]={A}}` — a **merge
driven by a logical removal**. (Erasing the *addresses* `[11,12]` instead is
`delete([11,12])`, which leaves a gap and never merges — a different operation.) The
record splitting/merging is transparent; the scope arithmetic is the caller's.

Leaf overflow ⇒ split; underflow ⇒ merge/redistribute with a sibling; a branch
dropping to one child collapses (child becomes root, `tree_height--`); last record
removed ⇒ `root_pgno = 0, tree_height = 0`. Split point / underflow threshold are
implementation‑defined (M3) provided §5.2/§5.3 invariants and the height bound hold —
Go and Rust MAY differ in shape but MUST return identical query results (§12).

## 9. Integrity & validation (reader and writer‑on‑open)
**Modes.** Default = **full validation before exposing any result**: walk the entire
reachable tree and check every invariant *before* returning a query (so a hostile but
checksum‑valid structure cannot leak a wrong answer). A caller that trusts the file
(e.g. the daemon's own files) MAY opt into **lazy** verification (per page, on first
touch) — an explicit flag, defaulting off, mirroring v3's verify‑once opt‑in. The
**writer always full‑validates on open** (§6.2).

Order:
1. Bootstrap; validate both metas; select the active one (§5.1).
2. Geometry: `page_size == 4096` (v4.0); family/`key_width`/`scope_width`/
   `record_size` cross‑checks; `meta_size` rules; file size `== m·page_size` and `≥
   total_pages·page_size`; `2 ≤ total_pages < 2^32`; **overflow‑checked**
   `total_pages·page_size`; `tree_height`/`root_pgno` consistency (`height == 0 ⇔ root
   == 0`; non‑empty `root_pgno ∈ [2, total_pages)`); `tree_height ≤ 32`. (The root's
   `page_type` is checked against its depth in the recursive walk below — step 4 —
   *after* the root page's CRC32C is verified, not from the header pass here.)
3. Per **reachable** page — the two metas plus the pages visited by `validate_node`
   (step 4), **not** every page in `[2, total_pages)`: free/orphan pages (e.g. torn
   pages from a crashed, uncommitted write, §6.3/§6.4) are **not** reachable from the
   active root, are never validated, and MUST NOT cause rejection. For each reachable
   page (lazy permitted only in the trusted mode): CRC32C (+ high‑32‑bits‑zero),
   self‑`pgno`, `page_type` known, `entry_count ≤ capacity` (where `capacity =
   leaf_max` for a leaf, `branch_max` for a branch — §5.2/§5.3) and `== 0` for a meta,
   reserved bytes zero (and, on the full pass, tail/unused slots zero), and the
   §5.2/§5.3 structural invariants incl. the inherited inclusive `[lo, hi]` key bound
   (the recursive walk below).
4. **Recursive structural walk** (the full‑validate default; the trusted/lazy mode
   does the same checks per page on first touch). If `root_pgno == 0` the tree is
   empty and the walk is skipped entirely (a valid empty file). Otherwise the entry
   point is `validate_node(root_pgno, 1, family_min, family_max)`, where
   `validate_node(pgno, depth, lo, hi)` is —
   `prev_to` is a single threaded variable (the largest `to` seen so far across the
   whole in-order walk; initialized "none" before the root call and updated as leaves
   are visited left-to-right), giving global cross-leaf disjointness:
   ```
   require depth ≤ tree_height           # cycle/DoS defense: a too-deep path (incl. any
                                          # pgno cycle) exceeds tree_height and is rejected;
                                          # with distinct child_pgno per branch no DAG re-visit
                                          # can extend a path, so no visited-set is needed.
   load+verify page pgno (CRC32C+high32=0, self-pgno, page_type, reserved bytes 0)
   if depth == tree_height:               # MUST be a leaf
       require page_type == leaf (3); let r = entry_count; require 1 ≤ r ≤ leaf_max
       require records sorted and disjoint within the leaf, with lo ≤ from ≤ to ≤ hi
       require prev_to == none OR prev_to < records[0].from   # cross-leaf disjointness
       prev_to = records[r-1].to
   else:                                  # MUST be a branch
       require page_type == branch (2); let s = entry_count (separators), s+1 children
       require 1 ≤ s ≤ branch_max   # ⇒ 2 ≤ s+1 children, and the s separators + s+1
                                     # child_pgnos fit the page (branch_max = max separators, §5.2)
       require child_pgno pairwise distinct, each ∈ [2, total_pages)
       require lo < sep[0] < … < sep[s-1] ≤ hi
       # separators are ROUTING keys: child[i]'s keys ∈ [sep[i-1], sep[i]-1]. A child's
       # smallest `from` MAY exceed its lower separator — leading gaps are legal (v4 is a
       # SPARSE interval map), so a reader MUST NOT require sep == child's first key.
       validate_node(child[0], depth+1, lo, sep[0]-1)
       for (i = 1; i < s; i++):  validate_node(child[i], depth+1, sep[i-1], sep[i]-1)
       validate_node(child[s], depth+1, sep[s-1], hi)   # rightmost inherits the parent hi
   ```
   The root call is `validate_node(root_pgno, 1, family_min, family_max)`. Each child
   **inherits** its parent's `lo`/`hi` (never `family_min`/`family_max` except at the
   root), so nested branches are bounded by their ancestor; `sep − 1 ≥ lo` is always
   representable and `family_max + 1` is never computed.
5. `record_count`: a conforming **writer MUST store the exact** `record_count == Σ
   leaf entry_count` (it is precise, not an estimate). A **reader MUST NOT size an
   allocation/bound from it** (it is untrusted on a hostile file); on a full pass a
   reader MAY additionally verify the equality and MUST reject on mismatch (a mismatch
   ⇒ a non‑conforming/corrupt file). Because conforming files always match, verifying
   and non‑verifying readers accept exactly the same conforming files; correctness
   never depends on the value. (Not verifying `record_count` is **not** itself a
   violation of the intro's "reject any MUST violation" rule — it is a writer‑MUST the
   reader is explicitly permitted to ignore: an optional integrity check, not a
   correctness requirement.) The full pass also enforces the balance invariant (all
   leaves at depth `tree_height`, §5.3).
6. **Never pre‑allocate from an unverified count** (v3 §15). mmap mode allocates
   nothing for data.

Any violation ⇒ reject (typed error; discard partial state; expose nothing).

## 10. mmap safety (port of v3 §15, adapted)
- The **writer uses `pwrite` + `fsync`** for all mutation (explicit barriers) and
  MUST **fully allocate** every referenced page (never a sparse hole). **Both** reader
  and writer open with `O_NOFOLLOW | O_CLOEXEC` and `fstat` the **fd** (not the path).
  **Readers `mmap` read‑only with `MAP_SHARED`** (so a concurrently‑opened reader sees
  a consistent file image under the lock). The **writer also `mmap`s the committed
  range read‑only** (`PROT_READ`) to avoid loading the full file into heap memory;
  dirty pages are COW'd into a private buffer pool and written via `pwrite` at commit
  time. The writer does **not** use `MAP_SHARED` + `PROT_WRITE` — committed pages are
  never modified in place before the commit point.
- Reader open MUST: reject non‑regular files; detect sparse holes via `SEEK_HOLE`
  and, if a hole is in the mapped range, **not mmap** (reject or fall back to
  `pread`); re‑`fstat` after `mmap` and reject on size/inode change; probe the last
  referenced byte (`pread` past EOF returns 0, not SIGBUS — check the return). On
  platforms without hole detection, use the `pread` path. **Invariant: a
  corrupt/truncated/hostile file never `SIGBUS`es, loops, or reads OOB — it is
  rejected.** The file is local; a trusted directory is assumed (as v3).
- **`fork()`:** an `flock` is shared across a `fork` (both processes hold it). A
  process that `fork`s while holding the lock MUST NOT let the child operate on the
  fd; `O_CLOEXEC` ensures the fd is not inherited across `exec`. The single‑writer
  model assumes one writing process at a time.

## 11. Concurrency & API contract (normative)
- Lock = **`flock(2)`** on the fd: `LOCK_SH` readers / `LOCK_EX` writer, mutually
  exclusive, advisory, whole‑file. `flock`'s per‑open‑file‑description semantics avoid
  the `fcntl` "any close drops the lock" footgun and are identical in Go
  (`syscall.Flock`) and Rust. The file MUST be on a **local** filesystem supporting
  `flock` (NFS unsupported; Windows = future via `LockFileEx`). Same‑process
  re‑entrancy is not supported. **A `flock` is released automatically when the fd is
  closed or the process dies** — so a crashed writer holding `LOCK_EX` leaves no
  stuck lock; the next writer acquires it and reclaims orphan pages (§6.4, §7).
- The **writer** SHOULD acquire `LOCK_EX` with a bounded wait (default 30 s via
  `LOCK_NB` retry) and return a typed timeout rather than block forever (a stalled
  reader defense). The timeout is a deployment knob, not part of the format.
- **Reader:** `open → (LOCK_SH, select meta, validate, traverse, copy out, unlock) →
  close`. **Writer:** `open → (LOCK_EX, validate, COW mutate, commit, unlock) →
  close`. ipsets are **open‑change‑close** / **open‑read‑close**, not long‑lived
  handles. The API MUST keep the locked section minimal and MUST NOT expose a handle
  holding the lock across caller processing.
- **Liveness (D6):** readers are blocked during a writer's critical section.
  Acceptable for batch `update-ipsets` (short COW txns); a long write stalls reads —
  documented, not a defect.

## 12. Conformance model (behavioral + cross‑read)
Byte‑identity is **not** required (mutable‑tree layout depends on history; Go and Rust
MAY differ in shape). The contract:
- **Cross‑read (mandatory):** any conforming reader reads any conforming writer's file
  and returns identical query results.
- **Behavioral corpus:** scripted op‑sequences (`set`/`delete`/`lookup`/`scan`) →
  identical query results in Go and Rust; **crash‑recovery** cases (commit interrupted
  at each barrier → the defined surviving tree); **property/fuzz** vs an in‑memory
  interval‑map oracle; **malformed‑input** cases (every §9 reject path).
- The static identity, page format, commit protocol, and validation rules (this spec)
  are the fixed contract; split/merge/alloc policy is free (M3).

## 13. Compaction & v3 export
Constant small mutations fragment a COW tree and grow the file. Compaction = a clean
rebuild into a fresh file (sequential leaves, minimal pages, cross‑leaf coalescing),
atomically renamed in. The natural compaction is the **periodic export to a sealed v3
snapshot** — the published artifact and the de‑fragmented checkpoint are one
operation. v4 = live store; v3 = what the website / SDK / threat‑intel consumers read.

**Export contract (normative).** Export **does not re‑implement v3's writer rules** —
it produces the ordered logical input for the **v3 writer** (`binary-format-v3.md`),
which already owns coalescing (v3 §9), value interning + the `u32` distinct‑value cap
(v3 §10), the `unique_ip_count` 128‑bit accounting (v3 §5), and byte‑identical
canonicalization. Signature:
`export_v3(v4_file, type_id, v3_meta) → v3_bytes | ExportUnrepresentable`, where
`v3_meta` carries the v3 inputs v4 does not store (the six feed‑meta fields,
`license_flags`, `generation_unixtime`). Steps:
1. Read the v4 records in key order (a single in‑order `validate_node` walk).
2. Map each opaque `scope` to a v3 value: `scope_width == 0` → the v3 "present, no
   value" sentinel (`type_id` is then ignored); `scope_width > 0` → a v3 value with
   the **caller‑supplied `type_id`** and the `scope` bytes **verbatim** (v4 stores no
   `type_id` — it is opaque, D11).
3. Feed that ordered `(range, value)` stream + `v3_meta` to the v3 writer, which
   **coalesces** adjacent byte‑equal values (so deferred v4 cross‑leaf coalescing,
   §4, is reconciled here — the v3 output is canonical regardless), interns values,
   and runs all v3 validation.

Export is **not total**: it returns `ExportUnrepresentable` whenever the v3 writer
rejects the stream — in particular (a) the **total `unique_ip_count` reaches 2^128**
(the entire IPv6 family is covered, by one record *or many* — splitting does **not**
help, the sum is invariant); (b) the number of distinct `(type_id, value)` pairs
**exceeds the v3 writer's values‑table cap** (v3 §10 — the exact bound is v3's, not
restated here, since the v3 writer enforces it); or (c) a caller `type_id` /
`scope` is not a conforming v3 value for that `type_id` (e.g. a `type_id == 1`
membership value must satisfy v3 §10). Callers needing guaranteed exportability keep
the v4 state within these v3 limits. The compaction‑into‑a‑fresh‑v4 path has no such
limit (it stays in v4).

## 14. Corner cases & failure modes
Empty tree (`root=0`); single‑leaf root; delete that empties the tree (`root=0`);
delete inside a record (split); `set`/`delete` spanning many leaves (`O(log n+k)`);
file growth mid‑commit (committed `total_pages` authoritative; trailing pages
reclaimed on open); torn/absent new meta (old tree survives, unacked); valid new meta
before Barrier 2 (new tree survives, unacked); two valid metas equal `txn_id` (pick
pgno 0); torn inactive meta (ignored, not file‑reject); both metas invalid (reject);
writer killed mid‑commit (no stuck `flock`; orphans reclaimed); `flags`/`key_width`
mismatch (reject); unknown `flags` bit / `page_type` (reject, fail closed);
`scope_width = 0` (presence map); `set`/`delete` with `from > to` or wrong scope width
(reject); IPv6/IPv4 `from−1`/`to+1` at family min/max (boundary pre‑check); `pgno`
cycle (height bound rejects); hostile counts/separators (§9 rejects); post‑commit torn
page (rejected, COW‑no‑WAL limit).

## 15. Complexity (honest)
`lookup`: `O(log n)`. `set`/`delete`: `O(log n + k)` pages COW'd (`k` = leaves
spanned; a wide range is not `O(log n)`). Writer open: `O(pages)` once (validate +
free‑set walk). Range scan: re‑descend, not `O(1)` leaf‑to‑leaf (D3).

## 16. Relationship to existing specs
- `binary-format-v3.md` — the sealed snapshot v4 exports to (§13); shares key encoding
  (§4) and LE/numeric‑compare; v4 ports v3's reject contract (§9), mmap safety (§10),
  forward‑compat (§5.1).
- `design-iprange-engine.md` — the multi‑language engine; v4 is the live‑store layer.
