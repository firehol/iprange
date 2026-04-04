# iprange

`iprange` is a fast command-line tool for managing IPv4 and IPv6 address sets. It reads IP addresses, CIDRs, ranges, and hostnames in any combination, normalizes them into optimal non-overlapping sets, and performs set operations (union, intersection, difference, complement). It can also compare sets as CSV, reduce prefix diversity for firewall performance, and produce binary output for fast round-trips.

For 1 million input lines, a merge completes in under a second.

## Input formats

`iprange` accepts one entry per line. All formats can be mixed in the same file.

### IPv4 (default mode)

| Format | Example | Notes |
|--------|---------|-------|
| Single IP | `1.2.3.4` | |
| CIDR | `1.2.3.0/24` | Network address applied by default |
| Netmask | `1.2.3.0/255.255.255.0` | Equivalent to /24 |
| Range | `1.2.3.0 - 1.2.3.255` | Dash with optional spaces |
| CIDR range | `1.2.3.0/24 - 1.2.4.0/24` | Network of first to broadcast of second |
| Abbreviated | `10.1` | Expands via `inet_aton()` |
| Numeric | `16909060` | Integer, parsed by `inet_aton()` |
| Octal | `012.0.0.1` | Components starting with 0 are octal |
| Hex | `0x0A000001` | Components starting with 0x are hex |
| Hostname | `example.com` | Resolved via parallel DNS |

Parsing uses `inet_aton()`, so all numeric forms it accepts (decimal integers, octal, hex, mixed) are supported. This is intentional and documented behavior.

### IPv6 (`-6` mode)

| Format | Example | Notes |
|--------|---------|-------|
| Full notation | `2001:0db8:0000:0000:0000:0000:0000:0001` | |
| Compressed | `2001:db8::1` | Standard `::` compression |
| Loopback | `::1` | |
| CIDR | `2001:db8::/32` | Prefix 0-128 |
| Range | `2001:db8::1 - 2001:db8::ff` | |
| IPv4-mapped | `::ffff:10.0.0.1` | |
| Plain IPv4 | `10.0.0.1` | Normalized to `::ffff:10.0.0.1` |
| Hostname | `example.com` | Both AAAA and A records resolved |

Parsing uses `inet_pton(AF_INET6)`.

### Comments and whitespace

- Lines starting with `#` or `;` are comments.
- Inline comments after `#` or `;` are stripped from data lines.
- Empty lines and leading/trailing whitespace are ignored.

### File inputs

| Syntax | Meaning |
|--------|---------|
| `file.txt` | Load a single file as one ipset |
| `-` | Read from stdin |
| `@filelist.txt` | Load a file list (one filename per line, comments allowed) |
| `@directory/` | Load all regular files in directory (sorted, no recursion) |
| `file.txt as name` | Override the name shown in CSV output |

When no files are given, stdin is assumed.

Feature detection for scripts:
```bash
iprange --has-filelist-loading && echo "supports @filename"
iprange --has-directory-loading && echo "supports @directory"
```

### Binary input

Binary files (produced by `--print-binary`) are auto-detected by their header. IPv4 binary uses format v1.0; IPv6 uses v2.0. Loading a binary file of the wrong family is an error.

## Address family

| Flag | Meaning |
|------|---------|
| *(none)* | IPv4 mode (default for text input) |
| `--ipv4` / `-4` | Explicit IPv4 mode |
| `--ipv6` / `-6` | IPv6 mode |

Rules:

- Without `-4` or `-6`, text input defaults to IPv4 mode.
- In IPv6 mode, plain IPv4 input is accepted and normalized to `::ffff:x.x.x.x`.
- In IPv4 mode, `::ffff:x.x.x.x` is converted back to IPv4. All other IPv6 input is dropped with one summary warning.
- Operations between IPv4 and IPv6 datasets are not supported.
- Mixed-family range endpoints (e.g., `10.0.0.1 - 2001:db8::1`) are invalid.
- Binary files declare their family in the header.
- Feature detection: `iprange --has-ipv6` exits 0 if IPv6 is supported.

## Operations

### Merge / Union (default)

Merge all inputs into one sorted, deduplicated set.

```bash
iprange blocklist-a.txt blocklist-b.txt
```

Aliases: `--optimize`, `--combine`, `--merge`, `--union`, `--union-all`, `-J`

### Intersection

Print only the IPs common to all inputs.

```bash
iprange --common blocklist-a.txt blocklist-b.txt
```

Aliases: `--common`, `--intersect`, `--intersect-all`

### Complement (exclude)

Merge all files before `--except`, then remove all IPs matched by the files after it.

```bash
iprange allow.txt --except deny.txt
```

Aliases: `--except`, `--exclude-next`, `--complement`, `--complement-next`

### Symmetric difference

Print IPs that exist in either A or B, but not both. Exits 1 if there are differences, 0 if the sets are equal.

```bash
iprange before.txt --diff after.txt
echo $?  # 0 = identical, 1 = different
```

Use `--quiet` to suppress the output and only check the exit code.

Aliases: `--diff`, `--diff-next`

### Reduce prefixes

Merge all inputs, then reduce the number of distinct CIDR prefixes while allowing a controlled increase in entry count. This optimizes netfilter/iptables `hash:net` ipsets, where each distinct prefix adds a lookup but entry count does not affect performance.

```bash
iprange --ipset-reduce 20 blocklist.txt
```

Parameters:

| Option | Default | Meaning |
|--------|---------|---------|
| `--ipset-reduce PERCENT` | 20 | Allow this % increase in entries |
| `--ipset-reduce-entries ENTRIES` | 16384 | Minimum acceptable entry count |

The tool computes the maximum acceptable entries as `max(current * (1 + PERCENT/100), ENTRIES)`, then iteratively eliminates the prefix with the smallest cost until the limit is reached. The result matches exactly the same set of IPs.

Use `-v` to see the elimination steps and prefix breakdown.

Aliases: `--reduce-factor`, `--reduce-entries`

### Compare (CSV)

Compare all files pairwise and print CSV with entry counts, unique IPs, combined IPs, and common IPs.

```bash
iprange --compare --header blocklist-a.txt blocklist-b.txt blocklist-c.txt
```

**Compare first**: compare the first file against every other:
```bash
iprange --compare-first --header reference.txt other1.txt other2.txt
```

**Compare next**: compare files before the option against files after:
```bash
iprange --compare-next --header before1.txt before2.txt --compare-next after1.txt after2.txt
```

### Count unique (CSV)

Merge all inputs and print a single CSV line with entry and unique IP counts:
```bash
iprange --count-unique --header blocklist.txt
```

Print one CSV line per input file:
```bash
iprange --count-unique-all --header blocklist-a.txt blocklist-b.txt
```

## Output formats

### CIDR (default)

Outputs optimal non-overlapping CIDR blocks:
```
10.0.0.0/24
10.0.1.0/25
10.0.1.128/26
```

### Ranges (`--print-ranges` / `-j`)

```
10.0.0.0-10.0.0.255
10.0.1.0-10.0.1.191
```

### Single IPs (`--print-single-ips` / `-1`)

Enumerates every individual IP. Ranges larger than 16,777,216 IPs (256^3) are skipped with a warning to prevent unbounded output.

### Binary (`--print-binary`)

Fast machine-readable format for the same architecture (no endianness conversion). Use for caching and fast round-trips:
```bash
iprange --print-binary blocklist.txt > cache.bin
iprange cache.bin  # reads binary, outputs CIDR
```

### Prefix and suffix strings

Customize output for ipset restore, iptables rules, or other tools:

```bash
# Generate ipset restore commands
iprange --print-prefix "add myset " blocklist.txt

# Different prefixes for single IPs vs networks
iprange --print-prefix-ips "add ips " --print-prefix-nets "add nets " blocklist.txt

# Add suffixes
iprange --print-suffix " timeout 3600" blocklist.txt
```

### Prefix control

Limit which CIDR prefixes appear in output:

```bash
# Only generate /24 to /32 (no large blocks)
iprange --min-prefix 24 blocklist.txt

# Only use specific prefixes
iprange --prefixes 24,32 blocklist.txt
```

Warning: restricting prefixes can dramatically increase the number of output entries.

## DNS resolution

Hostnames in input files are resolved in parallel using a thread pool.

| Option | Default | Meaning |
|--------|---------|---------|
| `--dns-threads N` | 5 | Number of parallel DNS queries |
| `--dns-silent` | off | Suppress DNS error messages |
| `--dns-progress` | off | Show resolution progress bar |

In IPv4 mode, only A records are resolved. In IPv6 mode, both AAAA and A records are resolved (A records normalized to `::ffff:x.x.x.x`).

Temporary failures (EAI_AGAIN) are retried up to 20 times. Permanent failures are reported to stderr (unless `--dns-silent`).

## Input behavior

| Option | Default | Meaning |
|--------|---------|---------|
| `--dont-fix-network` | off | Disable network address normalization (`1.1.1.17/24` read as `1.1.1.17-1.1.1.255` instead of `1.1.1.0/24`) |
| `--default-prefix N` / `-p N` | 32 | Default prefix for bare IPs without a mask |

## Feature detection

For scripts that need to check which features are available:

```bash
iprange --has-compare    && echo "compare modes available"
iprange --has-reduce     && echo "reduce mode available"
iprange --has-filelist-loading  && echo "@filename supported"
iprange --has-directory-loading && echo "@directory supported"
iprange --has-ipv6       && echo "IPv6 supported"
```

Each flag exits 0 if the feature is present, 1 otherwise.

## Examples

### Firewall optimization

Reduce a country blocklist for optimal ipset performance:

```bash
# Before: 406 entries using 18 prefixes (18 lookups per packet)
iprange -v country_gr.netset >/dev/null

# After: 4326 entries using 3 prefixes (3 lookups per packet)
# Same 6.3 million unique IPs matched
iprange -v --ipset-reduce 20 country_gr.netset >/dev/null
```

The reduction is lossless: piping the reduced output back through `iprange` reproduces the original set exactly.

### Blocklist management

```bash
# Merge multiple blocklists into one optimized set
iprange list1.txt list2.txt list3.txt > merged.txt

# Find IPs that appear in all blocklists
iprange --common list1.txt list2.txt list3.txt > common.txt

# Create a blocklist but exclude your own networks
iprange merged.txt --except my-networks.txt > final.txt

# Check if two versions of a blocklist differ
iprange old.txt --diff new.txt --quiet
echo $?  # 0 = no changes, 1 = changed

# Compare overlap between blocklists
iprange --compare --header list1.txt list2.txt list3.txt
```

### IPv6 workflows

```bash
# Merge IPv6 blocklists
iprange -6 v6-list1.txt v6-list2.txt

# Mix IPv4 and IPv6 in one file (IPv6 mode normalizes IPv4)
iprange -6 mixed-input.txt

# Count unique IPv6 addresses
iprange -6 -C v6-list.txt

# Binary cache for IPv6
iprange -6 --print-binary large-v6.txt > cache-v6.bin
iprange -6 cache-v6.bin
```

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

To skip the man page: `./configure --disable-man`

## Testing

| Command | What it tests |
|---------|---------------|
| `make check` | Full test suite (CLI + build) |
| `./run-tests.sh` | CLI regression tests |
| `./run-build-tests.sh` | Build and layout regressions |
| `./run-sanitizer-tests.sh` | ASAN/UBSAN/TSAN coverage |
| `make check-sanitizers` | Sanitizer-integrated path |

## Repository layout

| Directory | Contents |
|-----------|----------|
| `src/` | C sources and headers |
| `wiki/` | Documentation (synced to GitHub wiki) |
| `packaging/` | Spec template, ebuild, release tooling |
| `tests.d/` | CLI regression tests |
| `tests.build.d/` | Build and layout regressions |
| `tests.sanitizers.d/` | Sanitizer CLI regressions |
| `tests.tsan.d/` | TSAN regressions |
| `tests.unit/` | Unit-style internal harnesses |

## Documentation

Detailed guides in the [`wiki/`](wiki/) directory (also published to the [GitHub wiki](https://github.com/firehol/iprange/wiki)):

- [Input formats](wiki/input-formats.md) — every accepted format, file lists, directories, binary
- [Output formats](wiki/output-formats.md) — CIDR, ranges, single IPs, binary, CSV, prefix/suffix
- [Operations](wiki/operations.md) — merge, intersect, exclude, diff, reduce, compare, count
- [IPv6 support](wiki/ipv6.md) — address family, normalization, cross-family rules
- [DNS resolution](wiki/dns-resolution.md) — threading, retry, configuration
- [Optimizing ipsets for iptables](wiki/ipset-reduce.md) — prefix reduction with examples

## Getting help

```bash
iprange --help     # full option reference
iprange --version  # version and copyright
```
