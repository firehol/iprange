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


def _expand_env_vars(value):
    """Expand %NAME% and $NAME/${NAME} environment references.

    Unknown names stay literal so a comparison never misfires on a
    non-path string that merely resembles a variable reference."""
    try:
        value = os.path.expandvars(value)
    except (ValueError, TypeError):
        pass
    if "%" in value:
        import re

        def repl(match):
            name = match.group(1)
            return os.environ.get(name, match.group(0))

        try:
            value = re.sub(r"%([^%]+)%", repl, value)
        except (ValueError, TypeError):
            pass
    return value


# Windows device/verbatim prefix forms, built from chr(92) so the
# trailing backslash cannot be mistaken for a source escape:
# ``\\?\\UNC\`` rewrites a verbatim UNC path back to the ordinary
# ``\\server\share`` form; the other prefixes are stripped away.
_BS = chr(92)
# ``\\?\\UNC\\`` (with its trailing separator): the verbatim form
# of a UNC path; stripping it and re-adding the double-separator
# prefix restores the ordinary ``\\server\\share`` root.
_DEVICE_UNC = _BS + _BS + "?" + _BS + "UNC" + _BS
# Device/verbatim prefixes stripped before comparison: ``\\?\\``,
# ``\\.\\``, the W32 namespace ``\\??\\`` (two and one leading
# backslash spellings), and the native NT prefix ``\??\\``.
_DEVICE_PREFIXES = (_BS + _BS + "?" + _BS,
                    _BS + _BS + "." + _BS,
                    _BS + _BS + "?" + _BS + _BS,
                    _BS + _BS + "?" + "?" + _BS,
                    _BS + "?" + "?" + _BS)


def _strip_device_prefix(value):
    """Map Windows verbatim/device spellings back to ordinary path
    spellings before comparison.  ``\\?\\UNC\\server\\share``
    (any case) becomes ``\\server\\share``; the ``\\?\\``,
    ``\\.\\`` and ``\\??\\`` prefixes are stripped.  Applied
    only on Windows; other platforms keep the value unchanged."""
    lowered = value.lower()
    if lowered.startswith(_DEVICE_UNC.lower()):
        # ``\\?\\UNC\\server\\share`` -> ``\\\\server\\share``:
        # the second leading separator is spelled by the ``UNC``
        # component, so re-add the double-separator prefix after
        # stripping the verbatim marker.
        return _BS + _BS + value[len(_DEVICE_UNC):].lstrip(_BS)
    for prefix in _DEVICE_PREFIXES:
        if lowered.startswith(prefix.lower()):
            return value[len(prefix):]
    return value


def _is_drive_relative(value):
    """True for a drive-relative spelling such as ``C:foo`` (drive
    prefix without a following separator), which resolves against the
    current directory on that drive."""
    return (len(value) >= 2 and value[1] == ":"
            and (len(value) == 2
                 or value[2] not in (os.sep, "/", "\\")))


def _privacy_spellings(value):
    """Candidate normcased spellings of one string used for the
    operator-profile comparison.

    Every candidate is separator-normalized, environment-expanded,
    and (on Windows) device-prefix-stripped.  The direct spelling
    catches the literal form; the lexically resolved spelling catches
    ``..`` parent segments and, on POSIX, doubled leading separators
    (the kernel resolves ``//home`` as ``/home``); a drive-relative
    spelling (``C:Users\\...``) is kept as-is so the profile's own
    drive-relative comparison form can match it without depending on
    the per-drive current directory that ``ntpath.abspath`` consults.
    """
    norm = value.replace("/", os.sep).replace("\\", os.sep)
    if "%" in norm or "$" in norm:
        norm = _expand_env_vars(norm)
    if os.name == "nt":
        norm = _strip_device_prefix(norm)
    out = [os.path.normcase(norm)]
    resolved = os.path.normcase(os.path.normpath(norm)) if norm else norm
    if os.sep == "/" and resolved.startswith("//"):
        resolved = "/" + resolved.lstrip("/")
    if resolved != out[0]:
        out.append(resolved)
    if os.name == "nt" and _is_drive_relative(norm):
        anchored = os.path.normcase(os.path.normpath(os.path.abspath(norm)))
        if anchored not in out:
            out.append(anchored)
    return out


def _profile_comparisons(profile):
    """Profile spellings to match candidates against.

    The absolute form and, on Windows, its drive-relative form
    (``C:Users\\alice`` for ``C:\\Users\\alice``): a
    drive-relative candidate (``C:Users\\alice\\...``) then
    matches without depending on the per-drive current directory
    (native Windows resolution consults the drive's current
    directory, which the comparison must not rely on)."""
    forms = [profile]
    if os.name == "nt" and len(profile) >= 3 and profile[1] == ":":
        forms.append(profile[:2] + profile[3:])
    return forms


def _matches_profile(spelling, profile):
    """True when one normcased spelling is at or under any of the
    profile's comparison forms."""
    return any(spelling == form or spelling.startswith(form + os.sep)
               for form in _profile_comparisons(profile))


def under_profile(path):
    """True when a path lives at or under the operator's profile.

    Every candidate spelling of the path is compared --- the direct
    form, the lexically resolved form, the drive-relative anchored
    form, and the realpath (so a symlink or junction into the profile
    is refused with the same spelling the evidence records will
    carry).
    """
    profile = profile_path()
    if not profile:
        return False
    for spelling in _privacy_spellings(path):
        if _matches_profile(spelling, profile):
            return True
    try:
        real = os.path.normcase(os.path.normpath(os.path.realpath(path)))
    except OSError:
        return False
    return _matches_profile(real, profile)


def personal_path_in_report(report):
    """Return one string field that is or starts with the operator's
    profile path, or None.  Structural scan over every string value so
    a future report field cannot silently re-introduce a personal
    path."""
    profile = profile_path()
    if not profile:
        return None
    hit = []

    profile_abs = profile
    profile_term = profile_abs + os.sep

    def visit(value):
        if isinstance(value, str):
            for spelling in _privacy_spellings(value):
                if _matches_profile(spelling, profile):
                    hit.append(value)
                    return
            # Mid-string occurrences: a build command or an option
            # value that embeds the profile path (``--cases=
            # /home/alice/x``, ``cd /home/alice/x && make``) must
            # also trip the scan.  The separator-terminated form
            # keeps sibling names (``/home/alice-notes``) from
            # false-positive.
            for spelling in _privacy_spellings(value):
                if profile_term in spelling or spelling.endswith(profile_abs):
                    hit.append(value)
                    return
        elif isinstance(value, dict):
            for item in value.items():
                visit(item[0])
                visit(item[1])
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
