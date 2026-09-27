"""One JSON-RPC client for the benchmark scripts.

The user decision for this harness is that proofs use JsonRpcService
from v4/cli/run.py. A script must not open its own stdio parser.
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from run import JsonRpcService  # noqa: E402


class BenchSession:
    def __init__(self, binary, killable=False):
        self.service = JsonRpcService(
            [binary, "--jsonrpc"],
            "bench",
            start_new_session=killable,
            read_deadline=120,
            write_deadline=30,
        )
        self.proc = self.service.proc
        self._n = 0

    def call(self, method, params):
        self._n += 1
        response = self.service.call(f"bench-{self._n}", method, params)
        if "error" in response:
            raise AssertionError(response["error"])
        return response["result"]

    def submit(self, method, params):
        self._n += 1
        self.service.submit(f"bench-{self._n}", method, params)

    def raw(self, method, params):
        """Return the whole response, including an error object."""
        self._n += 1
        return self.service.call(f"bench-{self._n}", method, params)

    def kill(self):
        self.service.kill_process_group()

    def close(self):
        self.service.close(allow_forced=True, broken_exchange=True)


def call_json(binary, payload):
    """Run one JSON-RPC request through JsonRpcService and return its result."""
    if isinstance(payload, (bytes, bytearray)):
        payload = json.loads(payload)
    session = BenchSession(binary)
    try:
        return session.call(payload["method"], payload.get("params", {}))
    finally:
        session.close()
