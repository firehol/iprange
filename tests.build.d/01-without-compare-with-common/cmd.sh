#!/bin/bash

tmpdir=$(mktemp -d)
log="$tmpdir/build.log"
trap 'rm -rf "$tmpdir"' EXIT

srcroot=$(cd ../.. && pwd)
srcdir="$tmpdir/src"

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

mkdir -p "$tmpdir/build"
cd "$tmpdir/build"

if ! "$srcdir/configure" --disable-man --without-compare-with-common >"$log" 2>&1; then
    cat "$log"
    exit 1
fi

if ! make -j1 >>"$log" 2>&1; then
    cat "$log"
    exit 1
fi

echo "# OK: --without-compare-with-common build succeeds"
