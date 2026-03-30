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
    --exclude='./*.o' \
    --exclude='./iprange' \
    --exclude='./*.plist' \
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
    make -j"$(nproc)"
)

BUILD_DIR="$BUILD_DIR" TEST_DIRS="tests.sanitizers.d" IPRANGE_BIN="$BUILD_DIR/iprange" "$ROOT_DIR/run-tests.sh"
BUILD_DIR="$BUILD_DIR" CC="$CC_BIN" TEST_CFLAGS="$SAN_CFLAGS" TEST_LDFLAGS="$SAN_LDFLAGS" "$ROOT_DIR/run-unit-tests.sh"

(
    cd "$TSAN_BUILD_DIR"
    CC="$CC_BIN" \
    CFLAGS="$TSAN_CFLAGS" \
    LDFLAGS="$TSAN_LDFLAGS" \
    "$SRC_DIR/configure" --disable-man
    make -j"$(nproc)"
)

BUILD_DIR="$TSAN_BUILD_DIR" TEST_DIRS="tests.tsan.d" IPRANGE_BIN="$TSAN_BUILD_DIR/iprange" "$ROOT_DIR/run-tests.sh"
