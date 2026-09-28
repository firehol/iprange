#!/usr/bin/env python3
"""A stub JSON-RPC engine for negative controls.

Answers `iprange.v1.system.describe` with a correct, well-formed
result frame, then exits nonzero once stdin closes. The exit status
comes from `STUB_EXIT` in the environment, because the harness spawns
the engine as `[binary, "--jsonrpc"]` and the stub must accept that
argument shape.
"""

import json
import os
import sys


def main():
    for argument in sys.argv[1:]:
        if argument != "--jsonrpc":
            sys.stderr.write(f"stub_engine: unexpected argument {argument!r}\n")
            sys.exit(2)
    exit_status = int(os.environ.get("STUB_EXIT", "1"))
    die_now = os.environ.get("STUB_MODE") == "die-on-first-request"
    die_after = os.environ.get("STUB_MODE") == "die-after-answer"
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        if die_now:
            # Die on the first request, before answering it and before
            # any close() starts. os._exit terminates immediately, with
            # no interpreter teardown, so the engine is dead before the
            # runner can poll it; the exit gate is then the only check
            # that can attribute the nonzero exit.
            os._exit(exit_status)
        request = json.loads(line)
        response = {
            "jsonrpc": "2.0",
            "id": request.get("id"),
            "result": {
                "method": request["method"],
                "product": "stub",
                "jsonrpc_version": "2.0",
                "api_version": "1",
                "format": "v4",
                "families": ["ipv4"],
                "methods": [],
                "export_formats": [],
                "limits": {},
                "platform_result_fields": [],
            },
        }
        sys.stdout.write(json.dumps(response) + "\n")
        sys.stdout.flush()
        if die_after:
            # Immediate termination: the runner has consumed the
            # response. Whether the exit lands before close() polls is
            # scheduling-dependent, so either the gate or close()'s own
            # check attributes it; the contract — the run fails — is
            # pinned either way. os._exit avoids interpreter teardown.
            os._exit(exit_status)
    sys.exit(exit_status)


if __name__ == "__main__":
    main()
