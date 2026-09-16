#!/usr/bin/env python3
"""Committed Go coverage measurement: unit plus corpus-driven integration.

What this measures and why
--------------------------
The Go engine's own test suite reports unit coverage, and the CLI corpus
drives the shipped binaries through the JSON-RPC surface that the unit suite
cannot reach (a test calls the SDK in-process; a case speaks the wire
protocol to a child service).  A coverage claim that mixes the two is
worthless, and a claim that has neither is prose, so this harness measures
both and keeps them separable in the report:

  * ``unit``         -- ``go test -cover`` over every package of the module.
  * ``integration``  -- the committed corpus executed against binaries built
                        with ``go build -cover``, so the counters come from
                        the same code the qualification matrices exercise.
  * ``merged``       -- the two counter sets combined by ``go tool covdata``.

Coverage-instrumented binaries are built in their own staging directory and
are never the binaries used by the throughput or performance attestation:
instrumentation adds a counter block per basic block, and a rate measured on
instrumented code would attest to nothing.  The report records the separate
staging path and the digests of the instrumented binaries so that separation
is checkable rather than asserted.

Killed runs are excluded deliberately.  The crash battery kills its children,
and a Go coverage binary that is killed never writes its counter block, so a
partial file is not a measurement of anything; merging one would silently
depress the percentage.  Only complete runs are merged, and the harness
refuses to merge a directory whose expected member count does not match.

Usage
-----
    nice python3 v4/cli/coverage_harness.py --go-module v4/go \
        --work EMPTY_DIR [--matrices go,rust_to_go,go_to_rust] \
        [--json-report FILE]

The report is written outside the operator profile and copied into
``v4/cli/evidence/`` by the battery, like every other harness here.
"""

import argparse
import ast
import hashlib
import json
import os
import platform
import shutil
import subprocess
import sys
import time

sys.dont_write_bytecode = True

_HERE = os.path.dirname(os.path.abspath(__file__))
if _HERE not in sys.path:
    sys.path.insert(0, _HERE)
_SELF_DIR = os.path.dirname(os.path.abspath(__file__))

from command_sanitize import (  # noqa: E402
    audit_report_writers,
    committed_report_problems,
    personal_path_in_report,
    profile_path,
    recorded_git_identity,
    require_paths_outside_profile,
    run_shared_self_test,
    sanitized_command,
    sanitized_path_value,
    write_committed_report,
)

REPORT_SCHEMA = "iprange-cli-coverage-go-report-v1"
MODULE = "github.com/firehol/iprange/v4/go"
DEFAULT_MATRICES = ("go", "rust_to_go", "go_to_rust")
# A corpus run of one matrix writes one counter file per instrumented process
# that exits cleanly.  The count is recorded, never assumed, and a run that
# produces no files is an error rather than a zero-coverage pass.
MIN_COVER_FILES_PER_RUN = 1

# Canonical build configuration for every Go tool run by this harness.
# The qualified Go product is the static CGO_ENABLED=0 build (pure-Go
# resolver); the legacy DNS byte-exact pins are qualified against that
# configuration and diverge under a cgo/glibc build (SOW-0028, DNS parity
# scope), so instrumented builds and the unit suite must compile with the
# same setting the product is qualified under.
CANONICAL_CGO_ENABLED = "0"

# One counter mode for both halves of the measurement.  ``go build -cover``
# defaults to ``set`` while ``go test -cover`` was run with ``atomic`` here,
# and ``go tool covdata`` refuses to merge a ``set`` profile with an
# ``atomic`` one ("counter mode clash while reading meta-data file"), which
# silently yields an empty record set.  ``atomic`` is the correct mode for a
# server that serves replies from more than one thread, and naming it once
# keeps the two halves compatible.
COVERMODE = "atomic"

# The module's own tests open two trees as a *sibling* of the module
# directory: the shared conformance corpus (``../conformance/cases.json`` from
# the package root, ``../../../conformance/rust/*.iprdb`` from
# ``internal/reader``) and the committed harnesses, because
# ``internal/cli/handlers/fd_pressure_unix_test.go`` executes
# ``../cli/fd_pressure_harness.py`` and that harness imports its
# same-directory siblings.  Copying only ``v4/go`` into a private build
# directory therefore makes those tests fail for the wrong reason, and a
# harness that ignored the failure would publish a percentage measured over a
# partial run.  The harness stages the module together with every sibling it
# reads, and refuses to proceed when a required sibling member is absent.
STAGED_MEMBERS = ("go", "conformance", "cli")
REQUIRED_STAGED_MEMBERS = ("go", "conformance", "cli")
# The file per required member whose absence turns a specific test red rather
# than making it skip.  Checking them here means an incomplete staging fails
# with the reason, instead of surfacing as a red suite that reads like a
# product defect.
STAGED_REQUIRED_FILES = (("conformance", "cases.json"),
                         ("cli", "fd_pressure_harness.py"))


def run(command, cwd=None, env=None, timeout=3600):
    """Execute one build/tool command and return ``(rc, stdout, stderr)``."""

    process = subprocess.run(command, cwd=cwd, env=env, timeout=timeout,
                             capture_output=True, check=False)
    return (process.returncode,
            process.stdout.decode("utf-8", "replace"),
            process.stderr.decode("utf-8", "replace"))


def sha256_file(path):
    digest = hashlib.sha256()
    with open(path, "rb") as stream:
        for block in iter(lambda: stream.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()


def _require_data(directories):
    for directory in directories:
        if not os.path.isdir(directory) or not os.listdir(directory):
            raise SystemExit(
                f"coverage data directory {directory} is empty; merging it "
                f"would report a percentage that was never measured")


def covdata(tool, subcommand, directories, work_dir=None):
    """Run one ``go tool covdata`` subcommand over the given data dirs.

    ``textfmt`` refuses to stream (``-o -`` silently produces nothing), so
    it is written to a file in the harness's own work directory and read
    back."""

    _require_data(directories)
    for directory in directories:
        if "," in directory:
            raise SystemExit(
                f"coverage directory {directory} contains a comma, which "
                f"``go tool covdata -i`` cannot express")
    # ``go tool covdata <subcommand>`` -- invoking ``go covdata`` is an
    # unknown-command error, so the tool word is part of the command.
    command = [tool, "tool", "covdata", subcommand]
    # ``-i`` is one comma-separated flag, not a repeatable one: passing
    # ``-i=a -i=b`` keeps only ``b``, which turns the merged measurement
    # into the last directory alone and reports a real-looking percentage
    # for data that was never combined.
    command += ["-i=" + ",".join(directories)]
    if subcommand == "textfmt":
        root = work_dir or os.getcwd()
        target = os.path.join(root, f"covdata-textfmt-{os.getpid()}.txt")
        command += ["-o", target]
        rc, _out, err = run(command)
        if rc != 0:
            raise SystemExit(f"go tool covdata textfmt failed: "
                             f"{err.strip()[:400]}")
        try:
            with open(target, encoding="utf-8") as stream:
                return stream.read()
        except OSError as exc:
            raise SystemExit(f"go tool covdata textfmt wrote no readable "
                             f"output: {exc}")
        finally:
            if os.path.isfile(target):
                os.unlink(target)
    rc, out, err = run(command)
    if rc != 0:
        raise SystemExit(f"go tool covdata {subcommand} failed: "
                         f"{err.strip()[:400]}")
    return out


def _package_of_file(location):
    """Package import path from a coverage record's file location.

    Both ``covdata textfmt`` and ``covdata func`` name the source file
    with its full import-path prefix (``.../v4/go/internal/live/open.go``),
    so the package is that path's directory.  Deriving it from the symbol
    name instead would guess where the import path ends and the identifier
    begins, which is ambiguous for unexported functions and for any path
    that contains a dot.
    """

    path = location.split(":", 1)[0]
    return os.path.dirname(path)


def parse_textfmt(text):
    """``{package: {blocks, blocks_covered, statements, covered_statements}}``.

    A record line is ``<file>:<l>.<c>,<l>.<c> <statements> <count>``; the
    line is a basic block, so the number of lines is the block count and the
    second field is the statement count it covers.
    """

    packages = {}
    for line in text.splitlines():
        if line.startswith("mode:") or not line.strip():
            continue
        location, _, tail = line.partition(" ")
        fields = tail.split()
        if len(fields) < 2 or not fields[0].isdigit():
            continue
        package = _package_of_file(location)
        if not package:
            continue
        statements = int(fields[0])
        covered = _count_of(fields[1]) > 0
        bucket = packages.setdefault(package, _empty_bucket())
        bucket["blocks"] += 1
        bucket["statements"] += statements
        if covered:
            bucket["blocks_covered"] += 1
            bucket["covered_statements"] += statements
    return packages


def parse_func(text):
    """Merge ``covdata func`` output into the per-package function counts.

    A line is ``<file>:<line>:\t<qualified name>\t<percent>``.  A function
    is covered when any of its statements ran, which the printed percentage
    reports directly.
    """

    packages = {}
    for line in text.splitlines():
        fields = line.split()
        if len(fields) < 2 or not fields[0].endswith(":") and ":" not in fields[0]:
            continue
        package = _package_of_file(fields[0])
        if not package:
            continue
        percent_token = fields[-1].rstrip("%")
        try:
            percent = float(percent_token)
        except ValueError:
            continue
        bucket = packages.setdefault(package, _empty_bucket())
        bucket["functions"] += 1
        if percent > 0.0:
            bucket["covered_functions"] += 1
    return packages


def _empty_bucket():
    return {"blocks": 0, "blocks_covered": 0, "statements": 0,
            "covered_statements": 0, "functions": 0, "covered_functions": 0}


def _count_of(token):
    try:
        return int(token)
    except ValueError:
        return 0


def parse_covdata_percent(out):
    """``{package: statements percent}`` from ``go tool covdata percent`` output.

    One package per ``coverage:`` token, attributed to the token that
    immediately precedes it.  A package that contains no statements prints
    its name with no percentage, and the next package can then share the
    physical line; taking the first token of the line would attribute the
    second package's percentage to the first and invent a divergence the
    measurement does not have.
    """

    observed = {}
    for line in out.splitlines():
        parts = [token for token in line.split() if token]
        for position, token in enumerate(parts):
            if token != "coverage:" or position == 0:
                continue
            try:
                observed[parts[position - 1]] = float(
                    parts[position + 1].rstrip("%"))
            except (IndexError, ValueError):
                continue
    return observed


def covdata_percent_statements(tool, directories):
    """``{package: statements percent}`` as ``covdata percent`` reports it.

    Used only as an independent cross-check of the figures computed from the
    records: if the harness's own arithmetic disagreed with the tool, the
    measurement would be wrong in a way no reader could see.
    """

    return parse_covdata_percent(covdata(tool, "percent", directories))


def percent_of(covered, total):
    return round(100.0 * covered / total, 2) if total else 0.0


def summarize(packages):
    """Overall statements/functions/blocks percentages for a package table."""

    totals = _empty_bucket()
    for bucket in packages.values():
        for member in totals:
            totals[member] += bucket[member]
    return {"statements": percent_of(totals["covered_statements"],
                                     totals["statements"]),
            "functions": percent_of(totals["covered_functions"],
                                    totals["functions"]),
            "blocks": percent_of(totals["blocks_covered"], totals["blocks"]),
            "measured": totals}


def package_table(tool, directories, work_dir=None):
    """Per-package coverage table plus the tool's own cross-check.

    A package whose statements percentage differs from what ``covdata
    percent`` reports by more than rounding is a harness defect, and the
    measurement is refused rather than published.
    """

    textfmt = covdata(tool, "textfmt", directories, work_dir=work_dir)
    func = covdata(tool, "func", directories)
    table = parse_textfmt(textfmt)
    for package, bucket in parse_func(func).items():
        target = table.setdefault(package, _empty_bucket())
        target["functions"] = bucket["functions"]
        target["covered_functions"] = bucket["covered_functions"]
    reported = covdata_percent_statements(tool, directories)
    for package, bucket in sorted(table.items()):
        mine = percent_of(bucket["covered_statements"], bucket["statements"])
        theirs = reported.get(package)
        if theirs is not None and abs(mine - theirs) > 0.6:
            raise SystemExit(
                f"coverage of {package} computed as {mine}% but go tool "
                f"covdata percent reports {theirs}%; the measurement is "
                f"refused rather than published")
        bucket["statements_total"] = bucket["statements"]
        bucket["statement_percent"] = mine
        bucket["function_percent"] = percent_of(
            bucket["covered_functions"], bucket["functions"])
        bucket["block_percent"] = percent_of(bucket["blocks_covered"],
                                            bucket["blocks"])
    return table


def stage_from_revision(revision, dest, members=STAGED_MEMBERS):
    """Extract the measured members from a committed revision into ``dest``.

    The repository working tree is edited concurrently by other workers, and
    an in-flight file makes the instrumented build fail for reasons that have
    nothing to do with the revision being attested.  Measuring from
    ``git archive <revision>`` pins the measurement to a commit, and the
    commit is recorded in the report, so the number is reproducible and
    attributable.
    """

    # The harness lives at <checkout>/v4/cli, so the checkout root is two
    # levels up.  This is a filesystem decision (is there a .git here?), not a
    # report field: committed evidence records null for its checkout.
    root = os.path.dirname(os.path.dirname(_HERE))
    if not os.path.isfile(os.path.join(root, ".git", "HEAD")) and not \
            os.path.isdir(os.path.join(root, ".git")):
        raise SystemExit(f"{root} is not a git checkout; cannot stage "
                         f"revision {revision}")
    extracted = []
    for name in members:
        command = ["git", "-C", root, "archive", revision,
                   f"v4/{name}"]
        extract = ["tar", "-x", "-C", dest]
        first = subprocess.Popen(command, stdout=subprocess.PIPE,  # noqa: S603
                                 cwd=root)
        second = subprocess.Popen(extract, stdin=first.stdout,  # noqa: S603
                                  cwd=dest)
        first.stdout.close()
        if second.wait(timeout=900) != 0:
            raise SystemExit(
                f"cannot extract v4/{name} at {revision} into {dest}")
        if first.wait(timeout=900) != 0:
            raise SystemExit(
                f"git archive v4/{name} at {revision} failed; the revision "
                f"must contain the module and the conformance corpus")
        extracted.append(f"v4/{name}")
    rc, resolved, _err = run(["git", "-C", root, "rev-parse",
                              "--verify", f"{revision}^{{commit}}"])
    commit = resolved.strip()
    if rc != 0 or len(commit) != 40:
        raise SystemExit(f"--revision {revision} does not name a commit in "
                         f"{root}")
    # ``git archive v4/<name>`` preserves the archived path, so the members
    # land under dest/v4 and the module keeps its siblings.
    staged_root = os.path.join(dest, "v4")
    staged_module = os.path.join(staged_root, "go")
    if not os.path.isfile(os.path.join(staged_module, "go.mod")):
        raise SystemExit(f"revision {revision} staged no go module at "
                         f"{staged_module}")
    for member in STAGED_REQUIRED_FILES:
        probe = os.path.join(staged_root, *member)
        if not os.path.isfile(probe):
            raise SystemExit(f"revision {revision} staged tree has no {probe}")
    return staged_module, extracted, commit


def stage_sources(v4_tree, dest, members=STAGED_MEMBERS):
    """Copy the measured module and the sibling trees its tests read.

    Returns the staged module directory.  A missing sibling is fatal: the Go
    tests that consume the conformance corpus and the sibling harness skip
    nothing, they fail, and a coverage number taken from a tree that could not
    run them is not a measurement of the module.
    """

    missing = [name for name in REQUIRED_STAGED_MEMBERS
               if not os.path.isdir(os.path.join(v4_tree, name))]
    if missing:
        raise SystemExit(
            f"cannot stage the coverage source tree from {v4_tree}: "
            f"{', '.join(missing)} is missing; the Go unit suite reads "
            f"../conformance and ../cli as siblings of the module")
    os.makedirs(dest, exist_ok=True)
    for name in members:
        source = os.path.join(v4_tree, name)
        if not os.path.isdir(source):
            continue
        # ``.coverage-harness-*`` names this harness's own self-test scratch,
        # which another process may be holding in the checkout right now.
        shutil.copytree(source, os.path.join(dest, name), symlinks=False,
                        ignore=shutil.ignore_patterns("target", "node_modules",
                                                      "__pycache__", "*.pyc",
                                                      ".coverage-harness-*"))
    for member in STAGED_REQUIRED_FILES:
        probe = os.path.join(dest, *member)
        if not os.path.isfile(probe):
            raise SystemExit(
                f"staged coverage tree {dest} has no {probe}: the unit suite "
                f"would under-measure rather than fail loudly")
    return os.path.join(dest, os.path.basename(os.path.abspath(
        os.path.join(v4_tree, "go"))))


def assert_monotonic(inputs, merged):
    """A merge may only add covered work, never lose it.

    ``inputs`` maps a label to that input's overall percentages; ``merged``
    holds the percentages computed over all of them together.  Combining
    counter sets is a union, so every merged figure must be at least the
    corresponding figure of each input.  A merged number below one of its own
    inputs means that input never took part in the merge -- the classic cause
    is passing ``-i`` to ``go tool covdata`` more than once, where only the
    last spelling survives -- and publishing such a figure would be a
    plausible-looking measurement of something that was never measured.
    """

    tolerance = 0.05  # rounding only
    for label, percent in sorted(inputs.items()):
        for member in ("statements", "functions", "blocks"):
            if merged[member] + tolerance < percent[member]:
                raise SystemExit(
                    f"merged coverage {merged[member]}% for {member} is below "
                    f"{label} coverage {percent[member]}%; a union cannot lose "
                    f"covered work, so the merge did not include {label}")


def build_covered(module_dir, staging, go):
    """Build the corpus binaries with ``go build -cover`` into ``staging``."""

    os.makedirs(staging, exist_ok=True)
    built = {}
    for target, package in (("iprange", "./cmd/iprange"),
                            ("iprange-v4-worker", "./cmd/iprange-v4-worker")):
        destination = os.path.join(staging, target)
        env = dict(os.environ)
        env["CGO_ENABLED"] = CANONICAL_CGO_ENABLED
        rc, out, err = run([go, "build", "-cover",
                           f"-covermode={COVERMODE}", "-trimpath",
                           "-buildvcs=false", "-o", destination, package],
                           cwd=module_dir, env=env)
        if rc != 0:
            raise SystemExit(f"go build -cover {package} failed: "
                             f"{err.strip()[:400]}")
        built[target] = {"path": sanitized_path_value(destination),
                         "sha256": sha256_file(destination),
                         "instrumented": True, "covermode": COVERMODE}
    return built


def measure_unit(module_dir, coverdir, go):
    """Run the module's own tests with coverage written to ``coverdir``."""

    os.makedirs(coverdir, exist_ok=True)
    env = dict(os.environ)
    env["GOCOVERDIR"] = coverdir
    env["CGO_ENABLED"] = CANONICAL_CGO_ENABLED
    # ``GOCOVERDIR`` alone is not enough for ``go test -cover``: the
    # test binary only writes its counter block when the coverage flag is
    # passed through to it, so the environment variable on its own yields a
    # green run and an empty directory.  The harness therefore names the
    # destination explicitly as a test flag, and the empty-directory check
    # below is what catches any future drift in that recipe.
    command = [go, "test", "-count=1", "-cover",
               f"-covermode={COVERMODE}", "./...", "-args",
               f"-test.gocoverdir={coverdir}"]
    rc, out, err = run(command, cwd=module_dir, env=env, timeout=7200)
    files = len(os.listdir(coverdir))
    if rc != 0:
        # A red unit suite must not be reported as a coverage number: the
        # percentage would be measured over a build the module itself rejects.
        raise SystemExit(f"go test -cover failed (rc {rc}): "
                         f"{(err or out).strip()[:600]}")
    if files < MIN_COVER_FILES_PER_RUN:
        raise SystemExit(f"unit coverage produced {files} counter files; "
                         f"expected at least {MIN_COVER_FILES_PER_RUN}")
    return {"coverdir": sanitized_path_value(coverdir), "cover_files": files,
            "rc": rc, "covermode": COVERMODE,
            "command": sanitized_command(command)}


def measure_integration(module_dir, coverdir, staging, matrices, work_root,
                        rust_binary, fixture_tool):
    """Execute the corpus against instrumented binaries, one dir per source."""

    os.makedirs(coverdir, exist_ok=True)
    runs = []
    for matrix in matrices:
        matrix_work = os.path.join(work_root, f"matrix-{matrix}")
        os.makedirs(matrix_work, exist_ok=True)
        report = os.path.join(work_root, f"coverage-matrix-{matrix}.json")
        env = dict(os.environ)
        env["GOCOVERDIR"] = coverdir
        command = [sys.executable, os.path.join(_HERE, "run.py"),
                   "--go", os.path.join(staging, "iprange"),
                   # The Rust side is a plain reference here: this artifact
                   # measures Go coverage, and an uninstrumented Rust service
                   # still serves the mixed matrices exactly as it does in the
                   # qualification battery.
                   "--rust", rust_binary,
                   "--fixture-tool", fixture_tool,
                   "--matrix", matrix, "--work-dir", matrix_work,
                   "--json-report", report]
        rc, out, err = run(command, timeout=7200, env=env)
        summary = {}
        if os.path.isfile(report):
            with open(report, encoding="utf-8") as stream:
                document = _json_load(stream)
            summary = {member: document.get(member)
                       for member in ("passed", "failed", "skipped")}
        runs.append({"matrix": matrix, "rc": rc,
                             "command": sanitized_command(command),
                     **summary})
        if rc not in (0, 1):
            raise SystemExit(f"matrix {matrix} could not run under coverage: "
                             f"{(err or out).strip()[:400]}")
    files = len(os.listdir(coverdir))
    if files < MIN_COVER_FILES_PER_RUN * len(matrices):
        raise SystemExit(
            f"integration coverage produced {files} counter files for "
            f"{len(matrices)} matrix runs; a killed or crashed run yields "
            f"incomplete data and is not merged")
    return runs, files


def _json_load(stream):
    return json.load(stream)


def live_run(args):
    started = time.monotonic()
    module_dir = os.path.abspath(args.go_module)
    if not os.path.isfile(os.path.join(module_dir, "go.mod")):
        raise SystemExit(f"--go-module {module_dir} has no go.mod")
    work = os.path.abspath(args.work)
    if os.path.exists(work) and os.listdir(work):
        raise SystemExit(f"--work must be empty or absent: {work}")
    os.makedirs(work, exist_ok=True)
    if not args.rust or not os.path.isfile(args.rust):
        raise SystemExit("--rust must name the reference service executable "
                         "for the mixed matrices")
    if not args.fixture_tool or not os.path.isfile(args.fixture_tool):
        raise SystemExit("--fixture-tool must name the v4-fixture executable "
                         "the corpus generates its databases with")
    go = shutil.which("go")
    if not go:
        raise SystemExit("go toolchain not found on PATH")
    tool = go
    # A private copy of the source tree is used for the instrumented build so
    # that the unit run, the corpus run and the build all observe one
    # consistent revision of a tree other workers are editing concurrently.
    staged_root = os.path.join(work, "v4")
    os.makedirs(staged_root, exist_ok=True)
    if args.revision:
        staged_module, staged_from, staged_commit = stage_from_revision(
            args.revision, work)
        git_head = staged_commit
        staging_mode = {"mode": "revision", "revision": staged_commit,
                       "requested": args.revision,
                        "members": staged_from,
                        "checkout_root": None,
                        "git_head": recorded_git_identity()}
    else:
        v4_tree = os.path.abspath(args.v4_tree or os.path.dirname(module_dir))
        staged_module = stage_sources(v4_tree, staged_root)
        git_head = recorded_git_identity()
        staging_mode = {"mode": "working-tree", "v4_tree":
                        sanitized_path_value(v4_tree),
                        "members": list(STAGED_MEMBERS),
                        # A committed record never names the producer's
                        # checkout: command_sanitize owns the member and
                        # committed_report_problems() refuses a directory
                        # here.  The key stays so the member is visibly
                        # "recorded as null", never "forgotten".
                        "checkout_root": None}
    module_dir = staged_module
    staging = os.path.join(work, "bin")
    unit_dir = os.path.join(work, "cov-unit")
    integ_dir = os.path.join(work, "cov-integration")
    matrices = tuple(m.strip() for m in args.matrices.split(",") if m.strip())

    require_paths_outside_profile((("--go-module", args.go_module),
                                   ("--work", args.work),
                                   ("--rust", args.rust),
                                   ("--fixture-tool", args.fixture_tool),
                                   ("--v4-tree", args.v4_tree),
                                   ("--json-report", args.json_report)))
    built = build_covered(module_dir, staging, go)
    unit = measure_unit(module_dir, unit_dir, go)
    runs, integ_files = measure_integration(module_dir, integ_dir, staging,
                                            matrices, work, args.rust,
                                            args.fixture_tool)

    unit_table = package_table(tool, [unit_dir], work_dir=work)
    integration_table = package_table(tool, [integ_dir], work_dir=work)
    merged_table = package_table(tool, [unit_dir, integ_dir], work_dir=work)
    unit_percent = summarize(unit_table)
    integ_percent = summarize(integration_table)
    merged_percent = summarize(merged_table)
    # A merge is a union: anything below one of its own inputs means that
    # input never took part, and publishing the number would be a silent
    # measurement error rather than a finding.
    assert_monotonic({"unit": unit_percent,
                      "integration": integ_percent}, merged_percent)

    report = {
        "schema": REPORT_SCHEMA,
        # ``command``, ``git_head`` and ``checkout_root`` belong to the shared
        # provenance owner, which writes them at commit time.  The revision
        # this measurement actually compiled is a separate fact and is
        # recorded below as ``measured_git_head``.
        "platform": {"system": platform.system(),
                     "release": platform.release(),
                     "machine": platform.machine(),
                     "python": platform.python_version()},
        "toolchain": {"go_version": _go_version(go),
                      "covdata": "go tool covdata"},
        "module": MODULE,
        "instrumented_binaries": built,
        # Separation of evidence: these are the instrumented artifacts, and
        # they are not the qualification or throughput binaries.  Recording
        # the staging path makes the separation checkable.
        "staging": sanitized_path_value(staging),
        # The instrumented measurement ran on this private copy of the source
        # tree, staged together with the conformance corpus the Go tests read
        # as a sibling.  Record it so a reader can see the measurement was not
        # taken from a partially staged module.
        "staged_tree": sanitized_path_value(staged_root),
        "staged_from": staging_mode,
        "coverage_dirs": [sanitized_path_value(unit_dir),
                          sanitized_path_value(integ_dir)],
        "unit": {**unit, "percent": unit_percent,
                 "measured": unit_percent["measured"]},
        "integration": {"runs": runs, "cover_files": integ_files,
                        "percent": integ_percent,
                        "measured": integ_percent["measured"]},
        "merged": {"percent": merged_percent,
                   "measured": merged_percent["measured"]},
        "per_package": merged_table,
        "policy": {
            "killed_runs_merged": False,
            "note": ("crash-battery children are killed and a killed Go "
                     "coverage binary writes no counter block, so killed "
                     "runs are excluded rather than merged; percentages "
                     "here are measured from complete runs only"),
            "performance_use": ("instrumented binaries are never used for "
                                "the throughput or performance attestation")},
        "elapsed_seconds": None,
    }
    report["elapsed_seconds"] = round(time.monotonic() - started, 2)
    if args.json_report:
        target = os.path.abspath(args.json_report)
        os.makedirs(os.path.dirname(target), exist_ok=True)
        # The shared writer owns the provenance members and the privacy scan.
        # It records ``git_head`` as the revision of the checkout that ran the
        # measurement; the revision the instrumented binaries were *staged*
        # from is a separate fact and stays in ``measured_git_head`` (equal to
        # ``git_head`` for a working-tree run, different for --revision).
        report["measured_git_head"] = git_head
        write_committed_report(
            target, report,
            caller_paths=(("--go-module", args.go_module),
                          ("--work", args.work), ("--rust", args.rust),
                          ("--fixture-tool", args.fixture_tool),
                          ("--v4-tree", args.v4_tree),
                          ("--revision", args.revision),
                          ("--json-report", args.json_report)),
            indent=1)
    print(f"unit        statements={unit_percent['statements']} "
          f"functions={unit_percent['functions']} "
          f"blocks={unit_percent['blocks']}")
    print(f"integration statements={integ_percent['statements']} "
          f"functions={integ_percent['functions']} "
          f"blocks={integ_percent['blocks']}")
    print(f"merged      statements={merged_percent['statements']} "
          f"functions={merged_percent['functions']} "
          f"blocks={merged_percent['blocks']}")
    print(f"packages recorded: {len(merged_table)}")
    print(f"COVERAGE PASSED (git_head={report['git_head']})")
    return 0


def _go_version(go):
    rc, out, _err = run([go, "version"])
    return out.strip() if rc == 0 else None


# Executed-control count of ``_self_test``, as a literal.  The line used to
# print "PASSED (N cases)" from whatever the counter reached, so a control
# that stopped being reached lowered N and still exited 0.
COVERAGE_SELF_TEST_CONTROLS = 19


def _unimported_shared_helpers(module_file=None, helper_file=None):
    """Public ``command_sanitize`` helpers this module calls but never imports.

    Every shared provenance and privacy helper lives in ``command_sanitize``;
    calling one without importing it is a ``NameError`` on whichever branch
    names it, and a branch only the live run takes is invisible to every other
    control (wave-19.25 step [18b] died exactly that way, two functions deep
    into ``live_run``, while the self-test stayed green).  The check reads both
    modules' ASTs, so it cannot be satisfied by a comment or a differently
    spelled call site.
    """
    module_file = os.path.abspath(__file__) if module_file is None else os.path.abspath(module_file)
    helper_file = (os.path.join(_SELF_DIR, "command_sanitize.py")
                   if helper_file is None else os.path.abspath(helper_file))
    with open(helper_file, encoding="utf-8") as stream:
        helpers = {node.name for node in ast.parse(stream.read(), filename=helper_file).body
                   if isinstance(node, ast.FunctionDef) and not node.name.startswith("_")}
    with open(module_file, encoding="utf-8") as stream:
        tree = ast.parse(stream.read(), filename=module_file)
    imported = {alias.name for node in ast.walk(tree)
                if isinstance(node, ast.ImportFrom) and node.module == "command_sanitize"
                for alias in node.names}
    defined = {node.name for node in tree.body
               if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef))}
    used = {node.id for node in ast.walk(tree)
            if isinstance(node, ast.Name) and isinstance(node.ctx, ast.Load)}
    return sorted(name for name in helpers
                  if name in used and name not in imported and name not in defined)


def _self_test():
    """The parsers and the provenance pin must not invent a verdict.

    Offline: the measurement itself runs instrumented Go binaries and cannot
    execute here, so what this checks is the parsing of
    ``go tool covdata``/``go tool cover`` output, the staging and merge
    guards, and that this harness can only reach its committed artifact
    through the shared provenance owner.
    """

    failures = 0
    counter = {"n": 0}

    def expect(label, condition, detail=""):
        nonlocal failures
        counter["n"] += 1
        ok = bool(condition)
        print(f"{'ok  ' if ok else 'BAD '} {label:58} {detail}")
        if not ok:
            failures += 1

    # Records as go tool covdata textfmt really prints them.
    textfmt = "\n".join([
        "mode: set",
        "github.com/firehol/iprange/v4/go/internal/live/open.go:10.2,12.3 3 1",
        "github.com/firehol/iprange/v4/go/internal/live/open.go:12.4,14.1 2 0",
        "github.com/firehol/iprange/v4/go/internal/mapping/map.go:5.1,6.1 1 1",
    ])
    table = parse_textfmt(textfmt)
    live = table["github.com/firehol/iprange/v4/go/internal/live"]
    expect("statements counted from record lines", live["statements"] == 5
           and live["covered_statements"] == 3, str(live))
    expect("blocks counted as records", live["blocks"] == 2
           and live["blocks_covered"] == 1, str(live))
    expect("a package is its file's directory, not a name parse",
           "github.com/firehol/iprange/v4/go/internal/mapping" in table,
           sorted(table))
    func_text = "\n".join([
        "github.com/firehol/iprange/v4/go/internal/live/open.go:10:\tOpen\t60.0%",
        "github.com/firehol/iprange/v4/go/internal/live/open.go:20:\topenFd\t0.0%",
    ])
    funcs = parse_func(func_text)
    flive = funcs["github.com/firehol/iprange/v4/go/internal/live"]
    expect("functions covered by execution", flive["functions"] == 2
           and flive["covered_functions"] == 1, str(flive))
    expect("an unexported function name does not move the package boundary",
           funcs.get("github.com/firehol/iprange/v4/go/internal/live") is flive)
    overall = summarize(table)
    expect("overall percentages follow the counted statements",
           overall["statements"] == percent_of(4, 6), str(overall))

    # The committed artifact must never record the operator's home
    # directory, and the identity fields must be the ones the sanitizer
    # measured.  This used to be a regular-expression pin on the *spelling*
    # of the recording line, so binding the value through an intermediate
    # (``_raw_cmd = [str(a) for a in sys.argv]`` then ``"command": _raw_cmd``)
    # kept it green while the artifact went unsanitized.  The pin is now the
    # shared structural audit: it reads this module's AST, requires the commit
    # to go through ``write_committed_report`` -- the only function that owns
    # ``command``/``git_head``/``checkout_root`` and runs the personal-path
    # scan -- and refuses any direct ``json.dump`` or a commit that never
    # hands over the options it screened.  The rules cannot be met by editing
    # the harness toward a different spelling, because the sanctioned path is
    # a call, not a pattern.
    own_problems = audit_report_writers(cli_dir=_SELF_DIR, writers=["coverage_harness.py"],
                                        artifacts=False)
    expect("this harness commits through the shared provenance owner",
           not own_problems, "; ".join(own_problems))
    # A behavioural control, because the guarantee is about the artifact and
    # not about the source: a report that carries an unsanitized command, or
    # that lost the derived privacy block, must be refused by the same rules
    # the writer applies before it serializes.
    profile = profile_path() or "/nonexistent-profile"
    forged = {"schema": REPORT_SCHEMA, "command": [profile, "x"],
              "git_head": "0" * 40, "checkout_root": None}
    expect("a record that did not go through the sanitizer is refused",
           any("sanitized_command" in problem or "carries a personal path"
               in problem for problem in
               committed_report_problems(forged, require_privacy=True,
                                         screened=("--work",))),
           str(committed_report_problems(forged, require_privacy=True,
                                         screened=("--work",))))
    expect("an artifact from this harness without the privacy block is refused",
           any("privacy" in problem for problem in
               committed_report_problems(
                   {"schema": REPORT_SCHEMA, "command": ["v4/cli/x.py"],
                    "git_head": "0" * 40, "checkout_root": None},
                   where="coverage-go.json", require_privacy=True,
                   screened=("--work",))),
           "no privacy complaint")
    expect("a personal path anywhere in a committed report is refused",
           any("carries a personal path" in problem for problem in
               committed_report_problems(
                   {"schema": REPORT_SCHEMA, "command": ["v4/cli/x.py"],
                    "git_head": "0" * 40, "checkout_root": None,
                    "staging": profile + "/stage"},
                   where="coverage-go.json", require_privacy=False)),
           "no personal-path complaint")

    empty = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                         ".coverage-harness-empty-self-test")
    os.makedirs(empty, exist_ok=True)
    try:
        try:
            package_table("go", [empty], work_dir=empty)
            rejected = False
        except SystemExit:
            rejected = True
        expect("an empty coverage directory is refused, not scored as 0%",
               rejected)
    finally:
        shutil.rmtree(empty, ignore_errors=True)

    # Staging controls: the private source copy must carry the conformance
    # corpus the Go tests read as a sibling of the module.  A tree staged
    # without it would make those tests fail, and publishing a percentage
    # from such a run would overstate coverage rather than reveal the gap.
    # ``covdata percent`` prints a zero-statement package with no percentage,
    # which lets the following package share the line.  The cross-check must
    # attribute each percentage to its own package, or a perfectly good
    # measurement is refused as a divergence.
    # The merged set must never score below one of its own inputs.
    def _fake(percent):
        return {"statements": percent, "functions": percent,
                "blocks": percent}
    try:
        assert_monotonic({"unit": _fake(46.0)}, _fake(40.0))
        caught_shrink = False
    except SystemExit:
        caught_shrink = True
    expect("a merge that scores below its own input is refused",
           caught_shrink)
    try:
        assert_monotonic({"unit": _fake(46.0),
                          "integration": _fake(40.0)}, _fake(59.0))
        accepted_union = True
    except SystemExit:
        accepted_union = False
    expect("a union above every input is accepted", accepted_union)

    merged = ("\tgithub.com/firehol/iprange/v4/go/internal/work\t\t"
              "\tgithub.com/firehol/iprange/v4/go/internal/worker"
              "\t\tcoverage: 71.4% of statements")
    attributed = parse_covdata_percent(merged)
    expect("a line holding two packages attributes each percentage to its own",
           attributed.get("github.com/firehol/iprange/v4/go/internal/worker")
           == 71.4
           and "github.com/firehol/iprange/v4/go/internal/work" not in attributed,
           str(attributed))

    base = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                        ".coverage-harness-stage-self-test")
    shutil.rmtree(base, ignore_errors=True)
    try:
        complete = os.path.join(base, "full")
        os.makedirs(os.path.join(complete, "go", "internal"), exist_ok=True)
        os.makedirs(os.path.join(complete, "conformance", "rust"),
                    exist_ok=True)
        with open(os.path.join(complete, "go", "go.mod"), "w",
                  encoding="utf-8") as stream:
            stream.write("module github.com/firehol/iprange/v4/go\n")
        with open(os.path.join(complete, "conformance", "cases.json"), "w",
                  encoding="utf-8") as stream:
            stream.write("{}\n")
        os.makedirs(os.path.join(complete, "cli"), exist_ok=True)
        with open(os.path.join(complete, "cli", "fd_pressure_harness.py"),
                  "w", encoding="utf-8") as stream:
            stream.write("# staged sibling harness\n")
        staged = stage_sources(complete, os.path.join(base, "staged-full"))
        expect("a complete tree stages the module with its sibling trees",
               os.path.isdir(staged) and os.path.isfile(
                   os.path.join(os.path.dirname(staged), "conformance",
                                "cases.json")) and os.path.isfile(
                   os.path.join(os.path.dirname(staged), "cli",
                                "fd_pressure_harness.py")), staged)

        empty_v4 = os.path.join(base, "nocorp")
        os.makedirs(os.path.join(empty_v4, "go"), exist_ok=True)
        try:
            stage_sources(empty_v4, os.path.join(base, "staged-nocorp"))
            refused = False
        except SystemExit:
            refused = True
        expect("staging without the conformance sibling is refused", refused)

        broken = os.path.join(base, "brokenbody")
        os.makedirs(os.path.join(broken, "go"), exist_ok=True)
        os.makedirs(os.path.join(broken, "conformance"), exist_ok=True)
        try:
            stage_sources(broken, os.path.join(base, "staged-broken"))
            refused = False
        except SystemExit:
            refused = True
        expect("a staged tree missing conformance/cases.json is refused",
               refused)

        # The descriptor-pressure suite runs the sibling harness and fails,
        # not skips, when that file is absent, so staging that omits it would
        # be reported as a red module rather than a broken measurement.
        noharness = os.path.join(base, "noharness")
        os.makedirs(os.path.join(noharness, "go"), exist_ok=True)
        os.makedirs(os.path.join(noharness, "conformance"), exist_ok=True)
        os.makedirs(os.path.join(noharness, "cli"), exist_ok=True)
        with open(os.path.join(noharness, "go", "go.mod"), "w",
                  encoding="utf-8") as stream:
            stream.write("module github.com/firehol/iprange/v4/go\n")
        with open(os.path.join(noharness, "conformance", "cases.json"), "w",
                  encoding="utf-8") as stream:
            stream.write("{}\n")
        try:
            stage_sources(noharness, os.path.join(base, "staged-noharness"))
            refused = False
        except SystemExit:
            refused = True
        expect("a staged tree whose cli member lacks the harness is refused",
               refused)
    finally:
        shutil.rmtree(base, ignore_errors=True)

    # The live-only branches are where an unimported shared helper hides: no
    # other control reaches them, so the binding is checked structurally here.
    missing_helpers = _unimported_shared_helpers()
    expect("every shared helper this module calls is imported",
           not missing_helpers,
           "not imported from command_sanitize: %s" % ", ".join(missing_helpers))

    run_shared_self_test("coverage_harness")
    if counter["n"] != COVERAGE_SELF_TEST_CONTROLS:
        print(f"coverage harness self-test FAILED: executed {counter['n']} "
              f"controls, expected exactly {COVERAGE_SELF_TEST_CONTROLS}")
        return 1
    if failures:
        print(f"coverage harness self-test FAILED: {failures} case(s)")
        return 1
    print(f"coverage harness self-test PASSED ({counter['n']} controls)")
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go-module", default=os.path.join(
        os.path.dirname(_HERE), "go"))
    parser.add_argument("--work")
    parser.add_argument("--matrices", default=",".join(DEFAULT_MATRICES))
    parser.add_argument("--rust", help="uninstrumented Rust reference service")
    parser.add_argument("--fixture-tool", help="v4-fixture database producer")
    parser.add_argument("--v4-tree", help="directory holding go/ and "
                        "conformance/ (default: parent of --go-module)")
    parser.add_argument("--revision", help="stage the measured tree from this "
                        "committed revision via git archive instead of from "
                        "the working tree; use this while other workers edit "
                        "the checkout concurrently")
    parser.add_argument("--json-report")
    parser.add_argument("--self-test", action="store_true")
    args = parser.parse_args()
    if args.self_test:
        return _self_test()
    return live_run(args)


if __name__ == "__main__":
    sys.exit(main())
