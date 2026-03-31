#!/bin/bash

set -e

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
WORK_DIR=$(mktemp -d)
SRC_DIR="$WORK_DIR/src"
BUILD_DIR="$WORK_DIR/build-asan"
TSAN_BUILD_DIR="$WORK_DIR/build-tsan"
CC_BIN=${CC:-clang}
SAN_CFLAGS=${SAN_CFLAGS:-"-g -O1 -fno-omit-frame-pointer -fsanitize=address,undefined"}
SAN_LDFLAGS=${SAN_LDFLAGS:-"-fsanitize=address,undefined"}
TSAN_CFLAGS=${TSAN_CFLAGS:-"-g -O1 -fno-omit-frame-pointer -fsanitize=thread"}
TSAN_LDFLAGS=${TSAN_LDFLAGS:-"-fsanitize=thread"}

get_make_jobs() {
    if command -v nproc >/dev/null 2>&1; then
        jobs=$(nproc 2>/dev/null)
        if [ -n "$jobs" ]; then
            echo "$jobs"
            return
        fi
    fi

    if command -v getconf >/dev/null 2>&1; then
        jobs=$(getconf _NPROCESSORS_ONLN 2>/dev/null)
        if [ -n "$jobs" ]; then
            echo "$jobs"
            return
        fi
    fi

    echo 1
}

cleanup() {
    rm -rf "$WORK_DIR"
}

trap cleanup EXIT

mkdir -p "$SRC_DIR"

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
    -C "$ROOT_DIR" -cf - . | tar -C "$SRC_DIR" -xf -

if [ ! -x "$SRC_DIR/configure" ]; then
    (cd "$SRC_DIR" && autoreconf -fi)
fi

mkdir -p "$BUILD_DIR"
mkdir -p "$TSAN_BUILD_DIR"

(
    cd "$BUILD_DIR"
    CC="$CC_BIN" \
    CFLAGS="$SAN_CFLAGS" \
    LDFLAGS="$SAN_LDFLAGS" \
    "$SRC_DIR/configure" --disable-man
    make -j"$(get_make_jobs)"
)

BUILD_DIR="$BUILD_DIR" TEST_DIRS="tests.sanitizers.d" IPRANGE_BIN="$BUILD_DIR/iprange" "$ROOT_DIR/run-tests.sh"
BUILD_DIR="$BUILD_DIR" CC="$CC_BIN" TEST_CFLAGS="$SAN_CFLAGS" TEST_LDFLAGS="$SAN_LDFLAGS" "$ROOT_DIR/run-unit-tests.sh"

(
    cd "$TSAN_BUILD_DIR"
    CC="$CC_BIN" \
    CFLAGS="$TSAN_CFLAGS" \
    LDFLAGS="$TSAN_LDFLAGS" \
    "$SRC_DIR/configure" --disable-man
    make -j"$(get_make_jobs)"
)

BUILD_DIR="$TSAN_BUILD_DIR" TEST_DIRS="tests.tsan.d" IPRANGE_BIN="$TSAN_BUILD_DIR/iprange" "$ROOT_DIR/run-tests.sh"
