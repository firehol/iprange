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
    --exclude='./*.o' \
    --exclude='./iprange' \
    --exclude='./*.plist' \
    --exclude='./Makefile' \
    --exclude='./config.h' \
    --exclude='./config.log' \
    --exclude='./config.status' \
    -C "$srcroot" -cf - . | tar -C "$srcdir" -xf -

if [ ! -x "$srcdir/configure" ]; then
    (cd "$srcdir" && autoreconf -fi) >"$log" 2>&1 || {
        cat "$log"
        exit 1
    }
fi

for object in \
    iprange.o \
    ipset.o \
    ipset_binary.o \
    ipset_combine.o \
    ipset_common.o \
    ipset_copy.o \
    ipset_diff.o \
    ipset_exclude.o \
    ipset_load.o \
    ipset_merge.o \
    ipset_optimize.o \
    ipset_print.o \
    ipset_reduce.o; do
    : >"$srcdir/$object"
done

mkdir -p "$tmpdir/build"
if ! (
    cd "$tmpdir/build" &&
    "$srcdir/configure" --disable-man --without-compare-with-common >"$log" 2>&1 &&
    make -j1 >>"$log" 2>&1
); then
    cat "$log"
    exit 1
fi

echo "# OK: out-of-tree build succeeds after a prior in-tree build dirtied the source tree"
