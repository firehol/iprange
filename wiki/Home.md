# iprange

`iprange` is a fast command-line tool for managing IPv4 and IPv6 address sets. It reads IPs, CIDRs, ranges, and hostnames, normalizes them into optimal non-overlapping sets, and performs set operations: union, intersection, difference, complement, comparison, and prefix reduction.

For 1 million input lines, a merge completes in under a second.

## Documentation

### Operations

- [Merge / Union](merge.md) — merge all inputs into one optimized set (default mode)
- [Intersection](intersect.md) — find IPs common to all inputs
- [Complement / Exclude](exclude.md) — remove one set from another
- [Symmetric Difference](diff.md) — find IPs in either set but not both
- [Reduce Prefixes](reduce.md) — reduce CIDR prefix diversity for firewall performance
- [Compare](compare.md) — compare sets pairwise as CSV (all, first, next)
- [Count Unique](count-unique.md) — count entries and unique IPs as CSV

### Reference

- [Input formats](input-formats.md) — every accepted format, file lists, directories, binary input
- [Output formats](output-formats.md) — CIDR, ranges, single IPs, binary, CSV, prefix/suffix strings
- [IPv6 support](ipv6.md) — address family selection, normalization, cross-family rules
- [DNS resolution](dns-resolution.md) — parallel threading, retry, configuration
- [Optimizing ipsets for iptables](ipset-reduce.md) — extended tutorial with real-world examples

## Quick reference

```
iprange [options] file1 file2 ...
```

### Address family

| Flag | Mode |
|------|------|
| *(default)* | IPv4 |
| `-4` / `--ipv4` | Explicit IPv4 |
| `-6` / `--ipv6` | IPv6 (accepts both IPv6 and IPv4, normalizes IPv4 to `::ffff:x.x.x.x`) |

### Operations

| Option | Operation | Output |
|--------|-----------|--------|
| *(default)* | Union / merge | CIDR |
| `--common` | Intersection | CIDR |
| `--except` | A minus B (positional) | CIDR |
| `--diff` | Symmetric difference (positional) | CIDR |
| `--ipset-reduce N` | Merge + reduce prefixes | CIDR |
| `--compare` | All vs all | CSV |
| `--compare-first` | First vs rest | CSV |
| `--compare-next` | Group vs group (positional) | CSV |
| `--count-unique` / `-C` | Merged counts | CSV |
| `--count-unique-all` | Per-file counts | CSV |

### Output format

| Option | Format |
|--------|--------|
| *(default)* | CIDR (`10.0.0.0/24`) |
| `-j` / `--print-ranges` | Ranges (`10.0.0.0-10.0.0.255`) |
| `-1` / `--print-single-ips` | One IP per line |
| `--print-binary` | Binary (same-architecture cache) |

### Feature detection

```bash
iprange --has-compare           # compare modes
iprange --has-reduce            # reduce mode
iprange --has-filelist-loading  # @filename support
iprange --has-directory-loading # @directory support
iprange --has-ipv6              # IPv6 support
```

Each exits 0 if the feature is present.

## Related projects

- [FireHOL IP Lists](https://iplists.firehol.org) — curated collection of IP blocklists, updated daily using `iprange`
- [FireHOL](https://github.com/firehol/firehol) — Linux firewall tool that uses `iprange` for ipset management
