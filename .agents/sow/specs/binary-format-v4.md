# iprange binary format v4

**Status:** Normative unsigned Phase-1 implementation contract; decisions
resolved and contradiction audit passed
**Version identity:** exact `v4`; there are no v4 major/minor variants
**Byte order:** little-endian unless a field is explicitly defined as raw bytes

This specification defines the portable iprange v4 main file and the external,
local-only live-reader sidecar. It replaces the unreleased predecessor
experiment and every obsolete experimental v4 layout. Implementations MUST NOT
import, export, open, upgrade, or otherwise reinterpret those bytes as v4.
The stable Rust-provided C binding is separately normative in
[`c-abi-v4.md`](c-abi-v4.md); it adds no file-format state or physical-storage
authority.

Released legacy C v1/v2 files remain outside this specification. A v4 API MUST
reject them as non-v4 input.

This Phase-1 contract intentionally contains no snapshot-signing wire state,
API, dependency, verification, or conformance requirement. Signing is tracked
by pending SOW-0017 and may revise these unreleased bytes only after the core SDK
is proven reliable and measured. No compatibility is owed to Phase-1 files.

The key words **MUST**, **MUST NOT**, **REQUIRED**, **SHOULD**, **SHOULD NOT**,
and **MAY** are normative.

## 1. Contract summary

V4 has one portable main-file format. The same committed main-file bytes MAY be:

- a live mutable database when used with its local external reader sidecar; or
- an immutable snapshot when opened without live coordination.

The sidecar is not part of the portable format. It MUST NOT be distributed,
copied as snapshot state, or interpreted on another host.

The main file has these fundamental properties:

- fixed 4,096-byte pages;
- two alternating meta pages at page numbers 0 and 1;
- one inclusive, non-overlapping IPv4 or IPv6 range map;
- one static range-value kind: `direct` or `membership`;
- COW publication by one alternate-meta write with CRC tear detection;
- an optional opaque file-level zlib-compressed payload;
- a persistent COW free-page bitmap;
- persistent reader-protected retirement extents;
- no implicit full validation during open or ordinary access.

Cross-language compatibility is semantic. Go and Rust writers MAY choose
different valid page splits, COW shapes, membership IDs, inline/blob choices,
and zlib streams. Readers MUST expose the same ranges, direct values or named
memberships, feed catalog, and exact decompressed metadata bytes.

## 2. Fixed constants and primitive encodings

| Name | Value |
|---|---:|
| `PAGE_SIZE` | 4,096 bytes |
| `PAGE_SHIFT` | 12 |
| `META_SIZE` | 256 bytes |
| `MAX_PAGE_COUNT` | 4,294,967,296 (`2^32`) |
| `MAX_TREE_LEVEL` | 31 |
| `MAX_METADATA_UNCOMPRESSED` | 1,048,576 bytes |
| `BITMAP_LEAF_WORDS` | 500 |
| `BITMAP_LEAF_BITS` | 32,000 |
| `BITMAP_FANOUT` | 256 |

Unsigned integers are encoded little-endian. No on-disk field uses native
endianness, pointer width, structure padding, or language ABI layout.

An IPv4 address is one `u32` in numeric network-address order, encoded
little-endian. An IPv6 address is one unsigned 128-bit integer in numeric
network-address order, encoded as 16 little-endian bytes. Comparisons are
numeric, not lexicographic comparisons of encoded bytes.

A page number is a `u32`. Page number zero is the absent-root sentinel wherever
a root field permits absence. Non-meta roots MUST refer to pages in the range
`[2, page_count)`. The byte offset of page `p` is `u64(p) << PAGE_SHIFT` and
MUST be calculated with checked arithmetic.

`page_count` is a `u64` because the count can be `2^32` even though the highest
page number is `u32::MAX`.

All CRC fields use CRC-32C (Castagnoli), with reflected polynomial
`0x82f63b78`, initial value `0xffffffff`, and final XOR `0xffffffff`.

## 3. Main-file geometry and opening

The main file MUST be a regular file and MUST NOT be opened through a symbolic
link by an OS-backed v4 API. Its physical size MUST be a multiple of
`PAGE_SIZE` and MUST be at least two pages.

The final main basename is outside the engine-internal namespace. On every
platform, compare ASCII case-insensitively and reject a basename beginning
`.iprange-` or ending `.readers`; this conservative universal rule prevents
case-insensitive filesystem aliases. The exact canonical sidecar component is
the accepted main basename plus lowercase `.readers` and must fit the target
filesystem's component limit. On Windows, main/destination/final components
also reject `:`, trailing dot/space, alternate-data-stream syntax, and any
namespace-normalized alias rather than permitting an unnamed/named stream or
Win32-equivalent spelling. These checks occur before open/create/publication
mutation. Engine-created private/reservation/scratch/GC names deliberately
occupy the reserved lowercase `.iprange-` namespace.

Every create, live transition, recovery, snapshot, and publication attempt is
bound to the exact accepted final-main basename, not merely its parent
directory. The platform encoding is `1=POSIX basename bytes` or `2=Windows
UTF-16LE code units without a terminator`; other values are invalid. POSIX bytes
exclude NUL and `/`. Windows units are the exact accepted final component after
the alias rejections above and exclude NUL and separators. They MUST form one
well-formed UTF-16 sequence; an unpaired high or low surrogate is rejected before
open/create/publication mutation. This preserves the same accepted name in Rust,
Go, and C instead of depending on language-specific WTF-16 behavior. Let
`name_bytes` be that encoding and `name_len` its checked byte length. The 32-byte
commitment is:

```text
SHA-256("IPR4NAME" || encoding_kind:u16le || name_len:u32le || name_bytes)
```

Every destination-owning direct result, resolver result, and preparation error
returned after the destination directory/component has been accepted retains the
common binding `(directory_identity_kind, directory_local_identity,
encoding_kind, name_bytes)`. An earlier pre-binding error carries no destination
binding and cannot authorize resolution or namespace cleanup; any cleanup
artifact it owns carries its own exact directory/name binding under section
14.4. Returned operation results retain `encoding_kind` plus the exact
`name_bytes`.
Durable reservation/sidecar attempt records retain the kind, length, and
commitment. Every resolver recomputes them from its caller-supplied path and
requires equality with both the result and every selectable on-disk attempt
record before inspecting a derived private name or changing any namespace.
Using a result for another basename in the same directory is
`DestinationNameMismatch`, even when that other name is absent.

The first two pages are meta pages. No other page has a fixed physical page
number.

The selected meta's committed byte length is:

```text
committed_bytes = page_count * PAGE_SIZE
```

The multiplication MUST be checked. The physical file MUST contain at least
`committed_bytes`.

An immutable snapshot open MUST require physical size exactly equal to
`committed_bytes`. A live reader MUST accept an aligned physical tail beyond
`committed_bytes`; that tail is unpublished growth and MUST NOT be mapped or
read as committed data. After acquiring the single-writer lease, a live writer
open MUST truncate such a tail to `committed_bytes` before permitting a new
mutation. Failure to truncate makes that writer open fail.

Before writing any appended page, a writer MUST extend or preallocate the file
to a checked whole-page boundary. It never exposes a short page as its physical
tail. A failed extension is cleaned back to the prior aligned committed length
when provable; otherwise the writer is unusable and ordinary strict open rejects
the malformed tail.

Main-file bootstrap is an O(1) operation. It MUST inspect and classify the two
meta pages, file geometry, and static identity. It MUST NOT walk the range tree,
catalog, membership dictionary, metadata chain, free bitmap, retirement tree,
or any other page graph. A live open additionally scans the explicitly sized
reader table under its operation lock; that work is `O(reader_capacity)`, never
`O(main_file_size)`.

## 4. Exact meta-page layout

Each meta page is exactly 4,096 bytes. Bytes `[0,256)` have this layout:

| Offset | Size | Field | Required value or meaning |
|---:|---:|---|---|
| 0 | 8 | `magic` | ASCII `IPRANGE4` |
| 8 | 2 | `meta_size` | `256` |
| 10 | 1 | `page_shift` | `12` |
| 11 | 1 | `address_family` | `4` or `6` |
| 12 | 1 | `value_kind` | `1=direct`, `2=membership` |
| 13 | 3 | reserved | zero |
| 16 | 16 | `value_tag` | canonical tag, below |
| 32 | 16 | `database_id` | nonzero opaque 128-bit value |
| 48 | 8 | `txn_id` | selected committed transaction, nonzero |
| 56 | 16 | `commit_nonce` | random nonzero identity of this exact commit |
| 72 | 8 | `page_count` | `2..MAX_PAGE_COUNT` |
| 80 | 8 | `range_record_count` | current range-record count |
| 88 | 8 | `active_feed_count` | zero for direct files |
| 96 | 8 | `feed_index_limit` | membership high-water count |
| 104 | 8 | `membership_entry_count` | zero for direct files |
| 112 | 8 | `membership_id_limit` | zero for direct; otherwise `1..2^32` |
| 120 | 8 | `metadata_uncompressed_len` | exact decompressed length |
| 128 | 8 | `metadata_compressed_len` | exact zlib-stream length |
| 136 | 8 | `retired_extent_count` | current retirement extent count |
| 144 | 4 | `range_root` | range-tree root or zero |
| 148 | 4 | `catalog_name_root` | name-index root or zero |
| 152 | 4 | `catalog_index_root` | numeric-index root or zero |
| 156 | 4 | `feed_used_root` | feed used-bitmap root or zero |
| 160 | 4 | `membership_id_root` | membership dictionary root or zero |
| 164 | 4 | `membership_hash_root` | reverse-index root or zero |
| 168 | 4 | `membership_used_root` | membership-ID used-bitmap root or zero |
| 172 | 4 | `metadata_root` | first metadata chunk or zero |
| 176 | 4 | `free_bitmap_root` | free-page bitmap root or zero |
| 180 | 4 | `retirement_root` | retirement-tree root or zero |
| 184 | 16 | `allocator_reserve[4]` | optional meta-owned page numbers |
| 200 | 52 | reserved | zero |
| 252 | 4 | `meta_crc32c` | CRC-32C of the full meta page |

Bytes `[256,4096)` MUST be zero. To compute `meta_crc32c`, bytes `[252,256)`
are treated as zero.

There is no major field, minor field, feature-negotiation field, extension
directory, or compatible-header range. Any wrong fixed value, nonzero reserved
byte, or unknown value is not v4. After the first v4 release, a future
incompatible format MUST use a new identity such as `IPRANGE5`; revisions to
this explicitly unreleased Phase-1 contract do not create compatibility modes.

### 4.1 Static identity

These fields are immutable for a database:

- `magic`, `meta_size`, and `page_shift`;
- `address_family`;
- `value_kind` and `value_tag`;
- `database_id`.

A meta is **identity-readable** when its complete page is present and its magic,
fixed constants, reserved bytes, tag encoding, nonzero database ID, and meta
CRC are valid. If both pages are identity-readable but their static identity
differs, open MUST reject the file even when one page later fails a dynamic
bootstrap check. It MUST NOT select one side of the contradiction.

`database_id` MUST be generated from a cryptographically secure random source
and MUST NOT be all zero. A new database and a recovery output receive a new
ID. A byte copy and compact `SnapshotTo` preserve it. Rename preserves it.

`commit_nonce` MUST be generated from a cryptographically secure random source
and MUST NOT be all zero. Each commit attempt receives a fresh nonce before any
mutation that could be published. Creation transaction 1 and recovery output
transaction 1 receive fresh nonces. A byte copy and compact `SnapshotTo`
preserve the selected generation's nonce because they preserve that exact
logical commit.
Nonce-generation failure is a pre-mutation error.

Cryptographic digests are content-identity predicates under an explicit
collision-resistance assumption, not mathematical byte comparisons and not
authentication. Wherever this specification says that SHA-512 proves exact
content, it means that the required database/generation tuple, byte length,
local identity fields where applicable, and SHA-512 digest all match. SHA-256
membership identity and the section-3 destination-basename commitment likewise
assume collision resistance over their exact domain-separated inputs. Membership
interning additionally compares complete bitmap bytes before merging an ID; a
durable basename record necessarily retains only its commitment after restart.
Phase 1 has no signature or trusted-key statement; a caller that deliberately
supplies a digest collision is outside these identity predicates.

`value_tag` contains zero through 15 caller-defined non-NUL bytes, followed by
one mandatory NUL byte. Every byte after the first NUL MUST be zero. The exact
tag `retention` is encoded as:

```text
72 65 74 65 6e 74 69 6f 6e 00 00 00 00 00 00 00
```

The engine otherwise treats the tag bytes as opaque.

### 4.2 Meta selection

A meta page is bootstrap-valid only if it is identity-readable and its nonzero
commit nonce, kind-specific zero fields, root/count relations, root bounds,
lengths, counts, transaction, file geometry, and checked host-addressability
requirements are valid without reading another non-meta page.

Selection is:

1. If both metas are identity-readable and static identity differs, open fails.
2. If neither meta is bootstrap-valid, open fails.
3. If one is bootstrap-valid, select it.
4. If both are bootstrap-valid, their transaction IDs MUST be equal or differ
   by exactly one. A larger gap cannot be produced by v4 and open fails.
5. If they differ by one, the higher transaction MUST be stored at physical
   meta page `txn_id & 1` and the lower transaction at the other page; a swapped
   pair cannot be produced by v4 and open fails. Select the higher transaction.
6. If both have the same `txn_id`, bytes `[0,256)` MUST be identical; otherwise
   open fails. Select physical page `txn_id & 1` as the authoritative copy.

Creation writes identical transaction-1 meta images to pages 0 and 1. A commit
of transaction `T` writes page `T & 1`: even transactions write page 0 and odd
transactions write page 1. `txn_id` increments by exactly one. Overflow is
typed `TransactionIdExhausted` before mutation.

A sole bootstrap-valid meta is a readable candidate but cannot prove that the
other physical page did not contain a later durably committed generation before
it was damaged. A reader may select and expose that candidate factually, and a
non-mutating compact snapshot or explicit immutable/offline recovery may use it
under their documented trust/reporting rules. Writer open and every destructive
or mutable transition return typed `CurrentGenerationUnprovable` before lease
publication, tail truncation, mapping for mutation, reset, initialization,
reclamation, or any file change, regardless of the sole page's physical parity.
They require the two-meta current-generation proof from steps 4-6. Ordinary
writer/transition open never repairs or copies a meta. Thus transaction `T+1`
overwrites the nonselected physical meta while the selected `T` image survives,
but damage to either copy cannot silently authorize rollback and new mutation.

Every reader exposes `meta_selection: ProvenCurrent | SoleMeta0 | SoleMeta1`.
`ProvenCurrent` requires the complete two-meta proof in steps 4-6; a sole value
identifies only the physical candidate actually exposed and never claims it is
newest. Ordinary live-reader open, live `SnapshotTo`, `LiveCurrent` validation,
and any other API promising the current live generation require
`ProvenCurrent`; they return `CurrentGenerationUnprovable` before publishing a
reader slot for a possibly older generation. Immutable readers and immutable snapshots may expose
a sole candidate, and the snapshot result carries the same source-selection
status. No API silently upgrades a sole candidate to current.

### 4.3 Kind-specific meta invariants

For `direct` files, all catalog and membership counts, limits, and roots MUST
be zero.

For `membership` files:

- `feed_index_limit <= 2^32`;
- `active_feed_count <= feed_index_limit`;
- `membership_entry_count <= 2^32-1`;
- `membership_id_limit` is at least 1 and at most `2^32`; and
- ID zero is reserved and is not represented by a dictionary entry;
- zero active feeds requires zero membership entries and zero range records;
- zero membership entries requires zero range records; and
- `membership_entry_count <= range_record_count` because every live dictionary
  entry has at least one range-record reference.

Every nonzero allocator-reserve page is in `[2,page_count)`, distinct from the
other reserve entries, and distinct from every nonzero root in the same meta.
Zero denotes an empty reserve slot.

The following root/count relations are bootstrap invariants:

- `range_record_count == 0` if and only if `range_root == 0`;
- `active_feed_count == 0` requires both catalog roots and `feed_used_root` zero;
  a nonzero active count requires all three roots nonzero;
- `membership_entry_count == 0` requires both dictionary roots and
  `membership_used_root` zero and `membership_id_limit == 1`; a nonzero count
  requires all three roots nonzero;
- `retired_extent_count == 0` if and only if `retirement_root == 0`; and
- direct-file zero requirements override every membership relation above.

If `metadata_root` is zero, both metadata lengths MUST be zero. If it is
nonzero, `metadata_compressed_len` MUST be nonzero and the uncompressed length
MUST be at most `MAX_METADATA_UNCOMPRESSED`.

## 5. Common non-meta page header

Except for meta pages, every committed reachable page begins with this
32-byte header:

| Offset | Size | Field | Meaning |
|---:|---:|---|---|
| 0 | 4 | `page_magic` | ASCII `IP4P` |
| 4 | 1 | `page_type` | exact type from the table below |
| 5 | 1 | `page_flags` | zero |
| 6 | 2 | `header_size` | `32` |
| 8 | 8 | `born_txn` | transaction that created this page |
| 16 | 2 | `item_count` | type-specific count |
| 18 | 2 | `level` | zero for leaves; positive for branches |
| 20 | 2 | `lower` | type-specific used-area boundary |
| 22 | 2 | `upper` | type-specific used-area boundary |
| 24 | 4 | `aux` | type-specific discriminator |
| 28 | 4 | `page_crc32c` | CRC-32C of the full page |

`born_txn` MUST be nonzero and MUST NOT exceed the selected meta transaction.
`page_crc32c` is calculated with bytes `[28,32)` zero.

Every unused or reserved byte in a reachable page MUST be zero. Tree levels
MUST NOT exceed `MAX_TREE_LEVEL`.

Normal lookup, scan, and mutation MUST check page bounds, header size, type,
counts, offsets, and arithmetic needed for memory safety. They do not perform
general non-meta page CRC validation. Explicit `Validate` and recovery perform
those CRC checks. The sole narrow
ordinary-path exception is section 13's amortized verification of committed
allocator metadata before it authorizes destructive page reuse; that check is
not an implicit full `Validate`.

### 5.1 Page types

| Value | Page type |
|---:|---|
| 1 | range branch |
| 2 | range leaf |
| 3 | catalog-name branch |
| 4 | catalog-name leaf |
| 5 | catalog-index branch |
| 6 | catalog-index leaf |
| 7 | membership-ID branch |
| 8 | membership-ID leaf |
| 9 | membership-hash branch |
| 10 | membership-hash leaf |
| 11 | blob branch |
| 12 | blob leaf |
| 13 | metadata chunk |
| 14 | bitmap branch |
| 15 | bitmap leaf |
| 16 | retirement branch |
| 17 | retirement leaf |

All other page-type values are invalid v4.

### 5.2 COW ownership

After a meta publication, every page reachable from that meta is immutable.
A later transaction MUST write changed content to newly allocated pages and
publish new roots. A reachable page MUST have exactly one owning graph path;
page aliasing between roots or within a graph is invalid unless this
specification explicitly says otherwise. No such alias is currently defined.

Every ordered-index B+tree page (types 1 through 10 and 16 through 17) uses the
slotted-page convention in section 7. Root zero is the only empty-tree
representation; every reachable B+tree leaf and branch is nonempty.

### 5.3 Tree descent invariants

Every branch pointer is a non-meta page below selected `page_count`. Its child
has the page type and `aux` required by that owning tree, and its level is
exactly one less than the parent level. Every branch key is the exact first key
in that nonempty child subtree. Branch keys are strictly increasing.

Ordinary access checks the level decrease, child bounds, expected type, and
expected `aux` before following every pointer. It stops after
`MAX_TREE_LEVEL + 1` pages. These checks prevent cycles, stack exhaustion, and
out-of-bounds access without computing page CRCs or performing full validation.
Explicit `Validate` additionally proves global ownership and cross-page
ordering.

## 6. Range tree and range records

The range tree is a COW B+tree ordered by record `from`. `range_root == 0`
means an empty map.

Range pages use `aux == address_family` and the slotted-page convention.

### 6.1 Range leaf

A leaf has page type 2, `level == 0`, and at least one record.

IPv4 leaf records are 12 bytes:

```text
from:u32  to:u32  value:u32
```

IPv6 leaf records are 36 bytes:

```text
from:u128  to:u128  value:u32
```

The intervals are inclusive. Every record MUST have `from <= to`. Records are
strictly ordered by `from` and MUST NOT overlap. Globally adjacent records with
the same value MUST be coalesced.

In a direct file, `value` is an opaque caller-defined `u32`; zero is valid. In
a membership file, `value` is a nonzero `membership_id` present in the selected
snapshot's membership dictionary. Value zero means absence and MUST NOT be
stored as a range record.

### 6.2 Range branch

A branch has page type 1, `level > 0`, and at least one entry.

An IPv4 entry is 8 bytes:

```text
first_from:u32  child_pgno:u32
```

An IPv6 entry is 20 bytes:

```text
first_from:u128  child_pgno:u32
```

`first_from` is the exact first range start in the nonempty child subtree.
Entries are strictly ordered by `first_from`. A non-root one-child branch is
legal; writers collapse a one-child root.

Point lookup chooses the greatest branch `first_from` not greater than the
target at each level, then chooses the greatest leaf `from` not greater than the
target and tests that record's `to`. If no key qualifies, the target is absent.
Forward and backward cursors retain only the bounded ancestor path needed to
move to the next or previous nonempty page. No empty-subtree summaries,
predecessor backtracking, or empty-page skipping exist.

## 7. Slotted-page convention

Every ordered-index B+tree page uses this convention:

- `item_count` is the slot count.
- The slot array starts at byte 32 and contains `item_count` little-endian
  `u16` record offsets in logical key order.
- `lower == 32 + 2 * item_count`.
- `upper` is the smallest record offset.
- Each type-specific record MUST be wholly within `[upper,4096)`, MUST NOT
  overlap another record, and MUST be referenced by exactly one slot.
- Every gap and every unused byte is zero.

There is no generic per-record header. Variable-length record layouts below
define their own `record_len`; fixed-width records contain only their declared
fields. Empty reachable pages are invalid.

## 8. Feed catalog

Only membership files have a feed catalog. It is a one-to-one mapping between
active names and active `u32` feed indexes. The same generation publishes the
catalog, used-index bitmap, membership dictionary, range root, and metadata.

A feed name is 1 through 255 bytes and MUST:

- contain only lowercase ASCII `a-z`, digits `0-9`, `_`, `-`, or `.`;
- begin and end with a letter or digit; and
- be unique by exact byte comparison.

There is no case folding. Titles, descriptions, and aliases are not structural
catalog fields and belong in the opaque metadata payload if needed.

### 8.1 Name index

The name tree is ordered lexicographically by unsigned name bytes.
Name branches have page type 3 and positive level; name leaves have page type 4
and level zero. Both use `aux == 0` and the slotted-page convention.

A name-leaf record is:

```text
record_len:u16
record_flags:u16 = 0
feed_index:u32
name_len:u8
reserved:3 bytes = 0
name:name_len bytes
```

`record_len == 12 + name_len`.

A name-branch record stores the exact first name in its nonempty child:

```text
record_len:u16
record_flags:u16 = 0
child_pgno:u32
name_len:u8
reserved:3 bytes = 0
first_name:name_len bytes
```

`record_len == 12 + name_len`. Branch keys are strictly increasing. Lookup
chooses the greatest first key less than or equal to the target.

### 8.2 Numeric index

The numeric tree is ordered by `feed_index`.
Index branches have page type 5 and positive level. Index leaves have page type
6 and level zero. Both use `aux == 0` and the slotted-page convention.

An index-leaf record has the same layout as a name-leaf record and is ordered
by `feed_index`. An index branch contains fixed-width 8-byte records:

```text
first_feed_index:u32  child_pgno:u32
```

The first indexes are strictly increasing and exactly match their nonempty
children.

The name and numeric trees MUST contain exactly the same pairs. Their record
count MUST equal `active_feed_count`.

### 8.3 Feed index allocation

`feed_index_limit` is an exclusive, non-shrinking high-water count. It can be
`2^32`, representing every `u32` index.

The feed-used bitmap has one bit per index below the limit. One means active.
Creating a feed MUST choose the lowest zero bit below the limit. If none exists
and the limit is less than `2^32`, it uses the old limit as the new index and
increments the limit. If the limit is `2^32` and every bit is one, creation
returns typed `FeedIndexExhausted` before mutation.

Deleting a feed clears its used bit only in the same atomic generation that
removes its catalog entry and removes that bit from every membership. A later
generation MAY reuse the index. Existing memberships are logically
zero-extended when the limit grows and are not rewritten solely for growth.

## 9. Membership dictionary

A membership bitmap is a canonical, nonempty sequence of little-endian `u64`
words. Bit `feed_index` identifies the feed at that index in the same pinned
catalog generation.

Canonicalization removes all trailing zero words. Therefore the final word is
nonzero. Every set bit MUST be below `feed_index_limit` and MUST identify an
active catalog feed. An empty or all-zero bitmap is canonical membership ID
zero and has no dictionary entry or stored range.

### 9.1 Membership-ID tree

The ID tree is ordered by nonzero `membership_id`.
ID branches have page type 7 and positive level. ID leaves have page type 8 and
level zero. Both use `aux == 0` and the slotted-page convention.

An ID branch has fixed-width 8-byte records:

```text
first_membership_id:u32  child_pgno:u32
```

First IDs are strictly increasing and exactly match their nonempty children.

An ID leaf is slotted. Its record is:

| Offset in record | Size | Field |
|---:|---:|---|
| 0 | 2 | `record_len` |
| 2 | 1 | `storage` (`0=inline`, `1=blob`) |
| 3 | 1 | reserved zero |
| 4 | 4 | `membership_id` |
| 8 | 8 | `range_record_refcount` |
| 16 | 4 | `word_count` |
| 20 | 4 | `bitmap_len` |
| 24 | 4 | `blob_root` |
| 28 | 4 | reserved zero |
| 32 | 32 | `bitmap_sha256` |
| 64 | variable | inline bitmap bytes, when inline |

`bitmap_len == word_count * 8`, using checked arithmetic. `word_count` is
`1..67,108,864`. `range_record_refcount` is nonzero and is the exact number of
current range records whose value is this ID.

For inline storage, `blob_root == 0`, `record_len == 64 + bitmap_len`, and the
bitmap bytes follow the fixed fields. For blob storage, `record_len == 64`,
`blob_root != 0`, and the blob has kind 1 and exact length `bitmap_len`.
Either representation is conforming when its record fits.

`bitmap_sha256` is SHA-256 over the exact canonical bitmap bytes. It is a
lookup key, not an integrity substitute for page CRC validation.

### 9.2 Membership reverse index

The hash tree is ordered by the tuple:

```text
(bitmap_sha256[32], word_count:u32, membership_id:u32)
```

Digest bytes compare unsigned lexicographically; `word_count` and
`membership_id` then compare numerically.

Hash-leaf records are fixed 40-byte tuples in that order. Hash-branch records
append `child_pgno:u32` and are 44 bytes. Branch keys are the exact first tuple
in each nonempty child.

Hash branches have page type 9 and positive level. Hash leaves have page type
10 and level zero. Both use `aux == 0` and the slotted-page convention.

Interning computes SHA-256, searches all entries with the same digest and word
count, and compares the complete canonical bitmap bytes. A digest collision
MUST NOT merge unequal bitmaps.

Within one committed generation, one canonical nonempty bitmap MUST have
exactly one membership ID. Equal complete bitmap bytes MUST intern to the same
ID even when they arrive through different mutation paths. Explicit validation
rejects duplicate IDs for an equal canonical bitmap.

### 9.3 Membership-ID lifetime and allocation

The membership-used bitmap has one stored bit per nonzero live ID. ID zero is
implicitly reserved and MUST be skipped regardless of its stored bit.

`membership_id_limit` is the exclusive upper bound of the current live ID
namespace. It is 1 when the dictionary is empty. Otherwise it is one greater
than the greatest live ID, or `2^32` when `u32::MAX` is live. Unlike
`feed_index_limit`, it MUST shrink when trailing IDs disappear.

Allocation MAY choose any unused nonzero ID below the limit; if none exists it
uses the limit when representable. IDs are internal and snapshot-local, so
writers need not choose the same ID. A zero-refcount combination MUST be absent
from the new dictionary, reverse index, and used bitmap. Its ID is reusable.
If every nonzero `u32` ID is live and a new distinct membership is required,
mutation returns typed `MembershipIdExhausted` before changing the draft.

Mutations MUST maintain refcounts from bounded aggregated deltas. Commit MUST
NOT rescan the complete range tree or construct a file-sized in-memory count
map. Explicit validation recomputes all refcounts independently.

When distinct deltas exceed the bounded heap cache, the operation MUST aggregate
them in an operation-private page-backed ordered index allocated inside the
destination database. Those pages are unreachable from every committed meta,
are published only if they become part of the final canonical dictionary state,
and otherwise return to the draft's private reservation pool or unpublished
tail during commit/abort cleanup. No transaction, lifecycle, normalization, or
import operation creates an external delta/sort file. Aggregation checks
refcount underflow and overflow. The number of distinct IDs or feeds MUST NOT
become an implicit heap bound.

## 10. Generic blob tree

Blob trees store large membership bitmaps and retirement page-number lists.
`aux` is the blob kind:

| Value | Meaning |
|---:|---|
| 1 | membership bitmap bytes |
| 2 | retirement page-number list |

A blob branch has page type 11, positive level, and fixed 16-byte entries:

```text
logical_offset:u64  child_pgno:u32  reserved:u32=0
```

Offsets are strictly increasing and the first root offset is zero. A blob leaf
has page type 12 and level zero:

| Offset | Size | Field |
|---:|---:|---|
| 32 | 8 | `logical_offset` |
| 40 | 2 | `data_len` |
| 42 | 6 | reserved zero |
| 48 | `data_len` | data bytes |

`data_len` is `1..4048`. Every nonfinal leaf MUST have `data_len == 4048`;
only the final leaf may be shorter. Leaves in tree order MUST cover the
owner-declared length exactly, starting at zero, with no gap, overlap, or
trailing data.
Every non-root branch's first offset MUST equal the lower offset assigned by
its parent, every child level MUST be exactly one below its parent, and every
branch entry offset MUST equal its child's first logical offset.
Lookup chooses the greatest entry offset not greater than the requested logical
offset and rejects a request outside the owner-declared length. A branch or leaf
chain may descend at most `MAX_TREE_LEVEL + 1` pages.
For a blob leaf, `item_count == 1`, `lower == 48 + data_len`, and
`upper == 4096`; bytes after the data are zero.

Membership blob data length is a multiple of eight. Retirement blob data
length is a multiple of four and contains strictly increasing unique `u32`
page numbers.

## 11. Opaque compressed metadata and public semantics

The engine exposes the metadata as optional opaque bytes through
`GetMetadataJSON`, `SetMetadataJSON`, and `ClearMetadataJSON`. Despite the API
name, the engine MUST NOT parse, validate, normalize, merge, or impose a JSON
schema. Empty bytes, `{}`, whitespace, and any other supplied bytes are
distinct values.

Absent metadata has root zero and both lengths zero. Present metadata is one
complete RFC 1950 zlib stream containing RFC 1951 DEFLATE data with `CM=8`,
`CINFO<=7`, and `FDICT=0`. It MUST include a valid Adler-32 trailer. Raw
DEFLATE, gzip, dictionaries, concatenated streams, and trailing bytes are not
v4 metadata.

The uncompressed length MUST be at most 1,048,576 bytes. A syntactically valid
DEFLATE stream can contain an arbitrarily large number of blocks that produce
little or no output, so the uncompressed limit alone does not bound file growth
or validation work. The compressed length therefore MUST satisfy:

```text
blocks = max(1, ceil(uncompressed_len / 65535))
metadata_compressed_len <= uncompressed_len + 5 * blocks + 6
```

A writer can always meet this bound using RFC 1951 stored blocks when another
encoding would be larger.

Metadata uses a forward COW chunk chain. Each page has type 13, level zero,
`item_count == 1`, `aux == 0`, and this body:

| Offset | Size | Field |
|---:|---:|---|
| 32 | 4 | `next_pgno`, zero on the final chunk |
| 36 | 2 | `chunk_len` |
| 38 | 2 | reserved zero |
| 40 | 8 | `logical_offset` |
| 48 | `chunk_len` | compressed bytes |

`chunk_len` is `1..4048`. Every nonfinal chunk MUST have
`chunk_len == 4048`; only the final chunk may be shorter. Offsets start at zero
and are contiguous. The chain MUST end exactly at
`metadata_compressed_len`; cycles, gaps, extra chunks, and trailing compressed
bytes are invalid.
For each chunk, `lower == 48 + chunk_len`, `upper == 4096`, and bytes after the
chunk are zero.

`SetMetadataJSON` checks the uncompressed limit, compresses the complete object,
and stages private COW chunks before commit. `Commit` MUST NOT compress. Every
`SetMetadataJSON` call is an explicit replacement and stages the supplied bytes,
even when they equal the committed decompressed bytes; the SDK MUST NOT add an
implicit old-payload read/decompress/compare. Metadata retains its committed
root without rewrite only when the transaction contains no metadata stage.
`ClearMetadataJSON` stages root zero. Clearing already-absent metadata is O(1)
`NoChange`: on a clean writer it starts no transaction, and in an existing draft
it changes no root. Decompression MUST stop with an error if output would exceed
the declared length or limit and MUST reproduce exactly the declared number of
bytes.

A clean-writer call to `SetMetadataJSON` or `ClearMetadataJSON` takes an explicit
cancellation token and stores it as the new metadata-only transaction's token.
Inside an already active advanced transaction or high-level draft, the same
calls omit a new token and use the operation's stored token; supplying a second
token is `WrongState` before mutation. Clearing already-absent metadata on a
clean writer accepts the explicit token but starts no transaction and retains no
token.

A clean writer MAY start a metadata-only transaction. Every ordinary advanced
transaction or high-level workflow MAY stage at most one
`SetMetadataJSON` or `ClearMetadataJSON` alongside its data/catalog changes. A
second metadata stage is rejected before mutation rather than using last-call-
wins semantics. Changed metadata follows the complete transaction's abort,
cleanup, and commit rules.

A reader's `GetMetadataJSON` reads the exact optional bytes from its pinned
generation. A writer provides read-your-writes: staged Set returns the staged
exact bytes, staged Clear returns absence, and otherwise it reads the committed
base generation. The stable core uses two calls: first report presence and exact
decompressed length, then fill caller storage. Insufficient storage returns the
required size and no partial bytes. Native Go/Rust allocation helpers MAY return
the complete value because the uncompressed size is bounded at 1 MiB. A failed
metadata read never changes a draft.

## 12. Hierarchical bitmap pages

Page types 14 and 15 implement three bitmap kinds selected by `aux`:

| Value | Kind | Leaf bit meaning |
|---:|---|---|
| 1 | free pages | page is safe to overwrite |
| 2 | feed used | feed index is active |
| 3 | membership used | membership ID is live |

The governing limits are selected `page_count`, `feed_index_limit`, and
`membership_id_limit`, respectively. Feed-used bits at or above their limit are
zero. Membership-used bit zero and bits at or above their limit are zero.

A bitmap root covers an implicit range beginning at bit zero. A level-0 leaf
covers 32,000 bits. A level-`L` branch covers
`32,000 * 256^L` bits. All coverage arithmetic is checked `u64`. A nonzero root
MUST have the minimum level capable of covering the governing limit. A zero
root means all stored bits are zero.

Every nonzero child has the same bitmap kind as its parent and a level exactly
one lower. The child position determines its implicit bit range; pointers MUST
NOT repeat or form cycles. Traversal is bounded by `MAX_TREE_LEVEL` even on
ordinary non-validating paths.

### 12.1 Bitmap leaf

A bitmap leaf has type 15, level zero, and 500 consecutive little-endian `u64`
words at bytes `[32,4032)`. Bytes `[4032,4096)` are zero. `item_count` is the
number of nonzero words. `lower == 4032` and `upper == 4096`.

Bits outside the governing limit MUST be zero. All-zero leaves SHOULD be
omitted.

### 12.2 Bitmap branch

A bitmap branch has type 14 and positive level. Its body is:

| Offset | Size | Field |
|---:|---:|---|
| 32 | 32 | 256-bit `summary` |
| 64 | 1,024 | 256 `child_pgno:u32` values |
| 1088 | 3,008 | zero |

`item_count` is the count of nonzero child pointers. `lower == 1088` and
`upper == 4096`.

The summary is four consecutive little-endian `u64` words. Summary bit `i` is
word `i / 64`, bit `i % 64`, with bit zero as that word's least-significant bit.
This is the same LSB-first convention used by feed and membership bitmaps.

For free-page bitmaps, summary bit `i` is one exactly when child `i` is nonzero
and its subtree contains a one bit.

For feed-used and membership-used bitmaps, summary bit `i` is one exactly when
the child's logical coverage intersects the governing limit and contains an
allocation candidate zero bit. An absent child is logically all zero and can
therefore have summary one. Membership ID zero is excluded from candidacy.

Searching scans summary and leaf words as `u64` values and uses the least
significant one bit. It MUST NOT scan linearly from bit zero across represented
pages.

## 13. Free pages and retirement

The free-page bitmap's governing limit is `page_count`. A one bit means the
page is currently free and safe to overwrite. Meta pages, current reachable
pages, meta-owned allocator-reserve pages, and retired pages have zero bits.
Bits beyond `page_count` are zero.

Allocation takes the lowest eligible free-page bit, then aligned tail growth.
At `MAX_PAGE_COUNT`, absence of an eligible bit returns
`PageSpaceExhausted` before mutation. Checked page-number, byte-offset,
`off_t`, and host-index conversions precede every access.

Before a committed free bit authorizes overwrite, the transaction verifies the
normalized CRC and local invariants of that selected bitmap root-to-leaf path:
page type/kind/level, bounds, reserved bytes, counts, summary/child
relationship, and the selected in-range one bit. This narrow check does not
validate unselected subtrees or the global page partition. A committed bitmap
page is checked and copied at most once per transaction; its private copy is
then updated in place.

The four `allocator_reserve` entries break the bitmap's allocation dependency.
A transaction uses a nonzero reserve entry only as the destination of a
committed bitmap-page copy, removes it from the pending meta, and otherwise
falls back to tail growth for such copies. Four pages are sufficient for the
maximum free-bitmap path at the `2^32` page limit. Before commit, empty reserve
slots may be replenished with aligned tail pages. Reserve contents are ignored
until overwritten; abort therefore leaves every reserve named by the selected
meta safely reusable even if the failed draft wrote it.

A page created by the current unpublished transaction is not part of an older
reader generation. If that private page becomes unused, the transaction may
reuse it directly or expose it as free in its private bitmap. A replaced
committed page is different: it is never reused by the same transaction and is
inserted into the retirement tree under the target transaction.

Every ordinary commit retires replaced committed pages, whether or not a reader
currently needs them. Commit does not perform reader-dependent allocator
finalization. This deliberate one-maintenance-operation delay gives one uniform
bounded commit path. `Reclaim` alone proves which retired generations are safe,
moves their pages to the free bitmap, and removes their retirement entries.
The retirement tree contains only unreclaimed pages, not permanent history.

### 13.1 Retirement tree

The retirement tree is ordered lexicographically by
`(retired_by_txn, first_pgno)`. Branch records are:

```text
first_retired_by_txn:u64  first_pgno:u32  child_pgno:u32
```

Leaf records are canonical contiguous extents:

```text
retired_by_txn:u64  first_pgno:u32  page_count:u32
```

Both records are 16 bytes. Branches have type 16 and positive level; leaves
have type 17 and level zero. Both use `aux == 0` and the slotted-page
convention. Branch keys are the exact first key in each child. Root zero is the
only empty representation.

Every leaf has `1 < retired_by_txn <= selected_txn_id`, `first_pgno >= 2`,
`page_count > 0`, and checked `first_pgno + page_count <= selected
page_count`. Extents for one transaction are strictly increasing, disjoint, and
coalesced when adjacent. A page appears in exactly one extent.
`retired_extent_count` is the exact number of leaf records.

`retired_by_txn == T` means transaction T removed the pages from the current
roots. Its complete transaction group is safe when no active reader has a
transaction below T. Reader registration is protected by the gate, so the
compact sidecar protocol has no transaction-zero registration.

The writer updates retirement after every other logical root. The first insert
copies at most the committed rightmost path; those replaced retirement pages
are then inserted under the same target transaction. Later inserts and extent
coalescing update the private path in place. No page-list blob, sorting pass,
fixed-point planner, or file-sized retirement workspace exists.

`Reclaim(max_transactions,max_pages,cancellation)` is a clean-writer,
auto-publishing maintenance operation using the writer's existing transaction
resource budget. Both limits are nonzero. Under the exclusive operation gate it
scans stable reader registrations and selects complete oldest safe transaction
groups fitting both limits. If the oldest safe group alone exceeds
`max_pages`, it returns `WorkLimitTooSmall` with that exact page count before
mutation. Once one or more groups are selected, it stops before the next group
that would exceed either limit.

Reclaim streams the selected extents into the private free bitmap, deletes the
selected retirement records, and retires the committed bitmap/retirement pages
that this maintenance transaction replaces under its own target transaction.
It publishes no generation when no group is selected. Otherwise its result
contains exact reclaimed transaction/page counters and the complete
`CommitResult`. The operation lock remains held from the reader scan through
publication; existing readers continue and new registration waits.

Normal live commits do not reduce committed `page_count`, even when the highest
pages become free. Only unpublished tail cleanup may truncate a live file.
Compact snapshots are the supported physical compaction path.

Every committed page from 2 through `page_count-1` is exactly one of:

- reachable from a current root;
- named by `allocator_reserve`;
- marked free; or
- covered by one retirement extent.

Explicit validation checks this partition with bounded memory or authorized
external scratch. Open does not.

## 14. Transactions, mutations, and commit durability

All public writer handles are live handles bound to a valid external sidecar.
An existing sidecar-free immutable file cannot be opened for mutation; the
caller must first complete explicit offline `InitializeLive`. `CreateLive` is
the creation API for a new mutable database. Phase 1 has no separate
`CreateImmutable` writer or offline in-place mutation API: immutable outputs are
created only by `SnapshotTo` and recovery publication.

There is at most one writer. Its states are:

- clean at a committed generation;
- pending with one advanced logical transaction's private unpublished COW
  changes;
- executing one high-level workflow's private unpublished draft;
- abort/close-only after incomplete cleanup;
- outcome-unknown/resolve-only after an ambiguous commit; or
- unusable.

After preconditions succeed and immediately before the first private page or
transaction scratch byte is written, a clean writer enters a pending
transaction, checks `txn_id + 1`, and generates that transaction's nonzero
commit nonce. The nonce and target transaction remain fixed until commit or
abort. Exact private drafts do the same at operation start. Random-generation
failure occurs before mutation.

The writer is configured with one transaction resource budget. A pending
transaction owns its bounded heap/control buffers, operation-private page-backed
normalization/refcount indexes, reserved pages, and unpublished same-file growth
until commit or abort. It creates no external scratch or sorting file. Later
calls in the same pending transaction use the same ledger; a new per-call budget
cannot reset or bypass already charged resources.

### 14.1 Mutation failure atomicity

An input or state error proven before mutation leaves the existing pending
transaction unchanged.

If a public operation fails after it may have changed pending state, the writer
MUST abort the entire pending transaction to the last committed generation,
including earlier uncommitted operations. It returns a typed
`TransactionAborted` result with the section-14.4 cleanup state and unresolved
artifact ledger. The writer becomes clean and reusable only if rollback and
unpublished-growth cleanup both succeed and that ledger is empty.

Main-database I/O failure, detected committed-data corruption, or
rollback/cleanup failure makes the handle unusable or explicit cleanup-only as
specified by the failure result. An input, source, callback, or cancellation
error follows the whole-draft abort rule and leaves the writer reusable only
when cleanup is proven.

Exact bulk replacement and feed lifecycle operations require a clean writer,
own their complete private draft, and discard that draft on every pre-commit
failure. Cleanup failure makes the handle unusable.

### 14.2 Commit phases and result

Commit returns a structured result containing:

```text
attempted_database_id:[16]byte
directory_identity_kind:u16
directory_local_identity:[32]byte
main_identity_kind:u16
main_local_identity:[32]byte
attempted_txn_id:u64
attempted_commit_nonce:[16]byte
durability: NotCommitted | Committed | OutcomeUnknown
cleanup_state: Clean | ResiduePossible
cleanup_artifacts: bounded sequence of CleanupArtifact
coordination_cleanup: None | CleanupGuard | RetainedReaderCloseRequired | RetainedWriterCloseRequired
cause: optional typed error
```

`durability` describes only meta publication. Cleanup state is orthogonal:
`NotCommitted` remains valid when non-publication is proven but private-page or
main-tail cleanup leaves residue, and `Committed` remains valid when the meta is
durable but post-publication main-tail or coordination cleanup remains. Commit
owns no external scratch or private external file, so its Windows housekeeping
is always `None`. The directory and main
identities let resolution report whether it inspected the same local file or a
deliberate logical copy; database ID plus exact transaction/nonce remain the
logical authority. Any nonempty cleanup ledger or
`RetainedWriterCloseRequired` disposition poisons the writer for normal
operations even when durability itself is definitive.

Commit first performs a **preparation stage** before publication can become
ambiguous. It completes operation-private page-backed normalization and delta
aggregation, prepares all content roots, and reserves the worst-case pages and
growth for allocator/retirement finalization. It creates no external scratch.
Preparation may use only the transaction's existing resource ledger. Its
failure aborts the whole draft and is
`NotCommitted` whenever pre-publication non-commit is proven; cleanup state is
reported independently.

The durability phases are:

1. Acquire the live operation lock; recheck the writer lease, retained
   identities, sidecar binding, and unchanged selected generation, then
   complete the target meta using only prepared state.
2. Synchronize every new or changed non-meta page and required file growth.
3. Begin writing the complete target meta page.
4. Synchronize the target meta page to durable storage.
5. While still holding the operation lock, update the writer lease to the new
   selected transaction, trim provably unpublished excess growth when needed,
   and perform other non-publication cleanup.

A failure before phase 3 begins is `NotCommitted`; the previous meta remains
authoritative. A storage failure still makes the handle unusable.

From the first byte write of phase 3 until phase 4 succeeds, any failure is
`OutcomeUnknown`. The caller MUST close, reopen the same database ID, and
resolve the attempted transaction before retrying.

Every `OutcomeUnknown` writer is unconditionally close-only and returns
`coordination_cleanup == RetainedWriterCloseRequired` with
`cleanup_state == ResiduePossible`; it is never reusable or consumable merely
because no separate artifact cleanup failed. `Close` reselects the main metas
under the operation lock. It truncates an old-generation tail only when that old
generation remains selected, or marks the obligation satisfied by supersession
when the exact attempted or a valid later generation is selected, before
clearing the retained lease.

Successful phase 4 is `Committed`. A later cleanup failure remains `Committed`
and the transaction MUST NOT be retried. Artifact residue is listed completely;
an interrupted lease transition or any other result that makes the retained
writer close-only sets `coordination_cleanup == RetainedWriterCloseRequired`.
`cleanup_state == ResiduePossible` when either obligation exists, even when its
artifact ledger is empty because the sole obligation is the non-consuming writer
handle. Every non-clean commit result makes that writer handle unusable except
for idempotent `Close`.

"Synchronize" is an exact durability operation, not a buffered write or close:

- Linux and FreeBSD flush dirty mappings with `msync(MS_SYNC)` and call
  `fsync` on the retained main descriptor at phases 2 and 4; namespace changes
  are followed by `fsync` on the retained directory descriptor.
- macOS flushes dirty mappings with `msync(MS_SYNC)` and uses
  `fcntl(F_FULLFSYNC)` on the main file at phases 2 and 4, followed by `fsync`
  on the retained directory descriptor after namespace changes.
- Windows flushes mapped views with `FlushViewOfFile` and then calls
  `FlushFileBuffers` on the retained writable file handle at phases 2 and 4.
  Every handle that can coexist with rename, replacement, or retirement uses
  `FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE`. A source handle
  used for a namespace change also has `DELETE` access and was opened with
  `FILE_WRITE_THROUGH`. Atomic same-volume namespace changes use
  `SetFileInformationByHandle(FileRenameInfoEx)` on that retained source handle
  with a `FILE_RENAME_INFO` buffer: `RootDirectory` is the retained destination
  directory handle and `FileName` is one simple relative name. No-replace uses
  flags zero. Explicit no-rollback replacement uses flags `0x1 | 0x2`
  (replace-if-exists plus POSIX semantics, so retained handles to the replaced
  inode remain valid but its old name is discarded). Those flags are not an
  atomic exchange and MUST NOT implement strict rollback-safe replacement.
  The implementation then calls `FlushFileBuffers` on the renamed file handle
  and rechecks the final entry through the retained directory before reporting
  namespace durability. Phase-1 durable Windows
  namespace publication is supported only on a local NTFS volume, for which the
  documented `FILE_FLAG_WRITE_THROUGH` contract includes rename metadata.
  Another Windows filesystem is unsupported until the implementation has
  separately proved equivalent atomic, open-file replacement, write-through,
  and flush semantics.

Every return value is checked. A platform or filesystem that cannot provide
the required file, mapping, locking, atomic-namespace, or directory durability
primitive returns typed `DurabilityUnsupported` or the phase-appropriate
failure outcome; it MUST NOT silently downgrade the guarantee. The same
platform file-sync primitive is the synchronization required by unknown-commit
resolution.

`ResolveCommit(path, commit_result, Live | Immutable, cancellation)` first requires a complete
result, retains the supplied parent directory, opens the main without following
symlinks, and requires the attempted database ID. The recorded original
directory/main identities are comparison evidence, not a gate: a deliberate
byte copy may preserve the logical database and exact attempted generation on a
different local inode. The resolution result reports the actual retained
directory/main identities and `SameLocalFile | DifferentLocalFile`.

In `Live` mode the resolver takes the shared main lifetime lock, opens and
validates the sidecar bound to the actual retained main, then takes its operation
lock. Under that lock it strictly scans the complete table, reaping only through
the transition-provenance rule, and requires the writer lease to be free. A
live/uncertain writer is `WriterBusy`. A state-0 sidecar is
`LiveCoordinationCleanupRequired`: the caller must use the exact attempt
resolver or `ResolveInterruptedLiveTransition`. A malformed sidecar requires
caller-certified offline coordination reset before resolution can continue.
In `Immutable` mode the canonical sidecar must be absent; the resolver holds the
shared main lifetime lock and rechecks path identity plus sidecar absence before
and after the proof.

The resolver holds the applicable stable lock boundary from the first two-meta
read through main-file synchronization, a second two-meta read, and final
directory/main/sidecar identity rechecks. The two reads must select the same
classification; otherwise resolution is `Unresolvable`. Commit resolution
requires the same two-meta current-generation proof as mutable open; a sole
bootstrap-valid meta cannot prove non-commit and is `Unresolvable`. It then
examines both retained bootstrap-valid meta pages. Its result is:

- `Committed` only when a retained meta has the exact attempted transaction and
  exact 128-bit commit nonce and a subsequent synchronization of the reopened
  main file succeeds;
- `NotCommitted` when the selected transaction is lower than the attempt, or
  when either retained bootstrap-valid meta has the attempted transaction with
  a different nonce;
- `SupersededUnknown` when the selected transaction is later but neither
  retained meta carries either the exact attempt or a different nonce for the
  attempted transaction; and
- `Unresolvable` for database-ID mismatch, unselectable metas, or I/O failure.

A later transaction number alone is never proof that this caller's attempt
committed. Transaction IDs cannot skip within one valid meta sequence, but a
different writer can reuse an unpublished attempted number.

Every classification describes only the actual retained file identity reported
by the resolver. `Committed` means the exact attempted generation is now durably
present in that file; `NotCommitted` and `SupersededUnknown` likewise describe
only that file's selected metas. With `DifferentLocalFile`, no classification
retroactively proves what happened on the original inode or authorizes retry or
mutation against the original path. A caller resolving an intentionally revived
copy may continue only from that copy under its reported classification and
identities.

After the stable classification and identity rechecks, aligned physical bytes
beyond the selected generation's committed length are proved unpublished. The
resolver truncates only that tail, synchronizes the main, reselects the same
generation, and repeats the path/sidecar checks before reporting `Clean`.
Failure or inability to prove that cleanup preserves the factual commit
classification but returns one exact `UnpublishedMainTail` cleanup artifact and
the typed cause. It never reports an observed unpublished tail as clean.

Merely rereading an exact meta from the operating-system page cache does not
prove crash durability; the resolution synchronization is mandatory. Failure
of that synchronization leaves the outcome unknown and the resolver unusable.
In live mode the operation lock prevents a concurrent writer claim or meta
publication during the proof; in both modes the lifetime lock prevents reset,
replacement, or relink. Resolver cleanup failure follows the same retained-guard
rule and never guesses an outcome from an unstable inspection.

Commit with no pending published-state change returns a typed
`NoPendingTransaction` error and does not advance the transaction. If an
advanced `Begin` is active but no mutation or metadata stage ever started a
pending transaction, this call also terminates that empty operation, invalidates
its child references, releases its stored cancellation token, and leaves the
writer clean. `Reclaim` publishes internally and never leaves a caller-pending
maintenance draft.

#### 14.2.1 Explicit abort and pending close

`Abort()` is explicit and non-consuming. A clean writer returns
`NoPendingTransaction`. An active but still-empty advanced operation terminates,
invalidates its child references, releases its stored cancellation token, and
returns clean `Aborted`; it is not a clean writer until that termination. A
pending advanced transaction or high-level draft
immediately invalidates every transaction/feed/membership child handle, discards
unpublished COW and operation-private index state, and proves the committed
generation and physical tail. Once that proof succeeds the primary result is
`Aborted`.

`Aborted` with no cleanup obligation keeps the writer lease and returns the
writer clean and reusable. `Aborted` with independent exact private residue
reports that residue orthogonally and makes the writer close-only until `Close`
clears the lease and transfers or returns the remaining residue ledger. Only an
unresolved main-generation/tail proof is `AbortIncomplete`; it retains an
abort/close-only writer for explicit retry.

Explicit `Close()` on a healthy pending writer runs the same abort protocol and
then clears the writer lease. It never commits. Abort and Close are non-consuming
on failure. If abort completed but lease clear failed, the result records that
abort is complete and retains a close-only writer with
`RetainedWriterCloseRequired`. A writer with an `OutcomeUnknown` commit rejects
Abort and permits only meta-aware Close/Resolve. Automatic destructors and
finalizers never start any part of this protocol.

### 14.3 Memory contract

After fixed per-handle buffers and stacks are initialized:

- warmed steady-state lookup and cursor movement MUST perform no heap
  allocation;
- an advanced range mutation MUST perform no heap allocation when its COW pages fit in
  the existing mapping, reusable free pages, and preallocated per-writer
  scratch; mapping growth or remapping MAY use bounded control allocations;
- the durability phases of `Commit`, after transaction preparation, MUST
  perform no heap allocation or create scratch files;
- retained engine working memory for these operations MUST NOT scale with file
  size, highest page number, free-page count, retirement history, feed count,
  or dictionary history;
- metadata compression occurs during metadata staging, not commit; and
- normalization, lifecycle replacement, import, validation, recovery, and
  snapshotting use caller-bounded working memory. Only explicit validation and
  recovery graph-safety work MAY additionally use caller-authorized bounded
  external scratch files.

Mapped virtual address space MAY scale with the mapped byte range and is
measured separately from language heap and resident memory. No operation may
allocate one heap object per streamed range record. Output whose cardinality is
inherently proportional to records, feeds, or feed pairs MUST be streamed to a
caller sink or explicitly materialized under a caller budget; it is not retained
as hidden engine working state.

### 14.4 Resource budgets, private pages, and authorized scratch

Every writer receives one transaction resource budget containing at least:

```text
max_heap_bytes:u64
max_private_pages:u64
max_file_growth_pages:u64
max_open_files:u32
```

Snapshot/output construction additionally receives `max_output_pages:u64`.
Explicit validation and recovery receive an operation budget containing
`max_heap_bytes`, `max_open_files`, and optional external-scratch authority:

```text
max_scratch_bytes:u64
max_scratch_files:u32
scratch_directory: caller-selected local path, present only when either limit is nonzero
```

The numeric limits are maximum simultaneously retained engine resources, not
cumulative work. Private pages count every unpublished destination page retained
by the operation, whether allocated from committed free space or appended;
growth pages count only appended physical pages. Output pages count the actual
private final inode that may become the published result, not sorting scratch.
The implementation documents fixed overhead and rejects an insufficient or
unrepresentable budget before exceeding it. Host `usize`, file-offset, mapping,
descriptor, page-count, and scratch-space conversions are checked.

Normal ingestion, metadata staging, advanced transactions, lifecycle/direct/
retention workflows, import, commit, abort, query, and snapshot construction
MUST set external-scratch use to zero and MUST NOT create an external scratch or
sorting file. They use bounded heap, operation-private destination COW pages,
and—when producing a file—the one private inode that is the final output before
publication. Only explicit validation and recovery graph-safety work may create
external scratch, and only when the caller supplied nonzero authority. With zero
scratch authority those operations complete inside the heap budget or return
typed `InsufficientResourceBudget` before exceeding it.

Authorized scratch files use exclusive creation and fixed-width buffered
records. A write, flush, close, callback, merge, or cancellation error closes
and attempts to remove the partial file and every earlier still-owned scratch
file. Cleanup errors are returned through the exact ledger below. Successful
completion removes every scratch file. Scratch is never durable database state.

Every operation or pending transaction that can own temporary artifacts keeps
an exact cleanup ledger bounded by the maximum simultaneously retained
artifacts plus its fixed output/reservation overhead. A returned
`CleanupArtifact` has this shape:

```text
artifact_kind: PrivateOutput | PrivateReservation | OwnedCoordination | AuthorizedScratch | UnpublishedMainTail
directory_role: Destination | ScratchDirectory | MainFile
directory_identity_kind:u16
directory_local_identity:[32]byte
basename_encoding:u16  # 1=POSIX bytes, 2=well-formed Windows UTF-16LE
basename: bounded byte sequence in that encoding
identity_kind: optional u16
local_identity: optional [32]byte
creation_security_kind: optional u16
creation_security_commitment: optional [32]byte
expected_database_id: optional [16]byte
committed_target_txn_id: optional u64
committed_target_nonce: optional [16]byte
committed_target_length: optional u64
observed_tail_end_exclusive: optional u64
cleanup_state: ResiduePossible
cleanup_error: typed error
```

In every cleanup/list/removal API below,
`expected_directory_identity` is the pair
`(directory_identity_kind:u16, directory_local_identity:[32]byte)` from this
record. It is a required argument for mutation, never an optional diagnostic.

Each directory role identifies the exact caller-supplied destination directory,
authorized `scratch_directory`, or retained main-file parent; the record never
substitutes a current working directory. Before first artifact creation the
operation retains that no-follow directory and records its mandatory local
identity using the section-15.2 identity encoding; inability to establish it is
a pre-creation `DurabilityUnsupported` error. Artifact identity kind and local
identity are present together or absent together. Each basename is one component
and is never an arbitrary path. `PrivateReservation` covers the owned reservation
inode before sidecar conversion. Once that same inode has selected sidecar magic
or sidecar size, an obligated removal is `OwnedCoordination`; it remains bound to
the original attempt/sidecar ID and exact identity at canonical, private, or
authorized inert GC name.

`UnpublishedMainTail` requires identity kind/local identity, expected database
ID, committed target transaction/nonce, and both length fields; all other
artifact kinds omit all five tail-authority fields. Creation-security kind and
commitment are present together for every separately created artifact and absent
together for `UnpublishedMainTail`. Its exact unproved
interval is `[committed_target_length, observed_tail_end_exclusive)`, with the
target equal to the bootstrap-selected committed byte length and the observed
end strictly greater. Retry requires the same main identity/database, the same
selected committed transaction/nonce/length, exclusive writer/cleanup authority,
and a current length no greater than the recorded observed end; any
shorter-than-target or unexpected-growth state is `CleanupConflict`. If the same
identity-bound database instead selects a later transaction, or the same
transaction with a different nonce, retry MUST NOT truncate. Under the live
operation lock it requires a free/reaped writer lease, synchronizes and rechecks
that newer/different generation and its exact committed length, then marks the
old tail obligation satisfied-by-supersession: a cooperating writer could publish
that state only after open-time cleanup took ownership of any prior tail. A lower
or unselectable generation remains `CleanupConflict`. The tail is never removed
by a temporary-artifact cleanup API.

The cleanup ledger contains only artifacts that the operation was obligated or
attempted to remove and whose durable absence is not proved. An entry may be
dropped only after the platform's exact correctness-cleanup proof. POSIX requires
either exact owned-inode unlink or identity-bound proof that a prior unlink made
the name absent, followed by required containing-directory synchronization and
final identity/absence recheck. Windows uses the authoritative/private-to-inert
GC transition in section 14.4.1; final housekeeping unlink is not part of
correctness cleanup. An
`UnpublishedMainTail` entry instead drops only after exact truncation to the
recorded target, main-file synchronization, and final main identity/length/meta
recheck. If a shared directory synchronization fails after several unlinks,
every affected entry remains `ResiduePossible` with that synchronization error.
An artifact proven never created needs no entry; an absent identity is not proof
of absence.

An output, reservation, or sidecar intentionally retained at its authoritative
canonical/private name for an unresolved structured result is not a cleanup failure and is
identified by that primary result instead of this ledger. It enters the cleanup
ledger only if a definitive direct or resolver action becomes obligated to
remove it and cannot prove durable absence. Authorized scratch and unpublished
main tails are never resolution authority and therefore must be cleaned or ledgered
before return. A live-coordination cleanup guard or retained close-only handle
is a non-artifact cleanup obligation and is reported in the result's explicit
`coordination_cleanup` field; it is never fabricated as a `CleanupArtifact`.
The common field is exactly:

```text
coordination_cleanup:
    None
  | CleanupGuard
  | RetainedReaderCloseRequired
  | RetainedWriterCloseRequired
```

`CleanupGuard` transfers the one opaque owned guard in the terminal result or
error. Until that guard is taken, the owning result/error cannot be destroyed;
destroy returns `HandleBusy` and leaves the handle intact. A retained-reader/writer variant means the original non-consuming handle
still owns the obligation and permits only the specified cleanup/close retry.
Every terminal result or error from live open, live reader/writer close, commit,
validation, recovery, snapshot, a resolver, or any scan that may reap a live
slot includes this field. Operations that cannot own live coordination have it
implicitly `None`.
`Clean` means there is neither an artifact-ledger entry nor a coordination
cleanup obligation, not that an `OutcomeUnknown` result has no intentionally
retained attempt artifacts.

The returned aggregate cleanup state is `Clean` exactly when the artifact ledger
is empty and `coordination_cleanup == None`, and `ResiduePossible` otherwise.
It is therefore legal and required to return `ResiduePossible` with an empty
artifact sequence when the separately returned obligation is an opaque guard or
retained close-only handle. This remains bounded by simultaneous resources, not
cumulative runs created during a multi-pass merge, while ensuring one failed
unlink, directory sync, or live-slot transition never hides another possible
residue.

An explicit validation or recovery operation lazily generates one random
nonzero 128-bit scratch-attempt ID immediately before creating its first
authorized scratch file. Files use exclusive basenames
`.iprange-scratch-<attempt-id>-<ordinal>.tmp`, with lowercase fixed-width
hexadecimal fields (32 and 8 digits). The ordinal starts at zero, increases
monotonically, never repeats after removal, and fails before `u32` wrap. No
transaction, ingestion, import, query, or snapshot attempt has a scratch
namespace.

Every raw `[16]byte` identifier or nonce used in a basename is encoded as two
lowercase hexadecimal digits per byte in stored array order. Numeric transaction
and ordinal fields are the unsigned integer value in zero-padded lowercase
hexadecimal at their stated width. Decoders reject uppercase, signs, prefixes,
wrong widths, or any non-hexadecimal byte. This rule applies to every attempt-
derived basename in this specification.

Every authorized scratch file starts with this 128-byte ownership header before
its operation-specific records:

| Offset | Size | Field |
|---:|---:|---|
| 0 | 8 | ASCII `IPR4SCR1` |
| 8 | 2 | `version = 1` |
| 10 | 2 | `header_size = 128` |
| 12 | 2 | `owner_kind` (`1=validation`, `2=recovery`) |
| 14 | 2 | zero |
| 16 | 16 | source database ID when safely known; otherwise zero |
| 32 | 8 | selected source transaction when safely known; otherwise zero |
| 40 | 16 | selected source commit nonce when safely known; otherwise zero |
| 56 | 16 | scratch-attempt ID |
| 72 | 4 | ordinal |
| 76 | 2 | creation-security kind from section 15.6 |
| 78 | 2 | zero |
| 80 | 32 | creation-security commitment from section 15.6 |
| 112 | 12 | zero |
| 124 | 4 | CRC-32C of the header with this field zero |

The complete header is written before the first record. It needs no separate
durability synchronization because the file is scratch, but after a crash only
a CRC-valid header whose owner/attempt/ordinal exactly matches the canonical
basename can authorize an abandoned-scratch remover. A partial, malformed, or
mismatched lookalike is reported as unauthenticated and is never removed by an
engine API. The caller supplies a controlled scratch directory whose reserved
`.iprange-` namespace is not shared with untrusted writers; a random basename
alone is never deletion authority.

Every terminal validation/recovery success or error contains the optional
scratch-attempt ID, creation-security kind/commitment once artifact creation
begins, aggregate cleanup state, complete unresolved ledger, common
`coordination_cleanup`, and orthogonal housekeeping/visible-housekeeping fields;
the ID is absent only when no scratch creation was attempted.
`ListAbandonedScratch(scratch_directory, cancellation, sink)` enumerates only the exact
scratch pattern and reports attempt ID, ordinal, owner kind, and no-follow
identity. `RemoveAbandonedScratch(scratch_directory,
expected_directory_identity, attempt_id, ordinal,
expected_artifact_identity, cancellation)` additionally requires caller-certified absence of
an active validation/recovery operation with that attempt ID. Age is never
abandonment proof.

The mandatory cross-language streaming core is synchronous and batched. The
engine lends one bounded record buffer to a pull source, which returns
`Batch(nonzero_count) | End | Error`. Output is delivered as a nonempty borrowed
batch to a sink returning `Continue | Stop | Error`. Batch capacity is bounded
by the operation budget and ABI contract; neither side may retain borrowed
records after return. Immediate `End` is legal empty input. A source error
propagates exactly and aborts any private draft.

Every `AddRanges`, advanced direct assign/clear, and advanced membership-apply
call drains exactly one finite pull source through that contract. Records from
its nonempty batches are appended in callback order to the active operation;
`End` terminates that call, not the whole workflow. Repeated calls therefore
concatenate their sources exactly. Native borrowed-slice/iterator overloads are
adapters over this rule; the C ABI uses the callback directly. Only
`FinishInput` declares the complete high-level snapshot. A source error or
cancellation aborts the whole draft, including records from earlier successful
calls.

`Stop` returns `StoppedBySink`: read-only enumeration may report truthful partial
counters, while validation, recovery, construction, snapshot, and mutation are
incomplete and cannot publish. `Error` returns `SinkFailed` with the caller cause.
Native callback panic is contained by the same cleanup path. Cleanup and the
complete ledger are finalized before the synchronous operation returns; no
public iterator or stream handle owns scratch. Single-record adapters and native
iterators are convenience wrappers only.

All exact cleanup retry/removal APIs share one identity-bound idempotent rule.
They use the
retained no-follow parent descriptor when still available; after restart they
open the exact caller-supplied directory and require its identity to equal the
recorded directory identity before inspecting the basename. Path replacement or
unprovable directory identity is `CleanupConflict`, never absence proof. Under
that identity-bound directory they accept either the matching owned inode or
current authoritative-name absence. A foreign inode is `CleanupConflict` and is
never removed. POSIX unlinks a matching inode and, for either present or already-
absent state, synchronizes the containing directory and rechecks identity/name
absence before reporting durable removal. Windows instead performs or resolves
the section-14.4.1 exact move to the authenticated inert GC name; proved
authoritative-name absence is sufficient only when the selected GC envelope
proves that same transition. Final inert-name deletion is separately reported
housekeeping and is never called durable correctness cleanup. All abandoned-
artifact list APIs likewise report the retained
directory identity, and removal requires it. Thus an unlink-success/directory-
sync-failure record has a defined retry path. `UnpublishedMainTail` instead
requires the exact directory and retained main identities, proven committed
length, exclusive writer ownership, truncation/synchronization, and final
length/identity rechecks.

#### 14.4.1 Windows correctness cleanup and housekeeping

On Windows local NTFS, final close-triggered unlink is not treated as power-loss
durable. Correctness cleanup is instead complete once the exact owned artifact
has been durably removed from every authoritative/private name and moved through
a retained write-through handle to one inert, attempt-bound GC name. That GC
name is never publication, reservation, live coordination, retry authority, or a
blocker for later work.

No GC envelope exists during normal creation, publication, reservation, or
sidecar conversion. Only after an operation has decided to retire an exact
retained inode does correctness cleanup classify its then-current artifact kind
and authoritative/private basename, retain the applicable operation/lifetime
lock, and exclusively create one paired 8,192-byte GC authority envelope in the
same retained directory. From that point cleanup owns the inode: its artifact
kind and source basename cannot transition again. The envelope is metadata for
that payload, not another cleanup-obligated payload, so this rule does not
recurse. Its exact basename is
`.iprange-gcauth-<attempt-id>-<ordinal>.tmp`; the inert payload basename is
`.iprange-gc-<attempt-id>-<ordinal>.tmp`. Both numeric fields use the canonical
lowercase fixed-width encoding above. Ordinal 0 is the attempt's private main/
output, ordinal 1 is its reservation/coordination inode, and an authorized-
scratch attempt uses the scratch file's own ordinal. Output/publication and
scratch attempts have independent random IDs. Before accepting a newly generated
attempt ID, creation requires every exact source, envelope, and inert name for
its fixed ordinals to be absent; a collision generates another ID rather than
reusing old state. The envelope has two 4,096-byte blocks;
each block's bytes `[0,512)` are:

| Offset | Size | Field |
|---:|---:|---|
| 0 | 8 | ASCII `IPR4GCA1` |
| 8 | 2 | `header_record_size = 512` |
| 10 | 2 | `version = 1` |
| 12 | 2 | `artifact_kind` (`1=PrivateOutput`, `2=PrivateReservation`, `3=OwnedCoordination`, `4=AuthorizedScratch`) |
| 14 | 2 | basename encoding kind |
| 16 | 16 | nonzero attempt ID |
| 32 | 4 | ordinal |
| 36 | 2 | directory identity kind |
| 38 | 2 | artifact local-identity kind |
| 40 | 32 | directory local identity |
| 72 | 32 | authoritative/private basename commitment |
| 104 | 32 | inert GC basename commitment |
| 136 | 2 | payload digest kind (`0=unknown`, `1=exact SHA-512`) |
| 138 | 6 | zero |
| 144 | 32 | artifact local identity |
| 176 | 8 | exact payload byte length when known, otherwise zero |
| 184 | 64 | payload SHA-512 when known, otherwise zero |
| 248 | 16 | database ID when applicable, otherwise zero |
| 264 | 8 | transaction ID when applicable, otherwise zero |
| 272 | 16 | commit nonce when applicable, otherwise zero |
| 288 | 2 | creation-security kind from section 15.6 |
| 290 | 2 | directory role (`1=Destination`, `2=ScratchDirectory`, `3=MainFile`) |
| 292 | 4 | zero |
| 296 | 32 | creation-security commitment from section 15.6 |
| 328 | 4 | exact source filename byte length |
| 332 | 164 | zero |
| 496 | 8 | nonzero sequence |
| 504 | 4 | zero |
| 508 | 4 | block CRC-32C |

Bytes `[512,512+source_filename_length)` contain the exact filename component
in the declared basename encoding; they never contain a directory or full path.
The remaining bytes through 4,096 are zero. The filename is nonempty, must fit
this block and the retained filesystem's component limit, and must be one valid
component in the declared encoding. CRC covers the full block with its CRC field
zero. Selection uses the same independently selectable sequence/CRC rules as
sidecar headers. `UnpublishedMainTail` is not a separately named inode and is
invalid in a GC envelope. The two basename commitments are exactly:

```text
SHA-256("IPR4GCAUTH" || encoding_kind:u16le || name_len:u32le || name_bytes)
SHA-256("IPR4GCNAME" || encoding_kind:u16le || name_len:u32le || name_bytes)
```

The first hashes the stored exact authoritative/private component selected when
cleanup began and the second hashes the exact inert GC component. For each
artifact kind, the source component is one of the exact attempt-derived private
names defined by this specification, canonical `.readers`, or deterministic
`.readers.reset`; its stored bytes must reproduce the source commitment. The
directory role is authenticated because it cannot be reconstructed from a
directory path after restart. A digest-kind-1 payload identity has nonzero exact
length, SHA-512, and the applicable tuple; digest kind zero requires those fields
zero. Other digest kinds are invalid. An
unknown/partial payload identity has those fields zero but still binds exact
attempt, artifact kind, basename, directory, and local identity. If neither
header copy selects, automatic removal is forbidden and the files are reported
for caller-certified offline inspection.

Envelope creation writes byte-identical sequence-1 records to both blocks,
synchronizes the envelope and containing directory, and re-reads the selected
record before any payload rename. A partial or unselectable new envelope never
authorizes a payload move; the retained payload remains at its prior name and
under its pre-cleanup operation record. A selected envelope is immutable and is
never rewritten when a reservation becomes canonical or when reservation bytes
become sidecar bytes, because those transitions necessarily precede envelope
creation. On cleanup retry, an existing selected envelope fixes the exact source
name and artifact kind. A mismatch with the retained inode, selected reservation/
sidecar magic, or current source identity is `CleanupConflict`; the envelope is
never repurposed or advanced to another kind.

A selected envelope is a durable cleanup-intent barrier from the moment it is
synchronized, including while the payload remains at its committed source name.
Before any ordinary open, operation resolver, restore/complete action, abandoned-
artifact remover, or new cleanup attempt accepts an exact engine-owned source
inode, it checks the one attempt/kind-derived envelope name without scanning the
directory. A selected envelope whose source commitment and local identity match
means cleanup owns that inode: the API returns typed `CleanupInProgress` and only
the common GC move resolver used by `RemoveWindowsHousekeeping` or an already-
owned cleanup retry may act. An unselectable or mismatched envelope is
`CleanupConflict`; a second envelope is never created. Absence of the exact
envelope permits normal operation-record authority. Directory-wide enumeration
is needed only for offline discovery, not this precedence check.

Correctness cleanup atomically renames the exact retained artifact from the one
envelope-committed authoritative/private name to its GC name with write-through
semantics, durably resolves any temporary two-name ambiguity, and rechecks exact
identities. An
indeterminate rename remains a correctness-level transition record containing
both exact names and one identity until resolved. Once the move is proved, the
artifact leaves the cleanup ledger even if final unlink is not power-loss
provable. POSIX-style unlink of the inert payload and envelope is then best
effort.

Every terminal result or error from an operation that may create, retire, or
remove a Windows artifact contains orthogonal housekeeping:

```text
housekeeping: None | CrashReappearancePossible | Visible
visible_housekeeping: bounded sequence of HousekeepingArtifact
```

`HousekeepingArtifact` contains the directory role, directory identity kind and
local identity, basename encoding, attempt ID, ordinal, exact envelope basename
and local identity, the envelope-committed source basename, exact inert-payload
basename, source/inert presence state and optional local identities, artifact
kind, creation-security kind/commitment, and selected envelope sequence. Its
state is `MovePending | MoveAmbiguous | Inert | Conflict`; only `Inert` is pure
housekeeping. `selected_envelope_sequence=0` means a newly created envelope is
visible but neither block selected; it grants no move or deletion authority.
Every selected envelope has a nonzero sequence. The artifact is sufficient to
find the same transition after restart without
relying on a current working directory or an untrusted random name.

`Clean` means no correctness/retry-blocking obligation; it does not promise an
empty Windows directory. Visible inert files remain charged to heap, file-count,
and scratch/output byte budgets. A proved-currently-absent name may reduce to
`CrashReappearancePossible`; streamed offline enumeration rediscovers it if it
reappears after power loss. Every directory that may own such files must be
same-volume local NTFS. Exact identity-bound housekeeping removal is exposed but
makes no power-loss guarantee for the final unlink. Old ranges or JSON may
therefore consume quota and remain confidential-data residue until truncation or
housekeeping succeeds; results report that fact.

`ListWindowsHousekeeping(directory, cancellation, sink)` streams every exact GC
envelope/source/inert candidate with its retained directory identity and
classifies whether a selectable envelope authenticates the transition. Random
names, malformed envelopes, and candidates whose name/directory/local-identity
commitments disagree are reported but never become deletion authority.
`RemoveWindowsHousekeeping(directory, expected_directory_identity, attempt_id,
ordinal, expected_envelope_identity, optional_expected_payload_identity,
cancellation)` opens the exact committed source and derived inert names without
following links and requires the selected envelope and all supplied identities
to match. For `MovePending` or `MoveAmbiguous` it first completes and proves the
same identity's write-through move; only then does it best-effort unlink the
exact inert payload and envelope. It rechecks the retained directory and current
names and returns factual `None | CrashReappearancePossible | Visible`; it never
promises power-loss durability for final unlink. Because a proved inert GC name
is not operation authority, age and caller claims about an old process are
neither accepted nor required.

Normalization is integrated into the caller's advanced transaction or
high-level workflow and creates no external file. It MUST avoid quadratic work
for nested/overlapping input under a fixed budget. Direct input assigns a checked
monotonic arrival ordinal and applies records exactly in arrival order: a later
range overwrites only its own inclusive interval, and equal adjacent final values
coalesce. Value-free feed/retention input computes coverage union, coalescing
duplicates, overlap, and adjacency. A wrong-family endpoint, reversed range,
ordinal exhaustion, source error, sink error, or insufficient private-page
budget follows the whole-operation abort rule. Immediate source End is a legal
empty complete input.

Every potentially long operation accepts an explicit cancellation probe/token
with bounded documented engine-work checkpoints. This includes O(reader-capacity)
live opens and `CreateLive`, interruptible blocking lock acquisition,
normalization, lifecycle, import, validation, recovery, and snapshot
work. Cancellation cannot promise wall-clock interruption while an OS call such
as `fsync` is executing. The engine checks before each source/sink invocation;
once invoked, that callback's returned outcome wins until the next checkpoint.
Cancellation before mutation/publication returns typed `Cancelled`; after
pending mutation it runs whole-draft abort. The implementation checks once more
before every publication ambiguity boundary. After crossing such a boundary it
ignores an already-signaled token until the boundary is finished/resolved and
returns the factual `Committed`, `Published`, or `OutcomeUnknown` result with
cancellation only as a cause. It never abandons cleanup authority.

## 15. External live-reader sidecar

The canonical sidecar pathname is the exact database pathname plus `.readers`.
It is host-local coordination state, not part of the portable v4 main-file
bytes, and is never distributed with an immutable snapshot.

A live database is supported only on a local filesystem whose byte-range locks
are owned by an open file description or handle and are released automatically
when its last descriptor closes. Traditional process-associated POSIX
`F_SETLK` locks are forbidden. NFS, SMB, userspace filesystems, and platforms
without proven equivalent semantics return `Unsupported`.

The database directory must be controlled by the database owner. Main and
sidecar final components are opened without following symlinks. They must be
regular files with one link. Each handle retains the originally opened
descriptors and their local file identities. It never reopens a pathname for
content access, slot removal, or commit. Critical mutations recheck both
canonical paths against those retained identities and fail if either path was
removed, linked, or replaced.

Normal live open never creates, resizes, repairs, resets, or replaces a missing
or malformed sidecar. Explicit creation and offline transition operations are
the only writers of its header or length.

### 15.1 Exact sidecar layout

The exact sidecar length is checked as:

```text
4096 + capacity * 16
```

`capacity` is a nonzero caller-selected `u32`. The first 4,096 bytes are one
checksummed header page:

| Offset | Size | Field | Required value |
|---:|---:|---|---|
| 0 | 8 | magic | ASCII `IPRDRS4\0` |
| 8 | 2 | header size | `68` |
| 10 | 2 | slot size | `16` |
| 12 | 4 | state | `0=creating`, `1=ready` |
| 16 | 4 | capacity | nonzero |
| 20 | 12 | reserved | zero |
| 32 | 16 | database ID | exact main-file `database_id` |
| 48 | 16 | sidecar ID | random and nonzero |
| 64 | 4 | CRC-32C | complete page with this field zero |
| 68 | 4028 | reserved | zero |

Ordinary open accepts only state 1 and exact length. A zero, creating, unknown,
checksum-failed, identity-mismatched, or noncanonical header fails closed.
The random sidecar ID distinguishes replacement of the coordination inode
during one process lifetime. The retained local file identity remains the
authority for the currently opened inode.

Reader slot `i` is the 16-byte record at:

```text
offset = 4096 + i * 16
```

Its encoding is:

```text
selected_txn:u64le
bitwise_not_selected_txn:u64le
```

An all-zero slot is inactive. A locked active slot has nonzero
`selected_txn` and an exact bitwise complement. No PID, process-start token,
thread ID, claim nonce, transition state, or slot checksum exists. Slot
ownership is the lifetime byte-range lock, not the stale bytes.

### 15.2 Lock ranges and ownership

Every logical handle independently opens the main and sidecar. It does not
duplicate another handle's locking descriptor.

The protocol uses these one-byte advisory lock ranges:

| File | Offset | Purpose |
|---|---:|---|
| main | `2^44` | live-handle lifetime lock |
| sidecar | 0 | registration/publication gate |
| sidecar | 1 | single-writer lease |
| sidecar | reader slot offset | ownership of that reader slot |

Every live reader and writer holds the main lifetime range shared for its full
handle lifetime. Offline initialize/reset takes it exclusively. The writer
holds the sidecar writer range exclusively for its full handle lifetime. Each
reader holds its own slot range exclusively for its full handle lifetime.

The operating system releasing a slot or writer lock after process death is
the proof that its previous owner is gone. An available slot lock therefore
authorizes clearing or replacing stale bytes. An unavailable slot lock means a
reader is active; its record must be structurally valid. There is no PID
liveness inference and no persistent interrupted slot-transition state.

The gate is:

- exclusive while a live open scans the table and claims a lease or slot;
- shared while an established reader clears its slot;
- exclusive during commit and reclamation from their reader scan through
  metadata publication; and
- exclusive for offline coordination transitions.

This barrier prevents a writer from publishing between a reader's meta
selection and slot claim. Transaction-zero registrations are unnecessary.

Lock ranges may extend beyond end of file. Advisory locks do not authorize
ordinary reads or writes to those byte offsets. Slot bytes are volatile
coordination hints and are not synchronized for database durability; the held
lock and gate establish their authority and visibility.

### 15.3 Scanning, registration, and reclamation

A complete reader-table scan is `O(capacity)`. For every slot, the scanner
tries its exclusive slot lock:

- success proves no reader owns it; any stale bytes are cleared, then the lock
  is released;
- contention proves a reader owns it; the scanner reads the fixed record and
  requires a nonzero transaction, exact complement, and a transaction no newer
  than the selected main generation.

Malformed active state fails closed. The normal protocol never clears a slot
whose ownership lock is unavailable.

A live reader open:

1. opens the main without following the final component;
2. takes the shared main lifetime lock and verifies the retained path identity;
3. performs constant-time live bootstrap to obtain the database ID;
4. opens and validates the existing bound sidecar;
5. takes the gate exclusively;
6. rechecks both identities and the sidecar header;
7. reselects the current main generation and scans all slots;
8. locks one available slot and writes the selected transaction plus complement;
9. rechecks both identities and releases the gate; and
10. limits all reads to that pinned generation's committed page count.

If no slot is available, open returns `ReaderCapacityExhausted`. A reader
performs no full-file validation and no data-page checksum walk.

A reader slot at transaction `R` protects every page retired by transaction
`T` when `R < T`. Therefore a complete retirement transaction group is safe
to reclaim exactly when no active slot contains a transaction below its
`retired_by_txn`. Reclaim holds the gate exclusively from this scan through
its own commit.

A live writer open follows the same main/sidecar validation and full slot scan,
then claims the writer range. It returns `WriterBusy` when that range is
owned. After the claim, an aligned physical tail beyond the selected committed
length is unpublished growth and is truncated and synchronized before the
writer is returned. A short or unaligned file fails bootstrap.

### 15.4 Commit barrier

Ordinary data commit always retires every replaced committed page. It does not
make reader-dependent allocation decisions.

Commit preparation finishes before taking the gate. The writer then holds the
gate exclusively while it:

1. rechecks the retained main and sidecar path identities and ready header;
2. reselects and requires the unchanged committed base generation;
3. scans every reader slot for malformed active state;
4. synchronizes all private non-meta pages and file growth;
5. writes the complete alternate meta page;
6. synchronizes that meta page; and
7. releases the gate.

Existing readers continue through retained positional-read descriptors. New
readers cannot select a generation until publication finishes. A failure before
the first alternate-meta write is `NotCommitted`; a failure from that first
write until its successful synchronization is `OutcomeUnknown`; success is
`Committed`. Exact database ID, transaction ID, and commit nonce identify the
attempt.

### 15.5 Creation and offline transitions

`CreateLive(path, family, value_kind, value_tag, reader_capacity)` accepts no
writer budget, ranges, feeds, or metadata. It creates only the canonical empty
transaction-1 pair and returns no writer handle.

Creation ordering is sidecar first:

1. exclusively create the canonical sidecar in state 0, size it, write its
   complete header, synchronize it, and synchronize the parent directory;
2. exclusively create the canonical main, write identical empty transaction-1
   meta pages, synchronize it, and synchronize the parent directory;
3. rewrite and synchronize the sidecar header as state 1.

Thus every crash-left intermediate state blocks immutable open and fails live
open. `CreateResult` reports the database ID, creation commit nonce, reader
capacity, `NotCreated | Created | OutcomeUnknown`, whether residue may remain,
and an optional typed cause. Known-owned failure cleanup removes the main and
synchronizes that removal before removing the sidecar.

`InitializeLive` is an explicit offline conversion of an existing immutable
main. The caller must first prove no unregistered immutable reader can remain.
The operation takes the exclusive main lifetime lock, rechecks immutable
identity and exact length, creates a state-0 sidecar bound to its database ID,
and publishes state 1 only after all checks and synchronization succeed.

`ResetLiveCoordination(path, capacity, RollbackSafe | DiscardPrevious,
cancellation)` is an explicit offline repair for a missing or corrupt sidecar.
The caller must certify quiescence. It takes the exclusive main
lifetime lock and must prove that no conforming live handle remains. It
exclusively creates the deterministic private name
`<main-basename>.readers.reset`, prepares and synchronizes one complete ready
sidecar there, then installs it atomically:

- when canonical `.readers` is absent, no-replace rename is required;
- when canonical `.readers` exists and policy is `RollbackSafe`, an atomic
  exchange is required. The inode moved to the private name MUST equal the
  previously retained canonical identity. A mismatch is atomically exchanged
  back before conflict is returned; a failed rollback is `OutcomeUnknown`;
- when canonical `.readers` exists and policy is `DiscardPrevious`, an atomic
  destructive replacement is permitted after the old identity and complete new
  sidecar are rechecked. The old inode need not retain a name after the new
  inode wins.

After successful exchange, the canonical identity/header and unchanged main
are synchronized and rechecked before the exact old inode is removed from the
private name. Failure to remove that old inode leaves the reset factually
`Initialized` with cleanup residue. The deterministic private name is
SDK-reserved and makes an interrupted reset discoverable without guessing
random directory entries. After successful `DiscardPrevious`, resolution may
only complete the installed sidecar; rollback is unavailable. No platform
silently changes `RollbackSafe` into `DiscardPrevious`, and a platform without
exchange rejects a strict reset before creating the private sidecar.

`ResolveInterruptedLiveTransition(path, Complete | Rollback, cancellation)` is
the restart entry point when the original in-memory result was lost. It performs
only main bootstrap and sidecar checks, never full validation:

- a canonical state-0 sidecar plus an exact-length, two-meta-proven main with
  the same database ID may be synchronized and advanced to ready. This is safe
  for either interrupted create or initialize;
- a canonical sidecar without a main may be removed in `Rollback`; a present
  valid main is never removed without the original attempt result;
- a ready deterministic reset sidecar may be installed only when canonical
  coordination is absent; it never overwrites an unproved later canonical
  inode; and
- a private reset residue beside an already ready, matching canonical sidecar
  may be removed exactly.

Malformed or foreign state fails closed. The result-specific create and
transition resolvers remain the stronger authority when their exact result was
retained. Ordinary open never performs any transition or recovery.

Direct live rename/relink is unsupported in Phase 1. Relocation uses a compact
immutable snapshot, explicit initialization at the destination, and an
application-controlled switch.

### 15.6 Handle lifetime, fork, and access policy

Reader point lookups and independent cursors need no per-lookup mutex or active
counter. The caller must not race handle close with another method. A cursor
borrows its reader, so Rust prevents closing the parent first. Mutable writer
methods and each cursor are caller-serialized.

Every live handle caches its creator process ID. A forked copy rejects public
operations with `ForkedHandle`. Its destructor only closes the child's
descriptors. It never clears a slot, unlocks explicitly, truncates, aborts,
commits, or changes a namespace. The parent remains the owner of the inherited
open-description locks.

Explicit reader close takes the gate shared, clears its slot, and releases the
slot lock. Explicit healthy writer close aborts unpublished growth before
releasing its descriptors. Automatic destructors perform no file or namespace
I/O. Dropping a reader without explicit close is still safe: descriptor close
releases the slot lock, and the next exclusive scan clears its stale bytes.

Engine-created main and sidecar files use creator-only access. POSIX mode is
exactly `0600`, independent of umask; Windows uses a protected descriptor for
the effective user. Opens never silently change existing access. Every
descriptor is close-on-exec or non-inheritable.

For POSIX creation-security kind 1, the engine removes an inherited extended
access ACL, applies mode `0600`, and verifies the retained regular inode is
owned by the attempt-start effective UID with that exact mode. Filesystems that
cannot prove this fail with `AccessPolicyUnsupported`. The 32-byte commitment
is:

```text
SHA-256("IPR4PSEC" || effective_uid:u32le || 0600:u32le)
```

For Windows creation-security kind 2, the engine captures the effective token
at attempt start: the thread impersonation token when present, otherwise the
process token. It creates each artifact with a protected, non-inheriting DACL
owned by and containing exactly one allow ACE for that token's user SID. The
ACE grants exactly `FILE_ALL_ACCESS`; no other ACE is present. Its commitment
is:

```text
SHA-256(
    "IPR4PSEC" ||
    sid_len:u32le ||
    exact_sid_bytes ||
    FILE_ALL_ACCESS:u32le ||
    SE_DACL_PROTECTED:u16le
)
```

Windows local-identity kind 2 is the exact 32-byte value
`volume_serial_number:u64le || file_id:[16]byte || zero:[8]byte` returned from
one retained handle by `GetFileInformationByHandleEx(FileIdInfo)`. Link count,
regular-file type, and reparse-point state are checked separately and are not
identity bytes.

The same attempt-start commitment is recorded for every separately created
inode in that attempt. A resolver recomputes it from each retained inode's
current owner, mode, and absent extended access ACL. A mismatch reports
`ChangedOrUnproven`; it never silently changes existing access.

Linux and macOS use native open-file-description byte-range locks. Windows uses
equivalent per-handle byte-range locks. FreeBSD and any other target remain
unsupported until an equivalent automatic-release primitive is proven and
tested; implementations must not substitute process-associated record locks.

Phase-1 constructors are explicit:

- `OpenImmutableReader(path)` requires sidecar absence and exact committed
  main length;
- `OpenLiveReader(path)` requires and registers through the existing sidecar;
- `OpenLiveWriter(path, transaction_budget)` claims the writer lease.

None performs general validation, auto-detects mode, or implicitly creates,
repairs, initializes, relocates, or resets coordination.
## 16. Public semantic operations

### 16.1 Common operation model and direct transactions

Direct values are opaque `u32` values. The engine compares them only for range
coalescing. Value zero is valid. In the advanced direct layer the value is
caller-semantic application data; it is never an SDK storage identifier.

The retention API is available only when `value_kind == direct` and
`value_tag == retention`. Any other combination returns a typed kind/tag
mismatch before mutation.

A live writer owns the lease and permits at most one active advanced transaction
or high-level workflow. Advanced APIs expose logical mutations only; pages,
roots, COW paths, membership IDs, dictionary hashes/refcounts, allocator/
retirement state, and meta publication are never public. Feed indexes and
canonical bitmap words are read-only observations tied to one pinned reader
generation. Callers may enumerate or cache them for that reader, but cannot
assign them, persist them as cross-file identity, or pass them back as mutation
authority.

Every advanced/high-level `Begin` takes and stores one explicit cancellation
probe/token for that operation. Repeated range batches, normalization,
`FinishInput`, import, metadata compression, and Commit use the same token and
the common checkpoints in section 14.4. A clean-writer lifecycle call that begins
and finishes its private draft in one call takes the token directly. Cancellation
is never inherited from an earlier writer-open call.

An advanced direct transaction accepts ordered calls that assign
`(from,to,value:u32)` batches or clear `(from,to)` batches. Records inside and
across batches are applied exactly in arrival order. A later assignment
overwrites only its inclusive interval; earlier values remain on uncovered
sides. Clear removes only its interval. Reversed/wrong-family ranges fail under
the transaction atomicity rule. The SDK performs splitting, canonical adjacent
coalescing, normalization, COW allocation, and publication. It creates no
external scratch.

An operation may stage its one metadata Set/Clear after range work and then
`Commit` or `Abort`. Commit/Abort invalidates every operation-bound child.
High-level `FinishInput` returns the common terminal semantic report before
commit so a caller may derive and stage JSON describing the exact same result.
When it reports `Changed`, the draft remains active for that optional metadata
stage and Commit/Abort. When it reports `NoChange`, the workflow itself is
terminal and follows the exact clean-state rule below; metadata can then begin
only a separate metadata-only transaction. All address counts use
`Cardinality129`.

Every successful high-level `FinishInput` returns this fixed semantic schema:

```text
workflow: CreateFeed | ReplaceFeed | DirectReplacement |
          RetentionRefresh | MembershipImport
logical_change: Changed | NoChange
input_record_count:u64
input_normalized_interval_count:u64
before_range_record_count:u64
after_range_record_count:u64
input_addresses:Cardinality129
before_addresses:Cardinality129
after_addresses:Cardinality129
unchanged_value_addresses:Cardinality129
changed_value_addresses:Cardinality129
added_addresses:Cardinality129
removed_addresses:Cardinality129
source_feed_count:u64
matched_feed_count:u64
created_feed_count:u64
source_distinct_membership_count:u64
translated_membership_count:u64
```

All record/feed/membership counters are checked and overflow is typed
`ArithmeticOverflow`. Address classes are disjoint over the union of before/after presence:
`before = unchanged_value + changed_value + removed` and
`after = unchanged_value + changed_value + added`. For one-feed workflows the
semantic value is membership in that named feed, so `changed_value_addresses` is
zero; the address fields and before/after record counts describe the coalesced
projection of only that feed. For direct replacement and retention they describe
the complete direct map and compare exact `u32` values; record counts are
canonical direct-tree records. For membership import they describe the complete
destination membership map and compare resolved named-feed sets, never internal
IDs; record counts are canonical membership-tree records.

`input_record_count` counts accepted source records before normalization across
all `AddRanges` sources. Import has no `AddRanges`, so it counts canonical source
address-tree records read. `input_normalized_interval_count` counts canonical
intervals after normalizing the supplied logical snapshot but before comparing
or merging it with committed destination state; for import the already-canonical
source tree makes it equal to `input_record_count`. `input_addresses` is the
union coverage represented by that complete normalized snapshot or source file.
Import alone uses the five feed/membership counters; other workflows set them to
zero. `source_distinct_membership_count` is the source dictionary's live nonzero
membership count. `translated_membership_count` is the number of distinct
nonempty destination feed-name sets produced after translating those source
memberships, after deduplication; several source memberships may therefore count
once. `source_feed_count` counts every source catalog entry, including an empty
feed. `matched_feed_count` counts those names present in the destination base
generation; `created_feed_count` counts the absent names created by import, also
including empty feeds. Therefore `source_feed_count == matched_feed_count +
created_feed_count`. Physical page, byte, and allocation counters are deliberately not part of
semantic workflow statistics.

### 16.2 Membership values and handles

Public membership mutation MUST NOT accept a caller-supplied bit index or
membership ID.

Readers expose feed enumeration in strictly ascending feed-index order and exact
name lookup as copied `{name,index}` entries. An index is observable for
interpreting a membership in that pinned reader but is never accepted as
mutation authority. A separate opaque reader `FeedRef` is not part of the
mandatory Phase-1 surface; native bindings may provide one only as a convenience
over the same copied entry and reader lifetime.

Membership reads are lazy. A `MembershipView` exposes checked `word_count`,
random `Word(i)` access, and caller-buffer batched word reads without
materializing the bitmap. An optional native copy/materialize helper first
checks the exact byte length against a caller limit and host address space.
Public APIs never require a legal maximum-width bitmap to become one heap
allocation. Persistent views borrow the parent reader; parent Close returns
`HandleBusy` while a child exists.

An advanced membership transaction may ensure/create, look up, enumerate,
rename, and delete feeds through SDK-issued `FeedRef` values. It may change
several feeds atomically. `EnsureFeed(name)` returns the existing exact-name feed
or privately creates it at the lowest free index. Exact lookup never creates.
Rename requires the destination name absent and preserves the index/membership.
Delete removes the catalog entry, clears that feed from every membership, and
makes the index reusable only after commit.

The SDK provides an incremental, bounded builder that combines one or more
transaction-bound `FeedRef` values into an opaque operation-bound
`MembershipRef`. Empty construction denotes absence. The caller may cache and
move those references only while the owning transaction/catalog incarnation
remains valid. The SDK exclusively builds, canonicalizes, interns, deduplicates,
stores, translates, reference-counts, and reclaims every bitmap combination.

For each supplied range, `ApplyMembershipRanges(M, operation, source)` applies
one destination-operation-bound membership `M` to the current membership `S`:

```text
Replace       M
Union         S union M
Difference    S minus M
Intersection  S intersect M
Xor           S symmetric-difference M
```

The rule applies per address. Empty result removes coverage. Inputs are applied
in call/record order; the SDK performs overlap splitting, bitmap algebra,
canonical interning/refcounts, and adjacent coalescing directly in private COW
state. A stale, foreign, removed, reused, committed, aborted, or reopened
`FeedRef`/`MembershipRef` fails before mutation. Commit or abort invalidates all
transaction references even if the same name/index exists later.

Setting an empty membership deletes the covered range. Clearing its final bit
also deletes it. Interning empty returns ID zero without allocating.
The range map cannot represent “examined but belongs to no feed”; applications
needing that provenance store it in the opaque file-level metadata.

### 16.3 High-level exact feed workflow

Each high-level lifecycle operation requires a clean writer and owns one private
draft:

- `BeginCreateFeed(name)` requires an absent name and privately allocates the
  lowest free feed index.
- `BeginReplaceFeed(name)` requires an existing name and preserves its index.
- Both then accept repeated value-free `AddRanges` batches, `FinishInput`, an
  optional one metadata Set/Clear, and `Commit` or `Abort`. `FinishInput`
  declares that the complete desired feed snapshot has arrived and produces the
  final normalized private root plus terminal statistics.
- `DeleteFeed(name)` requires an existing name, clears its bit from all
  memberships, removes its entry, and frees its index.
- `RenameFeed(old, new)` requires old present and new absent, preserves the
  index and every membership, and changes only the catalog name.

Immediate source End is a valid empty create/replace and leaves an active
cataloged feed with zero ranges. `AddRanges` accepts unordered, duplicate,
overlapping, and adjacent ranges and normalizes them directly in the destination
draft without external scratch. There is no upsert and no alias.

`FinishInput` for replace returns `NoChange` when the complete resulting membership is
logically identical to the committed feed. It cleans any private preparation
artifacts, terminates the workflow, releases its cancellation/source ownership,
invalidates its children, and leaves the writer clean; no later Commit or Abort
belongs to that workflow. The caller may then start a separate metadata-only
transaction, whose Commit is required only if metadata changes. The engine does
not publish a transaction merely to reproduce the same catalog/ranges.
`CreateFeed`, `DeleteFeed`, and
`RenameFeed` necessarily change a catalog entry after their preconditions pass.

One high-level workflow contains at most one feed lifecycle operation. It MAY also
stage the opaque metadata describing that same change. It MUST NOT stage a
second lifecycle operation. Rename likewise MAY share its transaction with one
metadata replacement. Once a lifecycle draft succeeds, the only permitted
writer actions before publication are that one metadata stage, `Commit`, or
`Abort`; incremental range mutation cannot be mixed into the lifecycle draft.

A feed index cleared by committed `DeleteFeed` is eligible for the lowest-free
choice of a later committed `CreateFeed`. The one-lifecycle rule means public
APIs do not delete one feed and create another in the same private transaction.

Input endpoints MUST be valid for the file family and free of reversed ranges;
ordering, duplicates, and overlap are deliberately accepted. Any source,
normalization, mutation, cancellation, or cleanup error follows whole-draft
abort rules. The high-level one-feed restriction does not limit an advanced
membership transaction, which may intentionally change multiple feeds.

### 16.4 High-level direct replacement and retention refresh

`BeginDirectReplacement` requires a clean direct writer and owns one private
draft. Repeated `AddRanges` batches carry `(from,to,value:u32)` records;
`FinishInput` declares the complete desired snapshot and replaces the complete
direct map. Input may be unordered, duplicated, and overlapping. Records apply
exactly in arrival order across batches, with later records overwriting only
their own intervals. The operation normalizes directly in destination-private
COW pages and creates no external sorting file.

`BeginRetentionRefresh(tN)` is available only for the exact `retention` tag,
requires a clean writer, and accepts repeated value-free `AddRanges` batches.
`FinishInput` unions that unordered complete desired address set and merge-joins
it with the complete committed map:

- addresses in both retain their old value exactly;
- new-only addresses receive `tN`; and
- old-only addresses are deleted.

The rule applies per address and splits partial overlaps. A removed address
that later reappears is new and receives the later value. No refresh changes
the timestamp of an address continuously present across both snapshots.

Direct replacement and retention refresh return `NoChange` when their
complete logical result equals the committed direct map. They terminate exactly
like the no-change feed replacement above: clean private preparation artifacts,
release operation ownership, invalidate children, leave the writer clean, and
require no Commit or Abort. A later metadata Set starts an independent
metadata-only transaction. They do not publish a data transaction.
The equality result is derived during the required merge walk; it does not add a
second pass. After any exact no-op, the caller may still stage metadata normally;
changed metadata alone then starts and publishes the metadata transaction.

Normal replacement/retention has no best-effort flag. A read error cannot be interpreted
as intentional deletion.

After direct replacement or retention `FinishInput` succeeds, the same private
draft MAY stage exactly one `SetMetadataJSON` or `ClearMetadataJSON`. After that
workflow, the only legal writer actions are that one optional metadata
stage, `Commit`, or `Abort`; advanced mutation, a second metadata stage, another
exact operation, and feed operations are rejected before mutation. Failure of
the metadata stage follows the whole-draft abort rule. This publishes the exact
data refresh and its publisher metadata in one generation, matching the feed-
lifecycle rule rather than requiring a second commit.

### 16.5 High-level membership import

`BeginMembershipImport(source_reader, cancellation)` requires a clean destination
membership writer and an already pinned membership Reader with compatible
address family and value tag. The Reader may be explicitly immutable or live;
there is no import-time mode auto-detection. The workflow borrows that Reader,
so Reader Close returns `HandleBusy` until import `FinishInput` succeeds or the
whole workflow aborts. Source and destination exact local main inodes must differ;
a copied file with the same logical database ID but a different local inode is
legal and still translates by names.

Import accepts no `AddRanges`. Its `FinishInput` performs the complete source
enumeration/merge, returns the common statistics above, and releases the source
borrow before metadata staging or destination Commit. If the logical result is
`NoChange`, it also terminates and cleans the workflow under the common no-change
rule; any later metadata Set starts a separate metadata-only transaction. It owns one private
destination draft. The SDK enumerates the source catalog once,
maps exact source names to destination feeds, creates missing destination feeds,
and preserves destination-only feeds. An exact name match is one global logical
feed, so imported source coverage is unioned into the existing destination
membership for that name.

The engine translates each distinct source membership to a destination-owned
`MembershipRef`, caches translations and resulting unions under the operation
budget, and merge-walks source and destination address trees directly into the
private final root. If bounded heap is insufficient, the translation/aggregation
index uses unpublished destination pages. Source feed indexes and membership IDs
never cross the file boundary and the caller handles neither. No per-feed
expansion, physical temporary merged-feed file, or external sorting file is
created.

Destination JSON remains unchanged unless the caller uses the one explicit
metadata Set/Clear stage after successful import preparation. A source read,
translation, budget, cancellation, or storage failure aborts the complete import
and cannot be published by a later commit. `FinishInput` returns exact import
statistics before optional metadata and Commit/Abort. Exact whole-file copy is
`SnapshotTo`, not import. Phase 1 provides no generic direct-value import into an
existing direct map because conflicting opaque `u32` values have no natural
merge; advanced direct assignment or high-level direct replacement is explicit.

### 16.6 Primitive queries and high-level feasibility boundary

Point lookup returns the direct value or lazy membership view from the pinned
generation. Forward/backward cursors and bounded-range scans yield borrowed
records or write into caller storage; warmed movement allocates nothing.

Phase 1 also exposes feed-catalog enumeration and exact name lookup plus an
ordered cursor for one named feed. These are format-facing primitives. Phase 1
does not implement or freeze the detailed multi-file union, intersection,
exclusion, comparison, equality, overlap, or counting API.

The preliminary Phase-2 feasibility contract is:

- a set-producing result is always a materialized/published v4 file, never an
  in-memory feed object;
- merge/union, intersection, and exclusion produce a v4 result plus terminal
  statistics, while analytical operations may return counters only when no
  useful result set exists;
- a result may preserve global named feeds or flatten coverage into one
  caller-named feed; and
- equal feed names across supplied files identify one global logical feed.
  Implementations aggregate its ordered source views virtually and do not first
  write a physical temporary combined feed. Source feed indexes remain local to
  their pinned catalogs and have no cross-file identity.

The catalog, ordered per-feed cursors, ordinary v4 writer/publication, exact
`Cardinality129`, and explicit resource budgets MUST make this direction
implementable without an operation-specific page type, persisted derived
statistics, or another binary format. Exact high-level signatures, result
statistics, feed projection, direct-value behavior, algebra-specific batching,
and error precedence are Phase-2 decisions tracked outside this specification's
Phase-1 implementation gate. The common Phase-1 cancellation and batched sink
contracts still apply.

For update-ipsets, download/parse work MAY run concurrently, but one
writer serializes and commits one `CreateFeed` or `ReplaceFeed` generation at a
time. A failed replacement leaves that feed's earlier committed generation in
place and does not roll back unrelated feed commits already published. Aggregate
comparisons use a reader pinned after the chosen feed commits finish.

## 17. Exact 129-bit cardinality

Every public address-cardinality API returns a fixed unsigned 129-bit value:

```text
bit128:u8  hi:u64  lo:u64
```

`bit128` is zero or one. The numeric value is
`bit128 * 2^128 + hi * 2^64 + lo`. This represents a full IPv6 space and a
combined IPv4-plus-IPv6 total without heap big integers.

Addition is checked against `2^129-1`. Conversion to `u64` or `u128` is checked
and returns typed overflow; it never wraps or saturates. Text and conformance
data use exact unsigned decimal strings, including
`340282366920938463463374607431768211456` for `2^128`.

The C ABI generation-1 layout is exactly `{ bit128:u8, reserved:[7]u8, hi:u64,
lo:u64 }`, with offsets 0/8/16, alignment 8, total size 24, and every reserved
byte zero, as frozen by [`c-abi-v4.md`](c-abi-v4.md).

## 18. Explicit validation

`Validate` is intentional, non-mutating, and has three explicit modes:

- `LiveCurrent` opens the exact bound sidecar and takes the live operation lock
  exclusively. While that gate excludes publication, it scans the table,
  selects the proven current generation, and directly claims a reader slot for
  that exact nonzero transaction before the full graph scan.
- `ImmutableCurrent` holds the shared main-file lifetime lock, requires sidecar
  absence, and rechecks canonical path identity and sidecar absence before and
  after scanning.
- `OfflineCandidate` requires caller-certified quiescence, holds the exclusive
  lifetime lock, and binds an exact recovery-candidate token. It may validate a
  selected previous or generation-unordered candidate without claiming that it
  is current.

When a generation is selected, validation checks only the graph and allocation
partition owned by that meta. Pages reachable only from the alternate meta are
not a second current root set; when protected they appear through the selected
generation's retirement state.

When bootstrap is inspectable but no generation can be selected,
`ImmutableCurrent` or an appropriate offline-current inspection returns a
completed bootstrap-only invalid report: generation and roots are absent, the
graph is explicitly untraversable, and unknown coverage is explicit.
`LiveCurrent` may return that report only while trustworthy static main/sidecar
OS identities bind the sidecar and it holds the operation lock continuously
through inspection. It publishes no reader slot and never claims that an
unselectable generation was pinned. Ambiguous live identity/current-
generation binding or inability to acquire the required coordination is an
operational error, not a corruption finding.

For a selected generation, validation checks at minimum:

- exact bootstrap and meta rules;
- every reachable page's type, normalized CRC, bounds, reserved bytes, level,
  and ownership;
- cycles and page aliasing;
- range-tree fences, ordering, intervals, global non-overlap, and coalescing;
- catalog name rules, bijection, counts, used bits, limits, and active bits;
- membership canonicality, hashes, reverse index, used IDs, active feed bits,
  and independently recomputed record refcounts;
- blob and metadata graph length, order, cycles, CRCs, zlib framing, checksum,
  output length, and metadata limits;
- free/reachable/retired page partition and bitmap summaries; and
- retirement ordering, canonical extents, uniqueness, and counts.

Validation MUST use checked arithmetic and caller-bounded memory. Under explicit
nonzero external-scratch authority it MAY use bounded scratch to prove global
ownership, refcounts, and overlap. With zero scratch authority it completes in
the heap budget or returns `InsufficientResourceBudget`. It MUST NOT allocate
memory proportional to file size without an explicit caller-provided budget.

`Validate(LiveCurrent | ImmutableCurrent | OfflineCandidate(token),
resource_budget, cancellation, finding_sink)` is a detailed continued-scan
operation, not a fail-fast boolean. Each content defect is emitted once in
deterministic selected-root order and first-encounter page/key order as a
`ValidationFinding` containing a monotonic sequence, stable reason code, owning
graph/object kind, optional physical page/byte interval, optional related page,
and only independently trusted logical key/address bounds. Its stable reason
codes are:

```text
META_UNAVAILABLE
META_INVALID
META_STATIC_MISMATCH
FILE_GEOMETRY_INVALID
ROOT_COUNT_INVALID
IO_ERROR
ARITHMETIC_OVERFLOW
PAGE_OUT_OF_BOUNDS
PAGE_HEADER_INVALID
PAGE_CRC_MISMATCH
PAGE_TYPE_MISMATCH
PAGE_BORN_TXN_INVALID
PAGE_RESERVED_NONZERO
TREE_CYCLE
PAGE_ALIAS
TREE_LEVEL_INVALID
TREE_ORDER_INVALID
TREE_FENCE_INVALID
RANGE_REVERSED
RANGE_OVERLAP
RANGE_NOT_COALESCED
CATALOG_NAME_INVALID
CATALOG_BIJECTION_INVALID
CATALOG_BITMAP_INVALID
MEMBERSHIP_BITMAP_INVALID
MEMBERSHIP_HASH_INVALID
MEMBERSHIP_REVERSE_INDEX_INVALID
MEMBERSHIP_REFCOUNT_INVALID
MEMBERSHIP_ACTIVE_FEED_INVALID
BLOB_INVALID
METADATA_ZLIB_INVALID
METADATA_LENGTH_INVALID
BITMAP_SUMMARY_INVALID
ALLOCATION_PARTITION_INVALID
RETIREMENT_ORDER_INVALID
RETIREMENT_LIST_INVALID
```

The stable graph/object kinds, shared with recovery unknown envelopes, are
`FileGeometry`, `Meta`, `RangeTree`, `CatalogNameTree`, `CatalogIndexTree`,
`MembershipDictionary`, `MembershipReverseIndex`, `MembershipBlob`, `Metadata`,
`FreeBitmap`, `FeedUsedBitmap`, `MembershipUsedBitmap`, `RetirementTree`, and
`RetirementBlob`. A finding uses the narrowest independently known owner; it
never invents a logical owner from corrupt bytes.

An invalid header, checksum, pointer, fence, or length is never trusted to
discover more pages. Validation records that subtree as untraversable, does not
follow its untrusted pointers, and continues every independently reachable root,
sibling, and object. Localized source-page I/O failure is an `IO_ERROR` finding
with the same continuation rule. It does not guess hidden records or turn an
unreadable subtree into valid absence.

A completed `ValidationResult` identifies the retained file and, when selected,
generation/roots. A bootstrap-only report marks generation/roots absent. It
contains `valid`, checked unique pages and per-object examined counts, total and
per-reason finding counts, untraversable-subgraph count, trusted bounded-possible
address span, and `has_unbounded_unknown`. `valid` is true exactly when the full
selected graph/partition scan completed with zero findings. Invalid content is
a factual successful report, not a generic operation error.

Failure to open/pin the requested mode, acquire resources, create/read cleanup
scratch, deliver a finding to the sink, or finish coordination cleanup is an
operational failure reported separately with its partial counters, exact cause,
artifact ledger, and common coordination field; it never returns `valid=true`.
A sink that intentionally stops makes validation incomplete. Go, Rust, and the C
ABI expose the same reason codes and streamed ownership/lifetime rules.

Normal open, lookup, scan, advanced mutation, high-level workflow, and commit do not invoke
`Validate` implicitly. Plausible corruption can therefore produce an incorrect
value or miss when the caller intentionally skips validation. Memory-safety
checks remain mandatory in all paths.

Successful validation identifies the retained file identity, database ID,
selected transaction, commit nonce, and all selected roots and proves that the
complete selected source graph and allocation partition were intentionally
validated. Normal operations do not consume or imply this proof.

## 19. Recovery

Recovery is separate from normal replacement/import. It is non-mutating with
respect to the source and writes only to a new absent destination. Its private
output inode is the prospective final database, not sorting scratch. Recovery
supports only `FailIfExists`: both destination main and sidecar MUST be absent,
and it never overwrites forensic evidence or an earlier output.

Recovery independently applies a weaker **recovery-readable meta**
classification to both retained meta pages. The complete meta page, identity,
static fields, CRC, transaction, declared page count, root numbers, counts, and
internal arithmetic MUST be valid, but physical file alignment,
declared-length availability, and host mapping-size checks may fail. Recovery
uses checked windowed I/O rather than trusting a hostile mapping length. A root
beyond the complete physical pages becomes reported unknown coverage rather
than invalidating an otherwise trustworthy meta. The two generations are never
merged. If no meta is recovery-readable, candidate inspection returns a
successful structured diagnostic with zero candidates, `META_UNAVAILABLE`, and
`has_unbounded_unknown == true`; no recovery-output call is possible and no
database is produced. Roots are never guessed and unreachable pages are never
scanned as live data.

Live recovery requires a separate **current-generation proof**. A meta is
**generation-order-readable** when its complete page, static identity, reserved
bytes, meta CRC, nonzero transaction, and nonzero commit nonce are valid. Root,
count, declared-length, and physical-availability failures do not prevent this
narrow classification. Current-generation order is proven only when both
physical metas are generation-order-readable, have the same static identity,
and either:

- their transactions differ by exactly one, the higher transaction is stored at
  physical meta page `txn_id & 1`, and the lower transaction is at the other
  page, in which case the higher transaction is current; or
- their transactions are equal and bytes `[0,256)` are identical, in which case
  they describe one generation and meta page `txn_id & 1` is the deterministic
  candidate token.

Any other state—including an adjacent pair in swapped physical pages—leaves
current-generation order unprovable. In particular, a
sole recovery-readable meta is not promoted to current merely because the other
meta is damaged: the damaged page may have held a later committed generation.

When order is proven, a recovery-readable current meta is labeled `Newest` and
a distinct recovery-readable lower meta is labeled `Previous`. If the proven
current meta is not recovery-readable, an independently recovery-readable lower
meta remains `Previous`; it is never relabeled `Newest`. When order is not
provable, immutable/offline inspection may expose independently
recovery-readable pages as `UnorderedMeta0` or `UnorderedMeta1`. An unordered
candidate has no default and is never eligible for live recovery.

A recovery-candidate token contains its exact label and meta page number, source
identity kind and local identity, database ID, transaction ID, and commit nonce.
Inspection returns `Newest` then `Previous` when those labels exist, otherwise
physical unordered-page order, and changes nothing. Recovery reopens and
reclassifies both metas and requires every token field and classification to
match before scanning; a stale or replaced token is
`RecoveryCandidateChanged`. `Newest` is the default only when it exists. Every
other case requires explicit selection. Each selected candidate produces its
own output and report.

`InspectRecoveryCandidates(source, mode, resource_budget, cancellation)` returns candidate tokens plus that
bootstrap/recovery diagnostic summary. It uses checked windowed I/O and the same
initial lifetime-lock and coordination preconditions as the requested recovery
mode. `Live` returns exactly the proven, recovery-readable `Newest`; if current
order cannot be proved it returns typed
`LiveRecoveryCurrentGenerationUnprovable`, and if the proven current meta is not
recovery-readable it returns typed `LiveRecoveryCurrentGenerationUnreadable`.
`Immutable` and caller-certified `Offline` may return every independently
recovery-readable labeled or unordered candidate. Inspection releases its locks
after producing the bound tokens, so recovery always performs the complete
recheck above.

A newly claimed live-reader slot cannot retroactively protect pages referenced
only by an older retained meta: a later committed generation may already have
marked them free or overwritten them. Therefore `RecoverLive` accepts only a
current-generation-proven `Newest` token and rejects `Previous` and unordered
selectors. All other candidates are available only through an immutable copy or
caller-certified `RecoverOffline` quiescence for the complete scan. It may
recover less because prior reuse is reported as damage, but no concurrent writer
may destroy further remnants during that scan.

A damaged live source cannot use the strict normal-reader bootstrap. The
explicit `RecoverLive` path first obtains the proven-current recovery-readable
identity through
checked windowed I/O, takes the shared main-file lifetime lock, strictly opens
the existing sidecar bound to that database and local inode, and takes the
operation lock. It scans the complete table, reproves the current generation,
reselects that exact meta from the retained main descriptor, and directly
claims a slot for that exact nonzero transaction before releasing the operation lock.
The retained slot protects the recovery scan. At that point recovery caches the
complete selected tuple and every root/count used by the scan. Later commits may
legally overwrite the physical meta page after a second publication, so a live
final check MUST NOT require that old physical meta to remain selectable. A
missing, malformed, mismatched, or unsupported sidecar returns typed
`LiveRecoveryCoordinationUnavailable`; it is never repaired or bypassed.

`RecoverOffline` is a separate caller-certified mode for a copied or quiescent
source. It takes the exclusive main-file lifetime lock and requires the caller
to establish that no live or immutable handle can exist; it does not infer
offline safety from a damaged sidecar. `RecoverImmutable` is the ordinary path
for a genuinely immutable source: it requires no sidecar, holds a shared
lifetime lock for the scan, and rechecks canonical path identity and sidecar
absence immediately after lock acquisition before scanning and again afterward.
These two modes are eligible for an older retained-meta
candidate; `RecoverLive` is not. Source and destination identities MUST differ,
and recovery refuses an existing live destination or destination sidecar.

The public recovery entry points are:

```text
RecoverLive(source_path, newest_candidate, destination_path,
            recovery_resource_budget, unknown_sink, cancellation)
RecoverImmutable(source_path, candidate, destination_path,
                 recovery_resource_budget, unknown_sink, cancellation)
RecoverOffline(source_path, candidate, destination_path,
               quiescence_certification, recovery_resource_budget,
               unknown_sink, cancellation)
```

All three have the fixed `FailIfExists` destination policy and return one
terminal recovery report plus the section-20 publication result. The candidate,
source mode, budget, sink, and cancellation semantics are explicit; there is no
auto-detection, implicit validation, or replacement overload.

Recovery keeps its source registration/lifetime protection through the complete
scan, private-output construction, and final source checks. For live recovery,
the final check takes the operation lock and verifies the retained
directory/main/sidecar identities, database ID, link counts, and exact active
slot owner/nonce/transaction, and confirms that the cached tuple/roots are the
ones used for the scan. It does not reclassify or reselect physical metas. For
immutable/offline recovery, the held lifetime/offline exclusion makes the exact
candidate meta stable, so the final check rereads and matches it. Once the
private output is self-contained, recovery releases that registration or
lifetime lock before acquiring any blocking destination lifetime lock or
namespace reservation. It performs no later source access. This ordering is
mandatory even when source and destination paths are distinct, so concurrent
opposite-direction recoveries or recovery/snapshot replacements cannot deadlock.

Recovery verifies page CRCs and copies only independently verifiable reachable
records and metadata. It MUST NOT use a checksum-failed page, guess child
pointers, scan unreachable pages for candidate records, or treat unreadable
input as an intentional deletion.

A recovery output:

- has a new random nonzero database ID;
- starts at transaction 1;
- has a new random nonzero commit nonce;
- writes `born_txn == 1` on every rebuilt non-meta page;
- preserves the source address family, value kind, and value tag;
- preserves every accepted feed's exact `(name,index)` pair and the selected
  meta's valid non-shrinking `feed_index_limit` high-water mark;
- contains only verified logical state;
- has canonical empty free and retirement state; and
- has no sidecar.

For membership recovery, the catalog name and numeric trees are redundant
indexes. Independently verified equal pairs are deduplicated; a pair verified
from either tree MUST be rebuilt when it has no name or index conflict. Conflicts
are rejected. Rejected feeds leave their indexes free below the preserved
high-water mark; they do not cause accepted indexes to be renumbered.
Memberships are rebuilt from only accepted active catalog bits, with empty
results deleted canonically. The membership-ID tree is authoritative for bitmap bytes. The
hash tree, used bitmaps, and stored range-record refcounts are derived indexes:
recovery rebuilds them from accepted catalog entries, dictionary records, and
accepted ranges rather than requiring damaged copies to agree. A range is
copyable only when its complete membership and every referenced active catalog
entry verify.

Globally overlapping readable records are grouped into maximal connected
components under interval overlap. Every whole record in a conflicting
component is rejected; recovery never selects a winner or preserves only a
nonconflicting fragment. Metadata is copied only when the complete chain, page
CRCs, zlib stream, checksum, and exact output length verify.

### 19.1 Recovery report

The report keeps physical, logical, and address coverage in separate units. It
contains checked physical `u64` counters for pages examined, pages accepted,
pages rejected, and I/O-unreadable pages. It contains checked logical `u64`
counters, separated by object kind, for range records, catalog entries,
membership entries, metadata chunks, and retirement records examined,
accepted, and rejected. Physical pages and logical records MUST never be added
to address cardinalities.

Physical page counters count unique declared page numbers, not repeated
traversal attempts. I/O-unreadable pages are a reason-coded subset of rejected
pages, so totals MUST NOT add those categories as if disjoint.

The report also contains exact 129-bit address values:

- `verified_addresses`: union cardinality successfully copied;
- `rejected_addresses`: union cardinality of readable, endpoint-trustworthy
  records rejected for a known semantic reason; and
- `bounded_possible_span_addresses`: union cardinality of conservative address
  envelopes whose bounds are independently trustworthy. This is an exact size
  of the possible affected span, not a claim about how many records or addresses
  were actually lost.

It also contains `has_unbounded_unknown`. Every recovery entry point requires a
caller-provided `unknown_sink`; unknown envelopes are delivered to that sink and
are never accumulated in the returned report. Delivery order is deterministic:
selected-root order, then first-encounter physical page/key order, with each
independently identified envelope emitted once. Sink arguments are borrowed only
for the call. A sink error stops recovery, aborts before the destination
publication boundary, attempts exact private-output/authorized-scratch cleanup, and returns
typed `RecoveryPreparationFailed` with partial counters, source coordination
guard when required, cleanup state, and the complete artifact ledger. No output
may be published after the caller failed to receive the damage report.

An unknown envelope records the owning graph, physical page or
checked physical byte interval when known, logical object kind and key bounds
when known, address family, optional trusted inclusive address fence, whether
that fence participates in `bounded_possible_span_addresses`, and one reason
code:

```text
META_UNAVAILABLE
IO_ERROR
ARITHMETIC_OVERFLOW
PAGE_OUT_OF_BOUNDS
PAGE_HEADER_INVALID
PAGE_CRC_MISMATCH
PAGE_TYPE_MISMATCH
TREE_CYCLE
PAGE_ALIAS
TREE_ORDER_INVALID
RANGE_REVERSED
RANGE_OVERLAP
CATALOG_INVALID
MEMBERSHIP_MISSING
MEMBERSHIP_INVALID
BLOB_INVALID
METADATA_INVALID
```

Range branch separators contain only the exact first `from` key of each child;
they deliberately carry no address-coverage summary. An unreadable or invalid
range descendant therefore sets `has_unbounded_unknown`. Bytes from the failed
child itself, including record endpoints on a checksum-failed leaf, are never
promoted to a trusted fence. A CRC-valid range record with valid endpoints that
is rejected only for a known cross-record, catalog, or membership conflict
contributes its interval to `rejected_addresses` instead.

Unknown coverage without trustworthy conservative bounds sets
`has_unbounded_unknown`; it MUST NOT be assigned a guessed cardinality.
Parent branch keys alone are never enough to infer affected address cardinality
because a predecessor range may cross a later key. Cardinality
unions MUST avoid double counting overlapping rejected records or possible-span
envelopes.

The caller decides whether the recovered subset is acceptable, whether to keep
the old feed, or whether to retry from another source.

Every recovery terminal result or error contains the optional scratch-attempt
ID, creation-security kind/commitment once artifact creation begins, aggregate
`cleanup_state`, complete `cleanup_artifacts`, and common
`coordination_cleanup`, `housekeeping`, and bounded `visible_housekeeping`
fields. A failed live-source registration or reap returns
its opaque guard there. Failure to release the source registration after final
source checks is still strictly before the immutable-output publication
boundary: recovery acquires no destination lifetime/reservation lock, attempts
exact cleanup of its private output and authorized scratch, and returns typed
`RecoveryPreparationFailed` with the source guard and complete artifact ledger.
It MUST NOT cross the boundary while that guard retains the source lifetime
lock. After source release is proved, the section-20 publication result carries
only destination-originated coordination cleanup alongside the recovery report.

## 20. Compact snapshot operation

```text
SnapshotTo(source_path, Immutable | Live, destination_path,
           FailIfExists | ReplaceExisting | ReplaceExistingNoRollback,
           snapshot_resource_budget,
           cancellation)
```

`SnapshotTo` pins one selected source generation. A live source registers like
any other live reader so its pages remain protected while the writer continues.
After the slot publishes that selected transaction, the snapshot caches the
complete selected tuple and roots/counts used for copying; later writers may
legally overwrite the old physical meta page without invalidating the pin.
An immutable source requires no sidecar and holds a shared main-file lifetime
lock so a cooperating live transition cannot start during the copy. It rechecks
canonical path identity and sidecar absence immediately after acquiring the
lock, before reading source pages, and again after the copy.

After the private output is self-contained, live `SnapshotTo` takes the source
operation lock and verifies the retained directory/main/sidecar identities,
database ID, link counts, exact active slot owner/nonce/transaction, and that the
cached tuple/roots are the ones copied. It does not require the old physical meta
page to survive or reselect a newer generation. Immutable `SnapshotTo` instead
rereads the locked exact meta and repeats its path/sidecar-absence checks. It then
releases its live registration or immutable lifetime lock before taking any
blocking destination lifetime lock or namespace reservation, and performs no
later source access. This rule also covers a source and replacement destination
in the same directory and prevents concurrent `A -> B` and `B -> A` snapshots
from retaining opposite shared locks while waiting for exclusive destination
locks.

An immutable source MAY use its own canonical path as the destination only with
`ReplaceExisting` or `ReplaceExistingNoRollback`; this is the atomic in-place
compaction path. After releasing source protection, replacement MUST reopen and
exclusively lock the same recorded old inode, recheck its tuple/identity/digest
and sidecar absence, and then follow the ordinary replacement protocol. Any
intervening change is a typed conflict. `FailIfExists` on the same path returns
the ordinary destination-exists precondition error. A live source targeting its
own canonical path is rejected before output construction because its required
sidecar makes immutable replacement preconditions false; callers snapshot live
state to a different path.

It streams a new ordinary v4 file containing only:

- the reachable logical range map;
- the feed catalog and used-index state;
- the live membership dictionary and exact memberships;
- the exact decompressed metadata payload, recompressed as one valid v4 zlib
  stream when metadata is present.

It excludes unpublished growth, free pages, retirement extents, unreachable
pages, deleted bytes, and the source sidecar. The output free and retirement
roots and counts are zero.

The snapshot preserves source database ID, transaction ID, commit nonce,
address family, value kind/tag, feed indexes, feed-index limit, logical ranges,
metadata presence, and exact decompressed metadata bytes. It MAY rebuild
membership IDs, trees, page layout, and compressed bytes. Every rebuilt page
uses the preserved source transaction as `born_txn`, and both final meta pages
are identical.

`SnapshotTo` performs the mandatory page bounds, type, offset, length, and
checked-arithmetic checks needed to construct its output, but it invokes neither
full source `Validate` nor full final-output `Validate`. Encountered malformed or
unreadable input fails the operation; corruption that requires a global
validation proof may remain undetected when the caller intentionally skips
`Validate`. Phase 1 does not pretend that a separate path-based source validation
will pin the same transaction across two calls. A caller needing a complete
internal-consistency proof invokes `Validate` explicitly on the emitted immutable
output before trusting it. That output validation cannot prove source
completeness or equivalence: a malformed source can hide a reachable-intent child
that snapshot traversal never sees while still producing an internally valid
subset. Source-to-output assurance therefore requires a caller-stabilized source
(for example, a validated immutable/offline copy unchanged between operations)
or a future explicit fused validation/build operation; Phase 1 makes no such
claim for a concurrently writable live source. The publication digest below is
solely publication identity evidence and is not structural validation.

Snapshot creation generates a random nonzero publication-attempt ID and uses the
exact private name `.iprange-publish-<id>.tmp`, where `<id>` is its 32 lowercase
hexadecimal digits. It exclusively creates that file in the destination
directory using caller-bounded memory and writes complete final metas. Once the
exact output is final, it verifies link count one, takes that inode's exclusive
lifetime lock, forbids every further content write, records its local identity,
and performs one sequential read to compute the publication SHA-512 over every
exact file byte. It synchronizes the complete private output before any
namespace publication and retains that same lock after rename until final
publication proof and cleanup classification. Atomic same-inode rename plus
identity, link-count, length, and tuple rechecks preserve the digest's chain of
custody; the direct publisher does not hash the same output a second time. The
namespace primitive MUST publish that same inode without changing its local
identity; a platform or filesystem that cannot prove this returns typed
`PublicationUnsupported`.

A source, build, digest, reservation preparation, synchronization, or
other failure before the structured publication-result boundary attempts exact
cleanup of every artifact owned by the attempt and synchronizes each affected
directory as required. Cleanup is truthful, not promised: the typed
`SnapshotPreparationFailed` error contains `publication_attempt_id`, the exact
private output basename, optional known private-output identity, overall
creation-security kind/commitment,
`cleanup_state` (`Clean` or `ResiduePossible`), the complete bounded
`cleanup_artifacts` sequence defined in section 14.4, the common
`coordination_cleanup` field (including a live-source guard when required), and
the orthogonal `housekeeping` plus bounded `visible_housekeeping` fields, and the
original `cause`.
The sequence covers every cleanup-obligated private output or
reservation still live or not yet proven durably absent. A
`ResiduePossible` entry always identifies its exact directory role and basename,
contains its own cleanup error, and carries identity when it became known. The
canonical destination main has not been touched at this stage. No field is
filled with a sentinel, and failed cleanup never masquerades as successful
discard.

Failure to release a live source registration after the final source checks is
one such `SnapshotPreparationFailed` case. The operation MUST stop before any
blocking destination lifetime/reservation acquisition, clean or ledger its
private output, and return the retained source guard. Once source
release is proved and the publication boundary is crossed, every
`coordination_cleanup` obligation is destination-originated; a source guard can
never be carried across that boundary.

`ListAbandonedPublicationTemps(directory, cancellation, sink)` is an explicit read-only offline aid
which enumerates only exact `.iprange-publish-<32 lowercase hex>.tmp` names and
reports each no-follow regular-file identity plus a bootstrap-readable tuple and
digest when available. `RemoveAbandonedPublicationTemp(directory,
expected_directory_identity, id, expected identity, optional tuple,
optional digest, cancellation)` applies the section-14.4
idempotent cleanup rule only to the exact matching file. Exact name plus no-follow identity is
sufficient for a partial file without readable logical state; every readable
tuple/digest MUST additionally match.
The caller MUST first certify that no publisher/resolver is active in every supplied
directory. Neither aid infers
abandonment from age or removes a canonical main, sidecar, or reservation;
private reservation inspection/removal uses the section 20.1 APIs.

### 20.1 Namespace publication reservation

Immutable output publication owns the canonical destination `.readers` name
for its complete critical section. `FailIfExists`, `ReplaceExisting`,
`ReplaceExistingNoRollback`, compact snapshot, and recovery output creation
retain one transient reservation until main publication and namespace
durability are resolved, then retire it. An existing immutable main is not an
exception: either replacement policy first locks that main and then acquires
the same reservation. Live create, initialize, and reset use only the simple
section-15 sidecar protocol and never use this reservation format.

The immutable-publication reservation is exactly 8,192 bytes: two 4,096-byte
blocks with identical field positions. Table offsets are relative to the start
of either block:

| Offset | Size | Field |
|---:|---:|---|
| 0 | 8 | ASCII `IPR4RSV1` |
| 8 | 2 | `header_record_size = 512` |
| 10 | 2 | `version = 1` |
| 12 | 4 | `state` (`1=prepared`, `2=main namespace may have been attempted`) |
| 16 | 16 | attempted `database_id` |
| 32 | 8 | attempted `txn_id` |
| 40 | 16 | attempted `commit_nonce` |
| 56 | 16 | random nonzero `reservation_id` |
| 72 | 2 | `identity_kind` from section 15.2 |
| 74 | 6 | zero |
| 80 | 32 | this reservation file's local identity |
| 112 | 2 | publication policy (`1=FailIfExists`, `2=ReplaceExisting`, `3=ReplaceExistingNoRollback`) |
| 114 | 2 | attempted-output `identity_kind` |
| 116 | 4 | `record_flags` (bit 0 = previous destination present; all other bits zero) |
| 120 | 8 | attempted output byte length |
| 128 | 32 | attempted output local identity |
| 160 | 64 | SHA-512 of exact attempted output bytes |
| 224 | 32 | previous destination local identity when flag bit 0 is set; otherwise zero |
| 256 | 64 | SHA-512 of exact previous destination bytes when flag bit 0 is set; otherwise zero |
| 320 | 92 | reserved |
| 412 | 2 | destination-basename encoding kind |
| 414 | 2 | zero |
| 416 | 4 | destination-basename encoded byte length |
| 420 | 32 | destination-basename SHA-256 commitment from section 3 |
| 452 | 8 | previous destination byte length when flag bit 0 is set; otherwise zero |
| 460 | 2 | creation-security kind from section 15.6 |
| 462 | 2 | zero |
| 464 | 32 | creation-security commitment from section 15.6 |
| 496 | 8 | nonzero `header_seq` |
| 504 | 4 | zero |
| 508 | 4 | block CRC-32C |

CRC covers the complete 4,096-byte block with `[508,512)` zero. Every reserved
byte is zero. Reservation ID is the operation's random nonzero
publication-attempt ID. The creation-security kind/commitment is fixed at
attempt start and matches every engine-created inode in that attempt.

The publisher exclusively creates and sizes
`.iprange-reservation-<id>.tmp`, obtains its local identity, writes and
synchronizes a complete state-1 sequence-1 block, takes its operation lock, and
publishes that same inode at canonical `.readers` with an atomic no-replace
namespace operation. It retains that descriptor and lock until publication
durability is resolved and the reservation is durably retired or cleanup is
reported. A missing, partial, malformed, replaced, or foreign reservation is
never removed by ordinary open or another publication. Its presence blocks
immutable open and competing publication.

Every accepted canonical or private reservation is a regular non-symlink inode
with link count one, a matching self-recorded local identity, a selectable
header, and an attempted output identity distinct from the reservation
identity. The only exception is a same-process retained POSIX descriptor after
its exact unlink, whose zero link count is removal evidence. Hard-linked
reservations are conflicts except for the exact temporary FreeBSD no-replace
transition below.

The destination-basename encoding, exact encoded length, and SHA-256 commitment
are mandatory from the first synchronized image. The complete output identity,
page-aligned length, and SHA-512 digest are also mandatory. Both replacement
policies require flag bit 0 plus all three previous-destination fields;
`FailIfExists` requires the flag and those fields to be zero. Unknown policies,
flags, inconsistent optional fields, zero mandatory values, or nonzero reserved
bytes are malformed.

On Linux and macOS, the proved no-replace publication plus retained-directory
`fsync` and later exact unlink plus retained-directory `fsync` establish
reservation durability. On FreeBSD 14, every no-replace publication of a
prepared main or reservation uses this exact fallback:

1. `linkat(private, canonical)`; `EEXIST` means the no-replace attempt lost;
2. `fsync` the retained directory;
3. unlink the exact private alias;
4. `fsync` the retained directory again; and
5. recheck directory identity, canonical identity, private-name absence, and
   canonical link count one.

Between steps 1 and 3, the two exact names refer to the same retained inode and
link count two is legal transition evidence. A resolver accepts only this exact
same-attempt/same-identity pair and may finish alias removal; any other hard link
is conflict. No direct or resolved result reports ready/`Published` until step 5
succeeds. An interruption at either directory-sync or alias-removal boundary
remains `OutcomeUnknown` with both exact names and identity available for
resolution. Every finished main and reservation has link count one.
On Windows, the synchronized write-through private reservation is renamed to
canonical `.readers` without replacement using the handle-relative
`FileRenameInfoEx` operation defined in section 14.2, then flushed and rechecked.
Retirement renames that exact retained inode, again with no replacement, to
its decision-54 inert GC name under section 14.4.1, flushes, resolves ambiguity,
and rechecks it; canonical absence then completes correctness cleanup while
final payload/envelope removal remains reported housekeeping. A
platform/filesystem without these proven primitives returns
`DurabilityUnsupported` rather than claiming durable namespace publication.

Reservation retirement records the exact retained inode/attempt identity before
its first unlink or rename. Its final proof accepts canonical absence or a
different, fully valid, self-identity-bound reservation/sidecar with a different
attempt/sidecar ID in the same retained directory. Because canonical acquisition
is no-replace, that valid later use proves the old exact inode left the name; the
old operation leaves the later inode untouched, synchronizes the directory, and
reports its own retirement satisfied-by-reuse. A malformed or identity-invalid
foreign inode remains `Conflict`/cleanup residue and is never removed. POSIX also
requires the old inode's recorded and pre-retirement rechecked link count to have
been one. In the same process, a retained descriptor may additionally prove zero
links; after restart that descriptor is not required, because a valid later
no-replace acquisition in the same identity-bound directory plus a new directory
sync proves the old canonical name was vacated. Windows likewise accepts the
exact envelope-bound inert GC identity or valid later no-replace reuse;
authoritative-name absence alone is not correctness proof. This is
the namespace analogue of valid slot-nonce reuse.

After owning the ready reservation, `FailIfExists` rechecks that the main name
is absent and rechecks the already-held exclusive lifetime lock and identity of
the prepared main inode. It
retains that lock across no-replace publication and after the inode becomes
canonical. It synchronizes the main and namespace and rechecks exact canonical
identity, link count, length, and tuple, preserving the already locked
precomputed digest without rereading the file. That proof makes the primary result
`Published`. It then attempts exact reservation retirement and the second
namespace synchronization while retaining the main lock. A retirement failure
is `Published` plus cleanup residue, not `OutcomeUnknown`. From selection of the
synchronized state-2 write-ahead record only through the successful main/
namespace synchronization and exact desired-content recheck, an indeterminate
failure is `OutcomeUnknown`.

Immediately before the first main-name namespace call,
the publisher writes and synchronizes state 2 through the alternate reservation
block and re-reads it as selected. The namespace call MUST NOT begin unless that
transition succeeds. State 2 is therefore the durable ambiguity record used by
a resolver: it proves the namespace operation was authorized and may have been
attempted, not that the OS call was observed. State 1 proves the main namespace
call had not begun. Both states block ordinary open and competing publication.
Live lifecycle transitions use only section 15's `creating`/`ready` sidecar
states.

`ListAbandonedReservationArtifacts(directory, cancellation, sink)` is an explicit read-only offline
aid for exact `.iprange-reservation-<32 lowercase hex>.tmp` names. Inert Windows
GC pairs are housekeeping and use `ListWindowsHousekeeping`; they are never
reservation or resolver authority.
`RemoveAbandonedReservationArtifact(directory, expected_directory_identity,
attempt ID, local identity, cancellation)` additionally requires valid self-identity when
readable and caller-certified absence of any active publisher/resolver. It uses
the section-14.4 idempotent cleanup rule and never touches canonical `.readers`
or a main file.

`SnapshotTo` requires the caller to choose `FailIfExists`, `ReplaceExisting`,
or `ReplaceExistingNoRollback`; its convenience wrapper defaults to
`FailIfExists` and never switches policy. Recovery always uses `FailIfExists`.
Source and destination MUST NOT name the same inode except for the exact
immutable same-path compaction case using either replacement policy defined in
section 20. Recovery, a live source, and `FailIfExists` never receive this
exception. Every policy rejects a pre-existing canonical destination sidecar or
reservation, including an orphan left by partial creation; that rejection is
the failed atomic reservation acquisition itself, not a racy preliminary
check.

`FailIfExists` uses the reservation protocol above. Both replacement policies
require an already existing no-follow regular, link-count-one, sidecar-free
destination; it need not contain valid v4 bytes. Absence is a typed precondition
failure rather than a switch to replace semantics. Under caller-certified
absence of non-cooperating modifiers, replacement takes that inode's exclusive
lifetime lock,
rechecks the canonical identity and link count, synchronizes and reads the
stable previous bytes, records their exact byte length and SHA-512 digest, and
then atomically acquires the canonical reservation. Any inability to establish
that stable evidence fails before the state-2 ambiguity boundary. The prepared
output is already exclusively lifetime-locked by the one-pass digest protocol;
the publisher rechecks and retains that exact lock rather than reacquiring it.
For kinds 1-2 the destination lock order is always prepared-output lifetime
lock, existing-destination lifetime lock when replacement applies, then
reservation operation lock; source protection has already been released. Under
both main locks and the retained reservation, it rechecks the old inode,
canonical path identity, unchanged link count/content, and exact reservation
before atomic replacement. It retains the prepared-output lock after that inode
becomes canonical through main/namespace synchronization, exact desired-content
proof, reservation retirement attempt, cleanup classification, and final
rechecks. Every cooperating initialize, rename, relink, unlink, and replacement
operation on an existing v4 path uses the same lifetime-lock rule.
Thus a concurrent `InitializeLive` or delayed absent-path publisher cannot
create coordination state between the checks and replacement, and a waiter
that opened the old inode fails its post-lock path-identity recheck.

`ReplaceExisting` requires one rollback-safe atomic exchange. The previous
inode becomes the exact attempt-derived private name until desired publication
is proved and retirement completes. A platform/filesystem without exchange
returns `DurabilityUnsupported` before output construction and never
downgrades. `ReplaceExistingNoRollback` may instead use an atomic destructive
replacement after the same preparation and rechecks. The canonical name remains
old or becomes the complete new inode, but once the new inode wins the previous
inode need not retain a name. A resolver may remove a no-rollback attempt while
the exact previous inode remains canonical. If the desired inode is canonical,
`Complete` is the only possible resolution and `Remove` returns
`Unresolvable` without mutation.

The result contains:

```text
attempted_database_id:[16]byte
attempted_txn_id:u64
attempted_commit_nonce:[16]byte
publication_attempt_id:[16]byte
directory_identity_kind:u16
directory_local_identity:[32]byte
destination_basename_encoding:u16
destination_basename:bounded byte sequence
attempted_output_identity_kind:u16
attempted_output_local_identity:[32]byte
attempted_output_byte_length:u64
attempted_output_sha512:[64]byte
publication_policy: FailIfExists | ReplaceExisting | ReplaceExistingNoRollback
previous_destination_local_identity: optional [32]byte
previous_destination_byte_length: optional u64
previous_destination_sha512: optional [64]byte
namespace_reservation_identity_kind:u16
namespace_reservation_local_identity:[32]byte
main_namespace_may_have_been_attempted:bool
publication: NotPublished | Published | OutcomeUnknown
destination_content: Desired | Previous | Absent | Other | Unclassified
later_canonical: None | ReservationOrTransition | ReadyLiveSidecar
live_lineage: optional SameGenerationExactBytes |
                       SameGenerationPhysicalBytesChanged |
                       AdvancedGeneration
later_attempt_or_sidecar_id: optional [16]byte
later_selected_txn_id: optional u64
later_selected_commit_nonce: optional [16]byte
creation_security_kind:u16
creation_security_commitment:[32]byte
main_access_policy: CreatorOnly | ChangedOrUnproven | Unclassified
coordination_access_policy: Absent | CreatorOnly | ChangedOrUnproven | Unclassified
cleanup_state: Clean | ResiduePossible
cleanup_artifacts: bounded sequence of CleanupArtifact
coordination_cleanup: None | CleanupGuard | RetainedReaderCloseRequired | RetainedWriterCloseRequired
housekeeping: None | CrashReappearancePossible | Visible
visible_housekeeping: bounded sequence of HousekeepingArtifact
cause: optional typed error
```

`publication` describes only the canonical destination postcondition. Cleanup is
orthogonal: a proven `NotPublished` or `Published` result remains factual when
an exact owned private output or reservation cannot be cleaned, and the nonempty
ledger then reports that residue. Cleanup failure never upgrades a definitive
publication result to `OutcomeUnknown`.
For a direct result with no later canonical owner, `destination_content` agrees
with `publication`: `Published` is `Desired`; a proved absent fail-if-exists
destination is `NotPublished` plus `Absent`; and a proved unchanged replacement
destination is `NotPublished` plus `Previous`. The later-owner fields are empty,
and a newly published inode reports main `CreatorOnly` plus coordination
`Absent`. Resolvers use all of the
orthogonal fields below rather than inventing compound result values.

Publication-attempt ID equals the reservation ID and determines the private
output name. The three previous-destination fields are all absent for
`FailIfExists` and all present for either replacement policy; the previous
length/digest are computed over stable bytes while its exclusive lifetime lock
is held. The
structured-result boundary begins only after the prepared output identity and
digest, applicable previous identity/length/digest, and synchronized private
reservation identity are known. Failure to publish that reservation canonically
therefore returns a resolvable `NotPublished` record. Earlier preparation
failures return the operation-specific typed preparation error, including its
attempt ID, exact private-output name, optional known output identity, aggregate
cleanup state, complete per-artifact cleanup sequence, and cause as defined
above. Private-artifact cleanup is exact but may fail; fields are never filled with
sentinels and residue is never hidden.

For operation kinds 1-2, a failure before selection of the synchronized state-2
write-ahead record is `NotPublished` and sets
`main_namespace_may_have_been_attempted == false`. Immediately before the first
main namespace call the implementation synchronizes reservation state 2 through
the alternate block and sets the returned field true. That durable transition,
not entry into the OS call, is the outcome-ambiguity boundary. From its selection
until successful main/namespace synchronization and exact desired-content/
identity recheck under the prepared-output lifetime lock, an unresolved failure
is `OutcomeUnknown`, including a failure in the interval before the namespace
call begins; the implementation MUST NOT remove a possibly published main. The
resolver trusts the exact CRC-valid retained reservation state over an
unpersisted caller copy of the field. That desired-content proof makes the direct
result `Published`; exact reservation retirement is then cleanup. Failure to
retire it returns `Published`, `cleanup_state == ResiduePossible`, and the exact
private-reservation ledger entry. The destination is therefore absent/previous
or a complete file—never partially written—but crash durability can remain
unknown before desired-content proof.

`InspectPublicationResidue(path, cancellation)` is an explicit read-only operation. It retains
and records the containing directory identity, then opens canonical `.readers`
without following symlinks. Its result includes an optional non-serializable
opaque residue handle retaining those exact directory and coordination
descriptors only when canonical coordination was successfully opened. Canonical
absence produces a directory-only report and no handle. A valid
kind-1 or kind-2 reservation there
reconstructs the complete publication result record—including that parent
identity—plus attempt tuple,
policy, output identity/length/digest, optional previous identity/length/digest,
reservation identity, and durable namespace phase—without an in-memory caller
result; only then can its attempt ID derive and inspect the private
reservation/output names. When canonical `.readers` is
absent, path-specific reconstruction performs a bounded streamed scan of exact
private reservation names. A record is authority only when it is a
complete selectable kind-1/kind-2 record bound to the retained parent and the
requested section-3 destination-name commitment. Zero matches is absent, one
reconstructs the result, and multiple is `Conflict`; only then may the attempt ID
derive output names. Directory-wide list APIs may group other private artifacts
for operator cleanup, but random names alone never prove their intended
destination.

Its residue handle retains the exact same-process directory and coordination
descriptors until explicit Close or removal; inspection alone never transfers a
cleanup obligation.
When canonical coordination is absent, normal result-based resolution and exact
private-artifact cleanup apply; neither Remove API may be called without a
present residue handle.

`RemovePublicationResidue(residue_handle, cancellation)` is the explicit
offline escape hatch for a canonical reservation whose dual header cannot be
selected. The caller MUST certify that no publisher, resolver, live handle, or
immutable reader is active and that the destination namespace is quiescent. The
operation uses the exact retained parent and coordination descriptors from the
same-process inspection, requires that the canonical path still resolves to that
regular link-count-one inode, and takes its operation lock. If either block
selects a valid immutable-publication reservation, or the inode is a selectable
section-15 live sidecar, the operation returns `Conflict`; the caller must use
that record's operation-specific resolver. Only a coordination inode with no
selectable reservation or sidecar header is eligible. The retained descriptor
is then the only SDK
mutation authority; a serialized local identity, raw untrusted attempt ID, or
operator record is insufficient because POSIX inode identities can be reused.
A process restart requires a fresh inspection of the current canonical inode.
Age/pathname alone are never proof.

Before hashing or removing anything, it opens the current main when present
without following symlinks, takes that exact inode's exclusive lifetime lock,
and rechecks the canonical path/descriptor identity and link count. While
retaining that lock it records the main bootstrap tuple, length, and exact digest
when readable, and changes no main byte. It then retires/unlinks only the exact
coordination inode by the platform reservation-retirement rule, synchronizes the
containing directory, and rechecks the parent and main identities while the main
lock remains held. The final coordination proof accepts canonical absence or a
different, fully valid, self-identity-bound later reservation/sidecar in the same
retained directory. It leaves such a later inode untouched and reports the old
retirement satisfied by valid reuse; malformed or identity-invalid later state
is `CleanupConflict`. Only then may it release the main lock. The result reports
that main classification plus aggregate cleanup and the common coordination
field. Any mismatch or indeterminate sync is
`CleanupConflict`/`ResiduePossible`; the API never guesses or deletes the main.

`ResolvePublication(path, optional result, Complete | Remove, cancellation)` resolves the
destination postcondition; after a process restart it does not claim which
historical rename occurred. It accepts either a complete caller result or the
exact reconstructed reservation record above and requires equality when both
exist **and name the same publication/reservation attempt ID**. A different,
fully valid, self-identity-bound canonical record is never used as authority for
the old attempt; with a complete caller result it is classified only by the
later-reuse rules below. Without a caller result, the resolver uses the current
canonical record or, when canonical coordination is absent, the unique bound
private-record match from the streamed scan above. Zero is absent and multiple
is `Conflict`. It retains the containing directory
and, before inspection or action, requires a supplied result's directory
identity kind and local identity to equal that retained parent. A mismatch is typed `DirectoryIdentityMismatch` and
changes nothing. Without a caller result, the exact authoritative canonical or
unique bound private reservation record is bound to the identity of the retained
directory that contains it, and the reconstructed result records that parent
identity. If the
exact reservation is present, it opens it without following symlinks, matches the authoritative
record's tuple, attempt ID, identity kind, and local identity, takes its
operation lock, and rechecks its canonical identity and CRC-protected phase. It
also inspects the exact attempt-derived private reservation/output when canonical
`.readers` is absent. A foreign reservation is
never removed. A coordination inode for which neither header block is valid, or
whose two blocks cannot be selected under the sequence rules, is never removed
by the online resolver because a reusable OS inode number alone cannot prove
attempt ownership. A torn newer block with an older valid selected block is not
this case. Unselectable state requires caller-certified offline namespace
quiescence and explicit operator cleanup; its phase remains unknown.

If a final main is present, the resolver opens it without following symlinks,
takes its exclusive lifetime lock for `Complete` kind 2 and a shared lifetime
lock otherwise, and classifies any coordination inode before hashing the stable
complete bytes. A malformed, identity-invalid, or main-inconsistent foreign
coordination inode is `Conflict`. A fully valid later reservation/sidecar with a
different attempt identity is opened and rechecked under its operation lock. A
later reservation is retained through the full hash/sync/recheck and is accepted
and left untouched only when the main proves exact desired content, where it is
evidence that the old reservation was retired and the canonical name was reused.
A later ready sidecar is strictly validated against the retained main; its
operation lock is held through the complete hash, synchronization, and final
rechecks, and its writer lease must be free after provenance-safe reaping. An
active/uncertain writer returns `WriterBusy`. Exact desired content with that
sidecar reports `Published`, `Desired`, `ReadyLiveSidecar`, and the safely
determined live lineage so the caller does not mistake the path for an immutable
output. For every other destination classification a later
reservation/sidecar is `Conflict`. The
resolver rechecks length plus canonical path and descriptor identity after the
full hash pass. It classifies only exact content:

- attempted tuple plus exact attempted digest is `Desired`;
- for replacement, exact previous identity plus exact previous byte length and
  exact previous digest is
  `Previous`;
- every other complete main is `Other`.

Mode and state determine the one action:

- Exact desired content: both modes synchronize the retained main; recheck and
  remove the exact owned private output when its recorded identity, length, and
  digest still match; attempt to retire an exact remaining reservation; and
  synchronize/recheck every namespace action. It reports primary `Published`
  plus destination content `Desired` once retained-main durability and exact content are
  proved, even when every old reservation artifact is already absent or a valid
  later reservation has reused the canonical name. With the valid later sidecar
  described above, the same primary/content proof additionally reports that
  later canonical owner and its live lineage. Equivalent exact
  bytes satisfy the postcondition even if this attempt's rename cannot be
  proven. Failure to remove an exact owned private output or old reservation is
  reported separately in the unresolved cleanup ledger and does not change the
  proven destination result; a later valid reuse is left untouched.
- Exact previous replacement content: `Complete` requires both the exact
  prepared private output and exact reservation inode. It opens the private
  output without following symlinks, proves its recorded identity/length/digest/
  tuple, takes its exclusive lifetime lock, then retries atomic replacement under
  both main lifetime locks and the reservation operation lock. It retains the
  output lock after that inode becomes canonical through desired-content proof
  and cleanup classification. If either required artifact is missing, `Complete`
  is typed `Unresolvable` and changes nothing. `Remove`
  retires only exact owned reservation/private-output artifacts, synchronizes,
  and reports primary `NotPublished` plus destination content `Previous`.
- Absent main for kind 1: `Complete` requires both exact artifacts, then
  opens and fully verifies the private output, takes its exclusive lifetime lock,
  and atomically publishes it while retaining that lock through
  desired-content proof and cleanup classification. A missing required artifact makes `Complete` typed
  `Unresolvable` without cleanup. `Remove` retires only exact owned
  private-output/reservation state and reports primary `NotPublished` plus
  destination content `Absent` after synchronized cleanup for either durable
  reservation state; it reports the proven current
  destination, not whether a transient historical rename occurred.
- A third complete main reports primary `NotPublished` plus destination content
  `Other`. Neither mode overwrites or
  deletes it. When every attempt artifact is exact, both modes may remove only
  the exact owned private output, retire the exact owned reservation,
  synchronize, and return those same independent facts; an artifact mismatch is instead
  `Conflict` and is not cleaned online.
- An absent main for kind 2 is `Conflict`, because atomic replacement cannot
  legitimately remove both old and new names. The online resolver changes
  neither the destination nor attempt artifacts; caller-certified offline
  inspection is required.
- A foreign coordination inode that is not the exact valid later-use exception
  in the desired-content row, or a mismatched private artifact, is `Conflict`.
  A coordination inode unselectable under the dual-block rules is
  `Unresolvable`. `OutcomeUnknown` is returned only when a resolver namespace
  action, required synchronization, or final identity recheck has an
  indeterminate result.

`Complete` may retry a still-required main namespace operation only while the exact recorded
reservation inode still exists at canonical `.readers` or its private `.tmp`
name and can
be restored to canonical `.readers`
under its retained operation lock. If every exact reservation artifact is gone
and the main still needs publication/replacement, the old attempt cannot recreate
its recorded reservation identity. `Complete` then returns typed `Unresolvable`;
the caller may start a new publication attempt after ordinary preconditions are
re-established. This restriction does not apply when exact desired content is
already present, because no main namespace retry is needed. `Remove` may still
clean an exact owned private output and report the proven destination state.

Every definitive destination result requires retained-main synchronization when
present, required namespace synchronization, and final path/sidecar/identity
rechecks while the applicable main locks remain held. Exact old-reservation
retirement is attempted separately; failure is cleanup residue and valid later
reuse is proof of prior retirement. A main synchronization or destination
identity/content recheck failure is `OutcomeUnknown` or typed `Unresolvable`; a
cache observation alone is never durability proof. With no exact reservation
artifact, an absent main uses a complete caller result's phase; without either
authority it is `OutcomeUnknown`.

The resolver's structured result uses the complete publication-result schema
above. Its operation-specific primary field remains `publication`, with only
`NotPublished | Published | OutcomeUnknown`; current destination content, later
canonical ownership, live lineage, access state, cleanup, and housekeeping are
independent facts. It may instead return typed
`Conflict`/`Unresolvable`/`WriterBusy`. A later owner is never overwritten or
removed. Snapshot output reports the preserved source tuple. Recovery output
publication reports its newly generated tuple even on `NotPublished` or
`OutcomeUnknown`.

Recovery output uses the same prepared-final-inode publication and structured
result contract, but its destination policy is always `FailIfExists`. Recovery
never accepts either replacement policy and never overwrites a pre-existing
main or sidecar.

The output has no sidecar. Converting it to live use requires explicit
`InitializeLive(path, capacity, cancellation)`; live open never performs that transition
implicitly.

## 21. Conformance requirements

The shared conformance corpus MUST include Go-produced and Rust-produced files.
Each implementation MUST actually open the other implementation's files and
compare:

- every IPv4/IPv6 range and direct value;
- feed names and assigned indexes;
- resolved memberships rather than internal membership IDs;
- exact decompressed metadata bytes;
- empty metadata versus absent metadata;
- exact 129-bit cardinalities, including a full IPv6 space;
- commit-outcome resolution with same-transaction/different-nonce attempts;
- compact snapshots; and
- exact rejection of obsolete or malformed format identities.

Conformance MUST also exercise both implementations as cooperating subprocesses
on the same live database. In both Go-to-Rust and Rust-to-Go directions it covers
reader registration and release, writer exclusion, oldest-reader reclamation,
stale-slot cleanup, sidecar replacement detection, and every resolvable
reservation or live-transition phase. One implementation MUST prepare each
SHA-512-bound publication reservation and each live `creating`/`ready`
intermediate; the other MUST inspect and resolve it. Per-language tests alone
do not satisfy this requirement.

Byte-identical writer output is not required. Golden files from any obsolete
experimental v4 layout are not v4 fixtures and MUST be deleted.

## 22. Normative algorithm references

- Zlib wrapper: [RFC 1950](https://www.rfc-editor.org/rfc/rfc1950.html)
- DEFLATE stream: [RFC 1951](https://www.rfc-editor.org/rfc/rfc1951.html)
- SHA-256 and SHA-512: [RFC 6234](https://www.rfc-editor.org/rfc/rfc6234.html)
- POSIX/Linux file and directory synchronization:
  [`fsync(2)`](https://man7.org/linux/man-pages/man2/fsync.2.html)
- macOS durable file ordering:
  [`fsync(2)` and `F_FULLFSYNC`](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/fsync.2.html)
- Windows file synchronization:
  [`FlushFileBuffers`](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-flushfilebuffers)
- Windows handle-relative open and namespace operations:
  [`NtCreateFile`](https://learn.microsoft.com/en-us/windows/win32/api/winternl/nf-winternl-ntcreatefile),
  [`SetFileInformationByHandle`](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-setfileinformationbyhandle),
  [`FILE_RENAME_INFO`](https://learn.microsoft.com/en-us/windows/win32/api/winbase/ns-winbase-file_rename_info),
  and [`CreateFileW` write-through semantics](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-createfilew#caching-behavior)
