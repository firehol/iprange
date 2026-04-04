# Count Unique

Print entry counts and unique IP counts as CSV.

Two counting modes are available:

## Count merged (`--count-unique` / `-C`)

Merge all inputs and print a single CSV line with the totals:

```
# list-a.txt          # list-b.txt
10.0.0.0/24           10.0.0.128/25
10.0.1.0/24           10.0.2.0/24
192.168.1.0/24        192.168.1.0/24
```

```
$ iprange -C --header list-a.txt list-b.txt
entries,unique_ips
2,1024
```

The merged set has 2 entries (ranges) covering 1024 unique IPs.

## Count per file (`--count-unique-all`)

Print one CSV line per input file without merging:

```
$ iprange --count-unique-all --header list-a.txt list-b.txt list-c.txt
name,entries,unique_ips
list-a.txt,2,768
list-b.txt,3,640
list-c.txt,2,1114112
```

## Naming files in CSV output

Use `as NAME` to customize the name column:

```
$ iprange --count-unique-all --header list-a.txt as "Blocklist A" list-b.txt as "Blocklist B"
name,entries,unique_ips
Blocklist A,2,768
Blocklist B,3,640
```

## IPv6

```
$ printf '2001:db8::/32\n' | iprange -6 -C --header
entries,unique_ips
1,79228162514264337593543950336
```

IPv6 unique IP counts are printed as full 128-bit decimal numbers.

## CSV header

Add `--header` to include column names. Without it, only data lines are printed.
