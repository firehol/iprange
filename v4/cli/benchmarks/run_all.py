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


def s0_detect_verdict(returncode, output):
    """Why s0-detect passed, or why it did not.

    s0-detect is the mismatch detector. A nonzero exit is not enough:
    a crash, a missing worker, or a dead binary also exits nonzero and
    compares no field. The failure must name the compared field.
    """
    if returncode == 0:
        return "passed; a field difference must fail"
    if "implementation" not in output or "rust=" not in output:
        return f"failed without naming the compared field: {output[-300:]!r}"
    return ""


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
            reason = s0_detect_verdict(
                completed.returncode, completed.stdout + completed.stderr)
            if reason:
                failed.append(f"{name} {reason}")
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
