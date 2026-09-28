"""Run every committed scenario against both release binaries.

`s0-detect` is the mismatch detector. It must fail, and its failure
must name the compared field. Any other failure is not a field
difference. Every other scenario must pass. The worker binary must sit
beside each `iprange`.
"""

import argparse
import glob
import os
import subprocess
import sys


def run_one(runner, rust, go, scenario):
    completed = subprocess.run(
        [sys.executable, runner, "--rust", rust, "--go", go, "--scenario", scenario],
        capture_output=True, text=True, check=False,
    )
    sys.stdout.write(completed.stdout)
    sys.stdout.flush()
    if completed.stderr:
        sys.stderr.write(completed.stderr)
        sys.stderr.flush()
    return completed


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rust", required=True)
    parser.add_argument("--go", required=True)
    args = parser.parse_args()
    root = os.path.dirname(os.path.abspath(__file__))
    runner = os.path.join(root, "run.py")
    failed = []
    for scenario in sorted(glob.glob(os.path.join(root, "scenarios", "*.json"))):
        name = os.path.basename(scenario)
        completed = run_one(runner, args.rust, args.go, scenario)
        if name == "s0-detect.json":
            output = completed.stdout + completed.stderr
            if completed.returncode == 0:
                failed.append(f"{name} passed; a field difference must fail")
            elif "implementation" not in output or "rust=" not in output:
                failed.append(
                    f"{name} failed without naming the compared field: "
                    f"{output[-300:]!r}")
            else:
                print(f"PASS {name} detected the implementation mismatch",
                      flush=True)
            continue
        if completed.returncode != 0:
            failed.append(name)
    if failed:
        print("FAIL " + ", ".join(failed), file=sys.stderr, flush=True)
        return 1
    print("PASS all scenarios", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
