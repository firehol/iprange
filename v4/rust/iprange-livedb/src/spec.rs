//! Format constants for the v4 streaming mmap COW engine (v4.3).
//!
//! Every record is a fixed `[from: K, to: K, scope_id: u32]` — 12 bytes (IPv4)
//! or 36 bytes (IPv6). There is no `scope_width` field; `scope_mode` (0/1/2)
//! selects how the 4-byte `scope_id` is interpreted.
//!
//! Offsets are byte positions within a single `PAGE_SIZE` page buffer.

// ── identity ──────────────────────────────────────────────────────────────

/// File magic (8 ASCII bytes, compared bytewise).
pub const MAGIC: [u8; 8] = *b"IPRANGE4";

/// Major version. A reader MUST reject any other major.
pub const VERSION_MAJOR: u16 = 4;

/// v4.3: the streaming mmap COW engine. Breaking change from v4.0–v4.2 —
/// the record layout changed from `[from, to, scope_bytes]` (variable-width)
/// to `[from, to, scope_id:u32]` (fixed-width). Old files cannot be read.
pub const VERSION_MINOR: u16 = 3;

/// `meta_size`: offset past the last defined meta field. Unchanged from v4.2
/// layout (scope_mode reuses the scope_width byte at offset 37; free_list_head
/// and scope_table_root remain at 94 / 90).
pub const META_SIZE: u16 = 98;

// ── page geometry ─────────────────────────────────────────────────────────

/// Fixed page size for all v4.x.
pub const PAGE_SIZE: usize = 4096;

/// Size of the header present on every page.
pub const PAGE_HEADER_SIZE: usize = 16;

/// Maximum B+tree height (defense against cycles in hostile files).
pub const TREE_HEIGHT_MAX: u32 = 32;

/// `checksum_algo` for CRC32C/Castagnoli.
pub const CHECKSUM_ALGO_CRC32C: u8 = 1;

// ── page header field offsets (within a page) ─────────────────────────────

pub const PH_PAGE_TYPE: usize = 0;
pub const PH_RESERVED: usize = 1;
pub const PH_ENTRY_COUNT: usize = 2;
pub const PH_PGNO: usize = 4;
pub const PH_CHECKSUM: usize = 8;

// ── page_type values ───────────────────────────────────────────────────────

pub const PAGE_TYPE_META: u8 = 1;
pub const PAGE_TYPE_BRANCH: u8 = 2;
pub const PAGE_TYPE_LEAF: u8 = 3;
pub const PAGE_TYPE_SCOPE_BRANCH: u8 = 4;
pub const PAGE_TYPE_SCOPE_LEAF: u8 = 5;
pub const PAGE_TYPE_KV_BRANCH: u8 = 6;
pub const PAGE_TYPE_KV_LEAF: u8 = 7;
pub const PAGE_TYPE_OVERFLOW: u8 = 8;
/// A page in the transaction free-list (tracks pages freed during this txn).
/// Body: `next_txn_free_page: u32` at offset 16, `count: u32` at offset 20,
/// then `count` entries of `freed_pgno: u32` starting at offset 24.
pub const PAGE_TYPE_TXN_FREE: u8 = 9;

// ── meta page field offsets (within the page, after the 16-byte header) ───

pub const META_MAGIC: usize = 16;
pub const META_VERSION_MAJOR: usize = 24;
pub const META_VERSION_MINOR: usize = 26;
pub const META_META_SIZE: usize = 28;
pub const META_PAGE_SIZE: usize = 30;
pub const META_CHECKSUM_ALGO: usize = 34;
pub const META_FLAGS: usize = 35;
pub const META_KEY_WIDTH: usize = 36;
/// `scope_mode` (u8) — replaces `scope_width` at offset 37.
/// 0 = scalar, 1 = bitmap, 2 = indirect.
pub const META_SCOPE_MODE: usize = 37;
/// `record_size` (u32) — MUST equal `2·key_width + 4`.
pub const META_RECORD_SIZE: usize = 38;
pub const META_CREATED_UNIXTIME: usize = 42;
pub const META_ROOT_PGNO: usize = 50;
pub const META_TREE_HEIGHT: usize = 54;
pub const META_TOTAL_PAGES: usize = 58;
pub const META_RECORD_COUNT: usize = 66;
/// `txn_id` (u64) — monotonic generation number. The checksum-valid meta with
/// the higher `txn_id` is the active one. Readers register their txn_id in the
/// companion file so the writer knows which freed pages are still needed.
pub const META_TXN_ID: usize = 74;
pub const META_UPDATED_UNIXTIME: usize = 82;
pub const META_SCOPE_TABLE_ROOT: usize = 90;
pub const META_FREE_LIST_HEAD: usize = 94;

/// The static identity region `[16, 50)`: magic..=created_unixtime.
/// Two valid metas MUST agree byte-for-byte here.
pub const META_STATIC_START: usize = 16;
pub const META_STATIC_END: usize = 50;

// ── scope_mode values ─────────────────────────────────────────────────────

/// Scalar scope: `scope_id` is a raw value (e.g. a timestamp). Compare with `=`.
/// Used for retention files.
pub const SCOPE_MODE_SCALAR: u8 = 0;
/// Bitmap scope: `scope_id` IS a 32-bit bitmap (up to 32 feeds). Compare with `&`.
/// No scope table needed.
pub const SCOPE_MODE_BITMAP: u8 = 1;
/// Indirect scope: `scope_id` is a pointer into the scope table, which holds an
/// interned bitmap of arbitrary width. Compare with `&` after table lookup.
pub const SCOPE_MODE_INDIRECT: u8 = 2;

// ── flags ─────────────────────────────────────────────────────────────────

/// Meta `flags` bit 0: IP version (0 = IPv4, 1 = IPv6).
pub const FLAG_IP_VERSION: u8 = 0b1;

/// IP family of a file (meta `flags` bit 0).
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum IpVersion {
    V4,
    V6,
}

impl IpVersion {
    #[inline]
    pub const fn key_width(self) -> u8 {
        match self {
            IpVersion::V4 => 4,
            IpVersion::V6 => 16,
        }
    }

    #[inline]
    pub const fn flag(self) -> u8 {
        match self {
            IpVersion::V4 => 0,
            IpVersion::V6 => FLAG_IP_VERSION,
        }
    }

    #[inline]
    pub const fn from_flag_bit(flags: u8) -> IpVersion {
        if flags & FLAG_IP_VERSION != 0 {
            IpVersion::V6
        } else {
            IpVersion::V4
        }
    }
}

// ── record geometry ────────────────────────────────────────────────────────

/// Scope ID is always 4 bytes (u32), regardless of scope_mode.
pub const SCOPE_ID_SIZE: u32 = 4;

/// `record_size = 2·key_width + 4` (scope_id is always a u32).
#[inline]
pub const fn record_size(key_width: u8) -> u32 {
    2 * key_width as u32 + SCOPE_ID_SIZE
}

/// Maximum records in a leaf: `(page_size − 16) / record_size`.
#[inline]
pub const fn leaf_max(key_width: u8) -> usize {
    (PAGE_SIZE - PAGE_HEADER_SIZE) / record_size(key_width) as usize
}

/// Maximum separators in a branch: `(page_size − 16 − 4) / (key_width + 4)`.
#[inline]
pub const fn branch_max(key_width: u8) -> usize {
    (PAGE_SIZE - PAGE_HEADER_SIZE - 4) / (key_width as usize + 4)
}

// ── transaction free-list page layout ──────────────────────────────────────
//
// Freed pages during a transaction are tracked in growth-region pages of type
// PAGE_TYPE_TXN_FREE. Each such page holds:
//   @16  next_txn_free_page: u32  (0 = end of list)
//   @20  count: u32              (number of freed pgnos in this page)
//   @24  freed_pgnos: [u32; N]    (N = (PAGE_SIZE - 24) / 4 = 1018)

/// Offset of `next_txn_free_page` within a TXN_FREE page.
pub const TXN_FREE_NEXT: usize = PAGE_HEADER_SIZE;
/// Offset of `count` within a TXN_FREE page.
pub const TXN_FREE_COUNT: usize = PAGE_HEADER_SIZE + 4;
/// Offset of the freed-pgno array within a TXN_FREE page.
pub const TXN_FREE_ARRAY: usize = PAGE_HEADER_SIZE + 8;
/// Maximum freed pgnos per TXN_FREE page.
pub const TXN_FREE_CAPACITY: usize = (PAGE_SIZE - TXN_FREE_ARRAY) / 4;

// ── committed free-list page layout ─────────────────────────────────────────
//
// The committed free-list is a linked list in freed pages themselves. Each free
// page stores:
//   @16  next_free_pgno: u32     (0 = end of list)
//   @20  freed_in_txn: u64      (generation when this page was freed)
//
// A page is safe to reclaim only when no active reader is using a generation
// <= freed_in_txn.

/// Offset of `next_free_pgno` within a free page.
pub const FREE_NEXT: usize = PAGE_HEADER_SIZE;
/// Offset of `freed_in_txn` within a free page.
pub const FREE_FREED_IN_TXN: usize = PAGE_HEADER_SIZE + 4;

// ── v4.1 scope table (§C.2, §D): used for scope_mode == INDIRECT ───────────
//
// In INDIRECT mode, the scope table maps scope_id → interned bitmap. Each
// scope-table leaf entry holds a variable-width bitmap. The bitmap width grows
// when new feeds are added (CoW on the scope table, not on IP records).
//
// For SCALAR and BITMAP modes, scope_table_root is 0 (no scope table).

pub const FILE_SCOPE_ID: u32 = 0;
pub const SCOPE_KEY_WIDTH: usize = 4;
pub const SCOPE_NAME_MAX: usize = 256;

// Per-scope header record layout (scope-table leaf), little-endian:
pub const SCOPE_REC_ID: usize = 0;
pub const SCOPE_REC_VERSION: usize = 4;
pub const SCOPE_REC_TYPE: usize = 12;
pub const SCOPE_REC_NAME_LEN: usize = 13;
pub const SCOPE_REC_NAME: usize = 15;
pub const SCOPE_REC_KV_ROOT: usize = 271;
/// Fixed per-scope metadata header: `4 + 8 + 1 + 2 + 256 + 4 = 275`.
pub const SCOPE_RECORD_SIZE: usize = 275;

#[inline]
pub const fn scope_leaf_max() -> usize {
    (PAGE_SIZE - PAGE_HEADER_SIZE) / SCOPE_RECORD_SIZE
}

#[inline]
pub const fn scope_branch_max() -> usize {
    branch_max(SCOPE_KEY_WIDTH as u8)
}

// ── per-scope KV (§C.4, §D) ────────────────────────────────────────────────

pub const KV_KEY_MIN: usize = 1;
pub const KV_KEY_MAX: usize = 1024;
pub const KV_TYPE_TEXT: u32 = 0;
pub const KV_VALUE_INLINE: u8 = 0;
pub const KV_VALUE_OVERFLOW: u8 = 1;
pub const KV_SLOT_SIZE: usize = 2;
pub const KV_PAGE_BODY: usize = PAGE_SIZE - PAGE_HEADER_SIZE;
pub const OVERFLOW_NEXT_PGNO: usize = PAGE_HEADER_SIZE;
pub const OVERFLOW_PAYLOAD: usize = PAGE_SIZE - PAGE_HEADER_SIZE - 4;
pub const KV_INLINE_MAX: usize = 512;

#[inline]
pub const fn kv_entry_fixed(key_len: usize) -> usize {
    2 + key_len + 4 + 1
}

#[inline]
pub const fn kv_inline_entry_size(key_len: usize, value_len: usize) -> usize {
    kv_entry_fixed(key_len) + 4 + value_len
}

#[inline]
pub const fn kv_overflow_entry_size(key_len: usize) -> usize {
    kv_entry_fixed(key_len) + 4 + 8
}

#[inline]
pub const fn kv_branch_sep_size(sep_len: usize) -> usize {
    2 + sep_len + 4
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn meta_offsets_are_contiguous() {
        assert_eq!(META_MAGIC, PAGE_HEADER_SIZE);
        assert_eq!(META_VERSION_MAJOR, META_MAGIC + 8);
        assert_eq!(META_VERSION_MINOR, META_VERSION_MAJOR + 2);
        assert_eq!(META_META_SIZE, META_VERSION_MINOR + 2);
        assert_eq!(META_PAGE_SIZE, META_META_SIZE + 2);
        assert_eq!(META_CHECKSUM_ALGO, META_PAGE_SIZE + 4);
        assert_eq!(META_FLAGS, META_CHECKSUM_ALGO + 1);
        assert_eq!(META_KEY_WIDTH, META_FLAGS + 1);
        assert_eq!(META_SCOPE_MODE, META_KEY_WIDTH + 1);
        assert_eq!(META_RECORD_SIZE, META_SCOPE_MODE + 1);
        assert_eq!(META_CREATED_UNIXTIME, META_RECORD_SIZE + 4);
        assert_eq!(META_ROOT_PGNO, META_CREATED_UNIXTIME + 8);
        assert_eq!(META_TREE_HEIGHT, META_ROOT_PGNO + 4);
        assert_eq!(META_TOTAL_PAGES, META_TREE_HEIGHT + 4);
        assert_eq!(META_RECORD_COUNT, META_TOTAL_PAGES + 8);
        assert_eq!(META_TXN_ID, META_RECORD_COUNT + 8);
        assert_eq!(META_UPDATED_UNIXTIME, META_TXN_ID + 8);
        assert_eq!(META_SCOPE_TABLE_ROOT, META_UPDATED_UNIXTIME + 8);
        assert_eq!(META_FREE_LIST_HEAD, META_SCOPE_TABLE_ROOT + 4);
        assert_eq!(META_FREE_LIST_HEAD + 4, META_SIZE as usize);
    }

    #[test]
    fn record_size_is_fixed() {
        assert_eq!(record_size(4), 12);   // IPv4: 4+4+4
        assert_eq!(record_size(16), 36);  // IPv6: 16+16+4
    }

    #[test]
    fn leaf_max_constants() {
        assert_eq!(leaf_max(4), (4096 - 16) / 12);   // 340
        assert_eq!(leaf_max(16), (4096 - 16) / 36);   // 113
    }

    #[test]
    fn branch_max_constants() {
        assert_eq!(branch_max(4), (4096 - 16 - 4) / (4 + 4));   // 509
        assert_eq!(branch_max(16), (4096 - 16 - 4) / (16 + 4)); // 203
    }

    #[test]
    fn txn_free_capacity() {
        // (4096 - 24) / 4 = 1018 freed pgnos per page
        assert_eq!(TXN_FREE_CAPACITY, 1018);
    }

    #[test]
    fn scope_mode_values() {
        assert_eq!(SCOPE_MODE_SCALAR, 0);
        assert_eq!(SCOPE_MODE_BITMAP, 1);
        assert_eq!(SCOPE_MODE_INDIRECT, 2);
    }
}

// ── compatibility aliases (for code not yet migrated) ─────────────────────
// These allow gradual migration; they map old names to the new v4.3 values.
pub const VERSION_MINOR_METADATA: u16 = VERSION_MINOR;
pub const VERSION_MINOR_FREE_LIST: u16 = VERSION_MINOR;
pub const META_SIZE_V41: u16 = META_SIZE;
pub const META_SIZE_V42: u16 = META_SIZE;
