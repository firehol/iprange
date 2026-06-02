#!/bin/bash

set -euo pipefail

tmpdir=$(mktemp -d)
log="$tmpdir/build.log"
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
srcroot=$(cd "$script_dir/../.." && pwd)
srcdir="$tmpdir/src"
builddir="$tmpdir/build"
expected="$tmpdir/expected"
actual="$tmpdir/actual"

trap 'rm -rf "$tmpdir"' EXIT

mkdir -p "$srcdir"

tar \
    --exclude='./build-default' \
    --exclude='./build-asan' \
    --exclude='./build-tsan' \
    --exclude='./.deps' \
    --exclude='./src/.deps' \
    --exclude='./*.o' \
    --exclude='./src/*.o' \
    --exclude='./iprange' \
    --exclude='./*.plist' \
    --exclude='./src/*.plist' \
    --exclude='./Makefile' \
    --exclude='./config.h' \
    --exclude='./config.log' \
    --exclude='./config.status' \
    --exclude='./config.cache' \
    --exclude='./iprange.spec' \
    --exclude='./packaging/iprange.spec' \
    --exclude='./local-build-objects.stamp' \
    --exclude='./stamp-h1' \
    -C "$srcroot" -cf - . | tar -C "$srcdir" -xf -

if [ ! -x "$srcdir/configure" ]; then
    (cd "$srcdir" && autoreconf -fi) >"$log" 2>&1 || {
        cat "$log"
        exit 1
    }
fi

mkdir -p "$builddir"
cd "$builddir"

if ! "$srcdir/configure" --disable-man CFLAGS="-DIPRANGE_FORCE_PORTABLE_U128 -g -O2" >"$log" 2>&1; then
    cat "$log"
    exit 1
fi

if ! make -j1 >>"$log" 2>&1; then
    cat "$log"
    exit 1
fi

{
    echo "# Merge:"
    printf "2001:db8::3\n2001:db8::1\n2001:db8::2\n" | ./iprange -6

    echo "# Common:"
    printf "2001:db8::/32\n" > "$tmpdir/a"
    printf "2001:db8:1::/48\n" > "$tmpdir/b"
    ./iprange -6 "$tmpdir/a" --common "$tmpdir/b"

    echo "# Binary roundtrip:"
    printf "2001:db8::1\n2001:db8::2\nfe80::1\n" | ./iprange -6 --print-binary | ./iprange -6

    echo "# Count /64:"
    echo "2001:db8:1::/64" | ./iprange -6 -C

    echo "# Max adjacency:"
    printf "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff\n::\n" | ./iprange -6
} >"$actual" 2>&1

cat >"$expected" <<'EOF'
# Merge:
2001:db8::1
2001:db8::2/127
# Common:
2001:db8:1::/48
# Binary roundtrip:
2001:db8::1
2001:db8::2
fe80::1
# Count /64:
1,18446744073709551616
# Max adjacency:
::
ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff
EOF

if ! diff -u "$expected" "$actual"; then
    echo "portable uint128 build produced unexpected IPv6 results"
    cat "$log"
    exit 1
fi

echo "# OK: forced portable uint128 build succeeds and preserves IPv6 behavior"
