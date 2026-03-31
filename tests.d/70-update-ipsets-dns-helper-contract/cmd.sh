#!/bin/bash

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

printf 'localhost\n' | ../../iprange -1 --dns-threads 4 --dns-silent >"$tmpdir/silent.out" 2>"$tmpdir/silent.err"
silent_rc=$?

if [ $silent_rc -ne 0 ]; then
    echo "# ERROR: --dns-silent resolver mode failed"
    cat "$tmpdir/silent.err"
    exit 1
fi

if [ "$(cat "$tmpdir/silent.out")" != "127.0.0.1" ]; then
    echo "# ERROR: --dns-silent resolver stdout contract changed"
    cat "$tmpdir/silent.out"
    exit 1
fi

if [ -s "$tmpdir/silent.err" ]; then
    echo "# ERROR: --dns-silent should not emit stderr on successful resolution"
    cat "$tmpdir/silent.err"
    exit 1
fi

printf 'localhost\n' | ../../iprange -1 --dns-threads 4 --dns-silent --dns-progress >"$tmpdir/progress.out" 2>"$tmpdir/progress.err"
progress_rc=$?

if [ $progress_rc -ne 0 ]; then
    echo "# ERROR: --dns-progress resolver mode failed"
    cat "$tmpdir/progress.err"
    exit 1
fi

if [ "$(cat "$tmpdir/progress.out")" != "127.0.0.1" ]; then
    echo "# ERROR: --dns-progress resolver stdout contract changed"
    cat "$tmpdir/progress.out"
    exit 1
fi

if ! grep -q '0%' "$tmpdir/progress.err"; then
    echo "# ERROR: --dns-progress should emit progress on stderr"
    cat "$tmpdir/progress.err"
    exit 1
fi

echo "# OK: update-ipsets DNS helper contracts work"
