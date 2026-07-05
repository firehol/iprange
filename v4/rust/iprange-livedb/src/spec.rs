//! Format constants from `design-iprange-v4-livedb.md` — the single source of on-disk
//! truth for v4.
//!
//! Every value here is normative and was verified against the spec's byte-layout
//! tables (§5, §5.1). Changing one is a format change. Offsets are byte positions
//! within a single `PAGE_SIZE` page buffer.

/// File magic, compared **bytewise** (8 ASCII bytes, endianness-independent, §5.1).
pub const MAGIC: [u8; 8] = *b"IPRANGE4";

/// Major version. A reader MUST reject any other major (§5.1 forward-compat).
pub const VERSION_MAJOR: u16 = 4;
/// Minor version for the v4.0 contract.
pub const VERSION_MINOR: u16 = 0;

/// `meta_size` for v4.0: the offset just past the last defined meta field (§5.1). A
/// reader requires `meta_size >= 90`, and exactly `90` at `version_minor == 0`.
pub const META_SIZE: u16 = 90;

/// Minor version for the v4.1 contract (the metadata system: scope table + per-scope KV).
/// Additive (§C.6): a v4.0 reader reads the IP tree and skips the metadata; a v4.0 writer
/// refuses to mutate a v4.1 file.
pub const VERSION_MINOR_METADATA: u16 = 1;

/// `meta_size` for v4.1: v4.0's 90 plus the trailing `scope_table_root` (u32) at offset 90.
pub const META_SIZE_V41: u16 = 94;

/// The fixed page size for **all** v4.x (D10). A reader MUST reject any other value
/// at `version_major == 4`. Pinning it to 4096 fixes meta-B at byte offset 4096 and
/// completes bootstrap (§5.1). It is the page-aligned I/O / allocation unit (§2).
pub const PAGE_SIZE: usize = 4096;

/// `checksum_algo` for CRC32C/Castagnoli (D9). A field, so future algorithms are
/// possible; v4.0 readers require this value.
pub const CHECKSUM_ALGO_CRC32C: u8 = 1;

/// Hard cap on `tree_height` (§5.3): even at the degenerate minimum branch fanout of
/// 2, a `u32`-pgno file (< 2^32 pages) cannot exceed ~32 levels. A reader MUST reject
/// `tree_height > 32` and treat descending deeper as a hard error (cycle defense, §9).
pub const TREE_HEIGHT_MAX: u32 = 32;

// --- common 16-byte page header (§5), byte offsets within a page ---

/// Size of the header present on every page (§5).
pub const PAGE_HEADER_SIZE: usize = 16;
/// `page_type` (u8) — 1 meta, 2 branch, 3 leaf.
pub const PH_PAGE_TYPE: usize = 0;
/// `reserved` (u8) — MUST be 0.
pub const PH_RESERVED: usize = 1;
/// `entry_count` (u16) — records in a leaf / separators in a branch; 0 for a meta.
pub const PH_ENTRY_COUNT: usize = 2;
/// `pgno` (u32) — this page's own number; a reader MUST verify it matches.
pub const PH_PGNO: usize = 4;
/// `checksum` (u64) — D9, computed over the whole page with this field zeroed.
pub const PH_CHECKSUM: usize = 8;

/// `page_type` value for a meta page (§5).
pub const PAGE_TYPE_META: u8 = 1;
/// `page_type` value for a branch (internal) page (§5.2).
pub const PAGE_TYPE_BRANCH: u8 = 2;
/// `page_type` value for a leaf page (§5.3).
pub const PAGE_TYPE_LEAF: u8 = 3;
/// v4.1 metadata page types (§D). A v4.0 reader never reaches these (they hang off
/// `scope_table_root`); a v4.1 reader rejects an unknown `page_type`.
/// Scope-table branch (internal) page.
pub const PAGE_TYPE_SCOPE_BRANCH: u8 = 4;
/// Scope-table leaf page (fixed [`SCOPE_RECORD_SIZE`]-byte per-scope headers).
pub const PAGE_TYPE_SCOPE_LEAF: u8 = 5;
/// Per-scope KV branch page (variable-length separators, slot directory).
pub const PAGE_TYPE_KV_BRANCH: u8 = 6;
/// Per-scope KV leaf page (variable-length entries, slot directory).
pub const PAGE_TYPE_KV_LEAF: u8 = 7;
/// KV overflow page (chained payload for a large value).
pub const PAGE_TYPE_OVERFLOW: u8 = 8;

// --- meta page field offsets (§5.1), within the page (after the 16-byte header) ---

/// `magic` (u8[8]).
pub const META_MAGIC: usize = 16;
/// `version_major` (u16).
pub const META_VERSION_MAJOR: usize = 24;
/// `version_minor` (u16).
pub const META_VERSION_MINOR: usize = 26;
/// `meta_size` (u16).
pub const META_META_SIZE: usize = 28;
/// `page_size` (u32).
pub const META_PAGE_SIZE: usize = 30;
/// `checksum_algo` (u8).
pub const META_CHECKSUM_ALGO: usize = 34;
/// `flags` (u8) — bit0: 0 = IPv4, 1 = IPv6; bits 1-7 reserved = 0.
pub const META_FLAGS: usize = 35;
/// `key_width` (u8) — 4 or 16.
pub const META_KEY_WIDTH: usize = 36;
/// `scope_width` (u8).
pub const META_SCOPE_WIDTH: usize = 37;
/// `record_size` (u32) — MUST equal `2·key_width + scope_width`.
pub const META_RECORD_SIZE: usize = 38;
/// `created_unixtime` (u64) — static; identical in both metas.
pub const META_CREATED_UNIXTIME: usize = 42;
/// `root_pgno` (u32) — 0 = empty tree. First dynamic field.
pub const META_ROOT_PGNO: usize = 50;
/// `tree_height` (u32) — 0 = empty; leaf level = 1.
pub const META_TREE_HEIGHT: usize = 54;
/// `total_pages` (u64) — logical page count; `2 <= total_pages < 2^32`.
pub const META_TOTAL_PAGES: usize = 58;
/// `record_count` (u64) — UNVERIFIED hint; a reader MUST NOT size an allocation from it.
pub const META_RECORD_COUNT: usize = 66;
/// `txn_id` (u64) — monotonic; the checksum-valid meta with the higher value is active.
pub const META_TXN_ID: usize = 74;
/// `updated_unixtime` (u64) — caller-supplied per commit (deterministic tests).
pub const META_UPDATED_UNIXTIME: usize = 82;
/// `scope_table_root` (u32) — v4.1 only (`version_minor >= 1`, `meta_size >= 94`). 0 = no
/// metadata; else the root pgno of the scope table (§C.1). At v4.0 (`meta_size == 90`) this
/// offset lies in the reserved-zero tail.
pub const META_SCOPE_TABLE_ROOT: usize = 90;

/// The static identity region `[16, 50)` (§5.1): magic..=created_unixtime. Two valid
/// metas MUST agree byte-for-byte here; the dynamic region `[50, META_SIZE)` differs
/// per commit.
pub const META_STATIC_START: usize = 16;
/// End (exclusive) of the static identity region.
pub const META_STATIC_END: usize = 50;

/// Meta `flags` bit 0: IP version (0 = IPv4, 1 = IPv6). Bits 1-7 reserved = 0.
pub const FLAG_IP_VERSION: u8 = 0b1;

/// IP family of a file (meta `flags` bit 0).
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum IpVersion {
    /// IPv4: 4-byte keys.
    V4,
    /// IPv6: 16-byte keys.
    V6,
}

impl IpVersion {
    /// Key width in bytes (4 or 16).
    pub const fn key_width(self) -> u8 {
        match self {
            IpVersion::V4 => 4,
            IpVersion::V6 => 16,
        }
    }

    /// Meta `flags` value for this family (bit 0).
    pub const fn flag(self) -> u8 {
        match self {
            IpVersion::V4 => 0,
            IpVersion::V6 => FLAG_IP_VERSION,
        }
    }

    /// Family of a `flags` byte (only bit 0 is meaningful; the caller rejects other
    /// bits, §5.1).
    pub const fn from_flag_bit(flags: u8) -> IpVersion {
        if flags & FLAG_IP_VERSION != 0 {
            IpVersion::V6
        } else {
            IpVersion::V4
        }
    }
}

/// `record_size = 2·key_width + scope_width` (§4, D1). Widened to `u32` to match the
/// meta field; the inputs are bytes so it never overflows.
#[inline]
pub const fn record_size(key_width: u8, scope_width: u8) -> u32 {
    2 * key_width as u32 + scope_width as u32
}

/// Maximum records in a leaf: `(page_size − 16) / record_size` (§5.3). `record_size`
/// MUST be > 0 (it is `>= 2·key_width >= 8`).
#[inline]
pub const fn leaf_max(record_size: u32) -> usize {
    (PAGE_SIZE - PAGE_HEADER_SIZE) / record_size as usize
}

/// Maximum separators in a branch: `(page_size − 16 − 4) / (key_width + 4)` (§5.2).
/// The leading `−4` is `child_pgno[0]`; each separator adds `key_width + 4` (a
/// `sep_key` plus the following `child_pgno`). Children = separators + 1.
#[inline]
pub const fn branch_max(key_width: u8) -> usize {
    (PAGE_SIZE - PAGE_HEADER_SIZE - 4) / (key_width as usize + 4)
}

// --- v4.1 scope table (§C.2, §D): a fixed-record B+tree keyed by `scope_id` (u32) ---

/// `scope_id 0` is reserved for the file/dataset-level metadata (the FILE target).
/// `scope_define` never returns it.
pub const FILE_SCOPE_ID: u32 = 0;

/// The scope-table B+tree key is the 4-byte `scope_id`.
pub const SCOPE_KEY_WIDTH: usize = 4;

/// Max bytes of a per-scope `name` (UTF-8). The fixed slot keeps the seek a fixed offset.
pub const SCOPE_NAME_MAX: usize = 256;

// Per-scope header record layout (§C.2), little-endian, within a scope-table leaf:
/// `scope_id` (u32) — the B+tree key, first field of the record.
pub const SCOPE_REC_ID: usize = 0;
/// `version` (u64).
pub const SCOPE_REC_VERSION: usize = 4;
/// `type` (u8) — opaque caller value (the engine does not reject unknown values).
pub const SCOPE_REC_TYPE: usize = 12;
/// `name_len` (u16, 0..=256).
pub const SCOPE_REC_NAME_LEN: usize = 13;
/// `name` (`SCOPE_NAME_MAX` bytes; `name_len` used, the rest MUST be zero).
pub const SCOPE_REC_NAME: usize = 15;
/// `kv_root` (u32) — 0 = no KV; else this scope's KV tree root (§C.4).
pub const SCOPE_REC_KV_ROOT: usize = 271;
/// Fixed per-scope record size: `4 + 8 + 1 + 2 + 256 + 4 = 275`.
pub const SCOPE_RECORD_SIZE: usize = 275;

/// Max per-scope records in a scope-table leaf: `(page_size − 16) / 275`.
#[inline]
pub const fn scope_leaf_max() -> usize {
    (PAGE_SIZE - PAGE_HEADER_SIZE) / SCOPE_RECORD_SIZE
}

/// Max separators in a scope-table branch: same geometry as `branch_max(4)`.
#[inline]
pub const fn scope_branch_max() -> usize {
    branch_max(SCOPE_KEY_WIDTH as u8)
}

// --- v4.1 per-scope KV (§C.4, §D): a slot-directory B+tree behind each `kv_root` ---

/// Minimum KV `key` length (bytes). An empty key is rejected (§C.4).
pub const KV_KEY_MIN: usize = 1;
/// Maximum KV `key` length (bytes), UTF-8, no NUL (§C.4).
pub const KV_KEY_MAX: usize = 1024;

/// KV `type == 0` ⇒ the value is text the engine validates as UTF-8 + no NUL (§C.4); any
/// non-zero `type` is caller-defined binary the engine never interprets.
pub const KV_TYPE_TEXT: u32 = 0;

/// `value_kind` byte in a KV leaf entry: the value bytes live inside the entry (§D).
pub const KV_VALUE_INLINE: u8 = 0;
/// `value_kind` byte in a KV leaf entry: the value lives in an overflow chain (§D).
pub const KV_VALUE_OVERFLOW: u8 = 1;

/// One slot in a KV page's slot directory: a `u16` byte offset (from the page start) to
/// the entry heap (§D). The directory grows from the front; the heap from the back.
pub const KV_SLOT_SIZE: usize = 2;

/// Bytes available for KV slots + entry heap on a page: everything after the 16-byte
/// header. The slot directory and the entry heap share this region from opposite ends.
pub const KV_PAGE_BODY: usize = PAGE_SIZE - PAGE_HEADER_SIZE;

/// `next_pgno` (u32) offset within an overflow page, right after the common header (§D).
pub const OVERFLOW_NEXT_PGNO: usize = PAGE_HEADER_SIZE;
/// Payload bytes per overflow page: `page_size − 16 − 4` (header + `next_pgno`), §D.
pub const OVERFLOW_PAYLOAD: usize = PAGE_SIZE - PAGE_HEADER_SIZE - 4;

/// Inline/overflow threshold (writer choice, §C.4/§D): a value of at most this many bytes
/// is stored inline; larger values go to an overflow chain. `value_kind` makes each entry
/// self-describing, so a reader parses either regardless of this threshold. Chosen so a
/// single entry (max-key + descriptor + inline value) always fits a fresh leaf's body.
pub const KV_INLINE_MAX: usize = 512;

/// Fixed bytes of a KV leaf entry header before the inline value: `key_len(2) · key ·
/// type(4) · value_kind(1)`. `key` is the variable part; this returns the constant
/// surround for a given `key_len`.
#[inline]
pub const fn kv_entry_fixed(key_len: usize) -> usize {
    2 + key_len + 4 + 1
}

/// Encoded size of an inline KV leaf entry (entry bytes, excluding its slot): the fixed
/// header + `value_len(4)` + the value bytes (§D).
#[inline]
pub const fn kv_inline_entry_size(key_len: usize, value_len: usize) -> usize {
    kv_entry_fixed(key_len) + 4 + value_len
}

/// Encoded size of an overflow KV leaf entry (entry bytes, excluding its slot): the fixed
/// header + `first_pgno(4)` + `value_total_len(8)` (§D).
#[inline]
pub const fn kv_overflow_entry_size(key_len: usize) -> usize {
    kv_entry_fixed(key_len) + 4 + 8
}

/// Encoded size of a KV branch separator (entry bytes, excluding its slot): `sep_len(2) ·
/// sep_key · child_pgno(4)` (§D).
#[inline]
pub const fn kv_branch_sep_size(sep_len: usize) -> usize {
    2 + sep_len + 4
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn meta_offsets_are_contiguous_and_end_at_meta_size() {
        // Each field starts where the previous ended; the last ends at META_SIZE (90).
        assert_eq!(META_MAGIC, PAGE_HEADER_SIZE);
        assert_eq!(META_VERSION_MAJOR, META_MAGIC + 8);
        assert_eq!(META_VERSION_MINOR, META_VERSION_MAJOR + 2);
        assert_eq!(META_META_SIZE, META_VERSION_MINOR + 2);
        assert_eq!(META_PAGE_SIZE, META_META_SIZE + 2);
        assert_eq!(META_CHECKSUM_ALGO, META_PAGE_SIZE + 4);
        assert_eq!(META_FLAGS, META_CHECKSUM_ALGO + 1);
        assert_eq!(META_KEY_WIDTH, META_FLAGS + 1);
        assert_eq!(META_SCOPE_WIDTH, META_KEY_WIDTH + 1);
        assert_eq!(META_RECORD_SIZE, META_SCOPE_WIDTH + 1);
        assert_eq!(META_CREATED_UNIXTIME, META_RECORD_SIZE + 4);
        assert_eq!(META_ROOT_PGNO, META_CREATED_UNIXTIME + 8);
        assert_eq!(META_TREE_HEIGHT, META_ROOT_PGNO + 4);
        assert_eq!(META_TOTAL_PAGES, META_TREE_HEIGHT + 4);
        assert_eq!(META_RECORD_COUNT, META_TOTAL_PAGES + 8);
        assert_eq!(META_TXN_ID, META_RECORD_COUNT + 8);
        assert_eq!(META_UPDATED_UNIXTIME, META_TXN_ID + 8);
        assert_eq!(
            META_UPDATED_UNIXTIME + 8,
            META_SIZE as usize,
            "last field ends at meta_size"
        );
        // The static identity region is exactly magic..=created_unixtime.
        assert_eq!(META_STATIC_START, META_MAGIC);
        assert_eq!(META_STATIC_END, META_ROOT_PGNO);
    }

    #[test]
    fn geometry_matches_spec_formulas() {
        // IPv4, scope_width 0: record 8 bytes.
        assert_eq!(record_size(4, 0), 8);
        assert_eq!(leaf_max(8), (4096 - 16) / 8); // 510
                                                  // IPv6, scope_width 4: record 36 bytes.
        assert_eq!(record_size(16, 4), 36);
        assert_eq!(leaf_max(36), (4096 - 16) / 36); // 113
                                                    // branch fanout.
        assert_eq!(branch_max(4), (4096 - 16 - 4) / (4 + 4)); // 509 separators -> 510 children
        assert_eq!(branch_max(16), (4096 - 16 - 4) / (16 + 4)); // 203 separators -> 204 children
        assert!(record_size(4, 255) <= u16::MAX as u32);
    }

    #[test]
    fn kv_geometry_matches_spec() {
        // Overflow payload is page minus header minus the next_pgno field.
        assert_eq!(OVERFLOW_PAYLOAD, 4096 - 16 - 4); // 4076
        assert_eq!(OVERFLOW_NEXT_PGNO, PAGE_HEADER_SIZE);
        // Entry sizing helpers compose the §D layout exactly.
        assert_eq!(kv_entry_fixed(3), 2 + 3 + 4 + 1);
        assert_eq!(kv_inline_entry_size(3, 10), 2 + 3 + 4 + 1 + 4 + 10);
        assert_eq!(kv_overflow_entry_size(3), 2 + 3 + 4 + 1 + 4 + 8);
        assert_eq!(kv_branch_sep_size(5), 2 + 5 + 4);
        // The largest possible inline entry (max key + threshold value) plus its slot
        // must fit a fresh KV leaf body — otherwise bulk-load could wedge.
        let biggest = kv_inline_entry_size(KV_KEY_MAX, KV_INLINE_MAX) + KV_SLOT_SIZE;
        assert!(
            biggest <= KV_PAGE_BODY,
            "single max inline entry fits a leaf"
        );
    }

    #[test]
    fn ip_version_mapping() {
        assert_eq!(IpVersion::V4.key_width(), 4);
        assert_eq!(IpVersion::V6.key_width(), 16);
        assert_eq!(IpVersion::V4.flag(), 0);
        assert_eq!(IpVersion::V6.flag(), FLAG_IP_VERSION);
        assert_eq!(IpVersion::from_flag_bit(0), IpVersion::V4);
        assert_eq!(IpVersion::from_flag_bit(1), IpVersion::V6);
    }
}
