# Complement / Exclude

Merge all files before `--except`, then remove all IPs matched by the files after it.

**Aliases**: `--except`, `--exclude-next`, `--complement`, `--complement-next`

## How it works

This is a **positional** operation:
1. All files before `--except` are merged into set **A**
2. Each file after `--except` is subtracted from A, one by one
3. The result is the IPs in A that are not in any of the subtracted sets

## Examples

Remove specific entries from a whitelist:

```
# allow.txt               # deny.txt
10.0.0.0/8                10.0.0.0/24
172.16.0.0/12             172.16.0.0/16
192.168.0.0/16            192.168.1.100
```

```
$ iprange allow.txt --except deny.txt
10.0.1.0/24
10.0.2.0/23
10.0.4.0/22
10.0.8.0/21
10.0.16.0/20
10.0.32.0/19
10.0.64.0/18
10.0.128.0/17
10.1.0.0/16
10.2.0.0/15
10.4.0.0/14
10.8.0.0/13
10.16.0.0/12
10.32.0.0/11
10.64.0.0/10
10.128.0.0/9
172.17.0.0/16
172.18.0.0/15
172.20.0.0/14
172.24.0.0/13
192.168.0.0/24
192.168.1.0/26
192.168.1.64/27
192.168.1.96/30
192.168.1.101
192.168.1.102/31
192.168.1.104/29
192.168.1.112/28
192.168.1.128/25
192.168.2.0/23
192.168.4.0/22
192.168.8.0/21
192.168.16.0/20
192.168.32.0/19
192.168.64.0/18
192.168.128.0/17
```

The `10.0.0.0/24` was carved out of `10.0.0.0/8`, leaving the remaining address space as multiple CIDRs. The single IP `192.168.1.100` was punched out of `192.168.0.0/16`, creating a gap around it.

## IPv6

```
$ printf '2001:db8::/32\n' > all.txt
$ printf '2001:db8:1::/48\n' > remove.txt
$ iprange -6 all.txt --except remove.txt | head -5
2001:db8::/48
2001:db8:2::/47
2001:db8:4::/46
2001:db8:8::/45
2001:db8:10::/44
```

Carving a /48 out of a /32 leaves 16 CIDR blocks covering the remaining address space.
