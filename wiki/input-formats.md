# Input formats

`iprange` accepts one entry per line. All formats can coexist in the same file.

## IPv4 (default mode)

### Addresses and CIDRs

| Format | Example | Expansion |
|--------|---------|-----------|
| Dotted decimal | `1.2.3.4` | Single IP |
| CIDR prefix | `1.2.3.0/24` | 1.2.3.0 - 1.2.3.255 |
| Dotted netmask | `1.2.3.0/255.255.255.0` | Same as /24 |
| Abbreviated | `10.1` | `inet_aton()` expansion |
| Decimal integer | `16909060` | 1.2.3.4 |
| Octal | `012.0.0.1` | 10.0.0.1 (leading zero = octal) |
| Hex | `0x0A000001` | 10.0.0.1 |

IPv4 parsing uses `inet_aton()`, which accepts all the above forms. Be careful with leading zeros — `010.0.0.1` is octal 8.0.0.1, not decimal 10.0.0.1.

By default, CIDRs are normalized to the network address: `1.1.1.17/24` is read as `1.1.1.0/24`. Use `--dont-fix-network` to disable this.

The default prefix for bare IPs (no `/` suffix) is /32. Change with `--default-prefix N`.

### Ranges

| Format | Example | Meaning |
|--------|---------|---------|
| IP range | `1.2.3.0 - 1.2.3.255` | Explicit start-end |
| CIDR range | `1.2.3.0/24 - 1.2.4.0/24` | Network of first to broadcast of second |
| Mixed | `1.2.3.0/24 - 1.2.4.0/255.255.255.0` | CIDR and netmask can be mixed |

The dash can have optional spaces around it.

### Hostnames

Hostnames (one per line) are resolved via parallel DNS queries. In IPv4 mode, only A records are resolved. If a hostname resolves to multiple IPs, all are added.

See [DNS resolution](dns-resolution.md) for threading and configuration.

## IPv6 (`-6` mode)

### Addresses and CIDRs

| Format | Example | Notes |
|--------|---------|-------|
| Full notation | `2001:0db8:0000:0000:0000:0000:0000:0001` | |
| Compressed | `2001:db8::1` | Standard `::` compression |
| Loopback | `::1` | |
| CIDR | `2001:db8::/32` | Prefix 0-128 |
| IPv4-mapped | `::ffff:10.0.0.1` | |
| Plain IPv4 | `10.0.0.1` | Auto-normalized to `::ffff:10.0.0.1` |

IPv6 parsing uses `inet_pton(AF_INET6)`.

### Ranges

IPv6 ranges use the same `addr1 - addr2` syntax. Both endpoints must be the same address family — a range like `10.0.0.1 - 2001:db8::1` is rejected as a mixed-family error.

### Hostnames

In IPv6 mode, hostnames are resolved for both AAAA and A records. A-record results are normalized to IPv4-mapped IPv6 (`::ffff:x.x.x.x`).

## Comments and whitespace

- `#` or `;` at the start of a line marks it as a comment.
- `#` or `;` after an IP/range/hostname starts an inline comment (rest of line ignored).
- Empty lines and leading/trailing whitespace are silently skipped.

## File inputs

### Regular files

```bash
iprange file1.txt file2.txt file3.txt
```

Each file argument is loaded as a separate ipset. For modes like `--compare`, each file appears as a separate column in the output.

### stdin

```bash
cat blocklist.txt | iprange -
# or just:
cat blocklist.txt | iprange
```

If no file arguments are given, stdin is assumed. Explicit `-` reads stdin.

### File lists (`@filename`)

```bash
iprange @my-lists.txt
```

The file `my-lists.txt` contains one filename per line. Comments (`#`, `;`) and empty lines are ignored. Each listed file is loaded as a separate ipset.

```
# my-lists.txt
/path/to/blocklist-a.txt
/path/to/blocklist-b.txt
# /path/to/disabled.txt
```

Feature detection: `iprange --has-filelist-loading` exits 0 if supported.

### Directory loading (`@directory`)

```bash
iprange @/etc/firehol/ipsets/
```

All regular files in the directory are loaded (sorted alphabetically), each as a separate ipset. Subdirectories are not traversed.

Feature detection: `iprange --has-directory-loading` exits 0 if supported.

### Naming for CSV output

Any file argument can be followed by `as NAME` to override its name in CSV output:

```bash
iprange --compare --header file1.txt as "Blocklist A" file2.txt as "Blocklist B"
```

## Binary input

Binary files (produced by `--print-binary`) are auto-detected by their header line:
- IPv4 binary: format v1.0
- IPv6 binary: format v2.0

Loading a binary file of the wrong family is an error. In IPv4 mode, an IPv6 binary file is rejected. In IPv6 mode, an IPv4 binary file is rejected.

Binary files are architecture-specific (no endianness conversion). They are intended as a same-machine cache, not a portable interchange format.
