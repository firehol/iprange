# Intersection

Print only the IPs that appear in **all** input files.

**Aliases**: `--common`, `--intersect`, `--intersect-all`

## How it works

Each input file is optimized, then the intersection is computed pairwise. An IP appears in the output only if it is covered by every input file. If the files have no overlap, the output is empty.

## Examples

Find IPs common to two blocklists:

```
# list-a.txt          # list-b.txt
10.0.0.0/24           10.0.0.128/25
10.0.1.0/24           10.0.2.0/24
192.168.1.0/24        192.168.1.0/24
```

```
$ iprange --common list-a.txt list-b.txt
10.0.0.128/25
192.168.1.0/24
```

Only the upper half of `10.0.0.0/24` (which is `10.0.0.128/25`) overlaps with `list-b.txt`'s `10.0.0.128/25`. `192.168.1.0/24` is in both files. `10.0.1.0/24` and `10.0.2.0/24` have no overlap and are excluded.

## IPv6

```
$ printf '2001:db8::/32\n' > v6-a.txt
$ printf '2001:db8:1::/48\n2001:db9::/32\n' > v6-b.txt
$ iprange -6 --common v6-a.txt v6-b.txt
2001:db8:1::/48
```

The `/32` in v6-a.txt contains the `/48` from v6-b.txt. The `2001:db9::/32` in v6-b.txt does not overlap.
