#!/usr/bin/env python3
"""A stub JSON-RPC engine for negative controls.

Answers `iprange.v1.system.describe` with a correct, well-formed
result frame, then exits with the status given as argv[1]. Used to
prove that the scenario runner fails an engine that answers every
call correctly and then dies nonzero.
"""

import json
import sys


def main():
    exit_status = int(sys.argv[1]) if len(sys.argv) > 1 else 1
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
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
    sys.exit(exit_status)


if __name__ == "__main__":
    main()
