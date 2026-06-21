# Legacy iprange binary format (v1.0 / v2.0) — read-only migration spec

This documents the **legacy** `iprange --print-binary` output, so the v3 engine can
read old files for migration (§14 of `binary-format-v3.md`). It is **current
reality** reverse-engineered from this repo's C source and verified against real
artifacts produced by the built `iprange` binary (2026-06-21). Read-only: we never
write this format.

Evidence (firehol/iprange, this repo):
- `src/ipset_binary.h:4` — `BINARY_HEADER_V10 "iprange binary format v1.0\n"` (IPv4)
- `src/ipset6_binary.h:6` — `BINARY_HEADER_V20 "iprange binary format v2.0\n"` (IPv6)
- `src/ipset_binary.c:3`, `src/ipset6_binary.c:6` — `endianness = 0x1A2B3C4D`
- `src/iprange.h:122` — `network_addr_t { in_addr_t addr; in_addr_t broadcast; }` (8 bytes)
- `src/iprange6.h:9,12` — `ipv6_addr_t = uint128_t`; `network_addr6_t { addr; broadcast; }` (32 bytes)
- `src/uint128.h:63-67` — portable `uint128_t` is `{ lo; hi }` on little-endian, `{ hi; lo }` on big-endian (matches native `__uint128_t` memory order)
- `src/ipset_binary.c:196-310`, `src/ipset6_binary.c:123-261` — the loaders

## File structure

```
<text header, newline-terminated ASCII lines>
<uint32 endianness marker>          (native byte order of the writer)
<records>                           (entry_count fixed-size records)
```

### Text header (each line ends `\n`, read with fgets)

IPv4 (v1.0):
```
iprange binary format v1.0
optimized | non-optimized
record size 8
records <n>
bytes <8*n + 4>
lines <n>
unique ips <decimal u64>
```
IPv6 (v2.0) inserts an `ipv6` line second and uses a 128-bit unique-ips decimal:
```
iprange binary format v2.0
ipv6
optimized | non-optimized
record size 32
records <n>
bytes <32*n + 4>
lines <n>
unique ips <decimal u128>
```
Prefixes are matched exactly: `record size ` (12), `records ` (8), `bytes ` (6),
`lines ` (6), `unique ips ` (11). The magic line MAY be absent in internal re-reads
(`first_line_missing`); a standalone saved file always has it. The binary payload
begins at the byte **immediately after** the final header line's `\n`.

### Endianness marker (4 bytes)

`0x1A2B3C4D` written in the writer's native order:
- on-disk `4D 3C 2B 1A` ⇒ **little-endian** writer (the real-world case: x86-64),
- on-disk `1A 2B 3C 4D` ⇒ big-endian writer.

**Only little-endian is accepted.** The legacy C loader refuses a marker that does
not match its own host, and `binary-format-v3.md` §14 rejects a big-endian marker;
real legacy artifacts come from x86-64. Our readers therefore accept only
`4D 3C 2B 1A` and reject anything else (no big-endian code path — it would be
untested). Record integers are decoded little-endian.

### Records

Each record is an inclusive `[addr, broadcast]` range (start, end).

- **IPv4 (8 bytes):** `addr` u32 (bytes 0–3), `broadcast` u32 (bytes 4–7), in the
  marker's endianness.
- **IPv6 (32 bytes):** `addr` then `broadcast`, each a `uint128_t`. On a
  little-endian writer the in-memory order is `{ lo, hi }`, so each 16-byte address is
  on disk as **`lo` (bytes 0–7) then `hi` (bytes 8–15)**, each a u64 in the marker's
  endianness. This is the **opposite** of v3's `hi`-then-`lo` key layout — migrating a
  legacy IPv6 address to a v3 key requires this **hi/lo transposition**:
  `key = Ipv6Key{ hi = u64(disk[8:16]), lo = u64(disk[0:8]) }`.

### Validation enforced (mirrored by our reader)

- `record size` equals 8 (v4) / 32 (v6); `bytes == record_size*records + 4`.
- per record `addr ≤ broadcast`.
- no trailing bytes after the last record.
- `unique ips ≥ records` and `lines ≥ records`.
- if `optimized`, records are sorted + disjoint and `Σ(broadcast−addr+1) == unique ips`
  (we recompute and check). `non-optimized` files are parsed without the sort/sum check.

## Migration to v3

Read → list of `[start, end]` ranges (already sorted+disjoint when `optimized`) →
feed into the v3 `Writer` with caller-supplied feed-meta / license / generation
(legacy files carry none) → emit a v3 file. The conformance fixtures under
`conformance/legacy/` are real `iprange --print-binary` outputs with a JSON manifest
of their expected ranges; both the Rust and Go readers parse them and must agree.
