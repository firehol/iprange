# iprange

`iprange` is a fast command-line tool for reading, normalizing, comparing, and exporting IPv4 and IPv6 address sets.

It understands single IPs, CIDRs, netmasks, numeric IPs, ranges, and hostnames. You can use it to merge blocklists, compute intersections or exclusions, generate data for `ipset restore`, or compare multiple IP sets as CSV.

## What it can read

`iprange` accepts one entry per line and can mix formats in the same input:

- single IPs
  - `1.2.3.4`
- CIDRs
  - `1.2.3.0/24`
- dotted netmasks
  - `1.2.3.0/255.255.255.0`
- abbreviated IPs
  - `10.1`
  - `10.1.1`
- IP ranges
  - `1.2.3.0 - 1.2.3.255`
- ranges where both sides use CIDR or netmask notation
- numeric IPs
- hostnames

In IPv6 mode (`-6`), it additionally accepts:

- IPv6 addresses
  - `2001:db8::1`
- IPv6 CIDRs
  - `2001:db8::/32`
- IPv6 ranges
  - `2001:db8::1 - 2001:db8::ff`
- compressed and full notation
  - `::1`, `2001:0db8:0000:0000:0000:0000:0000:0001`
- IPv4-mapped IPv6
  - `::ffff:10.0.0.1`
- plain IPv4 (normalized to `::ffff:x.x.x.x` in IPv6 mode)

Important input behavior:

- Hostnames are resolved in parallel.
- Comments after `#` or `;` are ignored.
- In IPv4 mode (default), parsing uses `inet_aton()`, so octal and hex forms are accepted too.
- In IPv6 mode, parsing uses `inet_pton(AF_INET6)`.
- Inputs can come from `stdin`, files, file lists, or directory expansion.

## Main modes

- `union` / `merge` / `optimize`
  - merge all inputs and print the normalized result
- `common`
  - print the intersection of all inputs
- `exclude-next`
  - merge the inputs before the option, then remove anything matched by the inputs after it
- `ipset-reduce`
  - trade a controlled increase in entries for fewer prefixes
- `compare`
  - compare all inputs against all other inputs and print CSV
- `compare-first`
  - compare the first input against every other input
- `compare-next`
  - compare one group of inputs against the next group
- `count-unique`
  - merge all inputs and print CSV counts
- `count-unique-all`
  - print one CSV count line per input

## Quick examples

Merge and normalize:

```bash
iprange blocklist-a.txt blocklist-b.txt
```

Find common IPs:

```bash
iprange --common blocklist-a.txt blocklist-b.txt
```

Exclude one set from another:

```bash
iprange allow.txt --exclude-next deny.txt
```

Count unique entries:

```bash
iprange -C blocklist-a.txt blocklist-b.txt
iprange --count-unique-all --header blocklist-a.txt blocklist-b.txt
```

Generate single-IP output:

```bash
iprange -1 hosts.txt
```

Generate binary output for fast round-trips on the same architecture:

```bash
iprange --print-binary blocklist.txt > blocklist.bin
iprange blocklist.bin
```

Generate `ipset restore`-style lines:

```bash
iprange --print-prefix 'add myset ' --print-suffix '' blocklist.txt
```

## Address family

By default, `iprange` operates in IPv4 mode. Use `-6` / `--ipv6` for IPv6:

```bash
# IPv6 merge
iprange -6 blocklist-v6.txt

# IPv6 count
iprange -6 -C blocklist-v6.txt

# IPv4 input normalized to mapped IPv6
echo "10.0.0.1" | iprange -6
# output: ::ffff:10.0.0.1

# Explicit IPv4 mode (same as default)
iprange -4 blocklist.txt
```

Key rules:
- Without `-4` or `-6`, text input defaults to IPv4 mode.
- In IPv6 mode, plain IPv4 input is accepted and normalized to `::ffff:x.x.x.x`.
- Operations between IPv4 and IPv6 datasets are not supported.
- Mixed-family range endpoints (e.g., `10.0.0.1 - 2001:db8::1`) are invalid.
- Binary files declare their family in the header.
- Feature detection: `iprange --has-ipv6` exits with 0 if IPv6 is supported.

## Build and install

From a release tarball:

```bash
./configure
make
make install
```

From git:

```bash
./autogen.sh
./configure
make
make install
```

If you do not want to build the man page:

```bash
./configure --disable-man
```

## Testing

Project test entry points:

- `./run-tests.sh`
  - CLI regression suite
- `./run-build-tests.sh`
  - build and layout regressions
- `./run-sanitizer-tests.sh`
  - ASAN/UBSAN/TSAN coverage
- `make check`
  - normal build-integrated test path
- `make check-sanitizers`
  - sanitizer-integrated test path

## Repository layout

- `src/`
  - C sources and headers
- `packaging/`
  - packaging helpers, spec template, ebuild, and release tooling
- `tests.d/`
  - CLI regression tests
- `tests.build.d/`
  - build and layout regressions
- `tests.sanitizers.d/`
  - sanitizer CLI regressions
- `tests.tsan.d/`
  - TSAN regressions
- `tests.unit/`
  - unit-style harnesses for internal edge cases

## Getting help

For the full option list:

```bash
iprange --help
```

The project wiki content that originally documented the feature set is now folded into this README so the repository landing page explains the tool directly.
