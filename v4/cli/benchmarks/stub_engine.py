#!/usr/bin/env python3
"""A stub JSON-RPC engine for negative controls.

Answers `iprange.v1.system.describe` with a correct, well-formed
result frame. The harness spawns the engine as
`[binary, "--jsonrpc"]`, so the stub must accept that argument
shape. `STUB_MODE` selects a death mode (see main); `STUB_EXIT`
optionally overrides the exit status (default 1 — no harness site
sets it; the modes own the timing). When `STUB_MARKER` names a file,
the stub writes a line to it at startup, before reading any request:
the exit tests read the marker to prove this stub actually ran and
the wrapper did not fail before the exec.
"""

import json
import os
import sys
import time


def main():
    if sys.argv[1:] != ["--jsonrpc"]:
        # Legacy CLI mode: a point-in-time invocation with real CLI
        # arguments. The stub answers with a fixed stdout (STUB_CLI_STDOUT,
        # default empty) and exits STUB_CLI_EXIT (default 0), so cli-step
        # detection tests can drive both a pass and a wrong answer.
        if sys.argv[1:] and os.environ.get("STUB_CLI_STDOUT") is not None:
            sys.stdout.write(os.environ["STUB_CLI_STDOUT"])
            sys.exit(int(os.environ.get("STUB_CLI_EXIT", "0")))
        for argument in sys.argv[1:]:
            if argument != "--jsonrpc":
                sys.stderr.write(f"stub_engine: unexpected argument {argument!r}\n")
                sys.exit(2)
    exit_status = int(os.environ.get("STUB_EXIT", "1"))
    marker = os.environ.get("STUB_MARKER")
    if marker:
        with open(marker, "w", encoding="utf-8") as stream:
            stream.write("stub ran\n")
    die_now = os.environ.get("STUB_MODE") == "die-on-first-request"
    die_after = os.environ.get("STUB_MODE") == "die-after-answer"
    silent = os.environ.get("STUB_MODE") == "silent"
    deaf = os.environ.get("STUB_MODE") == "deaf"
    hang_at_eof = os.environ.get("STUB_MODE") == "hang-at-eof"
    if deaf:
        # Read nothing: a proof that waits for its write to be accepted
        # must fail at the write deadline, not hang (the write-arm test
        # pins this with a frame larger than the pipe buffer).
        time.sleep(3600)
        os._exit(exit_status)
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
        if silent:
            # Read the request and hold it: a proof waiting for an
            # answer must fail at its deadline, not hang (SilentPeerTest
            # pins this against a short service read deadline).
            time.sleep(3600)
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
    if hang_at_eof:
        # Answered every request, then hang at stdin EOF: close() must
        # report the force-termination as a qualification failure
        # (this is the non-poisoned forced-raise contract).
        time.sleep(3600)
        os._exit(exit_status)
    sys.exit(exit_status)


if __name__ == "__main__":
    main()
