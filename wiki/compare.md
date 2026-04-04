# Compare

Compare IP sets pairwise and print CSV with overlap statistics.

Three comparison modes are available:

## Compare all (`--compare`)

Compare every file with every other file.

```
# list-a.txt          # list-b.txt          # list-c.txt
10.0.0.0/24           10.0.0.128/25         10.0.0.0/16
10.0.1.0/24           10.0.2.0/24           172.16.0.0/12
192.168.1.0/24        192.168.1.0/24
```

```
$ iprange --compare --header list-a.txt list-b.txt list-c.txt
name1,name2,entries1,entries2,ips1,ips2,combined_ips,common_ips
list-a.txt,list-b.txt,2,3,768,640,1024,384
list-a.txt,list-c.txt,2,2,768,1114112,1114368,512
list-b.txt,list-c.txt,3,2,640,1114112,1114368,384
```

**Columns**: name1, name2, entries in each, unique IPs in each, combined (union) IPs, common (intersection) IPs.

## Compare first (`--compare-first`)

Compare the first file against each subsequent file.

```
$ iprange --compare-first --header list-a.txt list-b.txt list-c.txt
name,entries,unique_ips,common_ips
list-b.txt,3,640,384
list-c.txt,2,1114112,512
```

This is useful for checking how much of a reference list overlaps with each of several other lists.

## Compare next (`--compare-next`)

**Positional**: compare files before the option against files after it.

```
$ iprange list-a.txt --compare-next list-b.txt list-c.txt --header
name1,name2,entries1,entries2,ips1,ips2,combined_ips,common_ips
list-a.txt,list-b.txt,2,3,768,640,1024,384
list-a.txt,list-c.txt,2,2,768,1114112,1114368,512
```

Only `list-a.txt` (before `--compare-next`) is compared against `list-b.txt` and `list-c.txt` (after it).

## Naming files in CSV output

Use `as NAME` after a filename to set a custom name in the CSV:

```
$ iprange --count-unique-all --header list-a.txt as "Blocklist A" list-b.txt as "Blocklist B"
name,entries,unique_ips
Blocklist A,2,768
Blocklist B,3,640
```

## CSV header

All CSV modes default to no header. Add `--header` to include column names as the first row.
