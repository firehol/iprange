#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

outside="$tmpdir/outside"
mkdir -p "$outside"
printf '5.6.7.8\n' >"$outside/target.txt"

run_case() {
    # run_case <dir> <stdout> <stderr>: the released CLI merges every
    # file of one @directory input; stdout is compared by the harness.
    timeout 5 ../../iprange "@$1" >"$2" 2>"$3"
    echo $?
}

# A directory entry whose symlink target is a regular file is loaded:
# the C reference classifies entries with stat() + S_ISREG
# (src/iprange.h FILE_LIST) and Rust legacy/parse.rs uses fs::metadata,
# both of which follow symlinks.
mixed="$tmpdir/mixed"
mkdir -p "$mixed"
printf '1.2.3.4\n' >"$mixed/a-regular.txt"
ln -s "$outside/target.txt" "$mixed/c-symlink.txt"
out="$tmpdir/mixed.out"; err="$tmpdir/mixed.err"
rc=$(run_case "$mixed" "$out" "$err")
if [ "$rc" -ne 0 ]; then
    echo "# ERROR: @directory with a symlinked regular file failed (rc=$rc)"
    cat "$err"
    exit 1
fi
if [ -s "$err" ]; then
    echo "# ERROR: loading a symlinked regular file must not emit errors"
    cat "$err"
    exit 1
fi
grep -qx '1.2.3.4' "$out" || { echo "# ERROR: regular file missing from the merge"; cat "$out"; exit 1; }
grep -qx '5.6.7.8' "$out" || { echo "# ERROR: symlinked regular file missing from the merge"; cat "$out"; exit 1; }

# A directory holding only symlinks to regular files is valid input and
# must exit 0 with its content loaded.
links="$tmpdir/links"
mkdir -p "$links"
ln -s "$outside/target.txt" "$links/one.txt"
ln -s "$outside/target.txt" "$links/two.txt"
out="$tmpdir/links.out"; err="$tmpdir/links.err"
rc=$(run_case "$links" "$out" "$err")
if [ "$rc" -ne 0 ]; then
    echo "# ERROR: @directory of symlinked regular files failed (rc=$rc)"
    cat "$err"
    exit 1
fi
grep -qx '5.6.7.8' "$out" || { echo "# ERROR: symlink-only directory loaded nothing"; cat "$out"; exit 1; }

# Non-regular symlink targets stay skipped, and skipping must not block:
# a symlink to a FIFO has no writer, so an open that trusted the entry
# type would hang until the timeout.
special="$tmpdir/special"
mkdir -p "$special"
printf '9.9.9.9\n' >"$special/a-regular.txt"
mkfifo "$special/pipe"
ln -s "$special/pipe" "$special/pipe-link.txt"
out="$tmpdir/special.out"; err="$tmpdir/special.err"
rc=$(run_case "$special" "$out" "$err")
if [ "$rc" -ne 0 ]; then
    echo "# ERROR: @directory with a symlinked FIFO failed (rc=$rc)"
    cat "$err"
    exit 1
fi
if [ -s "$err" ]; then
    echo "# ERROR: skipping a symlinked FIFO must not emit errors"
    cat "$err"
    exit 1
fi
grep -qx '9.9.9.9' "$out" || { echo "# ERROR: regular file missing beside a symlinked FIFO"; cat "$out"; exit 1; }

echo "# OK: @directory loads symlinked regular files like stat() + S_ISREG"
