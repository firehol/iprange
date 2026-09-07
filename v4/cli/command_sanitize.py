"""Command and evidence sanitization shared by the v4 CLI qualification
harnesses (run.py, crash_harness.py, resource_harness.py, and the
Windows housekeeping harness).

Durable-artifact policy: committed evidence must never carry the
operator's profile path.  Every harness that writes a JSON report uses
these helpers so the three defenses stay identical everywhere:

1. ``under_profile`` refuses path-valued inputs that live at or under
   the operator's profile (normcase prefix match) before the run
   starts;
2. ``sanitized_command`` rewrites path-shaped argv elements to
   checkout-relative spellings, so ``report.command`` never records a
   profile path and is invariant to the invocation directory;
3. ``personal_path_in_report`` structurally scans the completed report
   immediately before it is serialized and refuses any string value
   that is or resolves to the profile path.

``owned_temp_root`` / ``owned_temp_dir`` pin harness scratch to a temp
root that is an absolute directory outside the checkout, so a
path-valued TMPDIR/TEMP override cannot place profile-named scratch
directories inside the checkout where they could be committed by
accident.
"""

import os
import sys
import tempfile

# Repository root that owns this module (v4/cli -> repo root).  All
# relative spellings are resolved against this root, never against the
# process working directory, so sanitized records and harness
# self-tests are invariant to the invocation directory.
_CHECKOUT = os.path.normpath(
    os.path.dirname(os.path.dirname(os.path.dirname(
        os.path.abspath(__file__)))))


def checkout_root():
    """Repository root that owns this module."""
    return _CHECKOUT


def sanitized_path_value(value):
    """Rewrite one path-shaped value to a checkout-relative spelling
    when it lives under the checkout; keep every other value as
    passed.

    Absolute spellings are compared as recorded.  Relative spellings
    are resolved against the checkout root (never the process working
    directory), so the rewritten result is identical regardless of the
    invocation directory.  The containment comparison uses normcase so
    a case-varied checkout spelling cannot escape rewriting; a
    different-drive value (commonpath ValueError) is never treated as
    under the checkout.
    """
    if not value:
        return value
    abs_path = _resolve(value)
    norm = os.path.normcase(abs_path)
    checkout_norm = os.path.normcase(_CHECKOUT)
    try:
        under_checkout = (os.path.commonpath([norm, checkout_norm])
                          == checkout_norm)
    except ValueError:
        under_checkout = False
    if under_checkout:
        return os.path.relpath(abs_path, _CHECKOUT)
    return value


def _resolve(value):
    """One value as an absolute normalized spelling.

    Relative spellings resolve against the checkout root so the
    sanitized record is invariant to the invocation directory.
    """
    if os.path.isabs(value):
        return os.path.normpath(value)
    return os.path.normpath(os.path.join(_CHECKOUT, value))


def _looks_like_path(value):
    """True when an argv element is path-shaped: it carries a path
    separator or a drive prefix, or is a dot spelling.  Bare tokens
    (``8``, option values that are not paths) are never cwd-resolved;
    the sanitizer resolves every path-shaped spelling against the
    checkout root, so the sanitized command record and the self-test
    are invariant to the invocation directory.
    """
    return ("/" in value or "\\" in value
            or len(value) >= 2 and value[1] == ":"
            or value in (".", ".."))


def sanitized_command(argv=None):
    """Return argv with every path-shaped element rewritten to a
    checkout-relative spelling when it lives under the checkout, so
    the committed evidence never records the operator's home
    directory (durable-artifact policy).  Option tokens are kept
    verbatim; ``--option=PATH`` and label-prefixed values (``rust=``,
    ``go=``) sanitize only the embedded path value.  Paths outside
    the checkout (binary and work-dir paths under the authorized
    validation host's scratch area) are kept as recorded.  ``argv``
    defaults to ``sys.argv`` and may be injected by the self-test.
    """
    if argv is None:
        argv = sys.argv
    out = []
    for arg in argv:
        if not arg:
            out.append(arg)
        elif arg.startswith("--") and "=" in arg:
            key, sep, value = arg.partition("=")
            out.append(key + sep + sanitized_path_value(value))
        elif arg.startswith("-"):
            # Plain option token (also covers the harness's single
            # dash options): never a path; keep verbatim.
            out.append(arg)
        elif "=" in arg:
            label, sep, value = arg.partition("=")
            if any(c in label for c in "/\\"):
                out.append(sanitized_path_value(arg))
            else:
                out.append(label + sep + sanitized_path_value(value))
        elif _looks_like_path(arg):
            out.append(sanitized_path_value(arg))
        else:
            # Bare non-path token: never cwd-resolved, kept verbatim.
            out.append(arg)
    return out


def profile_path():
    """Normcased operator profile root, or an empty string when the
    platform cannot determine it (never match in that case)."""
    home = os.path.expanduser("~")
    if not home:
        return ""
    norm = os.path.normcase(os.path.normpath(home))
    return norm if len(norm) >= 3 else ""


def under_profile(path):
    """True when an absolute path lives at or under the operator's
    profile (normcase prefix match, both separator spellings)."""
    profile = profile_path()
    if not profile:
        return False
    norm = os.path.normcase(os.path.normpath(os.path.abspath(path)))
    return norm == profile or norm.startswith(profile + os.sep)


def personal_path_in_report(report):
    """Return one string field that is or starts with the operator's
    profile path, or None.  Structural scan over every string value so
    a future report field cannot silently re-introduce a personal
    path."""
    profile = profile_path()
    if not profile:
        return None
    hit = []

    def visit(value):
        if isinstance(value, str):
            norm = os.path.normcase(
                value.replace("/", os.sep).replace("\\", os.sep))
            # Also test the `..`-resolved spelling so a path that
            # resolves into the profile through parent segments is
            # caught even when the raw spelling hides it.
            resolved = os.path.normcase(os.path.normpath(norm)) \
                if norm else norm
            if (norm == profile or norm.startswith(profile + os.sep)
                    or resolved == profile
                    or resolved.startswith(profile + os.sep)):
                hit.append(value)
        elif isinstance(value, dict):
            for item in value.values():
                visit(item)
        elif isinstance(value, list):
            for item in value:
                visit(item)

    visit(report)
    return hit[0] if hit else None


def owned_temp_root():
    """One stable scratch root that is an absolute directory outside
    the checkout.

    The ambient temp root (``tempfile.gettempdir``) can be redirected
    by a path-valued TMPDIR/TEMP override or by a harness debugging
    session; when that root is missing, relative, or inside the
    checkout, fall back to the platform-native system temp directory.
    """
    root = tempfile.gettempdir()
    if not (root and os.path.isabs(root) and not _inside_checkout(root)):
        root = neutral_temp_root()
    return root


def owned_temp_dir(prefix):
    """One owned scratch directory under ``owned_temp_root``.

    Callers own the returned directory and must remove it
    (``shutil.rmtree``); a leftover fixed-prefix directory from a
    killed run can never land inside the checkout.
    """
    return tempfile.mkdtemp(prefix=prefix, dir=owned_temp_root())


def _inside_checkout(path):
    """True when path is at or under the checkout (normcase realpath
    containment)."""
    try:
        norm = os.path.normcase(os.path.normpath(os.path.realpath(path)))
    except OSError:
        return False
    checkout_norm = os.path.normcase(os.path.normpath(
        os.path.realpath(_CHECKOUT)))
    return norm == checkout_norm or norm.startswith(checkout_norm + os.sep)


def neutral_temp_root():
    """Platform-native neutral temp root.

    POSIX: ``/tmp``.  Windows: the drive-root ``\\Temp`` directory
    (``C:\\Temp``), which is the documented authorized-scratch
    convention on the Windows validation host; when the checkout sits
    on a non-drive root, fall back to the system Temp directory.

    Unlike the ambient temp root, this root never resolves into the
    checkout or the operator's profile, so harness self-test negative
    probes and the scratch fallback stay environment-independent.
    """
    if os.name == "nt":
        drive, _ = os.path.splitdrive(_CHECKOUT)
        if drive:
            return os.path.join(drive, os.sep, "Temp")
        return os.path.join(os.environ.get("SystemRoot", r"C:\Windows"),
                            "Temp")
    return "/tmp"
