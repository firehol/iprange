"""Compare one golden describe response with both product binaries.

check_golden.py validates the committed exchange and does not run a
binary. This script sends the golden request and compares the contract
fields. implementation and product_version are build identity, not the
contract, so they are not required to match the golden.
"""

import argparse
import json
import os
import sys

from client import BenchSession


STABLE = (
    "method",
    "product",
    "jsonrpc_version",
    "api_version",
    "format",
    "families",
    "methods",
    "export_formats",
    "limits",
    "platform_result_fields",
)


def fail(message):
    print(f"FAIL {message}", file=sys.stderr)
    return 1


def golden_exchange(path):
    with open(path, encoding="utf-8") as stream:
        document = json.load(stream)
    for exchange in document["exchanges"]:
        request = exchange["request"]
        if request.get("method") == "iprange.v1.system.describe":
            return request, exchange["response"]
    raise AssertionError("golden describe exchange is missing")


def describe(binary, request):
    session = BenchSession(binary)
    try:
        response = session.service.call(
            request["id"], request["method"], request.get("params", {}))
    finally:
        session.close()
    if "error" in response:
        raise AssertionError(response["error"])
    return response


def compare(label, response, golden_response):
    if response.get("id") != golden_response.get("id"):
        raise AssertionError(f"{label} id {response.get('id')!r} != golden")
    got = response["result"]
    want = golden_response["result"]
    for field in STABLE:
        if got.get(field) != want.get(field):
            raise AssertionError(f"{label} {field} differs from the golden")
    protocol = got.get("fault_worker", {}).get("protocol")
    if protocol != want.get("fault_worker", {}).get("protocol"):
        raise AssertionError(f"{label} fault_worker.protocol={protocol!r}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rust", required=True)
    parser.add_argument("--go", required=True)
    parser.add_argument("--golden", required=True)
    args = parser.parse_args()
    request, golden_response = golden_exchange(args.golden)
    for label, binary in (("rust", args.rust), ("go", args.go)):
        if not os.path.isabs(binary) or not os.access(binary, os.X_OK):
            return fail(f"{label} is not an absolute executable: {binary}")
        compare(label, describe(binary, request), golden_response)
    print("PASS golden describe matches both product binaries")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, AssertionError, KeyError) as exc:
        sys.exit(fail(str(exc)))
