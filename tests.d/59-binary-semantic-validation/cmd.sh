#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

write_binary() {
    local path="$1"
    local optimized="$2"
    local records="$3"
    local lines="$4"
    local unique="$5"
    shift 5

    {
        printf 'iprange binary format v1.0\n'
        printf '%s\n' "$optimized"
        printf 'record size 8\n'
        printf 'records %s\n' "$records"
        printf 'bytes %s\n' $((4 + (records * 8)))
        printf 'lines %s\n' "$lines"
        printf 'unique ips %s\n' "$unique"
        perl -e 'print pack("V", 0x1A2B3C4D)'
        while [ $# -gt 0 ]; do
            perl -e "print pack('V', $1), pack('V', $2)"
            shift 2
        done
    } >"$path"
}

fake_counts="$tmpdir/fake-counts.bin"
write_binary "$fake_counts" optimized 1 999 999 \
    0x04030201 0x04030201

../../iprange --count-unique-all "$fake_counts" >"$tmpdir/fake-counts.out" 2>"$tmpdir/fake-counts.err"
rc=$?

if [ $rc -eq 0 ]; then
    echo "# ERROR: binary file with fake counters was accepted"
    cat "$tmpdir/fake-counts.out"
    cat "$tmpdir/fake-counts.err"
    exit 1
fi

if [ -s "$tmpdir/fake-counts.out" ]; then
    echo "# ERROR: binary file with fake counters should not produce output"
    cat "$tmpdir/fake-counts.out"
    exit 1
fi

duplicate_records="$tmpdir/duplicate-records.bin"
write_binary "$duplicate_records" optimized 2 2 2 \
    0x04030201 0x04030202 \
    0x04030202 0x04030202

../../iprange "$duplicate_records" >"$tmpdir/duplicate-records.out" 2>"$tmpdir/duplicate-records.err"
rc=$?

if [ $rc -eq 0 ]; then
    echo "# ERROR: binary file claiming optimized with overlapping records was accepted"
    cat "$tmpdir/duplicate-records.out"
    cat "$tmpdir/duplicate-records.err"
    exit 1
fi

if [ -s "$tmpdir/duplicate-records.out" ]; then
    echo "# ERROR: malformed optimized binary should not print output"
    cat "$tmpdir/duplicate-records.out"
    exit 1
fi

echo "# OK: malformed binary semantic metadata is rejected"
