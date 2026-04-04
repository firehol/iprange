# Optimizing ipsets for iptables

netfilter/iptables `hash:net` ipsets (netsets) are a fast way to manage IP lists for firewall rules. The number of entries in an ipset does not affect lookup performance. However, each **distinct prefix length** in the netset adds one extra lookup per packet. A netset using all 32 possible IPv4 prefixes forces 32 lookups per packet.

`iprange --ipset-reduce` consolidates prefixes while keeping the matched IP set identical. For example, one /23 entry becomes two /24 entries — same IPs, one fewer prefix.

## Parameters

| Option | Default | Purpose |
|--------|---------|---------|
| `--ipset-reduce PERCENT` | 20 | Allow this % increase in entries |
| `--ipset-reduce-entries ENTRIES` | 16384 | Minimum absolute entry cap |

You enable reduce mode by giving either option. The maximum acceptable entries is computed as:

```
max(current_entries * (1 + PERCENT / 100), ENTRIES)
```

This design works well across all netset sizes:
- Small netsets (hundreds of entries) are scaled up to ENTRIES
- Large netsets (hundreds of thousands) are scaled by PERCENT

## Algorithm

The algorithm is optimal: at each step it finds the prefix whose elimination adds the fewest new entries, merges it into the next available prefix, and repeats until the entry limit is reached. Use `-v` to see the elimination steps.

## Example: country netset

The GeoLite2 netset for Greece:

```bash
$ iprange -C --header country_gr.netset
entries,unique_ips
406,6304132
```

406 entries, 6.3 million unique IPs. The prefix breakdown (`-v`):

```
prefix /13 counts 1 entries
prefix /14 counts 3 entries
prefix /15 counts 7 entries
prefix /16 counts 42 entries
prefix /17 counts 19 entries
prefix /18 counts 17 entries
prefix /19 counts 21 entries
prefix /20 counts 21 entries
prefix /21 counts 30 entries
prefix /22 counts 50 entries
prefix /23 counts 50 entries
prefix /24 counts 98 entries
prefix /25 counts 4 entries
prefix /27 counts 2 entries
prefix /28 counts 7 entries
prefix /29 counts 25 entries
prefix /31 counts 3 entries
prefix /32 counts 6 entries
```

**18 distinct prefixes** = 18 lookups per packet.

After reduction with 20% entry increase:

```bash
$ iprange -v --ipset-reduce 20 country_gr.netset >/dev/null
Eliminated 15 out of 18 prefixes (3 remain in the final set).

prefix /21 counts 3028 entries
prefix /24 counts 398 entries
prefix /32 counts 900 entries
```

**3 prefixes, 4,326 entries** — same 6.3 million unique IPs. The kernel now does 3 lookups instead of 18.

With a higher entry cap:

```bash
$ iprange -v --ipset-reduce 20 --ipset-reduce-entries 50000 country_gr.netset >/dev/null
Eliminated 16 out of 18 prefixes (2 remain in the final set).

prefix /24 counts 24622 entries
prefix /32 counts 900 entries
```

**2 prefixes, 25,522 entries** — one more prefix eliminated thanks to the higher entry budget.

## Example: large blocklist

A large blocklist (218,307 entries, 25 prefixes, 765 million IPs):

```bash
$ iprange -v --ipset-reduce 20 --ipset-reduce-entries 50000 \
    ib_bluetack_level1.netset >/dev/null
Eliminated 17 out of 25 prefixes (8 remain in the final set).

prefix /16 counts 11118 entries
prefix /20 counts 5216 entries
prefix /24 counts 46718 entries
prefix /26 counts 17902 entries
prefix /27 counts 18123 entries
prefix /28 counts 32637 entries
prefix /29 counts 94802 entries
prefix /32 counts 33570 entries
```

From 25 prefixes to 8, entries from 218,307 to 260,086. At 50%: 6 prefixes. At 100%: 5 prefixes.

## Lossless round-trip

The reduction is lossless. Piping reduced output back through `iprange` reproduces the original optimized set:

```bash
iprange --ipset-reduce 100 blocklist.txt | iprange -v >/dev/null
# output is identical to: iprange -v blocklist.txt >/dev/null
```

## Typical usage

```bash
# Moderate reduction (good default)
iprange --ipset-reduce 20 blocklist.txt > reduced.txt

# Aggressive reduction for small lists
iprange --ipset-reduce 20 --ipset-reduce-entries 50000 country.netset > reduced.txt

# Generate ipset restore commands from reduced set
iprange --ipset-reduce 20 --print-prefix "add myset " blocklist.txt
```
