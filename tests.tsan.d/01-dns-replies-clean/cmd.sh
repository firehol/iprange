#!/bin/bash

tmpdir=$(mktemp -d)
stderr="$tmpdir/stderr"
trap 'rm -rf "$tmpdir"' EXIT

python3 - <<'PY' > "$tmpdir/input.txt"
for _ in range(50):
    print("localhost")
PY

TSAN_OPTIONS='halt_on_error=1 exitcode=66' ../../iprange "$tmpdir/input.txt" >/dev/null 2>"$stderr"
rc=$?

if [ $rc -ne 0 ]; then
    echo "# ERROR: TSAN DNS run failed"
    cat "$stderr"
    exit 1
fi

if grep -q "ThreadSanitizer" "$stderr"; then
    echo "# ERROR: TSAN reported a DNS reply queue race"
    cat "$stderr"
    exit 1
fi

echo "# OK: DNS reply queue is TSAN-clean"
