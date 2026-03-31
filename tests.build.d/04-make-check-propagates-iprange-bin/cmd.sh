#!/bin/bash

tmpdir=$(mktemp -d)
log="$tmpdir/build.log"
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
srcroot=$(cd "$script_dir/../.." && pwd)
srcdir="$tmpdir/src"
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

(cd "$srcdir" && autoreconf -fi) >"$log" 2>&1 || {
    cat "$log"
    exit 1
}

cat >"$srcdir/run-tests.sh" <<'EOF'
#!/bin/sh
if [ -z "$IPRANGE_BIN" ] || [ ! -x "$IPRANGE_BIN" ]; then
    echo "missing executable IPRANGE_BIN in run-tests.sh"
    exit 1
fi
exit 0
EOF
chmod +x "$srcdir/run-tests.sh"

cat >"$srcdir/run-build-tests.sh" <<'EOF'
#!/bin/sh
if [ -z "$IPRANGE_BIN" ] || [ ! -x "$IPRANGE_BIN" ]; then
    echo "missing executable IPRANGE_BIN in run-build-tests.sh"
    exit 1
fi
exit 0
EOF
chmod +x "$srcdir/run-build-tests.sh"

mkdir -p "$tmpdir/build"
cd "$tmpdir/build"

if ! "$srcdir/configure" --disable-man >"$log" 2>&1; then
    cat "$log"
    exit 1
fi

if ! make check >>"$log" 2>&1; then
    cat "$log"
    exit 1
fi

echo "# OK: make check propagates IPRANGE_BIN to run-build-tests.sh"
