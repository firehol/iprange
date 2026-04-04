# Merge / Union

Merge all inputs into one sorted, deduplicated, non-overlapping set. This is the default operation when no mode flag is given.

**Aliases**: `--optimize`, `--combine`, `--merge`, `--union`, `--union-all`, `-J`

## How it works

All input entries are combined, then sorted and optimized: overlapping ranges are merged, adjacent ranges are combined, and duplicates are eliminated. The result is the smallest set of non-overlapping CIDRs that covers exactly the same IPs.

## Examples

Normalize a file with mixed input formats:

```
# input: mixed.txt
# Blocklist from multiple sources
1.2.3.4
10.0.0.0/24
10.0.0.200 - 10.0.1.50
; another comment
192.168.1.0/255.255.255.0
```

```
$ iprange mixed.txt
1.2.3.4
10.0.0.0/24
10.0.1.0/27
10.0.1.32/28
10.0.1.48/31
10.0.1.50
192.168.1.0/24
```

Merge two overlapping files:

```
# list-a.txt          # list-b.txt
10.0.0.0/24           10.0.0.128/25
10.0.1.0/24           10.0.2.0/24
192.168.1.0/24        192.168.1.0/24
```

```
$ iprange list-a.txt list-b.txt
10.0.0.0/23
10.0.2.0/24
192.168.1.0/24
```

The two /24 networks `10.0.0.0/24` and `10.0.1.0/24` merged into one /23. The duplicate `192.168.1.0/24` was deduplicated. `10.0.0.128/25` was absorbed by `10.0.0.0/24`.

Merge from stdin:

```
$ printf '10.0.0.5\n10.0.0.6\n10.0.0.7\n10.0.0.8\n' | iprange
10.0.0.5
10.0.0.6/31
10.0.0.8
```

Four individual IPs consolidated into optimal CIDRs: one /31 block plus two singles.

## IPv6

```
$ printf '2001:db8::1\n2001:db8::2\n2001:db8::3\n2001:db8::4\n' | iprange -6
2001:db8::1
2001:db8::2/127
2001:db8::4
```

```
$ printf '2001:db8::/48\n2001:db8:1::/48\n' | iprange -6
2001:db8::/47
```

Two adjacent /48 networks merge into one /47.
