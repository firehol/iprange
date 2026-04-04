# Symmetric Difference

Print the IPs that exist in either A or B, but **not both**.

**Aliases**: `--diff`, `--diff-next`

## How it works

This is a **positional** operation:
1. All files before `--diff` are merged into set **A**
2. All files after `--diff` are merged into set **B**
3. The output is (A - B) union (B - A) — the XOR of the two sets

**Exit code**: 0 if the sets are identical (no output), 1 if there are differences. This makes `--diff` useful in scripts to detect changes.

## Examples

Compare two versions of a blocklist:

```
# before.txt            # after.txt
10.0.0.0/24             10.0.0.0/24
10.0.1.0/24             10.0.1.0/25      # shrunk
10.0.2.0/24             10.0.2.0/24
                         10.0.3.0/24      # added
```

```
$ iprange before.txt --diff after.txt
10.0.1.128/25
10.0.3.0/24
```

The output shows `10.0.1.128/25` (the upper half of the /24 that was shrunk to a /25) and `10.0.3.0/24` (newly added). The two entries that remained identical are excluded.

**Exit code check**:

```
$ iprange before.txt --diff after.txt
10.0.1.128/25
10.0.3.0/24
$ echo $?
1
```

```
$ iprange before.txt --diff before.txt
$ echo $?
0
```

**Quiet mode** — suppress output, only check exit code:

```
$ iprange before.txt --diff after.txt --quiet
$ echo $?
1
```
