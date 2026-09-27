"""Run every committed scenario against both release binaries.

`s0-detect` is the mismatch detector. It must fail. Every other
scenario must pass. The worker binary must sit beside each `iprange`.
"""

import argparse
import glob
import os
import subprocess
import sys


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
        completed = subprocess.run(
            [sys.executable, runner, "--rust", args.rust, "--go", args.go, "--scenario", scenario],
            check=False,
        )
        if name == "s0-detect.json":
            if completed.returncode == 0:
                failed.append(f"{name} passed; a field difference must fail")
            else:
                print(f"PASS {name} detected a field difference")
            continue
        if completed.returncode != 0:
            failed.append(name)
    if failed:
        print("FAIL " + ", ".join(failed), file=sys.stderr)
        return 1
    print("PASS all scenarios")
    return 0


if __name__ == "__main__":
    sys.exit(main())
