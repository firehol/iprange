#!/bin/bash

# A bare directory given as an input file is an empty set, not an error.
#
# C opens input files with fopen(path, "r") (src/iprange.h iprange_fopen_read),
# which on Linux succeeds for a directory; the first fgets() then fails, and
# ipset_load() treats a failed first read as an empty file and returns the
# empty set (src/ipset_load.c:271-280). So `iprange somedir` exits 0 and
# contributes nothing, whether or not the directory holds files -- it is
# never expanded. Directory expansion is a separate `@directory` feature
# (src/iprange.c:745 onward), which does report an error for a directory
# with no regular files.
#
# This is deliberately narrow: it turns a successful open of a directory
# into an empty set and nothing else. iprange_fopen_read() (src/iprange.h:
# 57-69) is the only open site, and the C error path it does have is the
# fopen() failure, so every other failure keeps the exact C diagnostic:
# a missing file, a directory the caller cannot open (EACCES there, not
# EISDIR here), and `@emptydir` all still fail with exit 1.
#
# Exit codes and stdout are compared for every case; the failure cases also
# pin the stderr bytes.

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

IPRANGE=../../iprange
REFERENCE=${IPRANGE_REFERENCE:-/usr/bin/iprange}

mkdir -p "$tmpdir/empty" "$tmpdir/withfiles"
printf '10.0.0.1\n' > "$tmpdir/withfiles/f1"
printf '10.0.0.2\n' > "$tmpdir/withfiles/f2"
printf '198.51.100.0/24\n' > "$tmpdir/a"

fail=0
check() { # check <label> <expected-rc> <expected-stdout> <args...>
    local label=$1 erc=$2 eout=$3
    shift 3
    timeout 20 "$IPRANGE" "$@" < /dev/null > "$tmpdir/out" 2> "$tmpdir/err"
    local rc=$?
    if [ "$rc" -ne "$erc" ]; then
        echo "# ERROR: $label exit $rc, expected $erc"; cat "$tmpdir/err"; fail=1; return
    fi
    if ! cmp -s <(printf '%b' "$eout") "$tmpdir/out"; then
        echo "# ERROR: $label stdout differs"; cat -v "$tmpdir/out"; fail=1; return
    fi
    if [ -x "$REFERENCE" ]; then
        timeout 20 "$REFERENCE" "$@" < /dev/null > "$tmpdir/ref_out" 2> /dev/null
        local rrc=$?
        if [ "$rrc" -ne "$rc" ] || ! cmp -s "$tmpdir/ref_out" "$tmpdir/out"; then
            echo "# ERROR: $label differs from the C reference ($REFERENCE)"
            cat -v "$tmpdir/ref_out" "$tmpdir/out"; fail=1; return
        fi
    fi
    echo "# OK: $label (exit $rc)"
}

# check_err <label> <expected-rc> <expected-stderr> <args...>: the read
# failures must keep the C diagnostic bytes, so that "a directory is an
# empty set" cannot be reached by swallowing errors in general.
check_err() {
    local label=$1 erc=$2 eerr=$3
    shift 3
    timeout 20 "$IPRANGE" "$@" < /dev/null > "$tmpdir/out" 2> "$tmpdir/err"
    local rc=$?
    if [ "$rc" -ne "$erc" ]; then
        echo "# ERROR: $label exit $rc, expected $erc"; cat -v "$tmpdir/err"; fail=1; return
    fi
    if ! cmp -s <(printf '%b' "$eerr") "$tmpdir/err"; then
        echo "# ERROR: $label stderr differs"; cat -v "$tmpdir/err"; fail=1; return
    fi
    if [ -x "$REFERENCE" ]; then
        timeout 20 "$REFERENCE" "$@" < /dev/null > /dev/null 2> "$tmpdir/ref_err"
        local rrc=$?
        if [ "$rrc" -ne "$rc" ] || ! cmp -s "$tmpdir/ref_err" "$tmpdir/err"; then
            echo "# ERROR: $label differs from the C reference ($REFERENCE)"; fail=1; return
        fi
    fi
    echo "# OK: $label (exit $rc)"
}

check "empty directory as input"        0 ''                  "$tmpdir/empty"
check "directory with files as input"   0 ''                  "$tmpdir/withfiles"
check "directory beside a real file"    0 '198.51.100.0/24\n' "$tmpdir/a" "$tmpdir/withfiles"
check "directory first, file second"    0 '198.51.100.0/24\n' "$tmpdir/withfiles" "$tmpdir/a"
check "IPv6 mode, directory as input"   0 ''                  -6 "$tmpdir/empty"
# The same rule inside a "@file list": C loads each entry with the same
# ipset_load(), so a directory entry is an empty ipset there too.
printf '%s\n%s\n' "$tmpdir/withfiles/f1" "$tmpdir/withfiles" > "$tmpdir/list_mixed"
printf '%s\n' "$tmpdir/empty" > "$tmpdir/list_dir_only"
check "directory entry inside @list"      0 '10.0.0.1\n'        "@$tmpdir/list_mixed"
check "@list whose only entry is a dir"   0 ''                  "@$tmpdir/list_dir_only"

# The distinct paths that must keep failing.
# A directory the caller may not even open keeps the C diagnostic: only the
# successful-open/failed-read case (a directory) is an empty set. Skipped
# when running as root, where mode 000 does not deny access.
if [ "$(id -u)" != "0" ]; then
    mkdir -p "$tmpdir/unreadable"
    printf '10.0.0.3\n' > "$tmpdir/unreadable/f"
    chmod 000 "$tmpdir/unreadable"
    check_err "unreadable directory keeps the C diagnostic" 1 \
        "iprange: $tmpdir/unreadable - Permission denied\niprange: Cannot load ipset: $tmpdir/unreadable\n" \
        "$tmpdir/unreadable"
    chmod 755 "$tmpdir/unreadable"
fi
check "missing file still fails"        1 ''                  "$tmpdir/no-such-file"
check "@empty directory still fails"    1 ''                  "@$tmpdir/empty"
check "@directory with files loads"     0 '10.0.0.1\n10.0.0.2\n' "@$tmpdir/withfiles"

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "# OK: 11 directory-input cases match the C reference"
