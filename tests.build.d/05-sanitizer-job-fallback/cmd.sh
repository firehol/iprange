#!/bin/bash

tmpdir=$(mktemp -d)
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
trap 'rm -rf "$tmpdir"' EXIT

root="$tmpdir/root"
fakebin="$tmpdir/bin"
log="$tmpdir/make.log"

mkdir -p "$root" "$fakebin"

cp "$script_dir/../../run-sanitizer-tests.sh" "$root/run-sanitizer-tests.sh"

cat >"$root/run-tests.sh" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod +x "$root/run-tests.sh"

cat >"$root/run-unit-tests.sh" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod +x "$root/run-unit-tests.sh"

cat >"$fakebin/autoreconf" <<'EOF'
#!/bin/sh
cat > configure <<'CONF'
#!/bin/sh
exit 0
CONF
chmod +x configure
exit 0
EOF
chmod +x "$fakebin/autoreconf"

cat >"$fakebin/getconf" <<'EOF'
#!/bin/sh
if [ "$1" = "_NPROCESSORS_ONLN" ]; then
    echo 3
    exit 0
fi
exit 1
EOF
chmod +x "$fakebin/getconf"

cat >"$fakebin/nproc" <<'EOF'
#!/bin/sh
exit 127
EOF
chmod +x "$fakebin/nproc"

cat >"$fakebin/make" <<'EOF'
#!/bin/sh
echo "$*" >> "$MAKE_LOG"
exit 0
EOF
chmod +x "$fakebin/make"

if ! PATH="$fakebin:/usr/bin:/bin" MAKE_LOG="$log" "$root/run-sanitizer-tests.sh" >/dev/null 2>&1; then
    echo "run-sanitizer-tests.sh failed unexpectedly"
    exit 1
fi

if [ "$(grep -c -- '-j3' "$log")" -ne 2 ]; then
    echo "run-sanitizer-tests.sh did not use the getconf fallback job count"
    cat "$log"
    exit 1
fi

echo "# OK: run-sanitizer-tests falls back when nproc is unavailable"
