# Reduce Prefixes

Merge all inputs, then reduce the number of distinct CIDR prefixes while keeping the matched IP set identical.

**Aliases**: `--ipset-reduce PERCENT`, `--reduce-factor PERCENT`, `--ipset-reduce-entries ENTRIES`, `--reduce-entries ENTRIES`

## Why

netfilter/iptables `hash:net` ipsets perform one lookup per distinct prefix length. An ipset using 18 prefixes does 18 lookups per packet. Reducing to 3 prefixes cuts that to 3 lookups — for the exact same set of matched IPs.

The number of entries does not affect ipset lookup performance.

## Parameters

| Option | Default | Purpose |
|--------|---------|---------|
| `--ipset-reduce PERCENT` | 20 | Allow this % increase in entries |
| `--ipset-reduce-entries ENTRIES` | 16384 | Minimum absolute entry cap |

Maximum acceptable entries = `max(current * (1 + PERCENT/100), ENTRIES)`.

The algorithm iteratively eliminates the prefix with the smallest cost (fewest new entries added), merging it into the next available prefix. Use `-v` to see each step.

## Example

A file with entries spanning many prefix lengths:

```
# input
10.0.0.0/24
10.0.1.0/25
10.0.1.128/26
10.0.1.192/27
10.0.1.224/28
10.0.1.240/29
10.0.1.248/30
10.0.1.252/31
10.0.1.254
10.0.1.255
10.0.2.0/24
10.0.3.0/25
```

Before reduction — 3 prefixes:

```
$ iprange -v input.txt 2>&1 | grep -E 'prefix|totals'
	- prefix /23 counts 1 entries
	- prefix /24 counts 1 entries
	- prefix /25 counts 1 entries
totals: 12 lines read, 1 distinct IP ranges found, 3 CIDR prefixes, 3 CIDRs printed, 896 unique IPs
```

After `--ipset-reduce 50` — 1 prefix:

```
$ iprange -v --ipset-reduce 50 input.txt 2>&1 | grep -E 'prefix|totals|Eliminated'
Eliminated 2 out of 3 prefixes (1 remain in the final set).
	- prefix /25 counts 7 entries
totals: 12 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 7 CIDRs printed, 896 unique IPs
```

Same 896 unique IPs, expressed as 7 /25 entries instead of a mix of /23, /24, and /25. The kernel now does 1 lookup instead of 3.

## Lossless round-trip

The reduction is lossless — piping the reduced output back through `iprange` produces the original optimized set:

```
$ iprange --ipset-reduce 100 input.txt | iprange -C
1,896
$ iprange input.txt | iprange -C
1,896
```

See [Optimizing ipsets for iptables](ipset-reduce.md) for an extended tutorial with real-world country and blocklist examples.
