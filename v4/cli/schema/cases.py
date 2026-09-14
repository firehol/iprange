"""Declarative external-case schema (iprange-cli-case-v1).

The schema is intentionally smaller than the runner: every member accepted here
must have one executable runner interpretation.  In particular, expectations
and filesystem assertions are validated before a subprocess is launched.

An rpc step with ``"notification": true`` sends a JSON-RPC notification: the
runner writes one frame without an id and never reads a response for it, so the
step must not carry ``expect_result``, ``expect_error``, ``capture``, or
``assert_files``.  The transport accepts only ``iprange.v1.cancel`` as a
notification, so a notification step must use that method (frame.py contract).

An rpc step with ``"expect_params_rejected"`` is a negative-params test: the
request is intentionally outside the published params contract, the runner
sends it verbatim instead of rejecting it client-side, and the step asserts
the JSON-RPC params-validator answer (transport code ``-32602``, optionally a
message substring).  The mode exists because the runner otherwise validates
every request against this schema before sending it, so a corpus could not
express "the service must refuse this params object" at all.  It is
non-vacuous by construction: the runner re-validates the request and fails
the step when the client-side schema accepts it, which would mean the step
proves nothing about the service validator while the committed schema still
disagrees with the service (schema/cases.py ``validate_rpc_request``).
"""

import os
import re
from pathlib import PureWindowsPath

from .engine import ValidationError, validate
from .frame import CANCEL_METHOD
from . import common as C

_ASSERT_FILE = {
    "type": "object",
    "properties": {
        "path": C.PATH,
        "sha256": {"type": "string", "hex": 64},
        "equals_fixture": C.PATH,
    },
    "required": ["path"],
    "additional": False,
}

_STREAM_EXPECTATION = {
    "type": "one_of",
    "options": [
        {
            "type": "object",
            "properties": {"$exact": {"type": "string"}},
            "required": ["$exact"],
            "additional": False,
        },
        {
            "type": "object",
            "properties": {"$contains": {"type": "string"}},
            "required": ["$contains"],
            "additional": False,
        },
    ],
}

def _rpc_step(expectation):
    properties = {
        "kind": {"type": "string", "enum": ["rpc"]},
        # The service role that executes this step: producer for artifact
        # creation/mutation/publication, consumer for observation and
        # transformation.  Single-language matrices run both roles on the
        # same executable; mixed matrices run them on the two real
        # binaries.  The declared actor is the single routing authority;
        # method names do not imply a role.
        "actor": {"type": "string", "enum": ["producer", "consumer"]},
        "method": {"type": "string", "min_len": 1},
        "params": {"type": "object"},
        "capture": {
            "type": "array",
            "items": {
                "type": "one_of",
                "options": [
                    {"type": "string", "min_len": 1},
                    {
                        "type": "object",
                        "properties": {
                            "name": {"type": "string", "min_len": 1},
                            "path": {"type": "string", "min_len": 1},
                        },
                        "required": ["name", "path"],
                        "additional": False,
                    },
                ],
            },
            "max": 16,
        },
        "assert_files": {"type": "array", "items": _ASSERT_FILE, "max": 64},
        "notification": {"type": "boolean"},
        # Digest-group label.  Steps that must produce byte-identical
        # artifacts label the artifact with the same group; the runner
        # records the observed artifact digest per group and per
        # language, and the kind gate requires one digest per group
        # attested by both product languages.  It exists so a
        # cross-language equivalence claim (for example "Go exports a
        # Rust-written database to the same netset bytes Rust exports")
        # is bound to executed evidence instead of to a prose constant.
        "digest_group": {"type": "string", "min_len": 1, "max_len": 64},
    }
    required = ["kind", "actor", "method", "params"]
    if expectation is not None:
        properties.update(expectation)
        required.extend(expectation)
    return {
        "type": "object",
        "properties": properties,
        "required": required,
        "additional": False,
    }


_EXPECT_RESULT = {"expect_result": {"type": "object"}}
_EXPECT_ERROR = {
    "expect_error": {
        "type": "object",
        "properties": {
            "code": {"type": "string", "min_len": 1},
            "outcome": {"type": "string", "min_len": 1},
            # Exact error-details member set plus value expectations
            # ($ignore supported).  The member set is enforced by the
            # runner: absent members fail, so absent-vs-null parity
            # regressions are caught by the corpus.
            "details": {"type": "object"},
        },
        "required": ["code"],
        "additional": False,
    }
}
_EXPECT_PARAMS_REJECTED = {
    "expect_params_rejected": {
        "type": "object",
        "properties": {
            # Optional substring of the JSON-RPC error message.  Message
            # text is diagnostic-only across engines (an accepted P3
            # difference), so a case pins it only where the Rust
            # authority and the Go port provably share the text; every
            # other negative asserts the transport code alone.
            "message_contains": {"type": "string", "min_len": 1},
        },
        "additional": False,
    }
}
_RPC_STEPS = (
    _rpc_step(None),
    _rpc_step(_EXPECT_RESULT),
    _rpc_step(_EXPECT_ERROR),
    _rpc_step(_EXPECT_PARAMS_REJECTED),
)

_LEGACY_STEP = {
    "type": "object",
    "properties": {
        "kind": {"type": "string", "enum": ["legacy"]},
        "argv": {"type": "array", "items": C.PATH, "min": 1},
        "stdin_fixture": C.PATH,
        "exit_status": {"type": "integer", "min": 0, "max": 255},
        "stdout": _STREAM_EXPECTATION,
        "stderr": _STREAM_EXPECTATION,
        "assert_files": {"type": "array", "items": _ASSERT_FILE, "max": 64},
    },
    "required": ["kind", "argv", "exit_status", "stdout", "stderr"],
    "additional": False,
}

CASE = {
    "type": "object",
    "properties": {
        "schema": {"type": "string", "enum": ["iprange-cli-case-v1"]},
        "name": {"type": "string", "min_len": 1},
        "requires": {"type": "string", "min_len": 1},
        "fixtures": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {
                    "path": C.PATH,
                    "source": {
                        "type": "one_of",
                        "options": [
                            {
                                # text_expand_work rewrites every
                                # "$WORK/" occurrence in the text to the
                                # absolute per-case work directory before
                                # the file is written.  It exists for
                                # @file-list inputs: the list contents
                                # are paths the service reads, so they
                                # cannot be substituted through params.
                                "type": "object",
                                "properties": {
                                    "text": {"type": "string"},
                                    "text_expand_work": {"type": "boolean"},
                                },
                                "required": ["text"],
                                "additional": False,
                            },
                            {
                                "type": "object",
                                "properties": {"base64": {"type": "string", "base64": True}},
                                "required": ["base64"],
                                "additional": False,
                            },
                            {
                                "type": "object",
                                "properties": {
                                    "generator": {"type": "string"},
                                    "seed": {"type": "integer", "min": 0},
                                },
                                "required": ["generator", "seed"],
                                "additional": False,
                            },
                            {
                                # A deterministic v4 database built by the
                                # v4-fixture csv tool from this exact CSV
                                # text; the runner registers the same text
                                # as scalar intervals for the algebra
                                # oracle. `csv_kind` selects the database
                                # value kind (direct or membership).
                                "type": "object",
                                "properties": {
                                    "csv_db": {"type": "string"},
                                    "csv_kind": {
                                        "type": "string",
                                        "enum": ["direct", "membership"],
                                    },
                                },
                                "required": ["csv_db"],
                                "additional": False,
                            },
                            {
                                # A symbolic link whose target is another
                                # fixture path in the same work directory,
                                # spelled absolute so the link resolves
                                # when the engine follows it from any
                                # working directory.  Directory scans
                                # classify entries through the followed
                                # stat, so a symlink to a regular file
                                # must be read by the engine; this source
                                # is what pins that class.
                                "type": "object",
                                "properties": {
                                    "symlink_to": C.PATH,
                                },
                                "required": ["symlink_to"],
                                "additional": False,
                            },
                        ],
                    },
                },
                "required": ["path", "source"],
                "additional": False,
            },
        },
        "steps": {
            "type": "array",
            "items": {"type": "one_of", "options": [*_RPC_STEPS, _LEGACY_STEP]},
            "min": 1,
        },
        "assertions": {
            "type": "object",
            "properties": {"files": {"type": "array", "items": _ASSERT_FILE, "max": 64}},
            "required": ["files"],
            "additional": False,
        },
    },
    "required": ["schema", "name", "fixtures", "steps"],
    "additional": False,
}


def _relative_work_path(value, path):
    if os.path.isabs(value) or PureWindowsPath(value).is_absolute():
        raise ValidationError(f"{path}", "work-directory path must be relative")
    parts = value.split("/")
    if any(part in ("", ".", "..") for part in parts):
        raise ValidationError(f"{path}", "work-directory path must stay inside $WORK")


# Anchored capture-pointer grammar: a member chain where each member
# may carry zero or more ``[index]`` list steps (``a[0].b[1][2]``).
# Members are any nonempty run of characters that are not ``.``, ``[``,
# or ``]``; indexes are decimal digits only.  The anchoring rejects
# trailing/bad brackets (``candidates[0]]``), leading or trailing dots,
# empty members, and pointers that start with an index step.
_POINTER_RE = re.compile(
    r"^[^.\]\[]+(?:\[\d+\])*(?:\.[^.\]\[]+(?:\[\d+\])*)*$")


def pointer_parts(value):
    """Split a capture pointer into its member and ``[index]`` parts.

    Returns the part list for a pointer that matches the anchored
    grammar, or None for any malformed pointer.
    """
    if _POINTER_RE.match(value) is None:
        return None
    return re.findall(r"[^.\[\]]+|\[\d+\]", value)


def _valid_pointer(value, path):
    """A capture pointer is a dotted chain of member names with optional
    ``[index]`` list steps (e.g. ``candidates[0]``); the grammar is
    anchored, so malformed pointers are rejected instead of being
    silently truncated."""
    if pointer_parts(value) is None:
        raise ValidationError(
            f"{path}",
            "capture pointer must be a member chain with optional "
            "\"[index]\" steps (e.g. \"candidates[0]\")")


def validate_case(case):
    """Validate the declarative schema and executable cross-field rules."""
    from . import methods

    validate(case, CASE, "case")

    fixture_paths = []
    for index, fixture in enumerate(case["fixtures"]):
        _relative_work_path(fixture["path"], f"fixtures[{index}].path")
        source = fixture["source"]
        if "symlink_to" in source:
            # The link target is a work-dir path like every other
            # fixture reference: it can never name anything outside
            # $WORK, so a directory-expansion case cannot reach a file
            # the operator did not declare.
            _relative_work_path(source["symlink_to"],
                                f"fixtures[{index}].source.symlink_to")
        fixture_paths.append(fixture["path"])
    if len(fixture_paths) != len(set(fixture_paths)):
        raise ValidationError("fixtures", "fixture paths must be unique")

    rpc_methods = set()
    available_captures = set()
    for index, step in enumerate(case["steps"]):
        if step["kind"] == "legacy":
            if "stdin_fixture" in step:
                _relative_work_path(step["stdin_fixture"], f"steps[{index}].stdin_fixture")
            for assertion_index, assertion in enumerate(step.get("assert_files", [])):
                _relative_work_path(assertion["path"], f"steps[{index}].assert_files[{assertion_index}].path")
                if "equals_fixture" in assertion:
                    _relative_work_path(
                        assertion["equals_fixture"],
                        f"steps[{index}].assert_files[{assertion_index}].equals_fixture",
                    )
            continue

        method = step["method"]
        if not methods.known(method):
            raise ValidationError(f"steps[{index}].method", f"unknown production method {method!r}")
        rpc_methods.add(method)

        if step.get("notification"):
            if method != CANCEL_METHOD:
                raise ValidationError(
                    f"steps[{index}].method",
                    "only iprange.v1.cancel may be sent as a notification",
                )
            for member in ("expect_result", "expect_error",
                           "expect_params_rejected", "capture",
                           "assert_files"):
                if member in step:
                    raise ValidationError(
                        f"steps[{index}].{member}",
                        "notification steps cannot expect a response, capture, or assert files",
                    )
        elif method == CANCEL_METHOD:
            raise ValidationError(
                f"steps[{index}].notification",
                "iprange.v1.cancel is a notification and requires \"notification\": true",
            )

        if "digest_group" in step:
            if step["method"] != "iprange.v1.export":
                raise ValidationError(
                    f"steps[{index}].digest_group",
                    "digest_group is only meaningful on iprange.v1.export, "
                    "the one method whose artifact both languages must "
                    "produce identically")
            if "expect_params_rejected" in step or "expect_error" in step:
                raise ValidationError(
                    f"steps[{index}].digest_group",
                    "a step that must produce an artifact cannot also "
                    "expect to be refused")

        if "expect_params_rejected" in step:
            # A params refusal happens before the service opens the
            # named paths, so capturing from the response or asserting a
            # produced file would encode an expectation the step cannot
            # legitimately have.
            for member in ("capture", "assert_files", "expect_result",
                           "expect_error"):
                if member in step:
                    raise ValidationError(
                        f"steps[{index}].{member}",
                        "expect_params_rejected steps cannot capture, "
                        "assert files, or expect a result/product error",
                    )
            if step["method"] == CANCEL_METHOD:
                raise ValidationError(
                    f"steps[{index}].method",
                    "iprange.v1.cancel is a notification and cannot be "
                    "asserted as a params rejection",
                )

        captures = []
        for pointer_index, spec in enumerate(step.get("capture", [])):
            if isinstance(spec, dict):
                capture_name = spec["name"]
                pointer = spec["path"]
            else:
                capture_name = pointer = spec
            _valid_pointer(pointer, f"steps[{index}].capture[{pointer_index}]")
            captures.append(capture_name)
        for value in _walk_strings(step["params"]):
            if value.startswith("$CAPTURE/") and value[len("$CAPTURE/"):] not in available_captures:
                raise ValidationError(
                    f"steps[{index}].params",
                    f"unresolved capture placeholder {value!r}",
                )
            if (value.startswith("/") or value == ".." or value.startswith("../")
                    or PureWindowsPath(value).is_absolute()):
                raise ValidationError(
                    f"steps[{index}].params",
                    "raw escaping paths are invalid; use $WORK/path",
                )
        for assertion_index, assertion in enumerate(step.get("assert_files", [])):
            _relative_work_path(assertion["path"], f"steps[{index}].assert_files[{assertion_index}].path")
            if "equals_fixture" in assertion:
                _relative_work_path(
                    assertion["equals_fixture"],
                    f"steps[{index}].assert_files[{assertion_index}].equals_fixture",
                )
        duplicates = sorted({name for name in captures if captures.count(name) > 1})
        if duplicates:
            raise ValidationError(
                f"steps[{index}].capture",
                f"capture names must be unique within one step: {duplicates}",
            )
        available_captures.update(captures)

    for assertion_index, assertion in enumerate(case.get("assertions", {}).get("files", [])):
        _relative_work_path(
            assertion["path"],
            f"assertions.files[{assertion_index}].path",
        )
        if "equals_fixture" in assertion:
            _relative_work_path(
                assertion["equals_fixture"],
                f"assertions.files[{assertion_index}].equals_fixture",
            )

    required = case.get("requires")
    if required is not None:
        if not methods.known(required):
            raise ValidationError("requires", f"unknown production method {required!r}")
        if required not in rpc_methods:
            raise ValidationError(
                "requires",
                "required capability must be exercised by an rpc step in this case",
            )
    return case


def _walk_strings(value):
    if isinstance(value, str):
        yield value
    elif isinstance(value, list):
        for item in value:
            yield from _walk_strings(item)
    elif isinstance(value, dict):
        for item in value.values():
            yield from _walk_strings(item)


def validate_rpc_request(method, params):
    from . import methods
    validate(params, methods.METHODS[method]["params"], f"params[{method}]")


def validate_rpc_result(method, result):
    from . import results
    results.validate_result(method, result)


def _self_test():
    """Exercise the capture-pointer grammar without a service."""

    import json

    accepted = (
        "result",
        "candidates[0]",
        "result.candidates[0]",
        "a[0][1].b.c[0]",
        "output.generation",
    )
    rejected = (
        "candidates[0]]",
        "[0]",
        ".candidates",
        "candidates.",
        "candidates..x",
        "candidates[]",
        "candidates[a]",
        "a[0].",
        "a.[0]",
        "a[0]..b",
        "",
    )
    for pointer in accepted:
        assert pointer_parts(pointer) is not None, pointer
        _valid_pointer(pointer, "self-test")
    for pointer in rejected:
        if pointer:
            assert pointer_parts(pointer) is None, pointer
            try:
                _valid_pointer(pointer, "self-test")
            except ValidationError:
                pass
            else:
                raise AssertionError(f"accepted malformed pointer {pointer!r}")
        else:
            assert pointer_parts(pointer) is None, pointer
    assert pointer_parts("result.candidates[0]") == [
        "result", "candidates", "[0]"]
    assert pointer_parts("a[0][1].b") == ["a", "[0]", "[1]", "b"]

    # The full case-schema path must reject a malformed pointer in a
    # capture spec and accept a well-formed one.
    base = {
        "schema": "iprange-cli-case-v1",
        "name": "self-test-pointer",
        "fixtures": [],
        "steps": [{
            "kind": "rpc",
            "actor": "producer",
            "method": "iprange.v1.database.create",
            "params": {"path": "$WORK/x.iprange"},
        }],
    }
    good = json.loads(json.dumps(base))
    good["steps"][0]["capture"] = ["candidates[0]"]
    validate_case(good)
    for bad_pointer in rejected[:-1]:
        bad = json.loads(json.dumps(base))
        bad["steps"][0]["capture"] = [bad_pointer]
        try:
            validate_case(bad)
        except ValidationError:
            pass
        else:
            raise AssertionError(
                f"case schema accepted malformed pointer {bad_pointer!r}")


if __name__ == "__main__":
    _self_test()
