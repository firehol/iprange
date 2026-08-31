"""Declarative external-case schema (iprange-cli-case-v1).

The runner consumes cases exclusively through this schema. Case files
live in v4/cli/cases/*.json and benchmark manifests in
v4/cli/benchmarks/*.json.
"""

from .engine import validate
from . import common as C

CASE = {
    "type": "object",
    "properties": {
        "schema": {"type": "string", "enum": ["iprange-cli-case-v1"]},
        "name": {"type": "string", "min_len": 1},
        "requires": {"type": "string"},
        "fixtures": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {
                    "path": C.PATH,
                    "source": {
                        "type": "one_of",
                        "options": [
                            {"type": "object",
                             "properties": {"text": {"type": "string"}},
                             "required": ["text"], "additional": False},
                            {"type": "object",
                             "properties": {"base64": {"type": "string", "base64": True}},
                             "required": ["base64"], "additional": False},
                            {"type": "object",
                             "properties": {"generator": {"type": "string"},
                                            "seed": {"type": "integer", "min": 0}},
                             "required": ["generator", "seed"], "additional": False},
                        ],
                    },
                },
                "required": ["path", "source"],
                "additional": False,
            },
        },
        "steps": {
            "type": "array",
            "items": {
                "type": "one_of",
                "options": [
                    # rpc step
                    {"type": "object",
                     "properties": {
                         "kind": {"type": "string", "enum": ["rpc"]},
                         "method": {"type": "string"},
                         "params": {"type": "object"},
                         "expect_result": {"type": "object"},
                         "expect_error": {
                             "type": "object",
                             "properties": {
                                 "code": {"type": "string"},
                                 "outcome": {"type": "string"},
                             },
                             "required": ["code"], "additional": False,
                         },
                         "capture": {"type": "array", "items": {"type": "string"}, "max": 16},
                         "assert_files": {"type": "array", "items": {"type": "object"}, "max": 64},
                     },
                     "required": ["kind", "method", "params"], "additional": False},
                    # legacy step
                    {"type": "object",
                     "properties": {
                         "kind": {"type": "string", "enum": ["legacy"]},
                         "argv": {"type": "array", "items": C.PATH, "min": 1},
                         "stdin_fixture": C.PATH,
                         "exit_status": {"type": "integer", "min": 0, "max": 255},
                         "stdout": {"type": "object"},
                         "stderr": {"type": "object"},
                         "assert_files": {"type": "array", "items": {"type": "object"}, "max": 64},
                     },
                     "required": ["kind", "argv"], "additional": False},
                ],
            },
            "min": 1,
        },
        "assertions": {"type": "object"},
    },
    "required": ["schema", "name", "fixtures", "steps"],
    "additional": False,
}


def validate_case(case):
    validate(case, CASE, "case")
    return case


def validate_rpc_request(method, params):
    from . import methods
    validate(params, methods.METHODS[method]["params"], f"params[{method}]")


def validate_rpc_result(method, result):
    from . import results
    results.validate_result(method, result)
