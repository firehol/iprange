# Operations

All operations produce sorted, non-overlapping output. The output family matches the input family (IPv4 or IPv6).

## Merge / Union (default)

Merge all inputs into one optimized set. This is the default when no mode option is given.

```bash
iprange file1.txt file2.txt file3.txt
```

Overlapping and adjacent ranges are combined. Duplicates are eliminated. The result is the union of all input sets.

Aliases: `--optimize`, `--combine`, `--merge`, `--union`, `--union-all`, `-J`

## Intersection (`--common`)

Print only the IPs that appear in **all** input files.

```bash
iprange --common file1.txt file2.txt file3.txt
```

If any file has no overlap with the others, the output is empty.

Aliases: `--common`, `--intersect`, `--intersect-all`

## Complement / Exclude (`--except`)

This is a **positional** operation. Files before `--except` form set A; files after form set B. The output is A minus B.

```bash
iprange allow.txt --except deny.txt
```

Multiple files can appear on either side:

```bash
iprange a1.txt a2.txt --except b1.txt b2.txt b3.txt
```

Files before `--except` are merged into A. Each file after is subtracted from A sequentially.

Aliases: `--except`, `--exclude-next`, `--complement`, `--complement-next`

## Symmetric Difference (`--diff`)

This is a **positional** operation. Files before `--diff` form set A; files after form set B. The output is the IPs in A or B but **not both** (the XOR).

```bash
iprange before.txt --diff after.txt
```

**Exit code**: 0 if the sets are identical (no output), 1 if there are differences.

Use `--quiet` to suppress the output and only check the exit code:

```bash
iprange old.txt --diff new.txt --quiet
if [ $? -eq 1 ]; then
    echo "sets differ"
fi
```

Aliases: `--diff`, `--diff-next`

## Reduce Prefixes (`--ipset-reduce`)

Merge all inputs, then reduce the number of distinct CIDR prefixes. The matched IP set remains identical — only the CIDR representation changes.

```bash
iprange --ipset-reduce 20 blocklist.txt
```

See [Optimizing ipsets for iptables](ipset-reduce.md) for a detailed explanation with examples.

| Option | Default | Meaning |
|--------|---------|---------|
| `--ipset-reduce PERCENT` | 20 | Allow this % increase in entries |
| `--ipset-reduce-entries ENTRIES` | 16384 | Minimum acceptable entry count |

Aliases: `--reduce-factor`, `--reduce-entries`

## Compare (CSV modes)

These modes produce CSV output for analyzing overlap between IP sets. Add `--header` to include column names.

### Compare all (`--compare`)

Compare every file with every other file:

```bash
iprange --compare --header file1.txt file2.txt file3.txt
```

Output columns: `name1,name2,entries1,entries2,ips1,ips2,combined_ips,common_ips`

### Compare first (`--compare-first`)

Compare the first file against each subsequent file:

```bash
iprange --compare-first --header reference.txt other1.txt other2.txt
```

Output columns: `name,entries,unique_ips,common_ips`

### Compare next (`--compare-next`)

**Positional**: compare files before the option against files after:

```bash
iprange file1.txt file2.txt --compare-next file3.txt file4.txt
```

Output columns: same as `--compare`

## Count Unique (CSV modes)

### Count merged (`--count-unique` / `-C`)

Merge all inputs and print one CSV line with totals:

```bash
iprange -C --header blocklist.txt
```

Output columns: `entries,unique_ips`

### Count per file (`--count-unique-all`)

Print one CSV line per input file (no merging):

```bash
iprange --count-unique-all --header file1.txt file2.txt
```

Output columns: `name,entries,unique_ips`
