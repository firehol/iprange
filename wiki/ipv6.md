# IPv6 support

`iprange` supports IPv6 with the `-6` / `--ipv6` flag. One invocation operates on one address family — there is no mixed-family mode.

## Address family selection

| Flag | Behavior |
|------|----------|
| *(none)* | IPv4 mode (default for text input) |
| `-4` / `--ipv4` | Explicit IPv4 mode |
| `-6` / `--ipv6` | IPv6 mode |

Feature detection for scripts:
```bash
iprange --has-ipv6 && echo "IPv6 supported"
```

## Normalization rules

### IPv6 mode

- IPv6 input is parsed directly via `inet_pton(AF_INET6)`.
- Plain IPv4 input is accepted and normalized to IPv4-mapped IPv6 (`::ffff:x.x.x.x`).
- Hostnames are resolved for both AAAA and A records. A-record results are normalized to `::ffff:x.x.x.x`.
- The default prefix for bare IPv6 addresses is /128.

### IPv4 mode

- IPv4 input is parsed normally.
- IPv4-mapped IPv6 (`::ffff:x.x.x.x`) is recognized and converted back to the IPv4 address.
- All other IPv6 input is dropped. A single summary warning is printed per file: `N IPv6 entries dropped (use -6 for IPv6 mode)`.
- Hostnames are resolved for A records only.

## Cross-family rules

- Operations between IPv4 and IPv6 datasets are not supported. Each invocation works with one family.
- Mixed-family range endpoints (e.g., `10.0.0.1 - 2001:db8::1`) are rejected.
- Binary files declare their family in the header. Loading a binary file of the wrong family is an error.

## Supported operations

All operations available in IPv4 mode work identically in IPv6 mode:

- Merge / union
- Intersection
- Complement / exclude
- Symmetric difference
- Prefix reduction (`--ipset-reduce`)
- All comparison and counting modes
- All output formats (CIDR, ranges, single IPs, binary, prefix/suffix)

### Prefix control for IPv6

- `--min-prefix N`: restrict to prefixes N through 128
- `--prefixes N,N,N,...`: allow only specific prefix lengths (128 always enabled)

### Single IP cap

The `--print-single-ips` safety cap (16,777,216 IPs) applies in IPv6 mode too. Ranges exceeding this are skipped with a warning.

## Binary format

IPv6 uses binary format v2.0 (IPv4 uses v1.0). The formats are not interchangeable:

| Format | Family | Address size | Header |
|--------|--------|-------------|--------|
| v1.0 | IPv4 | 4 bytes per address | `iprange binary format v1.0` |
| v2.0 | IPv6 | 16 bytes per address | `iprange binary format v2.0` |

Binary files are auto-detected by their header. Same-architecture only (no endianness conversion).

## Examples

```bash
# Merge IPv6 blocklists
iprange -6 v6-list1.txt v6-list2.txt

# IPv4 input normalized to mapped IPv6
echo "10.0.0.1" | iprange -6
# output: ::ffff:a00:1/128

# Count unique IPv6 addresses
iprange -6 -C --header v6-list.txt

# Find common IPs between IPv6 lists
iprange -6 --common list1-v6.txt list2-v6.txt

# Binary cache for IPv6
iprange -6 --print-binary large-v6.txt > cache.bin
iprange -6 cache.bin

# Compare IPv6 blocklists
iprange -6 --compare --header v6-a.txt v6-b.txt v6-c.txt
```

## Internal representation

IPv6 addresses are stored as `__uint128_t` (128-bit unsigned integer). This requires GCC or Clang with `__uint128_t` support. The `configure` script checks for this at build time.

All set operations (merge, common, exclude, diff, optimize) have dedicated IPv6 implementations that operate on 128-bit address pairs, following the same algorithms as the IPv4 versions.
