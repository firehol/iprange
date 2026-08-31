"""Strict schema validators for the iprange JSON-RPC v1 production API.

This package is the machine authority for the external qualification
suite (v4/cli/run.py and v4/cli/bench.py). It imports no SDK and no
third-party library: every validator is standard-library Python.

The normative contract is .agents/sow/specs/iprange-jsonrpc-v1.md.
Rules implemented here:

- JSON-RPC 2.0 envelopes: exact "2.0", string/integral ids only,
  unknown members rejected, batch bounds 1..16, one active request
  plus 16 queued.
- Frame ceiling 1,048,576 bytes input and output; response object
  ceiling 65,000 bytes.
- u64/cardinality/identity values are canonical unsigned decimal
  strings; u32 values are JSON integers; ids are 32 lowercase hex;
  digests are exact-width lowercase hex; base64 is canonical RFC 4648.
- All v1 method params/results are object-valued, snake_case, and
  reject unknown members.
"""
from .engine import validate, ValidationError
from . import common, frame, methods, results, cases

__all__ = ["validate", "SchemaError", "ValidationError", "common", "frame", "methods", "results", "cases"]
