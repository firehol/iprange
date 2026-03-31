#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

../../iprange --help >"$tmpdir/help.out" 2>"$tmpdir/help.err"
help_rc=$?
if [ $help_rc -ne 0 ]; then
    echo "# ERROR: --help should exit 0, got $help_rc"
    cat "$tmpdir/help.err"
    exit 1
fi
if ! grep -q "iprange manages IP ranges" "$tmpdir/help.out"; then
    echo "# ERROR: --help output is missing the usage text"
    cat "$tmpdir/help.out"
    exit 1
fi
echo "# OK: --help exited 0"

../../iprange --version >"$tmpdir/version.out" 2>"$tmpdir/version.err"
version_rc=$?
if [ $version_rc -ne 0 ]; then
    echo "# ERROR: --version should exit 0, got $version_rc"
    cat "$tmpdir/version.err"
    exit 1
fi
if ! grep -q "^iprange " "$tmpdir/version.out"; then
    echo "# ERROR: --version output is missing the version line"
    cat "$tmpdir/version.out"
    exit 1
fi
echo "# OK: --version exited 0"
