# Output formats

## CIDR (default)

The default output is sorted, non-overlapping CIDR blocks:

```
10.0.0.0/24
10.0.1.0/25
10.0.1.128/26
```

This is the optimal representation — the fewest entries that cover the exact set of IPs.

### Controlling CIDR prefixes

**Minimum prefix** — restrict the largest block size:

```bash
# Only /24 to /32 (no blocks larger than /24)
iprange --min-prefix 24 blocklist.txt
```

A /16 network would be expressed as 256 /24 entries. Warning: misuse can produce very large output.

**Specific prefixes** — allow only certain prefix lengths:

```bash
# Only /16, /24, and /32
iprange --prefixes 16,24,32 blocklist.txt
```

Prefix /32 is always enabled regardless of settings.

For IPv6, the same options work with prefix range 0-128.

## Ranges (`--print-ranges` / `-j`)

Print start-end ranges:

```
10.0.0.0-10.0.0.255
10.0.1.0-10.0.1.191
```

Single IPs print as ranges with identical endpoints: `10.0.0.5-10.0.0.5`

## Single IPs (`--print-single-ips` / `-1`)

Enumerate every individual IP address:

```
10.0.0.0
10.0.0.1
10.0.0.2
...
```

**Safety cap**: ranges larger than 16,777,216 IPs (256^3) are skipped with a warning to stderr. This prevents unbounded output from large ranges like `0.0.0.0/0`. The cap applies to both IPv4 and IPv6 modes.

## Binary (`--print-binary`)

Machine-readable binary format for fast round-trips:

```bash
# Save
iprange --print-binary blocklist.txt > cache.bin

# Load
iprange cache.bin
```

Binary files include:
- A header line identifying the format version (v1.0 for IPv4, v2.0 for IPv6)
- Metadata: family, optimization flag, record count, unique IP count
- An endianness marker
- Raw address-pair records

Binary files are **architecture-specific** — they use native byte order and are intended as a same-machine cache. Do not transfer between machines with different endianness.

## CSV output

CSV output is produced by the comparison and counting modes:

| Mode | Columns |
|------|---------|
| `--compare` | name1, name2, entries1, entries2, ips1, ips2, combined_ips, common_ips |
| `--compare-first` | name, entries, unique_ips, common_ips |
| `--compare-next` | name1, name2, entries1, entries2, ips1, ips2, combined_ips, common_ips |
| `--count-unique` | entries, unique_ips |
| `--count-unique-all` | name, entries, unique_ips |

Add `--header` to print the column header as the first line.

## Prefix and suffix strings

Customize output lines with arbitrary strings before and after each entry. This is useful for generating ipset restore commands, iptables rules, or other tool-specific formats.

### Basic usage

```bash
# Add prefix to every line
iprange --print-prefix "add myset " blocklist.txt
# Output: add myset 10.0.0.0/24

# Add suffix to every line
iprange --print-suffix " timeout 3600" blocklist.txt
# Output: 10.0.0.0/24 timeout 3600
```

### Separate handling for IPs and networks

Single IPs (/32 entries) and networks (other prefixes) can have different prefixes and suffixes. This is useful when single IPs and networks go into different ipsets:

```bash
iprange \
  --print-prefix-ips "add single-ips " \
  --print-prefix-nets "add networks " \
  blocklist.txt
# Output:
#   add networks 10.0.0.0/24
#   add single-ips 10.0.1.5
```

| Option | Applies to |
|--------|------------|
| `--print-prefix STRING` | Both IPs and networks |
| `--print-prefix-ips STRING` | Single IPs only (/32) |
| `--print-prefix-nets STRING` | Networks only (not /32) |
| `--print-suffix STRING` | Both IPs and networks |
| `--print-suffix-ips STRING` | Single IPs only (/32) |
| `--print-suffix-nets STRING` | Networks only (not /32) |

## Quiet mode

Use `--quiet` with `--diff` to suppress output and only use the exit code:

```bash
iprange old.txt --diff new.txt --quiet
echo $?  # 0 = identical, 1 = different
```
