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
- persistent reader-protected retirement batches;
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
| 136 | 8 | `retirement_batch_count` | current batch count |
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
| 184 | 68 | reserved | zero |
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
`ProvenCurrent`; after any transaction-zero registration they clean it and
return `CurrentGenerationUnprovable` rather than publishing a reader slot for a
possibly older generation. Immutable readers and immutable snapshots may expose
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

`retirement_batch_count <= txn_id - 1`. All count subtraction is checked.

The following root/count relations are bootstrap invariants:

- `range_record_count > 0` requires nonzero `range_root`; a zero count permits
  either canonical root zero or a legal all-empty range tree;
- `active_feed_count == 0` requires both catalog roots and `feed_used_root` zero;
  a nonzero active count requires all three roots nonzero;
- `membership_entry_count == 0` requires both dictionary roots and
  `membership_used_root` zero and `membership_id_limit == 1`; a nonzero count
  requires all three roots nonzero;
- `retirement_batch_count == 0` if and only if `retirement_root == 0`; and
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

For every tightly packed fixed-record page not given a stronger rule below,
records begin at byte 32, `lower == 32 + item_count * record_size`, and
`upper == 4096`. The multiplication and addition are checked. All bytes from
`lower` through byte 4095 are zero.

### 5.3 Tree descent invariants

Every branch pointer is a non-meta page below selected `page_count`. Its child
has the page type and `aux` required by that owning tree, and its level is
exactly one less than the parent level. Branch pages are nonempty unless a
section explicitly permits otherwise; no section permits a zero-child branch.
Non-range leaves are nonempty unless their whole tree is represented by root
zero. Keys and fences use the ordering stated by their tree section.

Ordinary access checks the level decrease, child bounds, expected type, and
expected `aux` before following every pointer. It stops after
`MAX_TREE_LEVEL + 1` pages. These checks prevent cycles, stack exhaustion, and
out-of-bounds access without computing page CRCs or performing full validation.
Explicit `Validate` additionally proves global ownership, exact summaries, and
cross-page ordering.

## 6. Range tree and range records

The range tree is a COW B+tree ordered by record `from`. `range_root == 0`
means an empty map.

Range pages use `aux == address_family`. Their fixed records start at byte 32
and are tightly packed. `upper == 4096`; `lower` equals the first unused byte.
Unused bytes are zero.

### 6.1 Range leaf

A leaf has page type 2 and `level == 0`.

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
the same value MUST be coalesced. A leaf MAY contain zero records, including
when it is a reachable non-root child.

In a direct file, `value` is an opaque caller-defined `u32`; zero is valid. In
a membership file, `value` is a nonzero `membership_id` present in the selected
snapshot's membership dictionary. Value zero means absence and MUST NOT be
stored as a range record.

### 6.2 Range branch

A branch has page type 1 and `level > 0`. Branch entries carry exact subtree
summaries so legal empty children never force descent through an unbounded run
of empty pages.

An IPv4 entry is 32 bytes:

```text
lower_fence:u32
child_pgno:u32
subtree_record_count:u64
first_from:u32
last_from:u32
last_to:u32
reserved:u32 = 0
```

An IPv6 entry is 80 bytes:

```text
lower_fence:u128
child_pgno:u32
reserved:u32 = 0
subtree_record_count:u64
first_from:u128
last_from:u128
last_to:u128
```

Fences are strictly increasing. The first root fence is the all-zero address.
For a non-root branch, its first fence equals the lower fence assigned by its
parent. An entry owns records whose `from` is at least its fence and less than
the next fence; the final entry uses the upper bound inherited from its parent.
A record's `to` MAY cross a later fence.

For a nonempty child, `subtree_record_count` is its exact record count,
`first_from` and `last_from` are its first and last record starts, and `last_to`
is the final record's endpoint. Because ranges are globally ordered and
non-overlapping, that endpoint is also the subtree's greatest endpoint. For an
empty child, the count and all three summary addresses are zero.

A branch has at least one child; a zero-child branch is invalid. A non-root
one-child branch, an empty child, and an all-empty branch subtree are legal.
Writers SHOULD collapse redundant branches and represent a completely empty
map with `range_root == 0`, but readers MUST accept every legal form.

Point lookup and backward-cursor initialization choose the rightmost nonempty
entry whose `first_from <= target`. They repeat that rule at every branch,
then choose the leaf's rightmost record whose `from <= target`; point lookup
finally tests that record's `to`. If no qualifying entry exists in a subtree,
predecessor search resumes at the nearest earlier nonempty sibling recorded on
the bounded ancestor stack. Forward seek first tests that predecessor for
containment, then advances to the first later record when needed. Cursor and
query traversal use the exact counts to skip whole empty subtrees. Work is
bounded by tree height times the fixed number of entries in one page; it MUST
NOT scale with the total number of consecutive empty leaves or subtrees.

## 7. Slotted-page convention

Catalog-name pages, catalog-index leaves, and membership-ID leaves contain
variable-length records and use this convention:

- `item_count` is the slot count.
- The slot array starts at byte 32 and contains `item_count` little-endian
  `u16` record offsets in logical key order.
- `lower == 32 + 2 * item_count`.
- `upper` is the smallest record offset, or 4096 when empty.
- Each record begins with `record_len:u16` and `record_flags:u16`.
- `record_flags` is zero unless a record layout below defines bits.
- Records MUST be wholly within `[upper,4096)`, MUST NOT overlap, and MUST be
  referenced exactly once.
- Every gap and every unused byte is zero.

Membership-ID records explicitly specialize bytes `[2,4)` as
`storage:u8, reserved:u8`; that layout replaces the generic `record_flags` field
for that page type.

Non-root catalog and dictionary leaves MUST be nonempty. An empty complete
tree is represented by a zero root.

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

A name-branch record stores the exact maximum name in its nonempty child:

```text
record_len:u16
record_flags:u16 = 0
child_pgno:u32
name_len:u8
reserved:3 bytes = 0
max_name:name_len bytes
```

`record_len == 12 + name_len`. Branch keys are strictly increasing. Lookup
chooses the first maximum key greater than or equal to the target.

### 8.2 Numeric index

The numeric tree is ordered by `feed_index`.
Index branches have page type 5 and positive level. Index leaves have page type
6 and level zero. Both use `aux == 0`; branches are fixed-record pages and
leaves use the slotted-page convention.

An index-leaf record has the same layout as a name-leaf record and is ordered
by `feed_index`. An index branch is fixed-width and contains tightly packed
8-byte entries starting at byte 32:

```text
max_feed_index:u32  child_pgno:u32
```

The maximum indexes are strictly increasing and exactly match their nonempty
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
ID branches have page type 7, positive level, and fixed records. ID leaves have
page type 8, level zero, and use the slotted-page convention. Both use
`aux == 0`.

An ID branch is fixed-width and has tightly packed 8-byte entries:

```text
max_membership_id:u32  child_pgno:u32
```

Maximum IDs are strictly increasing and exactly match their nonempty children.

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

Hash-leaf entries are fixed 40-byte tuples in that order. Hash-branch entries
append `child_pgno:u32` and are 44 bytes. Branch keys are the exact maximum
tuple in each nonempty child.

Hash branches have page type 9 and positive level. Hash leaves have page type
10 and level zero. Both use `aux == 0` and tightly packed fixed records.

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

## 13. Free pages and retirement batches

The free-page bitmap's governing limit is `page_count`. A one bit means the
page is currently free and safe for immediate overwrite. Meta pages, current
reachable pages, and pages protected by a retirement batch MUST have zero bits.
Bits beyond `page_count` are zero.

Allocation may take only a bit that was one in the selected committed free
bitmap when the transaction began, or a page explicitly released by a safe
retirement reclamation performed under the live operation lock. A page made
unreachable by the current unpublished draft is never reusable by that draft;
it remains part of the authoritative old generation until publication.

Before the first destructive overwrite authorized by the selected committed
free bitmap, the transaction MUST verify the normalized CRC and local structural
invariants of every committed bitmap page on the chosen root-to-leaf path. This
includes page type/kind/level, bounds, reserved bytes, item counts, the selected
summary bit and child relationship at each branch, and the in-range one bit in
the leaf. The descended child proves the selected summary path; this narrow
check does not recursively prove unselected subtrees or the global live/free
partition.

Each committed bitmap page is verified at most once per transaction, when first
read into that transaction's existing COW page state. All later allocations on
that private verified path MUST reuse that state rather than rechecking the
committed page per allocated data page. A CRC or local-invariant failure aborts
the complete pending transaction before any candidate page is overwritten.

The same rule applies when reclamation releases pages from committed retirement
metadata. Reclamation is necessarily two-pass for each selected batch. Its
first bounded streaming pass MUST completely verify every committed retirement-
tree and page-list blob page on the selected path, the complete list's exact
length, normalized CRCs and local structure, batch ordering, globally strictly
increasing unique in-range page numbers, and the reader-transaction safety
condition. No page from that batch may be inserted into the free bitmap or
reused until this complete first pass succeeds. A second bounded streaming pass
may then apply the already proven list; it MUST recheck the same selected batch
identity/root and abort before release if the committed selection changed.
Neither pass proves global unreachability without explicit `Validate`.
Deliberately self-consistent corruption with recomputed CRCs therefore remains
inside the caller's explicit validation trust choice; ordinary torn or
checksum-detectable allocator damage cannot authorize an overwrite.

Allocation takes the lowest eligible free-page bit. If there is none and
`page_count < MAX_PAGE_COUNT`, it appends at the pending `page_count`. At
`MAX_PAGE_COUNT`, absence of an eligible free bit returns typed
`PageSpaceExhausted` before mutation. Page numbers, byte offsets, host `off_t`,
mapping lengths, and host index conversions are checked before use.

Free-bitmap and summary changes are themselves COW and publish with the same
meta as every other root. Every mutation records each replaced committed page
in transaction-owned, fixed-width scratch; it does not rebuild the complete
retirement list after each call. Before publication, transaction preparation
sorts and deduplicates that stream and reserves a checked worst-case number of
eligible or appended pages for both the free-page and reader-protected outcomes.

Under the live operation lock, publication scans the stable reader table. For
new transaction `T`, a transaction-zero registration or any active reader with
`txn_id < T` protects every page removed from the selected generation. Those
pages enter the single batch for `T`. When no such registration exists, they
are published directly as free, but they are never reused by the same draft.
The writer then runs a monotonic fixed-point construction using only its
pre-reserved capacity and transaction scratch. Physical page identities are
selected under the live operation lock from the current draft's eligible free
pages, verifier-proven reclaimed pages, and then the live unpublished tail; a
pre-lock capacity plan does not reserve a physical page number:

1. reserve pages without exposing them as free in the draft;
2. rebuild every affected bitmap path;
3. add every replaced committed bitmap, retirement-tree, and retirement-blob
   page to the same protected batch or direct-free set selected above;
4. rebuild the affected free bitmap, retirement batch, and tree paths; and
5. repeat until no newly replaced committed page or newly required private page
   exists.

Private pages created and superseded within the same draft are returned only to
the private reservation pool or unpublished tail; they are not retirement
history. The iteration is bounded by the fixed tree-height limits, the sorted
candidate count, and the precomputed reservation. It performs no heap
allocation, external-scratch creation, or unbounded discovery while the operation lock is
held. Reservation exhaustion aborts the whole transaction; it MUST NOT fall
through to publication.

Every fixed-point read from the current draft carries exact provenance. A page
is either from the selected committed generation or is a transaction-private
page identified by its work unit, reservation scope, slot, page number, binding
epoch, owner, and generation. Replacing a committed page adds it to the
protected retirement batch or direct-free set chosen for the transaction.
Replacing an earlier transaction-private page returns it only to that page's
exact scope and never creates retirement history. A carried eligible-source
entry already consumed by an earlier work unit is skipped by the coordinator's
monotonic source authority; if the current draft free bitmap itself still
advertises an already-owned page as free, the transaction aborts as stale or
inconsistent rather than silently skipping it.

Multiple work units form one linear, single-use predecessor/successor chain.
Each work unit replans from the exact current draft root and live pending page
count, binds its proof to that predecessor and exact scope, mutates only after
complete preflight, resynchronizes every touched scope, seals the work unit, and
issues at most one successor. Two plans may inspect one predecessor, but only
one may consume it. A failure after private mutation that cannot be rolled back
by the same composite journal aborts the complete unpublished transaction and
issues no successor.

Pages removed from the current generation but potentially visible to an older
registered reader MUST NOT enter the free bitmap. They enter one transaction-
grouped retirement batch.

### 13.1 Retirement tree

The retirement tree is ordered by unique `retired_by_txn` values.

A retirement branch has type 16 and fixed 16-byte entries:

```text
max_retired_by_txn:u64  child_pgno:u32  reserved:u32=0
```

A retirement leaf has type 17 and fixed 32-byte entries:

```text
reserved:u64 = 0
retired_by_txn:u64
page_count:u64
page_list_blob_root:u32
reserved:u32 = 0
```

The page list is a kind-2 blob containing exactly `page_count` strictly
increasing unique `u32` page numbers. A listed page MUST be in
`[2, committed page_count)`, unreachable from every current root, absent from
every other batch, and zero in the free bitmap.

For every batch, `1 <= page_count <= 2^32`, `page_list_blob_root` is nonzero,
the checked blob length is exactly `4 * page_count`, and
`1 < retired_by_txn <= selected_txn_id`. An empty retirement batch is never
stored.

Retirement branches have positive level, retirement leaves have level zero,
and both use `aux == 0` and tightly packed fixed records.
Branch keys are strictly increasing, equal the exact maximum transaction in
their nonempty children, and every child level is exactly one below its parent.
Retirement branches and non-root leaves are nonempty; an empty complete tree is
root zero.

`retired_by_txn == T` means transaction T made the pages unreachable. The batch
is safe to reclaim when no active reader has a transaction below T. An active
registration at transaction zero prevents every reclamation.

Safe batches are streamed into the free bitmap and removed. Pages retired by
that allocator/retirement COW update are accounted for like all other replaced
pages. The structure contains only outstanding reader protection; it MUST NOT
retain permanent allocation history.

`Reclaim(max_batches,max_pages,cancellation)` is a clean-writer, auto-publishing maintenance
operation using the writer's existing transaction resource budget. Both limits
MUST be nonzero. It selects only complete oldest eligible batches fitting both
limits. If the oldest eligible batch alone exceeds `max_pages`, it returns
`WorkLimitTooSmall` with that batch's required page count before mutation. Once
one or more batches have been selected, it stops before the next oversized
batch. Structural preparation occurs before the operation lock where safe;
reader-threshold selection, the bounded second pass, COW finalization, and
publication hold that lock continuously. Its internal commit receives the
already-owned lock and MUST NOT reacquire it. Existing pinned readers continue;
new registration waits. The result is `NoChange`, which publishes no generation,
or exact reclaimed batch/page counters plus the complete `CommitResult`. There
is no separately staged reclaim draft and no second per-call resource budget.
There is no immutable/offline writer mode; compacting an immutable file is a
`SnapshotTo` operation producing another immutable file. No implementation may
decide a batch is safe before it holds the same barrier used for publication.

Normal live commits do not reduce committed `page_count`, even when the highest
pages become free. Only unpublished tail cleanup may truncate a live file.
The compact snapshot operations are the supported path that physically compacts
committed free trailing space.

Every committed page from 2 through `page_count-1` MUST be exactly one of:

- reachable from a current root;
- marked free; or
- listed in one retirement batch.

Explicit validation checks this partition with bounded memory or external
scratch storage. Open does not.

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

1. Acquire the live operation lock; recheck the writer lease,
   retained identities, sidecar table, and unchanged selected generation; scan
   the stable registrations; and finalize the allocator/retirement fixed point
   and complete target meta using only prepared scratch and reserved pages.
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
  flags zero; replacement uses flags `0x1 | 0x2`
  (replace-if-exists plus POSIX semantics, so retained handles to the replaced
  inode remain valid). The implementation then calls `FlushFileBuffers` on the
  renamed file handle and rechecks the final entry through the retained
  directory before reporting namespace durability. Phase-1 durable Windows
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
live/uncertain writer is `WriterBusy`. An unresolved state-2 transition is
`LiveCoordinationCleanupRequired`: the caller must finish the retained
writer/guard cleanup, or after its owning process has gone perform
caller-certified offline coordination reset, before resolution can continue.
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
| 290 | 6 | zero |
| 296 | 32 | creation-security commitment from section 15.6 |
| 328 | 168 | zero |
| 496 | 8 | nonzero sequence |
| 504 | 4 | zero |
| 508 | 4 | block CRC-32C |

Bytes `[512,4096)` of each block are zero. CRC covers the full block with its
CRC field zero. Selection uses the same independently selectable sequence/CRC
rules as sidecar headers. `UnpublishedMainTail` is not a separately named inode
and is invalid in a GC envelope. The two basename commitments are exactly:

```text
SHA-256("IPR4GCAUTH" || encoding_kind:u16le || name_len:u32le || name_bytes)
SHA-256("IPR4GCNAME" || encoding_kind:u16le || name_len:u32le || name_bytes)
```

The first hashes the exact authoritative/private component selected when cleanup
began and the second hashes the exact inert GC component. For each artifact kind,
the source component is one of the exact attempt-derived private names defined by
this specification or canonical `.readers`; the commitment selects exactly one
of those bounded candidates. A digest-kind-1 payload identity has nonzero exact
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
housekeeping. It is sufficient to find the same transition after restart without
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
It is host-local coordination state and is not part of the portable v4 bytes.

Live coordination is supported only on a local filesystem whose locking and
file-identity primitives satisfy this section. NFS, SMB, and other remote or
userspace filesystems are unsupported unless an implementation has separately
proved equivalent semantics. The database directory MUST be controlled by the
database owner and not writable by an untrusted principal. The protocol
protects cooperating implementations and detects accidental replacement; it
cannot make a correctly forged replacement by an attacker with directory write
access safe.

The no-follow rule applies to the final main-file and sidecar components.
Implementations resolve and retain the containing directory first, then perform
all final-component operations relative to that descriptor or handle. POSIX
uses the `*at` family with no-follow flags. Windows uses `NtCreateFile` with an
`OBJECT_ATTRIBUTES.RootDirectory` equal to the retained directory handle, a
single relative component, `FILE_OPEN_REPARSE_POINT`, and
`FILE_NON_DIRECTORY_FILE`; reparse points and non-regular files are rejected.
Windows renames use the same retained directory through
`SetFileInformationByHandle(FileRenameInfoEx)`. Trust in any parent path
resolution is part of the caller-controlled-directory precondition; the format
does not claim to defeat a malicious ancestor namespace.

Normal live open MUST NOT create, resize, replace, repair, or silently reset a
missing or malformed sidecar.

### 15.1 Explicit creation and transition APIs

`CreateLive(path, family, value_kind, value_tag, reader_capacity,
cancellation)` creates a new v4 database and its sidecar. Capacity is REQUIRED,
MUST be nonzero, and has no default. Family, kind, canonical 16-byte tag,
capacity, checked sidecar size, and host limits are validated before reservation
creation. The call accepts no writer transaction budget, feeds, ranges, or JSON
metadata and returns only `CreateResult`, never a writer lease. A caller opens
the created database separately with `OpenLiveWriter` and its transaction
budget.

The created main is exactly the canonical empty transaction-1 image for the
supplied family/kind/tag: fresh nonzero database ID and commit nonce,
`page_count == 2`, identical complete meta images on pages 0 and 1, absent
metadata, zero range/free/retirement state, and zero catalog/dictionary state.
Direct files have every membership field zero. Membership files have
`membership_id_limit == 1` and all other membership/catalog counts, limits,
and roots zero. The sidecar has the requested nonzero capacity and every slot
zero before it becomes ready. No initial feed, range, or metadata is implicit.

`InitializeLive(path, capacity, cancellation)` creates a sidecar for an existing
immutable v4 file. It is an explicit offline transition. The caller MUST prove
that no process can still treat the file as an uncoordinated immutable
snapshot; the engine cannot discover immutable readers because they
intentionally do not register. Cooperating implementations take the exclusive
main-file lifetime lock defined below, recheck the main identity/path and
sidecar absence, select the exact meta, and hash the stable exact main bytes.
They then create and synchronize a complete private kind-4 reservation containing
that identity, tuple, length, and digest, publish that same reservation inode at
canonical `.readers` with no-replace, synchronize the namespace, and recheck the
still-locked main path/descriptor identity, link count, length, and tuple before
conversion. The exclusive lifetime lock is retained continuously, so cooperating
actors cannot change the main and the first stable digest remains authoritative;
initialization does not add a redundant second full-file hash. It converts that
exact reservation inode through sidecar state 2 containing the complete attempt
record to state 1 before releasing the main lock. Acquisition is no-replace and
MUST fail if the canonical sidecar/reservation path already exists.

`InitializeLive` and `ResetLiveCoordination` return a structured transition
result containing:

```text
operation: Initialize | Reset
attempted_database_id:[16]byte
attempted_txn_id:u64
attempted_commit_nonce:[16]byte
transition_attempt_id:[16]byte
attempted_reader_capacity:u32
directory_identity_kind:u16
directory_local_identity:[32]byte
destination_basename_encoding:u16
destination_basename:bounded byte sequence
identity_kind:u16
coordination_local_identity:[32]byte
main_local_identity:[32]byte
main_byte_length:u64
main_sha512:[64]byte
previous_coordination_local_identity: optional [32]byte
previous_sidecar_id: optional [16]byte
previous_reader_capacity: optional u32
transition: NotInitialized | OldCoordinationRetained | LeftImmutable | Initialized | OutcomeUnknown
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

The transition-attempt ID is the reservation and eventual sidecar ID. The three
previous-coordination fields are absent for initialization. For reset, previous
coordination identity is present; previous sidecar ID and capacity are present
together only when the old header was selectable. The structured-result
boundary begins after the complete private kind-4 or kind-5 reservation has
been synchronized and its identity is known, before any canonical namespace
change. Earlier failure is an ordinary typed error with the same exact cleanup
report.
The durable kind-4/kind-5 reservation or origin-2/origin-3 sidecar record is
authoritative after a process crash.

`ResolveLiveTransition(path, optional result, Complete | Remove, cancellation)` requires the
same caller-certified offline exclusivity, takes the exclusive main lifetime
lock, and requires equality between caller and on-disk records when both exist.
Before inspecting or changing any main, coordination, or private name, it opens
and retains the containing directory and, when a caller result is supplied,
requires its directory identity kind and local identity to equal the result's
recorded parent identity. A mismatch is typed `DirectoryIdentityMismatch` and
changes nothing. Without a caller result, an authoritative complete canonical or
private reservation record is bound to the identity of the retained directory
that contains it; the returned resolution records that parent identity.
A selectable incomplete kind-4/kind-5 reservation or origin-2/origin-3 state-2
sidecar remains resolvable after a process-domain change. Under the retained
exclusive main lifetime lock and exact coordination operation lock, the
resolver treats every old slot as opaque. `Remove` may retire only the exact
attempt. `Complete` first writes and synchronizes the freshly derived current
domain into the next CRC-valid attempt block, zero-initializes every slot, and
then publishes state 1. It never interprets or reaps an old-domain owner. An
state-1 sidecar that is the attempted transition's new ready coordination and
has a mismatched process domain is already live; this resolver returns typed
`LiveCoordinationDomainMismatchRequiresReset` before slot inspection, whole-file
hashing, or content-lineage classification, and the caller uses offline
`ResetLiveCoordination`. This does not reject the exact old state-1 sidecar named
by an in-progress kind-5 reset record: the resolver may atomically replace that
old coordination while treating its slots as opaque, as the reset rows require.
A private kind-4/kind-5 reservation may be bound to this path without a caller
result only from its complete record, never its random name alone. Kind 4
requires the retained main identity, tuple, length, and digest to match. Kind 5
requires the retained main identity/database and exact old canonical
coordination identity to match; its tuple, length, and digest are then classified
by the reset rows below, so later legitimate live activity does not erase
attempt ownership. Zero or multiple matching records are respectively absent or
conflict.

Its structured result reports independent facts:

```text
primary: OldCoordinationRetained | LeftImmutable | Initialized | OutcomeUnknown
destination_content: Desired | Previous | Absent | Other | Unclassified
later_canonical: None | ReservationOrTransition | ReadyLiveSidecar
live_lineage: optional SameGenerationExactBytes |
                       SameGenerationPhysicalBytesChanged |
                       AdvancedGeneration |
                       UnavailableDomainMismatch
later_attempt_or_sidecar_id: optional [16]byte
later_selected_txn_id: optional u64
later_selected_commit_nonce: optional [16]byte
creation_security_kind:u16
creation_security_commitment:[32]byte
main_access_policy: CreatorOnly | ChangedOrUnproven | Unclassified
coordination_access_policy: Absent | CreatorOnly | ChangedOrUnproven | Unclassified
cleanup_state, cleanup_artifacts, coordination_cleanup, housekeeping,
visible_housekeeping, cause
```

The same orthogonal field meanings are used by `ResolveCreateLive` and
`ResolvePublication`; each retains only its operation-specific `primary` enum.
A later valid owner is never displaced or treated as authority for old-attempt
cleanup. An active/uncertain later live writer returns `WriterBusy` before stable
physical inspection. A ready later sidecar from another process domain reports
`UnavailableDomainMismatch` with destination content `Unclassified`; no slot is
inspected, no whole-file hash is computed, and nothing is mutated. The operation
otherwise returns typed `Conflict`/`Unresolvable`/`TransitionSuperseded` when the
state table requires it.

The exhaustive state table is:

- An exact origin-2/origin-3 state-1 sidecar bound to the same main inode proves
  that the live transition succeeded. The exact attempted tuple and whole-file
  digest reports primary `Initialized` plus
  `SameGenerationExactBytes`; the same bootstrap-valid attempted tuple with
  changed exact bytes reports `SameGenerationPhysicalBytesChanged`; and a later
  bootstrap-valid transaction in the same database reports
  `AdvancedGeneration`. Content change without transaction advancement can
  be a legal unpublished COW draft or damage, so the result does not classify
  its cause and the caller may run explicit validation. Either mode only
  synchronizes and rechecks this successful pair; `Remove` never silently
  undoes it. The attempted transaction ID with a different nonce is `Conflict`.
- For initialization, an exact kind-4 reservation at canonical or private name,
  or an exact origin-2 state-2 sidecar, is incomplete.
  `Complete` restores the same reservation inode to canonical `.readers` when
  necessary, reinitializes every slot, publishes state 1, synchronizes and
  rechecks, then returns `Initialized`. `Remove` retires only that exact
  coordination inode, synchronizes, and returns `LeftImmutable`.
- For reset, an exact old canonical coordination inode plus the exact attempted
  main tuple, length, and digest proves atomic replacement did not occur and the
  main did not advance. `Complete` requires the exact private kind-5
  reservation, retries the exact atomic replacement, and then converts it;
  absence of that required inode is `Unresolvable`. `Remove` removes that exact
  private reservation when present, or accepts its proven absence, then
  synchronizes and rechecks both names before returning
  `OldCoordinationRetained`.
- For reset, if that same old canonical coordination inode and main inode remain
  bound but the main now selects a later bootstrap-valid transaction in the same
  database, ordinary live use advanced after the interrupted reset. `Complete`
  returns typed `TransitionSuperseded` without applying or rewriting the stale
  reservation. `Remove` removes that exact private reservation when present, or
  accepts its proven absence, synchronizes and rechecks both names, and reports
  primary `OldCoordinationRetained` plus `AdvancedGeneration`.
- For reset, if that old coordination/main pair remains bound and bootstrap-valid
  at the exact attempted transaction and nonce but the exact file length or
  digest changed, the stale attempt cannot distinguish a legal unpublished COW
  draft from damage. `Complete` returns typed `TransitionSuperseded` without
  applying or rewriting it. `Remove` performs the same exact private-residue
  cleanup and reports primary `OldCoordinationRetained` plus
  `SameGenerationPhysicalBytesChanged`; the caller may
  intentionally validate before starting a new reset. The attempted transaction
  ID with a different nonce is `Conflict`, not content change or advancement.
- For reset, an exact canonical kind-5 reservation or origin-3 state-2 sidecar
  means the old sidecar was retired. `Complete` finishes conversion and returns
  `Initialized`; `Remove` retires only the new coordination inode,
  synchronizes, and returns `LeftImmutable`.
- If canonical coordination is absent and the retained main is exact and
  unchanged, `Complete` requires the exact private reservation inode, restores
  it to the canonical name, and proceeds from its recorded phase; absence of
  that required inode is `Unresolvable`. `Remove` removes that exact private
  reservation when present, or accepts its proven absence, then synchronizes
  the main and namespace, rechecks both names, and returns `LeftImmutable`.
- A foreign/replaced coordination or main inode is `Conflict`. A locally valid
  but operation/phase-inconsistent combination is `Conflict`. A required exact
  private reservation missing for `Complete` or an unselectable authoritative
  attempt/new-coordination record is `Unresolvable`; none is
  guessed, recreated with a new identity, or removed.

For reset, this does not require the prior old sidecar header to be selectable.
A valid kind-5 attempt with flag bit 1 clear intentionally binds an unselectable
old coordination inode by its exact retained local identity. Under the caller-
certified offline and cross-domain rules above, `Complete` may atomically replace
that exact opaque old inode and `Remove` may retain it while removing only the
exact private attempt.

Direct initialization returns `NotInitialized` when no canonical reservation
namespace attempt took effect; private cleanup is reported independently and
may be `ResiduePossible`. If canonical acquisition or conversion began but
resolution later proves
an unchanged immutable main and durable coordination absence, it returns
`LeftImmutable`. Direct reset returns `OldCoordinationRetained` when the old
exact sidecar is proven canonical; private cleanup is again independent. It
returns `LeftImmutable` only when absence of a new sidecar after old-sidecar
replacement is proven. From the first
canonical namespace attempt through successful state-1 publication, any
indeterminate failure is `OutcomeUnknown`. `Initialized` requires selected
state 1 at exact size plus successful main, coordination, and namespace
synchronization and final identity rechecks. Cleanup failure never changes a
proven primary transition outcome; it is reported by the residue ledger and
prevents a clean retry until resolved.

`ResetLiveCoordination(path, capacity, cancellation)` is explicit and offline. It MUST obtain
the exclusive main-file lifetime lock and MUST establish that no live
cooperating handle remains. It MUST NOT infer safety from timestamps. A caller
that cannot establish offline exclusivity cannot reset. Under the old
coordination inode's operation lock, it treats every old slot as opaque: it
does not inspect, reap, or derive authority from an owner PID, thread ID, start
token, or nonce. It reselects the main meta, truncates any aligned unpublished
tail to exact committed length, and synchronizes the main only under the
caller's certified offline authority. It then creates and synchronizes the
complete private kind-5 reservation, including the exact old coordination
identity and the freshly derived current process domain, before changing the
canonical namespace. Under both operation locks it atomically replaces the old canonical
sidecar with that exact reservation inode and synchronizes the namespace. Thus
canonical `.readers` changes directly from the old sidecar to a durable reset
attempt record; there is no absent-name window and no reset-retirement failure
without an attempt record. It then converts the reservation through origin-3
sidecar state 2 to state 1. A filesystem unable to atomically replace the open
old coordination inode returns `DurabilityUnsupported` before the namespace
attempt. Resolution classifies whether the old sidecar or new reservation won
that atomic replacement; it never guesses from absence.

A live main file and its sidecar MUST each have link count one. This prevents
two hard-link path aliases from deriving independent `.readers` files for the
same inode. Link count and both canonical path identities are rechecked before
writer publication. Direct live rename or relink is unsupported in Phase 1;
ordinary open/reset never rebinds the basename commitment. Relocation requires
`SnapshotTo` at a new immutable path, explicit `InitializeLive` there, and an
application-controlled switch. After caller-certified quiescence, external
tooling may remove the old pair at its own operational risk; Phase 1 provides no
engine-owned old-pair retirement API.

Partial create/initialize output MUST be removed when the operation can prove
it created it. A crash-left partial file is never repaired by ordinary open.

Before creating anything, `CreateLive` generates the database ID, transaction
commit nonce, and random nonzero creation-attempt ID. That one ID is also the
namespace-reservation ID and eventual `sidecar_id`. Its private main name is
exactly `.iprange-live-<id>.tmp`, where `<id>` is the 32 lowercase hexadecimal
digits of the creation-attempt ID. The name is relative to the retained
destination directory and is created exclusively without following a symlink.
This exact name makes a crash-left private main discoverable without a broad
temporary-file guess.

`CreateLive` uses this exact sidecar-before-main publication order:

1. require the final main absent, exclusively publish and synchronize the exact
   ready reservation from section 20.1 at the canonical `.readers` path after
   taking its operation lock, retain that descriptor/lock and local identity,
   then recheck final-main absence;
2. exclusively create the main file under the exact private name above, fully
   initialize it, compute its exact SHA-512 digest, and synchronize that inode;
3. while still holding the reservation inode's operation lock, recheck the reservation,
   private-main identity/content, and final-main absence; update and synchronize
   the still-state-1 reservation with the complete main identity/length/digest;
   write and synchronize a valid sidecar `state == 2` image with the complete
   main attempt record through the alternate 4,096-byte header block;
   grow that **same inode** to the exact reader-table size, zero and synchronize
   every slot while state 2 remains selected; then publish and synchronize the
   ready `state == 1` image through the alternate header block;
4. while still holding that operation lock, recheck the ready sidecar,
   private-main identity/content, and final-main absence; publish and
   synchronize sidecar `state == 3` (main namespace may have been attempted)
   through the alternate header block;
   atomically publish the same main inode at the final name with no-replace;
   synchronize the namespace; publish and synchronize restored sidecar
   `state == 1` through the alternate block; and release the lock.

The canonical reservation exists from the beginning of the attempt and is
never absent while the private main is being prepared or converted. It therefore
serializes the whole operation against every immutable publisher, competing
`CreateLive`, `InitializeLive`, and replacement. Publishing the valid main last
ensures every crash-left partial state fails ordinary immutable and live open:
before step 4 the main is absent, while the reservation, transition sidecar, or
ready orphan sidecar blocks unrelated publication. Publishing the main first is
forbidden.

Because the main file and sidecar are two directory entries, `CreateLive`
cannot promise one two-file atomic rename. Its result contains:

```text
attempted_database_id:[16]byte
attempted_txn_id:u64
attempted_commit_nonce:[16]byte
create_attempt_id:[16]byte
attempted_reader_capacity:u32
directory_identity_kind:u16
directory_local_identity:[32]byte
destination_basename_encoding:u16
destination_basename:bounded byte sequence
identity_kind:u16
coordination_local_identity:[32]byte
attempted_main_local_identity: optional [32]byte
attempted_main_byte_length: optional u64
attempted_main_sha512: optional [64]byte
creation: NotCreated | Created | OutcomeUnknown
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

The structured-result boundary starts after the private ready reservation has
been synchronized and its identity is known, before its canonical no-replace
publication. A failure to publish it canonically therefore returns a resolvable
`NotCreated` record. An earlier failure is an ordinary typed error and
the exact cleanup report; it cannot fabricate sentinel identities or digests. The
optional main fields become present together only after the exact private main
is synchronized. Its digest covers every byte. `Created` requires
both fully initialized files and the final synchronized namespace. From the
selection of the synchronized state-3 write-ahead record until resolution, an
indeterminate failure is `OutcomeUnknown`, including a failure before the
process can prove whether it entered the final-main namespace call. A pre-state-3
failure is `NotCreated` whenever final-main non-publication is proven; failed
cleanup is reported independently as `ResiduePossible` and does not falsify
that primary outcome. Ordinary open fails safely on every partial pair.

`ResolveCreateLive(path, optional result, Complete | Remove, cancellation)` is the only online
operation permitted to resolve that exact partial attempt. A complete caller
result may identify it, but is not required after a process crash. A valid
canonical initial kind-3 reservation reconstructs ID, tuple, capacity, and
derived names and is sufficient for `Remove`; `Complete` additionally requires
either its synchronized nonzero output fields, a sidecar attempt record, or a
fully validated and hashed exact private main from which those fields are
reconstructed. It retains the directory and, before inspection or action,
requires a supplied result's directory identity kind and local identity to equal
that retained parent. A mismatch is typed `DirectoryIdentityMismatch` and
changes nothing. Without a caller result, the authoritative complete canonical
reservation/sidecar record is bound to the identity of the retained directory
that contains it, and that parent identity is returned. The resolver opens every
present canonical component plus the exact private
reservation/main without following symlinks,
and matches the create-attempt/reservation/sidecar ID, identity kind, and exact
coordination identity, then compares every available main identity, tuple,
length, and digest under the phase-specific rules below. Sidecar
conversion uses the exact reader capacity from the caller result or
reconstructed attempt record; the resolver never substitutes a default or
caller override. A matching reservation, transition sidecar, or ready sidecar
is locked with its operation lock before the phase-specific inspection or any
namespace change. When that exact
coordination inode is at its private reservation name,
`Complete` first restores the same inode to canonical `.readers` with the
no-replace publication and synchronization protocol. Its operation-specific
primary is `Created | Removed | OutcomeUnknown`, or a typed
`Conflict`/`Unresolvable`/`WriterBusy` error. The structured result uses the
common independent `destination_content`, `later_canonical`, `live_lineage`,
later-owner identity, cleanup, coordination, and housekeeping fields defined by
`ResolveLiveTransition`. Cleanup never changes an already proven primary.

If canonical coordination is absent and no caller result survives, path-specific
Create resolution performs a bounded streamed scan of exact private reservation
names. It opens only complete selectable kind-3 records
whose retained parent identity and section-3 destination-name commitment match
the requested path. Zero matches means absent, exactly one reconstructs the
attempt authority, and more than one is `Conflict`; only that record's attempt ID
may derive and inspect the private main. Random filenames or unbound private-main
bytes remain insufficient authority.

A selectable kind-3 reservation, origin-1 state-2/state-3 sidecar, or origin-1
state-1 sidecar while the final main is absent remains resolvable after a
process-domain change. Under its exact operation lock and every phase-required
main lifetime lock, the resolver treats old slots as opaque. `Remove` may retire
only the exact attempt in the existing absent-final-main removal row; a state-3
sidecar paired with the exact final main remains a successful creation crash
state and both modes finish state 1. Whenever the exhaustive rows finish the
transition, the resolver first writes and synchronizes the freshly derived
current domain into the next CRC-valid attempt block, zero-initializes every
slot, and then continues to state 1. Only an exact state-1 sidecar paired with a
present valid final main has established ready live coordination; a mismatched
domain there returns typed
`LiveCoordinationDomainMismatchRequiresReset` before slot inspection, whole-file
hashing, or lineage classification; it is handled by caller-certified offline
`ResetLiveCoordination`, not by CreateLive resolution.

Before classifying a final main paired with a state-1 sidecar, the resolver
takes the shared main lifetime lock and then the sidecar operation lock. Under
that lock it strictly scans the complete sidecar, reaps the writer lease only
with the section-15.3 OS death proof, and requires the lease to be free. An
active or uncertain writer returns typed `WriterBusy` before tuple or physical-
content classification. The resolver keeps both locks through meta selection,
the complete length/digest pass when needed, file and namespace
synchronization, and final identity/tuple rechecks. This prevents a new writer
claim and makes the physical comparison stable while allowing registered
readers to coexist.

- If the final main and exact state-1 sidecar form the valid identity-bound pair,
  the sidecar state proves that creation completed. The same selected attempted
  tuple plus the exact prepared length/digest reports primary `Created` plus
  `SameGenerationExactBytes`; the same bootstrap-valid selected tuple with
  changed exact bytes reports `SameGenerationPhysicalBytesChanged`; and a later
  bootstrap-valid transaction in the same database reports
  `AdvancedGeneration`. Physical change without selected-
  tuple advancement can be an aligned unpublished tail, an inactive-meta write,
  another unpublished COW draft, or damage; the resolver reports the fact but
  does not guess its cause. Either mode synchronizes and rechecks this successful
  pair, and `Remove` never silently undoes it. The attempted transaction ID with
  a different nonce is `Conflict`.
- If the final main and exact state-3 sidecar form the valid pair, ordinary live
  use was never possible. The exact attempted tuple, identity, length, and digest
  are therefore still mandatory. The resolver synchronizes the reopened main
  and namespace, restores and synchronizes state 1 under the operation lock,
  rechecks all identities, and reports primary `Created` plus
  `SameGenerationExactBytes`; any mismatch is
  `Conflict`.
- If the final main is absent and the exact private main plus exact coordination
  inode are present, `Complete` finishes or repeats the in-place reservation-to-
  sidecar conversion and performs steps 3-4. Before publishing through an
  already-ready sidecar it synchronizes that retained sidecar under the lock.
- If the final main is absent, `Remove` unlinks only the exact private main,
  then retires only the exact reservation/transition/ready coordination inode
  owned by this result using the platform reservation-retirement protocol, and
  synchronizes the namespace. Either component may already be absent.
  `Complete` cannot proceed when any complete main identity/length/digest field
  or the private main itself is absent.
- A present final main that is neither the exact attempted generation nor a
  valid later generation on the exact attempted inode, or a foreign, replaced,
  malformed, or mismatched private main or coordination inode, is never removed
  or repaired by this API. It returns a typed conflict requiring caller-certified
  offline handling.

Every other locally valid but phase-inconsistent combination is also
`Conflict`. In particular, an exact final main paired only with a kind-3
reservation or state-2 sidecar is not a legal CreateLive crash state: main
publication is authorized only after selected sidecar state 3. The resolver
does not infer or repair a missing phase transition.

`ListAbandonedCreateTemps(directory, cancellation, sink)` is an explicit read-only offline aid. It
enumerates only exact `.iprange-live-<32 lowercase hex>.tmp` names and reports
the no-follow regular-file identity plus a bootstrap-readable v4 tuple when one
exists; it changes nothing. `RemoveAbandonedCreateTemp(directory,
expected_directory_identity, id, expected identity, optional readable tuple,
cancellation)` applies the section-14.4 idempotent
cleanup rule only to that exact matching private file. Exact name plus no-follow identity
is sufficient for a partial file with no readable tuple; when a tuple is
readable it MUST also match.
The caller MUST first certify that no create or resolve operation is active in
the directory. It never removes a canonical main or sidecar and never infers
abandonment from age. `ResetLiveCoordination` is not a creation-cleanup API.

`InspectCreateResidue(path, cancellation)` is an explicit read-only offline aid for the
canonical coordination name. It first retains the containing directory and
reports its identity kind/local identity, the coordination inode's no-follow
local identity, any fully readable reservation or sidecar attempt record,
derived private names, and exact main comparison without changing them. It also
returns an optional non-serializable opaque residue handle retaining that exact
directory and coordination descriptor only when canonical coordination was
successfully opened. Canonical absence produces a directory-only report and no
handle. A valid reconstructed record may be passed
directly to `ResolveCreateLive`.

`RemoveCreateResidue(residue_handle, cancellation)` may remove a canonical residue only with
that present handle and after the caller certifies that the final main is absent
and no create, initialize, reset, publication, resolver, live handle, or
immutable reader is active in the destination namespace. It takes the retained
coordination operation lock, rechecks the canonical path against the retained
descriptor and parent, and rechecks final-main absence before acting. If either
header selects any reservation or sidecar record—including a kind-3/origin-1
CreateLive record—the operation returns `Conflict`; the caller must use that
record's operation-specific resolver. Only a coordination inode with neither
header selectable is eligible for this offline escape hatch. The retained
same-process descriptor from `InspectCreateResidue` is then the only SDK
mutation authority; a serialized local identity, raw untrusted attempt ID, or
operator record is insufficient because POSIX inode identities can be reused.
A process restart therefore requires a fresh inspection of the current
canonical inode. Age and pathname alone are never proof.

The inspection result owns its opaque residue handle. `Close`/C-ABI destroy is
idempotent, closes only the retained descriptors, and performs no namespace or
slot transition; an inspect-only caller MUST invoke it. `RemoveCreateResidue`
and `RemovePublicationResidue` borrow a mutable handle and mark it closed only
after successful final proof. On error the same handle remains open and
retryable. Language destruction may only close these descriptors because a
residue handle owns no active slot and no process-local state-2 provenance.

### 15.2 Sidecar header

The 8,192-byte sidecar header region contains two independently CRC-protected
4,096-byte blocks at physical offsets 0 and 4,096, so one torn 4 KiB
storage-block write cannot damage both. Each block contains a 512-byte header
record followed by zero bytes `[512,4096)`. Table offsets below are relative to
the start of either block:

| Offset | Size | Field |
|---:|---:|---|
| 0 | 8 | ASCII `IPR4RDRS` |
| 8 | 2 | `sidecar_version = 1` |
| 10 | 2 | `header_record_size = 512` |
| 12 | 2 | `slot_size = 64` |
| 14 | 2 | `identity_kind` |
| 16 | 4 | explicit `capacity` |
| 20 | 4 | `state` (`1=ready`, `2=initializing`, `3=main namespace may have been attempted`) |
| 24 | 16 | main-file `database_id` |
| 40 | 32 | local main-file identity |
| 72 | 32 | local sidecar identity |
| 104 | 16 | random nonzero `sidecar_id` |
| 120 | 2 | `origin` (`1=CreateLive`, `2=InitializeLive`, `3=ResetLiveCoordination`) |
| 122 | 2 | `attempt_record_version = 1` |
| 124 | 8 | initial/attempted `txn_id` |
| 132 | 16 | initial/attempted `commit_nonce` |
| 148 | 8 | initial/attempted main byte length |
| 156 | 64 | SHA-512 of exact initial/attempted main bytes |
| 220 | 2 | `process_domain_kind` (`1=Linux PID namespace`, `2=FreeBSD jail`, `3=host-global`) |
| 222 | 2 | zero |
| 224 | 32 | `process_domain_token` |
| 256 | 2 | destination-basename encoding kind |
| 258 | 2 | zero |
| 260 | 4 | destination-basename encoded byte length |
| 264 | 32 | destination-basename SHA-256 commitment from section 3 |
| 296 | 2 | creation-security kind from section 15.6 |
| 298 | 2 | zero |
| 300 | 32 | creation-security commitment from section 15.6 |
| 332 | 164 | zero |
| 496 | 8 | nonzero `header_seq` |
| 504 | 4 | zero |
| 508 | 4 | block CRC-32C |

CRC covers the complete 4,096-byte block with `[508,512)` zero. Header selection
examines both block positions and recognizes both sidecar and reservation magic
during conversion. It selects the CRC-valid block with the greater sequence;
when both valid sequences differ, the greater MUST be exactly the checked
`lower + 1`. Equal sequences are legal only for byte-identical 4,096-byte
blocks. A zero sequence, sequence gap, sequence wrap, or equal-sequence
disagreement is malformed.
The creation-security kind/commitment is immutable across all selected sidecar
phases and every later normal writer header update. A mismatch between valid
header copies or the attempt's reservation/result is malformed.

Every header/phase transition writes one complete prospective 4,096-byte block
with `header_seq = selected + 1` into the other position, synchronizes it, and
re-reads it as selected before taking the action that transition authorizes. It
never overwrites the sole selected block. A torn new block therefore leaves the
previous complete attempt record authoritative. If neither block is valid, the
coordination inode is corrupt and only caller-certified offline reset/removal
may act; the normal protocol never creates that state.

If the selected sequence is `u64::MAX`, a transition returns typed
`CoordinationSequenceExhausted` before changing the selected block or taking the
authorized action. Sequence numbers never wrap.

States 2 and 3 are recognized only by exact `ResolveCreateLive` for origin 1 or
`ResolveLiveTransition` for origins 2-3. State 3 is valid only for origin 1;
origins 2-3 with state 3 are conflicts. State 2 is allowed at 8,192 bytes, the
exact final size, or an intermediate checked size no greater than that final
size; the resolver reinitializes every slot rather than trusting partial bytes.
Ordinary live open accepts only the selected state 1 at exact final size.

The attempt record is written before sidecar state 2 is synchronized. It binds
an interrupted conversion/publication to exact main content without an
in-memory result. For state 1 it is retained as creation provenance but is not a
current-main digest and is never required to remain equal after live use becomes
possible; resolvers classify the selected tuple and any physical-content change
as specified above. For states 2 and 3 it is mandatory recovery evidence;
unknown origin/version, zero
transaction/nonce/length, invalid destination-basename commitment, or a
mismatched exact main is a conflict. State 1 retains the same basename
commitment so ordinary live open and every resolver remain bound to the
canonical main component even after later transactions advance.

`identity_kind == 1` is POSIX. In each 32-byte identity, bytes `[0,8)` are
`st_dev:u64`, bytes `[8,16)` are `st_ino:u64`, and the remainder is zero.
`identity_kind == 2` is Windows. Bytes `[0,8)` are the volume identity,
bytes `[8,24)` are the 128-bit file identity, and the remainder is zero. Other
kinds are invalid. The sidecar records its own identity obtained from the
retained descriptor after exclusive sidecar creation or reservation creation;
copying a valid header onto a different inode therefore does not create a valid
replacement.

The process domain makes stored PIDs meaningful. On Linux, kind 1 is mandatory
and the token is the POSIX local identity encoding (`st_dev`, `st_ino`, then
zeros) of the caller's own `/proc/self/ns/pid`; inability to obtain it makes live
coordination unsupported. On FreeBSD, kind 2 is mandatory and the token is
`ki_jid:u32` from the caller's canonical `kinfo_proc` followed by 28 zero bytes.
JID zero denotes the host. macOS and Windows use kind 3 with an all-zero token
because their supported Phase-1 host processes use one host-global PID domain;
an isolated environment where that cannot be established returns
`LiveCoordinationUnsupported`. Every ordinary opener, resolver, and operation
that interprets or reaps slots requires its freshly derived kind/token to equal
the sidecar first. Cross-PID-namespace, cross-jail, and cross-container ordinary
live coordination is unsupported even when the main/sidecar files are visible
in both environments; immutable files remain portable. The one explicit
exception is offline `ResetLiveCoordination`: after caller-certified quiescence
and the exclusive locks in section 15.1, it treats old-domain slots as opaque,
atomically retires that exact old sidecar, and writes the current domain into
the kind-5 reservation and resulting sidecar. This is the supported recovery
path after a reboot, PID-namespace recreation, jail change, or intentional
offline domain migration.
Each explicit create/initialize/reset generates a cryptographically random
nonzero sidecar ID. Handles cache and recheck it together with both OS
identities before critical operations.

The exact sidecar size is `8192 + (capacity + 1) * 64`, checked in `u64` and
against host file-offset and mapping limits. The first 64-byte slot begins at
offset 8,192 and is the single writer lease; the following `capacity` slots are
reader registrations.
Open MUST reject a symlink, non-regular file, link count other than one, wrong
size, incompatible header, database-ID mismatch, either local identity
mismatch, equal main/coordination local identities, or a main-file link count
other than one. It also recomputes the section-3 commitment from the exact
caller-supplied main basename and rejects a kind, length, or digest mismatch.
Main and coordination must be distinct inodes/lock domains.

Every sidecar is created by reservation conversion. Conversion keeps the
complete updated reservation block, writes and synchronizes sidecar state 2
through the other block while the file is still 8,192 bytes, then grows, zeroes,
and synchronizes the slots before publishing state 1 through the alternate
block. Publication or conversion synchronizes the namespace as required. A
selected zero, state-2, state-3, or unknown state is not ready and ordinary
open MUST fail; it MUST NOT finish initialization.

### 15.3 Writer lease and reader slots

The writer lease and every reader slot share this 64-byte encoding:

| Offset | Size | Field |
|---:|---:|---|
| 0 | 4 | `state` (`0=free`, `1=active`, `2=transition`) |
| 4 | 4 | zero |
| 8 | 8 | `txn_id` (`0` while registering) |
| 16 | 8 | process ID |
| 24 | 8 | OS process-start token, or zero if unavailable |
| 32 | 8 | thread/task ID, or zero if unavailable |
| 40 | 16 | cryptographically random nonzero per-claim nonce |
| 56 | 4 | reserved zero |
| 60 | 4 | slot CRC-32C |

An exact all-zero slot is free and has no CRC. For an active slot, CRC is
calculated over the prospective state-1 image with `[60,64)` zero. Active
process ID and the 128-bit nonce are nonzero, and all reserved fields are zero.
The stored process ID MUST convert exactly, without truncation or sign change,
to the host process-ID type used by `kill`/`OpenProcess`; a nonzero thread/task
ID MUST likewise fit its host native type before any host use. An unrepresentable
value is malformed and is never truncated into authority over another process.
Nonce generation uses a CSPRNG and failure occurs before transition provenance
is armed or any slot byte is changed. State zero
with any nonzero byte, state 2 observed after acquiring the operation lock
without this process's exact armed transition provenance, an unknown state, or
an active slot with invalid fields or CRC is malformed and fails closed.
Ordinary open never treats it as free or repairs it.

All slot transitions occur under the operation lock. State 2 is the exact
transient updating value and is never a valid stable slot. Claim first writes
`state=2`, then writes the body plus the CRC calculated for the prospective
state-1 image, and writes `state=1` last. Updating an active slot first writes
`state=2`, writes the new body and prospective state-1 CRC, then restores
`state=1` last. Clearing first writes `state=2`, zeroes bytes `[4,64)`, then
writes `state=0` last. A crash or torn write is thus either the old valid active
image, the new valid image, or a malformed image that fails closed; it is never
silently accepted as a free slot. Cross-process visibility ordering is
established by the operation lock and the platform's documented positional-I/O
or shared-mapping barriers.

Before update or clear, a handle MUST compare the slot's state, process/start
identity, nonzero nonce, and role with the values it claimed. A mismatch is not
another handle's slot and fails closed.

Every open attempt, established live handle, and cleanup guard that can write a
slot owns one in-memory **transition provenance** record. It contains the exact
sidecar/header identity, role and slot index, transition kind
(`Claim | Update | Clear`), expected source image (`Zero` or the owned active
claim, or an exact `ProvenDeadActive` claim), prospective active owner identity
and nonce when applicable, and an `armed` bit. `ProvenDeadActive` stores the
complete valid old slot image and the exact section-15.3 OS death-proof class and
observations established for that image. Under the object's mutex and the
operation lock, the implementation populates this record and sets `armed=true`
immediately before the first attempt to write state 2. It clears the record only
after it reads back the complete target state 1 or all-zero image. An error
before that proof retains both the armed record and the already-held operation
lock; retained descriptor, lifetime lock, and object ownership are not released.
An armed transition MUST NOT release and later reacquire the operation lock.
Otherwise a first write that in fact changed nothing could permit a second
process to begin a transition in the same slot, leaving an indistinguishable
state-2 image that the first process could incorrectly clear. The cleanup guard
owns the held lock until readback proves the original transition complete,
or absent through the exact target/all-zero readback rules below.

An armed transition makes its owner authoritative to abandon that transition
by proving the slot absent or clearing it to zero under the same retained
descriptor and identities. Retry accepts an all-zero slot, a well-formed active
image with the same role/owner/nonce (using either the old or prospective
transaction during an interrupted update), or state 2 from that exact armed
transition. Because the armed owner continuously holds the exclusive operation
lock, a different active nonce cannot be a conforming later reuse; it proves
foreign mutation or broken lock discipline and is `CleanupConflict`. Any state-2
image without matching in-process armed provenance, or any other malformed or
foreign image, remains fail-closed. Thus a process may finish only a transition
it initiated; a new process can never infer ownership of crash-left state 2.

Nonce uniqueness is a cryptographic probabilistic identity assumption, not a
deterministic global registry: an exact independent 128-bit collision cannot be
distinguished from the original claim. The accepted collision probability is
`2^-128` per independent draw under the required CSPRNG. Conformance therefore
injects generator failure and all-zero-output rejection before any write, but does not claim
that a forced exact collision is safely detectable.

Reaping a proven-dead foreign claim is a `Clear` transition with
`ProvenDeadActive` as its source. The reaper MUST recheck the exact active image
and establish the canonical death proof while holding the operation lock, then
perform the role-specific pre-clear work below, and only then arm the provenance
record before its first state-2 write.

Reader-slot reaping has no main-file cleanup. Before clearing a proven-dead
writer lease, however, the reaper MUST retain the main descriptor/lifetime lock,
reselect the committed meta under the same operation lock, and compare the
physical length with that exact generation's committed length. Any longer
aligned region is recorded as an exact `UnpublishedMainTail` and is truncated,
main-file synchronized, and identity/length/meta rechecked through the section-
14.4 rule while the old valid lease still establishes exclusive writer
authority. The writer `Clear` transition MUST NOT be armed before that proof.
An unselectable generation, unaligned/short main, unexpected growth, or cleanup
failure stops the enclosing operation without clearing the lease and transfers
an opaque guard containing the exact `ProvenDeadActive` source plus any tail
obligation. Guard retry repeats the tail proof before it may arm `Clear`.
Already-written inactive pages or an inactive meta within committed length are
not rewritten by ordinary reaping; selected-meta authority and explicit
validation remain separate.

An error after arming also stops the enclosing operation. A failed open returns
`LiveOpenCleanupRequired`; a standalone resolver or inspection/mutation
operation returns typed `LiveCoordinationCleanupRequired`; both carry the same
opaque guard. An established live handle retains the reaping record itself,
becomes cleanup-only, and finishes the foreign writer-tail/clear obligation
before clearing its own claim. No scan, resolver, commit, reclamation pass, or
stale-owner reap may discard a pre-clear writer obligation, an armed transition,
or reduce either to a generic I/O error.

The writer lease uses a nonzero selected transaction in `txn_id`. A reader uses
transaction zero only during registration, then publishes its selected nonzero
transaction in the same slot. A valid active lease or slot is cleared or reaped
only through this protocol.

Process-start tokens are platform-canonical so Go and Rust compare the same
value. Linux stores unsigned `/proc/<pid>/stat` field 22 clock ticks, parsing
after the final `)` of the command field. macOS reads
`proc_pidinfo(PROC_PIDTBSDINFO)` and encodes
`pbi_start_tvsec * 1_000_000 + pbi_start_tvusec`. FreeBSD reads
`sysctl(CTL_KERN, KERN_PROC, KERN_PROC_PID, pid)` and encodes
`ki_start.tv_sec * 1_000_000 + ki_start.tv_usec`. The microsecond fields MUST
be in `0..999999`; multiplication and addition are checked. Windows reads the
creation time from `GetProcessTimes` and stores the unsigned 100-nanosecond
`FILETIME` value. If that exact nonzero token cannot be obtained, the writer
stores zero. Thread/task ID never participates in death proof.

A zero stored token or a zero/unreadable current token never proves staleness.
Start-token mismatch proves PID reuse only when both the valid stored slot token
and the freshly read token are nonzero. On POSIX,
`kill(pid, 0)` returning `ESRCH` proves death; success and `EPERM` mean the PID
exists, after which that two-nonzero start-token mismatch proves that the recorded
instance is gone. On Windows, only a successfully opened process handle may
prove death: a signaled handle proves termination, and a nonzero creation-time
mismatch with both values nonzero proves PID reuse. `OpenProcess` failure, access denial, unavailable
times, `WAIT_FAILED`, and every other uncertain result mean alive. Equality or
uncertainty always means alive.

### 15.4 One locking protocol

The protocol uses two open-description-associated whole-file locks and the
on-disk writer lease:

- the **lifetime lock** is held shared on the retained main-file descriptor for
  the complete lifetime of every live handle and exclusively for
  initialize/reset; and
- the **operation lock** is held exclusively on the retained sidecar descriptor
  for writer-lease claim/removal, reader registration/update/removal, stale
  reaping, oldest-reader scans, reclamation decisions, and writer publication.

On POSIX local filesystems these locks use `flock(LOCK_SH/LOCK_EX)` semantics.
On Windows they use `LockFileEx` on the exact one-byte range
`[2^44, 2^44 + 1)` of the respective main or sidecar handle: shared mode for a
shared lifetime lock and exclusive mode for every exclusive lock. This range
starts immediately after the maximum legal v4 main-file length and is also
above the maximum sidecar length. A platform without equivalent
open-description-associated automatic release MUST return typed
`LiveCoordinationUnsupported`; it MUST NOT silently fall back to
process-associated POSIX record locks. Traditional `F_SETLK` locks are
forbidden because closing an unrelated descriptor for the inode can release
them.

Each live handle owns an in-process mutex that encloses every acquisition,
transition, and release involving its retained operation-lock descriptor.
The descriptor or Windows handle used for locking MUST NOT be duplicated into
or shared with another logical handle; another handle performs an independent
no-follow open. This local mutex is required because reacquiring `flock` or
`LockFileEx` through the same open description is not a thread mutex. Lookup
methods do not take this mutex and may run concurrently; writer methods and
close/registration transitions obey section 15.6's per-handle serialization
contract.

Single-writer exclusion is the dedicated writer lease, inspected and claimed
under the operation lock. An active lease owned by a process not proven dead
returns typed `WriterBusy`. A crashed writer is reaped by the same strict
OS-proof rule as a reader slot.

Every handle retains the originally opened main-file descriptor, containing
directory descriptor, and sidecar descriptor. It MUST NOT reopen the sidecar
pathname to update or clear a slot. Path checks use the retained directory
descriptor and no-follow relative stat. Before every critical operation it MUST
compare both canonical path identities, link counts, database ID, and sidecar
header identities with the retained descriptors. Missing, aliased, or replaced
paths fail closed. Cleanup MAY clear the handle's old retained slot through its
descriptor but MUST report the replacement.

Every engine-owned descriptor is close-on-exec on POSIX and non-inheritable on
Windows. Every live reader/writer, cleanup guard, residue handle, and persistent child
handle caches its creator process ID. Each public/content/cleanup method compares
the current process ID before taking an inherited mutex, lock, or writing any
byte; a mismatch returns typed `ForkedHandle` and performs no slot, file, or
namespace mutation. In a POSIX fork child, destructor/finalizer/C-destroy on the
copied object may only close that child's inherited descriptors and mappings; it
MUST NOT unlock the shared `flock` open description, clear a parent-owned slot,
commit, unlink scratch, or finish an armed transition. The parent copy remains
the sole authority. A fork child that needs v4 access opens independent handles.

### 15.5 Registration and commit barrier

A live reader:

1. opens and bootstrap-validates the main file without following symlinks;
2. takes a shared lifetime lease;
3. opens and validates the existing sidecar and both retained identities;
4. takes the exclusive operation lock and scans the writer lease and complete
   explicitly sized reader table for malformed state;
5. claims a free reader slot with transaction zero and makes it visible;
6. selects a meta page;
7. updates the same slot to the selected transaction;
8. releases the operation lock; and
9. establishes data-page read access limited to the selected committed byte
   range.

An ordinary live reader has not proved the selected graph/allocation partition.
A self-consistently corrupt graph can therefore point at a page that committed
allocator metadata also authorizes a concurrent writer to overwrite. A safe-
language implementation MUST NOT create a shared reference or ordinary slice
into live mapped bytes unless every byte covered by that reference is proven
immutable for its complete lifetime. Rust and Go ordinary live readers use
fixed caller-/cursor-owned page buffers with positional reads, or an equivalent
raw-copy mechanism that cannot create a language-level alias with a concurrent
writer. A raw mapping may remain an internal optimization only when it preserves
the same memory-safety property on plausible corruption. Immutable readers may
borrow their certified-offline bytes normally.

If no slot is free, open returns typed `ReaderCapacityExhausted`.

All fallible setup that can occur before step 5 does so before the slot is
published. If selection, slot update, read-view setup, allocation, identity recheck, or
any later open step fails after the claim may be visible, open keeps the
operation-lock descriptor and first tries to clear and read back that exact
claim under the operation lock. Proven clearing returns the original open error.
If clearing or its final identity/slot recheck cannot be proved, the error is
`LiveOpenCleanupRequired` and carries an opaque cleanup guard rather than a
reader handle.

The guard retains the original directory, main, and sidecar descriptors plus
claim kind (`ReaderSlot` or `WriterLease`), exact slot index, database ID,
sidecar ID, both local identities, transaction, process/start/thread tokens, and
claim nonce, the complete transition-provenance record from the failed open
attempt, plus any exact unpublished-main-tail cleanup obligation. For a failed
foreign-writer reap this includes the complete `ProvenDeadActive` source even
when tail cleanup failed before the `Clear` provenance could be armed. It exposes
only idempotent `RetryCleanup` and `Close`. `Close` MUST execute the same
proof-and-cleanup protocol as `RetryCleanup`; it is not an abandon operation. Both methods retain
the guard, its descriptors, and retry authority on every error, and mark it
closed/release ownership only after proven success. Neither method may consume
or discard retry authority on error. The guard cannot read or mutate database
content.

Under its per-guard mutex, retry retains an inherited armed transition's already-
held sidecar operation lock. If no transition was armed, it acquires the retained
sidecar operation lock. It then rechecks descriptor/header identity and applies
the transition-provenance rule above. If the failed opener had already completed
its claim/update and a later setup step failed, retry arms a new `Clear`
transition before writing state 2. If claim, update, or that clear itself stopped
in state 2, the inherited armed record and continuously held operation lock
authorize completion to zero. This authority remains deliberately process-local.

A different active nonce is `CleanupConflict` and MUST NOT be cleared. No
conforming claimant can reuse the slot while the guard continuously owns the
exclusive operation lock. A malformed nonzero image, unknown state, or any
ownership mismatch also remains `CleanupConflict`.

The guard clears the exact claim through the retained descriptor, but for a
writer it first resolves any recorded tail by the section-14.4
identity/length/truncate rule while the lease remains owned; a new `Clear`
transition cannot be armed until that tail obligation is proven resolved. It
then reads back zero and reports whether the canonical path was unchanged or
replaced. Success releases the retained lifetime lock and descriptors. Until
success or process exit, the claim intentionally continues to block reclamation
or writers rather than being guessed stale. Destructors/finalizers MUST NOT begin
a slot or lease transition. Callers MUST explicitly resolve or close the guard,
and the C ABI exposes it as an opaque owned handle. Dropping an armed cleanup-
only guard abandons process-local provenance and requires caller-certified
offline coordination reset.

Established live readers and writers use the same rule. Their `Close` operation
is idempotent and non-abandoning: it takes a mutable/non-consuming handle, marks
the handle cleanup-only before its first clear attempt, and does not mark it
closed or release ownership until exact all-zero readback proves the old claim
absent. On error the same handle, descriptors, lifetime lock, and
armed provenance remain available, and only another `Close` attempt is allowed.
Before a writer arms its own lease `Clear`, `Close` MUST truncate and synchronize
every recorded `UnpublishedMainTail` through the section-14.4 exact
identity/length rule while that lease or its armed transition provenance remains
owned. Tail-cleanup failure retains the close-only writer and leaves its lease
uncleared; it never releases the only authority needed for a safe retry.
Rust close therefore cannot consume the handle on error; the C ABI cannot
destroy an unresolved opaque handle; Go retains the receiver. Finalizers and
destructors never begin the transition. Dropping an unclosed established handle
leaves its valid claim fail-closed until process exit or caller-certified
offline reset. Phase 1 has no hidden process-global cleanup registry.

A writer with any still-owned feed reference, membership reference, membership
builder, or other persistent child returns `HandleBusy` from Close before Abort,
lease clearing, or any state change. Commit and explicit Abort may terminate the
operation and invalidate those children, but their wrappers remain parent borrows
until explicitly destroyed; Close continues to return `HandleBusy` until then.
No new child is admitted once Close begins.

The same cleanup-only transition applies if the writer-lease transaction update
after durable meta publication fails at state 2 or has an uncertain final
state-1 write. Commit still reports the factual `Committed` durability and the
coordination cleanup error independently; that writer permits only retrying
`Close`. A successful exact clear closes it, after which the caller may reopen
the committed database. No post-publication coordination failure changes the
commit result to `NotCommitted` or permits the transaction to be retried.

A live writer open may use an initial bootstrap only to identify the candidate
database and sidecar. Before truncating, mapping for mutation, or exposing the
writer, it MUST take the shared lifetime lock and exclusive operation lock,
validate and scan the complete sidecar, reap only proven-dead owners, reselect
the main meta from the retained descriptor, and then claim the dedicated writer
lease with that exact selected transaction. It rechecks both canonical path
identities and link counts and only then truncates a provably unpublished tail
to the reselected committed length. Any failure after lease publication follows
the exact cleanup-guard protocol above and never exposes a writer. This
reselection prevents a stale pre-lock bootstrap from truncating or mutating over
a newer commit.

Internal live registrations used by validation, recovery, or snapshotting obey
the same rule. Their terminal error carries the cleanup guard when claim clearing
cannot be proved; no internal helper may discard it or reduce it to a generic I/O
error.

During commit the writer again takes the operation lock, verifies that its
lease nonce/owner/transaction and selected main generation are unchanged, and
holds the lock from its oldest-reader scan through durable meta publication and
writer-lease transaction update. This prevents a new transaction-zero
registration from appearing inside allocator finalization or publication while
already registered readers continue without blocking.

Registration, update, removal, reaping, and oldest-reader scans all use the
same operation lock. A slot is reaped only by the exact OS proof above;
unsupported checks and transient errors mean alive.

For a structurally and CRC-valid active slot, proven-dead owner reaping occurs
before comparing its transaction with the selected main meta. This precedence
is required for a writer that dies after durable meta publication but before
phase-5 lease update: its otherwise valid lease carries the previous
transaction and is safely cleared as dead. If the owner is not proven dead, the
transaction checks below apply. A state-2, CRC-invalid, or structurally invalid
slot cannot use this exception because its claimed owner is not trusted.

Every active reader transaction MUST be zero during registration or no greater
than the currently selected main transaction. A larger value or a writer-lease
transaction that does not match the selected generation is malformed and fails
closed, except for the brief lease update performed under the operation lock
immediately after durable publication.

An immutable open is permitted only when the caller explicitly chooses
immutable mode and the canonical sidecar path is absent. It never creates
coordination. Because immutable readers intentionally hold no lifetime lock,
the caller is responsible for never opening a live database through immutable
mode and for certifying offline exclusivity before `InitializeLive`.

### 15.6 Public open modes, handle concurrency, and access policy

Phase 1 exposes three explicit constructors:

- `OpenImmutableReader(path)` requires sidecar absence and exact committed file
  length;
- `OpenLiveReader(path, cancellation)` discovers fixed capacity from the bound
  sidecar and registers; and
- `OpenLiveWriter(path, transaction_resource_budget, cancellation)` claims the
  dedicated lease and stores that one writer budget.

None performs general validation or implicitly creates, repairs, resets,
initializes, relocates, or switches mode. Main bootstrap is constant-time; live
open additionally performs its mandatory O(reader-capacity) coordination scan.
Snapshot is path-based and takes explicit `Immutable | Live` source mode,
destination policy, operation/output budget, and cancellation; it does not
borrow an already-open reader whose source protection cannot be released before
destination locking.

Reader point lookups and independent scans may run concurrently with no per-call
active counter, mutex, or atomic. The caller MUST ensure Reader `Close` does not
race any Reader call; Rust ownership enforces this where possible and Go/C state
it as a lifetime rule. A persistent cursor, membership view, feed reference, or
other child borrows its parent. Parent Close returns `HandleBusy` while a child
exists and admits no new child after Close begins.

Each writer, cursor, membership view, writer feed/membership reference, cleanup/
residue handle, and mutable result is otherwise caller-serialized. The FFI
boundary uses a fail-fast non-reentrant gate returning `HandleBusy` before
mutation; it never silently waits. Different handles may run concurrently under
the database locks. A source or sink callback MUST NOT reenter its originating
handle.

Every engine-created main, sidecar, private output, reservation, authorized
scratch, and Windows GC artifact is **CreatorOnly**, independent of process
umask or directory defaults. POSIX mode is exactly `0600`; inherited extended
ACL grants are neutralized. Windows uses a protected non-inheriting DACL for the
effective token user SID. The effective principal is captured at attempt start;
the applied state is verified and synchronized through the retained descriptor
before a canonical publication boundary. Failure to prove it is
`AccessPolicyUnsupported` before that boundary.

At attempt start the engine derives one creation-security commitment; callers
cannot supply or override it. Its exact encoding is:

```text
SHA-256("IPR4SEC1" || security_kind:u16le ||
        principal_length:u32le || principal_bytes || policy_bytes)
```

`security_kind == 1` is POSIX. `principal_bytes` is the effective UID converted
losslessly to `u64le`; `policy_bytes` is the ASCII literal
`POSIX-MODE-0600-NO-EXTENDED-ACL`. Verification requires that UID as owner,
effective mode exactly `0600`, and no extended access/default ACL grant.
`security_kind == 2` is Windows. `principal_bytes` is the effective token user
SID in canonical binary SID encoding; `policy_bytes` is the ASCII literal
`WINDOWS-PROTECTED-NONINHERITING-USER-FULL`. Verification requires that SID as
owner and a protected, non-inheriting DACL granting full access only to that SID,
with no other allow ACE. Other kinds, lossy UID/SID encodings, or noncanonical
policy bytes are invalid.

The kind and commitment are copied unchanged into every reservation, sidecar,
GC envelope, destination-owning result, and preparation error created by the
attempt. Thus a later resolver compares current semantic security state with the
original principal/policy without redefining the creator as the resolver's
current principal. An engine-created private inode must match the recorded
commitment before it can cross a canonical namespace boundary. The commitment
contains no secret and does not replace OS access control.

Ordinary opens and files supplied to `InitializeLive` never enforce or mutate
CreatorOnly. `ReplaceExisting` does not copy prior owner/group/mode/ACL/xattrs or
security descriptor; the new inode is CreatorOnly. A later legitimate access
change is reported independently as `CreatorOnly | ChangedOrUnproven`, never
changes content classification, never permits republishing, and is never
silently restored by a resolver. Applications deliberately widen or change
ownership after factual publication, and for live use must update both main and
sidecar consistently. Results and resolvers report main and coordination access
separately as `CreatorOnly | ChangedOrUnproven | Unclassified`, with
coordination additionally permitting `Absent`; one field never conflates an
intentionally pre-existing main with an engine-created sidecar.

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

- `LiveCurrent` takes the live operation lock, opens the exact bound sidecar,
  claims a transaction-zero reader slot before bootstrap selection, and updates
  it to the proven selected current transaction before a full graph scan.
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
through inspection and cleanup of the transaction-zero claim. It never claims
that an unselectable generation was pinned. Ambiguous live identity/current-
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
- retirement ordering, uniqueness, counts, and page lists.

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
operation lock. It scans the complete table, claims a transaction-zero slot,
reproves the current generation and reselects that exact meta from the retained
main descriptor, updates
the slot to that exact nonzero transaction, and releases the operation lock.
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

Conservative address-fence trust is deterministic. A range-branch entry may
bound an unreadable or invalid descendant only when every page from the
recovery-selected root through that entry has a valid normalized CRC, expected
type/level, valid local layout, and a unique checked path. The entry must have
nonzero count and locally valid `first_from <= last_from <= last_to`; its
inclusive `[first_from,last_to]` envelope then participates in
`bounded_possible_span_addresses`. A valid zero-count entry contributes no
address envelope. If the nearest such entry is unavailable or locally invalid,
the failure sets `has_unbounded_unknown`. Bytes from the failed child itself,
including record endpoints on a checksum-failed leaf, are never promoted to a
trusted fence. A CRC-valid range record with valid endpoints that is rejected
only for a known cross-record, catalog, or membership conflict contributes its
interval to `rejected_addresses` instead.

Unknown coverage without trustworthy conservative bounds sets
`has_unbounded_unknown`; it MUST NOT be assigned a guessed cardinality.
Parent lower fences alone are never enough to infer affected address
cardinality because a predecessor range may cross a later fence. Cardinality
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
           FailIfExists | ReplaceExisting, snapshot_resource_budget,
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
`ReplaceExisting`; this is the atomic in-place compaction path. After releasing
source protection, replacement MUST reopen and exclusively lock the same
recorded old inode, recheck its tuple/identity/digest and sidecar absence, and
then follow the ordinary replacement protocol. Any intervening change is a
typed conflict. `FailIfExists` on the same path returns the ordinary
destination-exists precondition error. A live source targeting its own canonical
path is rejected before output construction because its required sidecar makes
immutable replacement preconditions false; callers snapshot live state to a
different path.

It streams a new ordinary v4 file containing only:

- the reachable logical range map;
- the feed catalog and used-index state;
- the live membership dictionary and exact memberships;
- the exact decompressed metadata payload, recompressed as one valid v4 zlib
  stream when metadata is present.

It excludes unpublished growth, free pages, retirement batches, unreachable
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

Every cooperating namespace publisher owns the canonical destination
`.readers` name for its complete critical section. `CreateLive` acquires the
reservation before preparing its private main and converts that exact inode
into the final sidecar. `FailIfExists`, `ReplaceExisting`, immutable snapshot,
and recovery output creation retain a transient reservation until main
publication and namespace durability are resolved, then retire it. An existing
immutable main is not an exception: `ReplaceExisting` first locks that main and
then acquires the same reservation. `InitializeLive` first locks the existing
main and acquires the name by the same no-replace publication primitive before
converting it to a sidecar. `ResetLiveCoordination` keeps the old sidecar at the
canonical name while it prepares the kind-5 reservation, then atomically
replaces the old sidecar with that reservation. No cooperating operation relies
on a check followed later by acquisition, and canonical coordination is never
absent during reset.

The reservation is exactly 8,192 bytes and uses the same two block positions,
per-block zero padding, and global sequence/selection/transition rules as the
sidecar header. Table offsets are relative to the start of either 512-byte
header record:

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
| 112 | 2 | `operation_kind` (`1=FailIfExists`, `2=ReplaceExisting`, `3=CreateLive`, `4=InitializeLive`, `5=ResetLiveCoordination`) |
| 114 | 2 | attempted-output `identity_kind`, or zero for kind 3 before main preparation |
| 116 | 4 | `record_flags` (bit 0 = previous destination present; bit 1 = readable prior sidecar record; all other bits zero) |
| 120 | 8 | attempted output byte length, or zero for kind 3 before main preparation |
| 128 | 32 | attempted output local identity, or zero for kind 3 before main preparation |
| 160 | 64 | SHA-512 of exact attempted output bytes, or zero for kind 3 before main preparation |
| 224 | 32 | previous destination local identity when flag bit 0 is set; otherwise zero |
| 256 | 64 | SHA-512 of exact previous destination bytes when flag bit 0 is set; otherwise zero |
| 320 | 4 | reader capacity for kinds 3-5; otherwise zero |
| 324 | 32 | prior coordination local identity for kind 5; otherwise zero |
| 356 | 16 | prior sidecar ID when flag bit 1 is set; otherwise zero |
| 372 | 4 | prior reader capacity when flag bit 1 is set; otherwise zero |
| 376 | 2 | process-domain kind for kinds 3-5; otherwise zero |
| 378 | 2 | zero |
| 380 | 32 | process-domain token for kinds 3-5; otherwise zero |
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

CRC covers the complete 4,096-byte block with `[508,512)` zero. Reservation ID
is the operation's random nonzero publication-attempt ID; for `CreateLive` it is
also the eventual sidecar ID. The creation-security kind/commitment is fixed at
attempt start and must match every engine-created inode before publication or
sidecar conversion. For kinds 1-4, creation first exclusively creates
and sizes a private reservation inode at exact name
`.iprange-reservation-<id>.tmp`, obtains its local identity, writes and
synchronizes a complete state-1 sequence-1 block, takes its operation lock, and
publishes that same inode at canonical `.readers` with an atomic no-replace
namespace operation. It retains that descriptor and lock until the final sidecar
is ready, the reservation is durably retired, or abort cleanup is complete. It
then performs the required namespace synchronization and verifies
canonical-path/descriptor identity. Kind 5 instead keeps that complete private
reservation locked and synchronized while the old sidecar remains canonical,
then atomically replaces the old sidecar under both operation locks exactly as
defined by `ResetLiveCoordination`. A missing, partial, malformed, replaced, or
foreign reservation is never removed by ordinary open or another publication.
Its mere presence makes immutable open fail and blocks every competing
reservation/sidecar publisher, so every crash state is closed rather than
split-brain.

Every canonical or private reservation accepted as ready authority MUST
be a regular non-symlink inode with link count one, a matching recorded local
identity, and a selectable self-identity-bound header. Wherever an attempted
output identity is present it MUST differ from the reservation identity; a
kind-5 reservation MUST also differ from the prior coordination identity.
Creation and every
resolver recheck this invariant before a namespace action. The only exception
is a same-process retained POSIX descriptor after its exact unlink, whose zero
link count is removal evidence rather than reusable authority. Hard-linked
reservations are conflicts except for the exact temporary FreeBSD-14
no-replace transition described below.

Every reservation kind requires the nonzero, valid section-3 destination-
basename encoding kind, exact encoded byte length, and matching SHA-256
commitment from its first synchronized image. Conversion copies the same fields
unchanged into the sidecar attempt record. Result-based and reconstructed
resolution require the caller path, result basename bytes when present,
reservation, and sidecar to agree before deriving an attempt-private name or
performing any namespace action.

For operation kinds 1-2, the complete output identity, length, and digest are
mandatory before reservation publication and reader capacity is zero. Kind 2
also requires flag bit 0 and all three previous-destination fields; every other kind
forbids bit 0 and those fields. Kind 3 requires nonzero reader capacity; its output fields may initially
be zero, but after the private main is complete it MUST synchronize a new
CRC-valid state-1 reservation image containing those exact fields before
sidecar conversion. Kinds 4-5 require nonzero reader capacity and complete
existing-main fields from their first private reservation image. Kind 5 also
requires the exact nonzero prior coordination identity before its atomic
replacement attempt. If the old sidecar header is selectable, flag bit 1 and
the prior sidecar ID/capacity are mandatory; if not, the flag and both fields
are zero and caller-certified offline reset relies on the exact old local
identity. Bit 1 and every prior-coordination field are forbidden for kinds 1-4.
Kinds 3-5 MUST publish the complete sidecar attempt record through the
alternate 4,096-byte block before any main namespace operation. Unknown
kinds/flags, inconsistent optional fields, or zero mandatory values are
malformed.

Kinds 3-5 derive and bind the exact section-15.2 process domain before their
first reservation publication. Same-domain conversion and ordinary resolution
must match it. After a domain change, only the operation-specific incomplete-
attempt resolver may replace that field under the exact offline/locking rules
in section 15.1; it treats old slots as opaque, zero-initializes them, and
synchronizes the current-domain record before state 1. A ready state-1 sidecar
paired with its valid final main never changes domain through an ordinary
resolver and requires `ResetLiveCoordination`. The origin-1 state-1/absent-main
creation crash state remains an incomplete attempt under section 15.1. Kinds
1-2 have no live coordination and require both process-domain fields zero.

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
resolution. Every finished main, reservation, and sidecar has link count one.
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

For operation kinds 1-2, immediately before the first main-name namespace call,
the publisher writes and synchronizes state 2 through the alternate reservation
block and re-reads it as selected. The namespace call MUST NOT begin unless that
transition succeeds. State 2 is therefore the durable ambiguity record used by
a resolver: it proves the namespace operation was authorized and may have been
attempted, not that the OS call was observed. State 1 proves the main namespace
call had not begun. Both states block ordinary open and competing publication.
CreateLive instead uses the equivalent sidecar state-3 transition in section
15.1; kinds 4-5 do not rename the main.

`ListAbandonedReservationArtifacts(directory, cancellation, sink)` is an explicit read-only offline
aid for exact `.iprange-reservation-<32 lowercase hex>.tmp` names. Inert Windows
GC pairs are housekeeping and use `ListWindowsHousekeeping`; they are never
reservation or resolver authority.
`RemoveAbandonedReservationArtifact(directory, expected_directory_identity,
attempt ID, local identity, cancellation)` additionally requires valid self-identity when
readable and caller-certified absence of any active publisher/resolver. It uses
the section-14.4 idempotent cleanup rule and never touches canonical `.readers`
or a main file.

`SnapshotTo` requires the caller to choose `FailIfExists` or
`ReplaceExisting`; its convenience wrapper defaults to `FailIfExists` and never
switches policy. Recovery always uses `FailIfExists`. Source and destination
MUST NOT name the same inode except for the exact immutable
`SnapshotTo(source_path, source_path, ReplaceExisting)` compaction case defined
in section 20. Recovery, a live source, and `FailIfExists` never receive this
exception. Both policies reject a pre-existing canonical destination sidecar
or reservation, including an orphan left by partial creation; that rejection
is the failed atomic reservation acquisition itself, not a racy preliminary
check.

`FailIfExists` uses the reservation protocol above. `ReplaceExisting` requires
an already existing no-follow regular, link-count-one, sidecar-free destination;
it need not contain valid v4 bytes. Absence is a typed precondition failure
rather than a switch to replace semantics. Under caller-certified absence of
non-cooperating modifiers, it takes that inode's exclusive lifetime lock,
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
                       AdvancedGeneration |
                       UnavailableDomainMismatch
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
`FailIfExists` and all present for `ReplaceExisting`; the previous length/digest
are computed over stable bytes while its exclusive lifetime lock is held. The
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

Its residue handle uses the inspect/Close/remove ownership contract defined by
`InspectCreateResidue`; inspection alone never transfers a cleanup obligation.
When canonical coordination is absent, normal result-based resolution and exact
private-artifact cleanup apply; neither Remove API may be called without a
present residue handle.

`RemovePublicationResidue(residue_handle, cancellation)` is the explicit
offline escape hatch for a canonical reservation whose dual header cannot be
selected. The caller MUST certify that no publisher, resolver, live handle, or
immutable reader is active and that the destination namespace is quiescent. The
operation uses the exact retained parent and coordination descriptors from the
same-process inspection, requires that the canonical path still resolves to that
regular link-count-one inode, and takes its operation lock. If either header
selects any kind-1 through kind-5 reservation or origin-1 through origin-3
sidecar record, the operation returns `Conflict`; the caller must use that
record's operation-specific resolver. Only a coordination inode with neither
header selectable is eligible. The retained descriptor is then the only SDK
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
output. A ready later sidecar from another process domain reports
`OutcomeUnknown`, `Unclassified`, and `UnavailableDomainMismatch` without slot
inspection, hashing, or mutation. For every other destination classification a
later reservation/sidecar is `Conflict`. The
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
never accepts `ReplaceExisting` and never overwrites a pre-existing main or
sidecar.

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
stale-slot process-token comparison, sidecar conversion/replacement detection,
and every resolvable reservation/sidecar write-ahead phase. One implementation
MUST prepare each SHA-512-bound publication or live-transition record and the
other MUST inspect and resolve it, including the state-2/state-3 ambiguity
boundary. Per-language tests alone do not satisfy this requirement.

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
