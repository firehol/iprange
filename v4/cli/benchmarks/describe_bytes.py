"""Measure the raw describe frame size on both release binaries.

The two engines' frames differ by design — the `implementation`
member self-names the engine (`rust` vs `go`). This prints each
frame's byte length and whether the frames are identical, so the
byte divergence is a measured fact with a staged log, not an
assertion about serialization.
"""

import argparse
import importlib.util
import os
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.dirname(_HERE))
from run import JsonRpcService  # noqa: E402

_spec = importlib.util.spec_from_file_location(
    "benchmarks_run", os.path.join(_HERE, "run.py"))
_bench = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_bench)
readline_bounded = _bench.readline_bounded


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rust", required=True)
    parser.add_argument("--go", required=True)
    args = parser.parse_args()
    frames = {}
    for label, binary in (("rust", args.rust), ("go", args.go)):
        service = JsonRpcService([binary, "--jsonrpc"], label)
        try:
            service.proc.stdin.write(
                b'{"jsonrpc":"2.0","id":"1","method":"iprange.v1.system.describe","params":{}}\n')
            service.proc.stdin.flush()
            frames[label] = readline_bounded(service.proc.stdout)
        finally:
            service.close()
    rust, go = frames["rust"], frames["go"]
    print(f"rust {len(rust)} bytes")
    print(f"go {len(go)} bytes")
    print("identical" if rust == go else "differ")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, AssertionError, KeyError) as exc:
        print(f"FAIL {exc}", file=sys.stderr)
        sys.exit(1)
