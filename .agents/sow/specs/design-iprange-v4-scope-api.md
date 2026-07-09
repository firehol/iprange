# Design: iprange v4.3 Scope Model

## Status

**Current reality (v4.3).** Replaces the v4.0–v4.2 scope_width model entirely.

## The Three Scope Modes

The file's `scope_mode` (meta byte @37) is fixed at creation time and selects
how the 4-byte `scope_id` on every record is interpreted:

### Mode 0 — Scalar (retention files)

`scope_id` IS the value. Typically a u32 timestamp (1-second resolution,
136-year range). Each IP range has exactly one scope_id — scopes never overlap.

- Compare with `=` (equality for coalescing, exact match for lookups)
- No scope table needed (`scope_table_root = 0`)
- Migrate API: replaces the scope_id; returns old timestamps for age computation
- Use case: retention files where scope_id = upstream-detected save timestamp

### Mode 1 — Bitmap (≤32 feeds)

`scope_id` IS a 32-bit bitmap. Bit N = feed N. An IP range can belong to
multiple feeds simultaneously.

- Compare with `&` (bitwise AND for overlap/membership queries)
- Coalescing: adjacent ranges merge iff `scope_id` is equal (same feed set)
- No scope table needed (`scope_table_root = 0`)
- Feed add: `scope_id |= (1 << feed_bit)`
- Feed remove: `scope_id &= ~(1 << feed_bit)`; delete record if scope_id == 0
- Use case: small multi-feed files (up to 32 feeds)

### Mode 2 — Indirect (unlimited feeds)

`scope_id` is a pointer into a scope table. The scope table holds interned
bitmaps of arbitrary width. Adding a feed grows the bitmap width in the scope
table via COW — zero IP record rewrites.

- Compare with `&` after table lookup (resolve scope_id → bitmap, then AND)
- Coalescing: adjacent ranges merge iff `scope_id` is equal (pointer equality)
- Scope table maps `scope_id → {bitmap, name, version, type, kv_root}`
- Up to 4 billion distinct feed-combinations
- Common combinations interned (1M ranges with {A,B,C} → one bitmap)
- Use case: large multi-feed files (hundreds of feeds)

## Engine Opacity

The engine core (B+tree, COW, commit, scan, lookup, migrate) is **scope-opaque**.
It never interprets scope_id bytes — it only compares with `=` (for coalescing).
All scope interpretation (bitmap ops, timestamp logic, feed membership) lives in
the API layer.

## Scope Table (Mode 2 Only)

A separate B+tree keyed by `scope_id` (u32). Each entry holds:
```
scope_id   u32      (B+tree key)
version    u64
type       u8       (caller hint)
name_len   u16
name       u8[256]  (UTF-8)
kv_root    u32      (0 = no KV)
```

The bitmap data for each scope_id lives behind `kv_root` or in a dedicated
bitmap field (pending Phase 4c re-implementation).

## What Was Removed

- `scope_width` meta field (variable-width scope bytes per record)
- D-A "Uncapped scope" via scope-as-value (now: fixed u32 scope_id)
- D-B "Opaque fixed-width value per file" (now: scope_mode selects interpretation)
- Bitmap capped at `scope_width × 8` (now: mode 1 = fixed 32 bits; mode 2 = unlimited)
- Per-scope KV metadata as the primary metadata system (pending Phase 4c)

## Pending

- Mode 2 scope table implementation (Phase 4c)
- Per-scope KV metadata (Phase 4c)
- Bitmap width growth in the scope table (CoW, not IP-record rewrite)
