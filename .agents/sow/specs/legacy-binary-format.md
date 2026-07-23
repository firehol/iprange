# Released C iprange binary formats (v1.0 / v2.0)

This documents the binary output produced and consumed by the released C
`iprange` CLI. Version 1.0 is the IPv4 format and version 2.0 is the IPv6 format.
These are current C behavior, established from this repository's source and
verified against files produced by the built CLI on 2026-06-21.

This document does not make these bytes a compatibility mode of the new engine.
The final v4 database is a separate exact format.

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

The released C loader requires the marker to match the host's native byte order.
Files are therefore architecture-dependent. Known operational artifacts are
little-endian (`4D 3C 2B 1A`), but the on-disk contract itself records native
writer order rather than defining one portable order.

### Records

Each record is an inclusive `[addr, broadcast]` range (start, end).

- **IPv4 (8 bytes):** `addr` u32 (bytes 0–3), `broadcast` u32 (bytes 4–7), in the
  marker's endianness.
- **IPv6 (32 bytes):** `addr` then `broadcast`, each a `uint128_t`. On a
  little-endian writer the in-memory order is `{ lo, hi }`, so each 16-byte address is
  on disk as **`lo` (bytes 0–7) then `hi` (bytes 8–15)**, each a u64 in the marker's
  endianness.

### Validation enforced by the released C loader

- `record size` equals 8 bytes (IPv4) / 32 bytes (IPv6);
  `bytes == record_size*records + 4`.
- per record `addr ≤ broadcast`.
- no trailing bytes after the last record.
- `unique ips ≥ records` and `lines ≥ records`.
- if `optimized`, records are sorted + disjoint and `Σ(broadcast−addr+1) == unique ips`
  (we recompute and check). `non-optimized` files are parsed without the sort/sum check.
