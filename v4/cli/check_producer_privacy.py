#!/usr/bin/env python3
"""Prove the committed-report contract of the lead-owned writers and producers.

Three committed-evidence producers are owned here rather than by a product
harness:

* ``run.py``, which writes the four ``evidence/matrix-*.json`` reports;
* ``check_refusal_class_parity.py``, which writes
  ``evidence/refusal-class-parity.json``; and
* ``command_sanitize.py`` itself, which recomputes
  ``evidence/build-ids.json`` (``--emit-build-ids``) and promotes the
  battery manifest that ``check_kind_coverage.py --emit-manifest`` authored
  (``--commit-report``).

A writer reaches the evidence directory only through
``command_sanitize.write_committed_report``, which owns the provenance members
(``command``, ``checkout_root``, ``git_head``) and the derived ``privacy``
block, and refuses the write when a screened input or any finished string value
names the operator's profile.  Each of those three properties is a separate
possibility to regress, so each is attacked here: a writer that screens nothing,
a writer that serializes its own JSON, and an artifact whose privacy block is
absent are all named by a control, not by a reading of the source.

Usage::

    python3 v4/cli/check_producer_privacy.py --self-test

The executed-control count is an obligation (``SELF_TEST_CONTROLS``): a control
that stops running fails the gate with the same authority as one that fails.
"""

import argparse
import copy
import json
import os
import shutil
import sys
import tempfile

sys.dont_write_bytecode = True

_HERE = os.path.dirname(os.path.abspath(__file__))
if _HERE not in sys.path:
    sys.path.insert(0, _HERE)

import command_sanitize as cs  # noqa: E402  (side-effect free)

# The writers whose commit path is owned by this wave's lead, plus the two
# artifacts that had no registered producer: an identity record and the report
# set the battery reviewed.
OWNED_WRITERS = ("run.py", "check_refusal_class_parity.py")
ORPHAN_ARTIFACTS = (cs.BUILD_IDS_ARTIFACT, cs.BATTERY_MANIFEST_ARTIFACT)

# A committed report of each owned writer, reduced to the members the audit
# judges.  The measurement fields are irrelevant here; what matters is that the
# artifact-side rules are driven over a document shaped like a real one.
def matrix_shaped_report():
    return {"schema": "iprange-cli-report-v3", "matrix": "rust",
            "passed": 1, "failed": 0, "skipped": 0,
            "cases": [], "engine_deaths": [], "matrix_verdicts": []}


def _synthetic_package(root):
    """A minimal package layout under the name the producer expects.

    The directory is named ``iprange-livedb`` because that is the package the
    committed record describes, and the controls must drive the same entry
    point the battery does rather than a parallel one.
    """
    package = os.path.join(root, cs.BUILD_IDS_PACKAGE)
    os.makedirs(os.path.join(package, "src", "worker"), exist_ok=True)
    with open(os.path.join(package, "Cargo.toml"), "w",
              encoding="utf-8") as stream:
        stream.write("[package]\nname = \"iprange-livedb\"\n")
    for name, body in (("src/lib.rs", "// lib\n"),
                       ("src/worker/control.rs", "// control\n")):
        with open(os.path.join(package, name), "w",
                  encoding="utf-8") as stream:
            stream.write(body)
    return package


def _writer_copy_with_source(cli_dir, module_name, replacement):
    """Copy one writer module into ``cli_dir``, replacing one statement.

    The mutant keeps every other property of the real file, so the only reason
    the audit can name it is the change under test.
    """
    with open(os.path.join(_HERE, module_name), encoding="utf-8") as stream:
        source = stream.read()
    if source.count(replacement[0]) != 1:
        raise AssertionError(
            f"{module_name}: mutation anchor appears "
            f"{source.count(replacement[0])} times, expected exactly 1: "
            f"{replacement[0]!r}")
    os.makedirs(cli_dir, exist_ok=True)
    with open(os.path.join(cli_dir, module_name), "w",
              encoding="utf-8") as stream:
        stream.write(source.replace(replacement[0], replacement[1], 1))


def _controls():
    """Run every control; return (executed, failures)."""
    executed = 0
    failures = []

    def check(group, description, condition, detail=""):
        nonlocal executed
        executed += 1
        if not condition:
            failures.append(f"[{group}] {description}: {detail}")
        print(f"{'ok  ' if condition else 'FAIL'} {group:14s} {description}")

    root = tempfile.mkdtemp(prefix="qual-producer-privacy-", dir="/tmp")
    try:
        # --- 1: the registry claims what the evidence directory holds ------
        claimed = {}
        for module_name, entry in cs.COMMITTED_REPORT_WRITERS.items():
            for artifact in entry.get("artifacts") or ():
                claimed[artifact] = module_name
        for artifact in ORPHAN_ARTIFACTS:
            check("registry", f"{artifact} has a registered producer",
                  claimed.get(artifact) is not None,
                  "no writer owns it, so the committed audit refuses it")
        check("registry", "the identity records are produced by this module",
              cs.registry_entry_for("/x/" + cs.BUILD_IDS_ARTIFACT)
              is cs.COMMITTED_REPORT_WRITERS.get(cs._OWNER_MODULE)
              and cs.registry_entry_for("/x/" + cs.BATTERY_MANIFEST_ARTIFACT)
              is cs.COMMITTED_REPORT_WRITERS.get(cs._OWNER_MODULE),
              "an identity record named by another writer would attribute a "
              "promotion to a measurement that never happened")
        check("registry", "the whole registry is well formed",
              not cs._registry_problems(), str(cs._registry_problems()))
        check("registry", "a writer left on the legacy tier names its owner",
              all(entry.get("owner")
                  for entry in cs.COMMITTED_REPORT_WRITERS.values()
                  if entry.get("tier") == cs.LEGACY_TIER),
              "an unowned legacy entry is a defence nobody is coming back for")

        # --- 2: the owned writers pass the source-level audit -------------
        for module_name in OWNED_WRITERS:
            problems = cs.audit_report_writers(
                cli_dir=_HERE, writers=[module_name], artifacts=False)
            check("writer", f"{module_name} commits through the shared owner",
                  not problems, "; ".join(problems))

        # --- 3: mutations the audits must catch ---------------------------
        mutants = os.path.join(root, "mutants")
        # Two bypass classes per writer, both of which leave the file
        # otherwise identical: serializing a report outside the sanctioned
        # writer, and reaching the sanctioned writer without handing over the
        # options it must record as screened.  Dropping the screening call that
        # is *not* mutated here, because for a shared-tier writer the input
        # refusal also runs inside ``write_committed_report``; the bypass that
        # matters is the one that skips the writer, and the one that empties
        # the record it derives.
        cases = (
            ("check_refusal_class_parity.py",
             ("    text = write_committed_report(report_path, report, argv=sys.argv,",
              "    with open(report_path, 'w', encoding='utf-8') as _s:\n"
              "        json.dump(report, _s, sort_keys=True, indent=1)\n"
              "    text = ''\n    if 1:\n        write_committed_report("
              "report_path, report, argv=sys.argv,"),
             "serializes a report outside write_committed_report()"),
            ("check_refusal_class_parity.py",
             ("\n                                  caller_paths="
              "live_run_caller_paths(args),", ""),
             "without caller_paths="),
            ("run.py",
             ("        write_committed_report(args.json_report, report,\n"
              "                              caller_paths=caller_paths, indent=2)",
              "        with open(args.json_report, \"w\", encoding=\"utf-8\") "
              "as _s:\n"
              "            json.dump(report, _s, indent=2, sort_keys=True)\n"
              "            _s.write(\"\\n\")"),
             "serializes a report outside write_committed_report()"),
            ("run.py",
             ("                              caller_paths=caller_paths, indent=2)",
              "                              indent=2)"),
             "without caller_paths="),
        )
        for index, (module_name, replacement, needle) in enumerate(cases):
            one = os.path.join(mutants, f"case{index}")
            _writer_copy_with_source(one, module_name, replacement)
            problems = cs.audit_report_writers(cli_dir=one,
                                               writers=[module_name],
                                               artifacts=False)
            check("mutation", f"{module_name}: {needle}",
                  any(needle in problem for problem in problems),
                  f"audit reported: {problems}")

        # --- 4: the artifact-side rules, over a report-shaped document ----
        entry = cs.registry_entry_for("/x/matrix-rust.json")
        clean = cs.write_committed_report(
            os.path.join(root, "matrix-clean.json"), matrix_shaped_report(),
            argv=["v4/cli/run.py"],
            caller_paths=tuple((label, None) for label in entry["screened"]),
            indent=2)
        document = json.loads(clean)
        check("artifact", "a report written by the shared owner carries the "
                          "derived privacy block",
              document.get("privacy", {}).get("sanitizer")
              == cs.PRIVACY_SANITIZER_NAME, str(document.get("privacy")))
        check("artifact", "the committed rules accept it",
              not cs.committed_report_problems(
                  copy.deepcopy(document), where="matrix",
                  require_privacy=True, screened=entry["screened"]),
              str(cs.committed_report_problems(
                  copy.deepcopy(document), where="matrix",
                  require_privacy=True, screened=entry["screened"])))
        stripped = copy.deepcopy(document)
        del stripped["privacy"]
        problems = cs.committed_report_problems(
            stripped, where="matrix", require_privacy=True,
            screened=entry["screened"])
        check("mutation", "a committed matrix report without the privacy "
                          "block is named",
              any("privacy" in problem for problem in problems),
              str(problems))
        stopped = copy.deepcopy(document)
        stopped["privacy"] = dict(stopped["privacy"],
                                  checked_inputs=sorted(
                                      set(entry["screened"]) - {"--cases"}))
        problems = cs.committed_report_problems(
            stopped, where="matrix", require_privacy=True,
            screened=entry["screened"])
        check("mutation", "a writer that stopped screening one option is named",
              any("stopped screening" in problem for problem in problems),
              str(problems))

        # --- 5: the build-id producer -------------------------------------
        package = _synthetic_package(root)
        first = cs.compute_build_id(package)
        check("build-id", "the digest is a sha-256 in hex",
              len(first) == 64 and all(c in "0123456789abcdef" for c in first),
              first)
        with open(os.path.join(package, "src", "lib.rs"), "a",
                  encoding="utf-8") as stream:
            stream.write("// one more byte\n")
        check("build-id", "changing one source byte changes the digest",
              cs.compute_build_id(package) != first,
              "a digest that ignores content cannot bind a binary to a tree")
        with open(os.path.join(package, "src", "notes.txt"), "w",
                  encoding="utf-8") as stream:
            stream.write("not a source file\n")
        check("build-id", "a non-.rs file is outside the identity",
              cs.compute_build_id(package) == cs.compute_build_id(package),
              "the hashed input set must be the one build.rs walks")
        check("build-id", "the pre-normalizer Windows spelling hashes "
                          "differently",
              cs.compute_build_id(package, "windows")
              != cs.compute_build_id(package, "posix"),
              "if the host spelling made no difference there was nothing for "
              "the logical name to fix")
        moved = os.path.join(root, "staged-elsewhere")
        shutil.copytree(os.path.dirname(package), moved)
        check("build-id", "one source state has one identity wherever it is "
                          "staged",
              cs.compute_build_id(os.path.join(moved, cs.BUILD_IDS_PACKAGE))
              == cs.compute_build_id(package),
              "a path-dependent identity cannot attest that two binaries came "
              "from one tree")
        document = cs.build_ids_document(package)
        check("build-id", "every host entry names the recomputed digest",
              set(document["expected_build_id"]) == set(cs.BUILD_IDS_HOSTS)
              and len(set(document["expected_build_id"].values())) == 1,
              str(document["expected_build_id"]))
        check("build-id", "built products not supplied are recorded as not "
                          "measured",
              document["verified_against_built_products"]["cli_contains_expected"]
              == "not measured"
              and document["verified_against_built_products"]
              ["worker_contains_expected"] == "not measured",
              str(document["verified_against_built_products"]))
        blob = ("prefix" + document["expected_build_id"]["linux"]
                + "suffix").encode("ascii")
        built = os.path.join(root, "built-worker")
        with open(built, "wb") as stream:
            stream.write(blob)
        measured = cs.build_ids_document(package, built={"worker": built})
        check("build-id", "a digest found in the built product is measured, "
                          "not assumed",
              measured["verified_against_built_products"]
              ["worker_contains_expected"] is True
              and measured["verified_against_built_products"]
              ["cli_contains_expected"] == "not measured",
              str(measured["verified_against_built_products"]))
        emitted_dir = os.path.join(root, "emitted")
        emitted = os.path.join(emitted_dir, cs.BUILD_IDS_ARTIFACT)
        cs.emit_build_ids(emitted, rust_tree=root, argv=[
            "v4/cli/command_sanitize.py", "--emit-build-ids", emitted,
            "--rust-tree", root])
        with open(emitted, encoding="utf-8") as stream:
            emitted_document = json.load(stream)
        problems = cs.committed_report_problems(
            emitted_document, where=cs.BUILD_IDS_ARTIFACT,
            require_privacy=True,
            screened=cs.registry_entry_for("/x/" + cs.BUILD_IDS_ARTIFACT)
            ["screened"])
        check("build-id", "what --emit-build-ids writes passes the committed "
                          "rules",
              not problems and emitted_document["schema"] == cs.BUILD_IDS_SCHEMA
              and emitted_document["package"] == cs.BUILD_IDS_PACKAGE,
              "; ".join(problems))
        try:
            cs.emit_build_ids(os.path.join(root, "leak", cs.BUILD_IDS_ARTIFACT),
                              rust_tree=os.path.dirname(package),
                              built={"cli": os.path.join(cs.profile_path()
                                                         or "/nonexistent",
                                                         "iprange")},
                              argv=["v4/cli/command_sanitize.py"])
            refused = False
        except SystemExit:
            refused = True
        check("mutation", "a built product under the profile is refused",
              refused and not os.path.exists(os.path.join(root, "leak")),
              "the operator's home path would have entered a committed record")

        # --- 6: the promotion path ----------------------------------------
        staged = os.path.join(root, "staged", cs.BATTERY_MANIFEST_ARTIFACT)
        os.makedirs(os.path.dirname(staged), exist_ok=True)
        document = {"schema": cs.BUILD_IDS_SCHEMA, "reports": [],
                    "git_head": "0" * 39 + "1", "ledger": None}
        with open(staged, "w", encoding="utf-8") as stream:
            json.dump(document, stream)
        dest = os.path.join(root, "committed", cs.BATTERY_MANIFEST_ARTIFACT)
        cs.commit_report(staged, dest, argv=["v4/cli/command_sanitize.py",
                                            "--commit-report", staged,
                                            "--commit-report-to", dest])
        with open(dest, encoding="utf-8") as stream:
            promoted = json.load(stream)
        problems = cs.committed_report_problems(
            promoted, where=cs.BATTERY_MANIFEST_ARTIFACT,
            require_privacy=True,
            screened=cs.registry_entry_for("/x/" + cs.BATTERY_MANIFEST_ARTIFACT)
            ["screened"])
        check("promote", "a promoted report passes the committed rules",
              not problems, "; ".join(problems))
        check("promote", "the promotion is what the record names as its "
                         "command",
              "--commit-report" in promoted["command"], str(promoted["command"]))
        try:
            cs.commit_report(staged, os.path.join(root, "committed",
                                                  "crash.json"))
            refused = False
        except SystemExit:
            refused = True
        check("mutation", "promoting over another writer's artifact is refused",
              refused and not os.path.exists(os.path.join(root, "committed",
                                                          "crash.json")),
              "a promotion would then masquerade as the harness measurement")
        try:
            cs.commit_report(staged, staged)
            refused = False
        except SystemExit:
            refused = True
        check("mutation", "promoting a report onto itself is refused", refused,
              "the record could not say who wrote it")
        profile = cs.profile_path()
        if profile:
            leaked = os.path.join(root, "leaked", cs.BATTERY_MANIFEST_ARTIFACT)
            with open(staged, "w", encoding="utf-8") as stream:
                json.dump({"schema": cs.BUILD_IDS_SCHEMA,
                           "note": profile + "/staged"}, stream)
            try:
                cs.commit_report(staged, leaked)
                refused = False
            except SystemExit:
                refused = True
            check("mutation", "a personal path in the report is refused",
                  refused and not os.path.exists(leaked),
                  "the scan runs whatever channel the path arrived through")

        # --- 7: the completeness rule over an evidence directory ----------
        # Outside a git tree the audits judge every present report, which is
        # the same code path a committed tree reaches once the file is tracked.
        orphan_dir = os.path.join(root, "cli", "evidence")
        os.makedirs(orphan_dir, exist_ok=True)
        with open(os.path.join(orphan_dir, "someone-elses-report.json"),
                  "w", encoding="utf-8") as stream:
            json.dump({"schema": "x"}, stream)
        problems = cs.audit_committed_reports(
            cli_dir=os.path.join(root, "cli"))
        check("registry", "a committed report from an unregistered producer "
                          "is named",
              any("unregistered writer" in problem for problem in problems),
              str(problems[:3]))
        check("registry", "a registered artifact that vanished is named",
              any("absent from the evidence directory" in problem
                  for problem in problems), str(problems[:3]))
    finally:
        shutil.rmtree(root, ignore_errors=True)
    return executed, failures


SELF_TEST_CONTROLS = 32


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--self-test", action="store_true",
                        help="run the controls and exit")
    args = parser.parse_args()
    if not args.self_test:
        parser.error("--self-test is required")
    executed, failures = _controls()
    cs.run_shared_self_test("check_producer_privacy")
    for problem in failures:
        print(f"FAIL producer privacy: {problem}")
    if executed != SELF_TEST_CONTROLS:
        print(f"producer privacy FAILED: executed {executed} controls, "
              f"expected exactly {SELF_TEST_CONTROLS}; a control that stopped "
              "running is the defect this pin exists to catch")
        return 1
    if failures:
        print(f"producer privacy FAILED: {len(failures)} of {executed}")
        return 1
    print(f"producer privacy PASSED: {executed} controls "
          f"(committed total {SELF_TEST_CONTROLS})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
