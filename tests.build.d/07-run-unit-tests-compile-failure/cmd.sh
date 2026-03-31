#!/bin/bash

tmpdir=$(mktemp -d)
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
trap 'rm -rf "$tmpdir"' EXIT

root="$tmpdir/root"
fakebin="$tmpdir/bin"
log="$tmpdir/run-unit-tests.log"

mkdir -p "$root/tests.unit" "$fakebin"

cp "$script_dir/../../run-unit-tests.sh" "$root/run-unit-tests.sh"

cat >"$root/config.h" <<'EOF'
/* synthetic config */
EOF

cat >"$root/ipset.c" <<'EOF'
/* stub */
EOF
cp "$root/ipset.c" "$root/ipset_binary.c"
cp "$root/ipset.c" "$root/ipset_combine.c"
cp "$root/ipset.c" "$root/ipset_common.c"
cp "$root/ipset.c" "$root/ipset_copy.c"
cp "$root/ipset.c" "$root/ipset_diff.c"
cp "$root/ipset.c" "$root/ipset_exclude.c"
cp "$root/ipset.c" "$root/ipset_load.c"
cp "$root/ipset.c" "$root/ipset_merge.c"
cp "$root/ipset.c" "$root/ipset_optimize.c"
cp "$root/ipset.c" "$root/ipset_print.c"
cp "$root/ipset.c" "$root/ipset_reduce.c"

cat >"$root/tests.unit/broken.c" <<'EOF'
int main(void) { return 0; }
EOF

cat >"$fakebin/fake-cc" <<'EOF'
#!/bin/sh
echo "synthetic compiler failure" >&2
exit 1
EOF
chmod +x "$fakebin/fake-cc"

if CC="$fakebin/fake-cc" BUILD_DIR="$root" UNIT_TESTS_DIR=tests.unit "$root/run-unit-tests.sh" >"$log" 2>&1; then
    echo "run-unit-tests.sh unexpectedly succeeded after a compile failure"
    cat "$log"
    exit 1
fi

if ! grep -q 'synthetic compiler failure' "$log"; then
    echo "run-unit-tests.sh did not surface the compiler error output"
    cat "$log"
    exit 1
fi

if ! grep -q 'Unit test build failed' "$log"; then
    echo "run-unit-tests.sh did not report the failure as a build failure"
    cat "$log"
    exit 1
fi

if grep -q 'Unit test failed: Exit code 127' "$log"; then
    echo "run-unit-tests.sh still reported a runtime failure after compile failure"
    cat "$log"
    exit 1
fi

echo "# OK: run-unit-tests surfaces compiler failures as build failures"
