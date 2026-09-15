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

import argparse
import ast
import hashlib
import json
import os
import re
import subprocess
import sys
import tempfile
import unicodedata

# Platform discrimination must not depend on ``os.sep`` alone: the
# msys2-mingw64 NT interpreter reports ``os.sep == "/"`` while
# ``os.name == "nt"`` and its ntpath emits forward slashes.  The
# doubled-leading-separator rule is POSIX kernel semantics (on Windows
# ``//C:`` is a UNC server spelling, never a repeated root), and the
# Windows privacy spellings must be canonical backslashes so the
# device/verbatim prefix rules (spelled in backslashes) always match.
_IS_WINDOWS = os.name == "nt"
_IS_POSIX = os.sep == "/" and not _IS_WINDOWS
_WIN_SEP = chr(92)

# Repository root that owns this module (v4/cli -> repo root).  All
# relative spellings are resolved against this root, never against the
# process working directory, so sanitized records and harness
# self-tests are invariant to the invocation directory.
_CHECKOUT = os.path.normpath(
    os.path.dirname(os.path.dirname(os.path.dirname(
        os.path.abspath(__file__)))))
# The module can be launched through a doubled-leading-separator
# script spelling (``//home/alice/src/iprange/.../run.py``), which
# CPython preserves; the kernel and APFS (macOS repeated-separator
# handling) resolve that root as one separator on POSIX, so the
# checkout root must use the same
# single-root spelling as every candidate path or containment
# fails and personal paths survive sanitization.
if _IS_POSIX and _CHECKOUT.startswith("//") \
        and not _CHECKOUT.startswith("///"):
    _CHECKOUT = _CHECKOUT[1:]


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

    Path-shaped values that exist on the filesystem are first resolved
    with kernel semantics (``os.path.realpath`` on the un-collapsed
    spelling): the runner records the effective executable (run.py
    ``executable()`` realpaths every binary argument), and a lexical
    ``normpath`` collapses ``link/..`` before the symlink is followed,
    so the recorded command would name a different file than the
    kernel executes.  The effective spelling then feeds the same
    containment rendering, and when it differs from the lexical
    spelling the rendered record is the effective spelling (the
    identity the kernel would execute).  Nonexistent values keep the
    lexical spelling verbatim: nothing was executed through them.
    """
    if not value:
        return value
    abs_path = _effective_absolute(value)
    norm = _normcase(abs_path)
    checkout_norm = _normcase(_CHECKOUT)
    try:
        under_checkout = (os.path.commonpath([norm, checkout_norm])
                          == checkout_norm)
    except ValueError:
        under_checkout = False
    if under_checkout:
        if sys.platform == "darwin":
            # APFS (macOS default) is case-insensitive and realpath
            # preserves the recorded spelling, so a case-varied
            # checkout spelling would render as a ``..``-walk that
            # repeats the account path in the record; the suffix is
            # computed from the raw components below the checkout
            # instead, so the record can never carry the checkout's
            # own spelling.
            suffix = _checkout_suffix(abs_path)
            if suffix is not None:
                return suffix
        return os.path.relpath(abs_path, _CHECKOUT)
    if abs_path == _resolve(value):
        # The lexical spelling is already the effective file; keep
        # the recorded spelling so outside-checkout values pass
        # through verbatim.
        return value
    return abs_path


def _effective_absolute(value):
    """One value as the absolute spelling the kernel resolves.

    For path-shaped values that exist on the filesystem the raw
    spelling (symlinks intact, ``..`` un-collapsed) is resolved with
    ``os.path.realpath``, which follows every component in kernel
    order (symlinks before ``..``) -- the same resolution the runner's
    ``executable()`` applies to the binaries it executes.  Values that
    do not exist keep the lexical ``_resolve`` spelling: no file was
    executed through them, and resolving a phantom path could change
    its meaning.  Relative spellings are probed against the checkout
    root (never the process working directory), keeping the record
    invariant to the invocation directory.
    """
    if os.path.isabs(value):
        probe = value
    else:
        probe = os.path.join(_CHECKOUT, value)
    if _IS_POSIX and probe.startswith("//") \
            and not probe.startswith("///"):
        probe = probe[1:]
    if os.path.exists(probe):
        return os.path.realpath(probe)
    return _resolve(value)


def _resolve(value):
    """One value as an absolute normalized spelling.

    Relative spellings resolve against the checkout root so the
    sanitized record is invariant to the invocation directory.
    A doubled leading separator (``//home/...``) is collapsed to one
    on POSIX: the kernel and APFS (macOS repeated-separator
    handling) resolve it as the root, so every comparison path must
    see the same single-root spelling or a case-varied,
    ``//``-prefixed checkout spelling falls out of containment and
    renders as a ``..``-walk repeating the account path.  Windows is
    intentionally excluded: there ``//`` starts a UNC server name,
    not the same path.
    """
    if os.path.isabs(value):
        norm = os.path.normpath(value)
    else:
        norm = os.path.normpath(os.path.join(_CHECKOUT, value))
    if _IS_POSIX and norm.startswith("//") and not norm.startswith("///"):
        return norm[1:]
    return norm


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


# Quote-shaped Unicode characters that delimit a shell word when
# they follow the profile (smart quotes, guillemets, CJK brackets).
# Followed by a path separator they instead belong to a sibling
# segment name on a localized host (``/home/alice’/x``).
_QUOTE_CHARS = frozenset(
    "\u2018\u2019\u201a\u201b\u201c\u201d\u201e\u201f"
    "\u00ab\u00bb\u2039\u203a"
    "\u300c\u300d\u300e\u300f\u301d\u301e"
    "\uff02\uff07")


def _normcase(path):
    """Case folding for path comparisons.

    ``os.path.normcase`` lowercases on Windows and is identity on
    POSIX, but the macOS default volume (APFS) is case-insensitive,
    so darwin comparisons fold case on both sides; otherwise a
    case-varied profile spelling would resolve to the real profile
    directory while every comparison missed it."""
    norm = os.path.normcase(path)
    if sys.platform == "darwin":
        norm = norm.lower()
    return norm


def _fold_windows(path):
    """Windows case fold for the privacy comparison layer.

    Converts every separator to the canonical backslash and folds to
    lower case (case-insensitive volume), independent of ntpath's
    separator conventions.  The msys2-mingw64 NT interpreter reports
    ``os.name == "nt"`` but ``os.sep == "/"`` and its ntpath emits
    forward slashes; the device/verbatim prefix rules are spelled in
    backslashes, so the privacy candidates must be canonical
    backslashes or a mixed-separator device spelling escapes every
    comparison."""
    return path.replace("/", _WIN_SEP).lower()


def profile_path():
    """Normcased operator profile root, or an empty string when the
    platform cannot determine it (never match in that case)."""
    home = os.path.expanduser("~")
    if not home:
        return ""
    if _IS_WINDOWS:
        norm = _fold_windows(os.path.normpath(home))
    else:
        norm = _normcase(os.path.normpath(home))
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
# Every device/verbatim prefix spelling may precede ``UNC\\`` (the
# W32 verbatim, W32 namespace, native-NT, and device namespace
# forms); stripping any of them and re-adding the double-separator
# root restores the ordinary ``\\server\\share`` UNC root.
_DEVICE_UNC_FORMS = (
    _BS + _BS + "?" + _BS + "UNC" + _BS,       # \\?\\UNC\\
    _BS + _BS + "?" + "?" + _BS + "UNC" + _BS, # \\??\\UNC\\
    _BS + "?" + "?" + _BS + "UNC" + _BS,       # \\??\\UNC\\ (native-NT)
    _BS + _BS + "." + _BS + "UNC" + _BS,       # \\\\.\\UNC\\
)
# Device/verbatim prefixes stripped before comparison: ``\\?\\``,
# ``\\.\\``, the W32 namespace ``\\??\\`` (two and one leading
# backslash spellings), and the native NT prefix ``\??\\``.
_DEVICE_PREFIXES = (_BS + _BS + "?" + _BS,
                    _BS + _BS + "." + _BS,
                    _BS + _BS + "?" + _BS + _BS,
                    _BS + _BS + "?" + "?" + _BS,
                    _BS + "?" + "?" + _BS)

# Device/verbatim prefixes anywhere inside a string (built with chr(92)
# for the same reason as the prefix constants; IGNORECASE mirrors the
# prefix stripping).  ``_strip_device_prefix`` only handles a prefix at
# the start of the whole value; a provenance build command embeds
# quoted device paths mid-string, so the privacy scan also compares a
# variant with every inline occurrence removed.
_DEVICE_INLINE_RE = re.compile(
    re.escape(_BS + _BS + "?" + _BS)
    + "|" + re.escape(_BS + _BS + "?" + "?" + _BS)
    + "|" + re.escape(_BS + "?" + "?" + _BS)
    + "|" + re.escape(_BS + _BS + "." + _BS),
    re.IGNORECASE)

# Inline verbatim-UNC form: ``\?\UNC\server\share`` anywhere in a
# string must restore the ordinary ``\\server\share`` root, exactly
# like the whole-string branch in ``_strip_device_prefix``; the generic
# inline strip alone would leave ``UNC\server\share`` without its
# root and a UNC home profile would escape the scan.
_DEVICE_INLINE_UNC_RE = re.compile(
    "|".join(re.escape(form) for form in _DEVICE_UNC_FORMS),
    re.IGNORECASE)


def _strip_device_prefix(value):
    """Map Windows verbatim/device spellings back to ordinary path
    spellings before comparison.  ``UNC\\server\\share`` preceded by
    any device/verbatim prefix spelling (``\\?\\``, ``\\??\\``,
    ``\\??\\``, ``\\.\\`` — any case) becomes ``\\server\\share``;
    the device/verbatim prefixes are stripped.  Applied only on
    Windows; other platforms keep the value unchanged."""
    lowered = value.lower()
    for unc in _DEVICE_UNC_FORMS:
        if lowered.startswith(unc.lower()):
            # ``\\?\\UNC\\server\\share`` -> ``\\\\server\\share``:
            # the second leading separator is spelled by the ``UNC``
            # component, so re-add the double-separator prefix after
            # stripping the prefix marker.
            return _BS + _BS + value[len(unc):].lstrip(_BS)
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
    drive-relative comparison form can match it, and is additionally
    anchored through ``os.path.abspath`` (native Windows resolution
    consults the per-drive current directory) for the kernel's own
    resolution semantics.
    """
    if _IS_WINDOWS:
        norm = value.replace("/", _WIN_SEP)
    else:
        norm = value.replace("/", os.sep).replace("\\", os.sep)
    if "%" in norm or "$" in norm:
        norm = _expand_env_vars(norm)
    if _IS_WINDOWS:
        norm = _strip_device_prefix(norm)
        fold = _fold_windows
    else:
        fold = _normcase
    out = [fold(norm)]
    resolved = fold(os.path.normpath(norm)) if norm else norm
    if not _IS_WINDOWS and resolved.startswith("//"):
        resolved = "/" + resolved.lstrip("/")
    if resolved != out[0]:
        out.append(resolved)
    if _IS_WINDOWS and _is_drive_relative(norm):
        anchored = fold(os.path.normpath(os.path.abspath(norm)))
        if anchored not in out:
            out.append(anchored)
    if _IS_WINDOWS:
        # re.sub interprets backslashes in a string replacement as
        # escapes, so the two-separator root is supplied through a
        # function replacement.
        inline = _DEVICE_INLINE_UNC_RE.sub(
            lambda _match: _WIN_SEP + _WIN_SEP, norm)
        inline = _DEVICE_INLINE_RE.sub("", inline)
        inline = fold(inline)
        if inline not in out:
            out.append(inline)
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
    sep = _WIN_SEP if _IS_WINDOWS else os.sep
    return any(spelling == form or spelling.startswith(form + sep)
               for form in _profile_comparisons(profile))


def _checkout_suffix(path):
    """Checkout-relative spelling of one path without its checkout
    prefix, or None when the folded components do not start with the
    checkout's.

    The checkout prefix is matched folder-by-folder on folded
    spellings, so a case-varied checkout spelling (valid on a
    case-insensitive volume) is consumed entirely and the suffix is
    taken from the raw components below the checkout."""
    norm = os.path.normpath(path)
    if _IS_POSIX and norm.startswith("//") and not norm.startswith("///"):
        norm = norm[1:]
    raw = norm.split(os.sep)
    base = _CHECKOUT.split(os.sep)
    index = 0
    while (index < len(base) and index < len(raw)
           and _normcase(raw[index]) == _normcase(base[index])):
        index += 1
    if index < len(base):
        return None
    suffix = raw[index:]
    return os.path.join(*suffix) if suffix else "."


def recorded_checkout_root():
    """Producer checkout root to record in a report for evidence
    binding, or None when the checkout lives under the operator's
    profile.

    The kind gate resolves checkout-relative command values against
    the recorded root, so evidence produced from one checkout keeps
    its binary-identity binding when assessed from another clone;
    a personal checkout root must never reach the report, so the
    field records None there and relative values fall back to the
    gate's own checkout root (None, absent, and empty are
    equivalent to the gate)."""
    root = _CHECKOUT
    if under_profile(root):
        return None
    return root


def recorded_git_identity(root=None):
    """Full commit OID of the reviewed tree, or None when unavailable.

    Every harness report records this alongside the binaries' sha256 so a
    passing battery is bound to the exact product tree it measured:
    re-running the same binaries against a different checkout is visible
    in the evidence instead of being silently absorbed.  ``checkout_root``
    cannot carry it --- that field is a directory used to resolve
    checkout-relative command arguments --- so the identity is a separate
    member.

    The lookup shells out to ``git`` against the checkout that owns this
    module (never the process working directory) and returns None ---
    never an exception --- when git is absent, the tree is not a git
    checkout, or the output is not a plausible object id, so a report
    produced outside a repository stays honest with a null rather than a
    fabricated revision.
    """
    import subprocess

    root = _CHECKOUT if root is None else root
    try:
        completed = subprocess.run(
            ["git", "-C", root, "rev-parse", "--verify", "HEAD"],
            capture_output=True, text=True, timeout=10, check=False)
    except (OSError, ValueError, subprocess.SubprocessError):
        return None
    if completed.returncode != 0:
        return None
    oid = (completed.stdout or "").strip()
    if len(oid) not in (40, 64) \
            or any(c not in "0123456789abcdef" for c in oid.lower()):
        return None
    return oid


def same_path(a, b):
    """True when two spellings name the same existing path.

    The normcased realpath of each side is compared, so a
    case-varied spelling of the same path matches on
    case-insensitive volumes (macOS APFS default), where the raw
    realpath strings differ even though the kernel resolves both to
    the same object."""
    try:
        return _normcase(os.path.realpath(a)) == _normcase(
            os.path.realpath(b))
    except OSError:
        return False


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
        real = os.path.normpath(os.path.realpath(path))
    except OSError:
        return False
    if _IS_WINDOWS:
        real = _fold_windows(real)
    else:
        real = _normcase(real)
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

    profile_forms = _profile_comparisons(profile)

    def _is_path_sep(ch):
        return ch in ("/", chr(92))

    def _is_path_cont(ch, nxt=None):
        """True when ch continues a path segment after the profile.

        Alphanumerics in any script, ``_``, ``.``, ``-``, combining
        marks, symbols, and number forms continue a segment, so
        sibling names on localized hosts (``/home/alice-notes``,
        ``/home/alice\u03bb/x``, an emoji or superscript suffix)
        stay clean.  Whitespace, every Unicode punctuation category
        (``P*``), control/format characters (``C*``), and
        fullwidth punctuation (a CJK IME substitute for
        ``;?&|,!()``) delimit a shell word; a quote-shaped character
        counts as continuation only when the character after it is a
        path separator, which makes it part of a sibling segment name
        (``/home/alice\u2019/x``)."""
        if ch.isalnum() or ch in "_.-":
            return True
        if ch.isspace():
            return False
        if ord(ch) < 128:
            return False
        if ch in _QUOTE_CHARS:
            return nxt is not None and _is_path_sep(nxt)
        # Fullwidth/ideographic forms produced by CJK IME input map
        # back to their ASCII counterpart: alphanumerics and
        # ``_``/``.``/``-`` continue a sibling segment, punctuation
        # (；？＆｜，！ etc.) delimits like ASCII.
        if 0xFF01 <= ord(ch) <= 0xFF5E:
            ascii_ch = chr(ord(ch) - 0xFEE0)
            return ascii_ch.isalnum() or ascii_ch in "_.-"
        # Every remaining Unicode punctuation (P*) and control/format
        # character (C*) delimits: copy-pasted prose carries
        # ellipsis, dashes, wave dashes, halfwidth CJK marks, and
        # zero-width characters the explicit lists cannot enumerate.
        # Unassigned ``M`` marks, ``S`` symbols (emoji), ``No``
        # number forms, and ``L`` letters (alnum above) continue a
        # segment, so sibling names on localized hosts stay clean.
        return not unicodedata.category(ch).startswith(("P", "C"))

    def _occurrence(spelling):
        """True when any profile comparison form appears in spelling
        with a word boundary on the left and a segment end on the
        right.

        The left side accepts start-of-string or a character that is
        neither a path continuation nor a path separator, so a
        different-root subpath that merely contains the same segments
        (``/var/backups/home/alice/x``) stays clean.  The right side
        accepts end-of-string, a path separator
        (``--cases=/home/alice/x``), or a non-continuation character
        (``cd /home/alice && make``, ``HOME=C:Users\\alice
        make``); sibling names (``/home/alice-notes``) stay clean."""
        for form in profile_forms:
            idx = spelling.find(form)
            while idx != -1:
                left_ok = idx == 0 or (
                    not _is_path_cont(spelling[idx - 1])
                    and not _is_path_sep(spelling[idx - 1]))
                after = idx + len(form)
                nxt = (spelling[after + 1]
                       if after + 1 < len(spelling) else None)
                right_ok = (after == len(spelling)
                            or not _is_path_cont(spelling[after], nxt))
                if left_ok and right_ok:
                    return True
                idx = spelling.find(form, idx + 1)
        return False

    def visit(value):
        if isinstance(value, str):
            for spelling in _privacy_spellings(value):
                if _matches_profile(spelling, profile):
                    hit.append(value)
                    return
            # Mid-string occurrences: a build command or an option
            # value that embeds any profile comparison form (``--cases=
            # /home/alice/x``, ``cd /home/alice && make``, Windows
            # ``cd C:Users\alice && make``) must also trip the
            # scan.  One occurrence walk covers the separator-
            # terminated containment, the profile at the end of the
            # string, and every boundary-delimited form; the left
            # boundary excludes path separators so different-root
            # subpaths that merely contain the same segments cannot
            # false-positive, and the right boundary excludes
            # continuation characters so sibling names stay clean.
            for spelling in _privacy_spellings(value):
                if _occurrence(spelling):
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


# ---------------------------------------------------------------------------
# Committed-report provenance (the single owner).
#
# Every report installed under v4/cli/evidence/ is a durable artifact, so its
# provenance belongs to the artifact rather than to the prose around it: a
# reader must be able to tell from the JSON alone which command produced it,
# from which revision, and whether the privacy policy was applied.  Before
# this section existed, each harness re-implemented those fields and the
# wave-19.24 security review measured that only four of the twelve report
# writers applied the three shared defences (``sanitized_command``,
# ``under_profile``, ``personal_path_in_report``); a fifo-surface record
# carrying the operator's home path passed every committed gate.
#
# Three mechanisms enforce the contract, and none of them skips:
#
#   * ``write_committed_report`` is the only sanctioned commit path.  It owns
#     the provenance and privacy members -- it discards what the caller put
#     there and writes the identity this module measured -- and it refuses the
#     write, leaving no file, when a rule cannot be satisfied;
#   * ``audit_report_writers`` derives each registered writer's compliance
#     from its AST, so a writer that never calls the shared helpers, that
#     commits its own JSON, or that stops screening the options which name
#     paths is named as a problem instead of passing a lint that only matched
#     one spelling of the recording expression; and
#   * ``audit_committed_reports`` applies the same rules to the artifacts that
#     are actually installed, including the personal-path scan, so a report
#     that was hand-edited, truncated, or copied from an older tree fails even
#     if its producer is compliant.
# ---------------------------------------------------------------------------

# The provenance members of every committed report.  ``write_committed_report``
# overwrites all three, so a caller cannot record the identity it prefers.
REPORT_PROVENANCE_MEMBERS = ("command", "checkout_root", "git_head")

# The privacy block ``write_committed_report`` writes.  Its content is derived
# from the checks this module performed, never asserted by the writer.
REPORT_PRIVACY_MEMBER = "privacy"
PRIVACY_SANITIZER_NAME = "command_sanitize.write_committed_report"

# The tiers a registry entry may declare.  ``legacy-provenance`` exists so the
# audit can be complete over every committed report while a writer owned by
# another worker migrates; an entry naming it must also name its owner, and the
# audit says so in its output, so the state is visible instead of implied.
# No registered writer is on it in this tree: the two that were (``run.py`` and
# ``check_refusal_class_parity.py``) commit through ``write_committed_report``
# now.  The tier stays because a gate that silently lacked it would have to
# re-learn the difference the first time a writer cannot migrate.
SHARED_TIER = "shared-writer"
LEGACY_TIER = "legacy-provenance"
VALID_TIERS = (SHARED_TIER, LEGACY_TIER)

# ---------------------------------------------------------------------------
# The committed identity records this module produces.
#
# Two artifacts in ``evidence/`` are not harness measurements of a product:
# ``build-ids.json`` records the identity the Rust build stamps into its
# binaries, and ``battery-manifest.json`` binds one report set to one revision
# and one staged-artifact ledger.  Both are durable committed reports, so they
# owe the same provenance and privacy as a harness measurement, and the module
# that owns those rules is the module that writes them.
#
# ``build-ids.json`` is recomputed, never copied: the expected value is derived
# from the same package inputs and the same record framing that
# ``v4/rust/iprange-livedb/build.rs`` hashes, so a hand-edited digest is a
# regenerable artifact rather than a reviewed number.  ``battery-manifest.json``
# is authored by ``check_kind_coverage.py --emit-manifest``, which is a
# consumer-side gate and cannot own a writer; the manifest is therefore
# *promoted* here, so the report bytes stay the gate's and the identity members
# stay this module's.
# ---------------------------------------------------------------------------

# The module that owns the committed-report rules also owns the two identity
# records, so the audit judges its definitions rather than demanding it import
# the helpers it implements (see ``_writer_defences``).
_OWNER_MODULE = "command_sanitize.py"

BUILD_IDS_SCHEMA = "iprange-cli-build-ids-v1"
BUILD_IDS_ARTIFACT = "build-ids.json"
BATTERY_MANIFEST_ARTIFACT = "battery-manifest.json"
BUILD_IDS_PACKAGE = "iprange-livedb"
# The hosts whose build identity the record names.  They are spelled out
# because the point of the record is that a POSIX and a Windows checkout of one
# source state agree: a single digest for all four is the claim under test, not
# an assumption.
BUILD_IDS_HOSTS = ("darwin", "freebsd", "linux", "windows")
# The path-valued options of the two producer entry points below.  Every one is
# handed to ``require_paths_outside_profile`` and recorded in the artifact's
# ``privacy.checked_inputs``, so the audit can tell a screened option from one
# that quietly stopped being checked.
PRODUCER_CALLER_PATHS = ("--emit-build-ids", "--rust-tree", "--built-cli",
                         "--built-worker", "--commit-report",
                         "--commit-report-to")

# The framing ``build.rs`` hashes per input: a u64-le name length, the name, a
# u64-le content length, then the content.  ``build.rs`` splits the stored name
# on both separators and rejoins it with ``/`` so the host's own path spelling
# cannot change the digest; the pre-normalizer framing is kept here because the
# committed record documents what the absence of that split produced.
_BUILD_ID_RECORD_HOST_SPelling = {"posix": "/", "windows": chr(92)}


def _build_id_logical_name(components):
    """The name ``build.rs`` hashes for one input file.

    Empty and ``.`` components are dropped and the rest joined by ``/``, which
    is why one source state has one identity across build hosts.
    """
    return "/".join(part for part in components if part and part != ".")


def _build_id_host_name(components, separator):
    """The name a host hashed before the logical-name split existed."""
    return separator.join(part for part in components if part and part != ".")


def collect_build_id_inputs(package_dir):
    """The package inputs whose content determines ``IPRANGE_V4_BUILD_ID``.

    The order mirrors ``build.rs``: ``Cargo.toml`` first, then every ``.rs``
    file under ``src``, sorted by logical name.  A directory walk that sorted by
    raw path instead would disagree with the Rust build on a tree whose
    directory and file names sort apart.
    """
    manifest = os.path.join(package_dir, "Cargo.toml")
    if not os.path.isfile(manifest):
        raise SystemExit(f"{package_dir}: no Cargo.toml, so this is not a "
                         "Rust package root")
    inputs = [(["Cargo.toml"], manifest)]
    root = os.path.join(package_dir, "src")
    if not os.path.isdir(root):
        raise SystemExit(f"{package_dir}: no src/ directory to hash")
    found = []
    for directory, _dirs, files in os.walk(root):
        for name in files:
            if not name.endswith(".rs"):
                continue
            path = os.path.join(directory, name)
            components = os.path.relpath(path, package_dir).split(os.sep)
            found.append((_build_id_logical_name(components), path,
                          components))
    if not found:
        raise SystemExit(f"{root}: no .rs sources to hash")
    found.sort(key=lambda entry: entry[0])
    inputs.extend((components, path) for _name, path, components in found)
    return inputs


def compute_build_id(package_dir, host_spelling=None):
    """Recompute ``IPRANGE_V4_BUILD_ID`` for one package state.

    ``host_spelling`` selects the name framing: ``None`` hashes the logical
    name, which is the identity the built binaries carry; ``"posix"`` or
    ``"windows"`` hashes that host's own path spelling, which is what the
    pre-normalizer build hashed and the committed record quotes as evidence for
    why the split is needed.
    """
    digest = hashlib.sha256()
    for components, path in collect_build_id_inputs(package_dir):
        if host_spelling is None:
            name = _build_id_logical_name(components)
        else:
            name = _build_id_host_name(
                components, _BUILD_ID_RECORD_HOST_SPelling[host_spelling])
        try:
            with open(path, "rb") as stream:
                payload = stream.read()
        except OSError as exc:
            raise SystemExit(f"{path}: cannot hash ({exc})")
        name_bytes = name.encode("utf-8")
        digest.update(len(name_bytes).to_bytes(8, "little"))
        digest.update(name_bytes)
        digest.update(len(payload).to_bytes(8, "little"))
        digest.update(payload)
    return digest.hexdigest()


def build_ids_document(package_dir, built=None):
    """The committed build-identity record for one source state.

    ``built`` maps a role (``cli``/``worker``) to a built executable to check
    the recomputed digest against; a role that was not supplied is recorded as
    ``"not measured"`` rather than inheriting a previous run's claim, because a
    digest that was never compared against a binary only attests to a source
    tree, not to a delivered product.
    """
    expected = compute_build_id(package_dir)
    inputs = collect_build_id_inputs(package_dir)
    measured = {}
    for role in ("cli", "worker"):
        path = (built or {}).get(role)
        if not path:
            measured[role] = "not measured"
            continue
        try:
            with open(path, "rb") as stream:
                blob = stream.read()
        except OSError as exc:
            raise SystemExit(f"{path}: cannot read built product ({exc})")
        measured[role] = expected.encode("ascii") in blob
    return {
        "schema": BUILD_IDS_SCHEMA,
        "package": BUILD_IDS_PACKAGE,
        "purpose": ("Expected IPRANGE_V4_BUILD_ID for one source state, per "
                    "build host. The Rust CLI and the iprange-v4-worker "
                    "compare this identity during the worker handshake, so a "
                    "host-dependent value cannot attest that two binaries came "
                    "from one tree. Each native run checks its own platform "
                    "entry against the binary it built."),
        "algorithm": {
            "hash": "sha-256 over one record per input",
            "record": "u64le(name-bytes-length) name-bytes "
                      "u64le(content-length) content",
            "input_order": ("Cargo.toml first, then every .rs file under src, "
                           "sorted by logical name"),
            "logical_name": ("the file path split on both the POSIX and the "
                             "Windows separator, empty and '.' components "
                             "dropped, remaining components joined by '/'"),
            "host_invariance": ("the logical name is why one source state has "
                                "one identity: hashing the build host's own "
                                "path string instead makes the value differ "
                                "between a POSIX and a Windows checkout"),
        },
        "inputs": {"manifest": "Cargo.toml",
                   "source_files": len(inputs) - 1},
        "expected_build_id": {host: expected for host in BUILD_IDS_HOSTS},
        "measured_without_the_normalizer": {
            "note": ("the two digests below are what the pre-normalizer "
                     "build.rs produces from this same source state: equal on "
                     "a POSIX host and different for a Windows host, which is "
                     "the defect this record guards against"),
            "posix_host": compute_build_id(package_dir, "posix"),
            "windows_host": compute_build_id(package_dir, "windows"),
        },
        "verified_against_built_products": {
            "method": ("the recomputed digest is searched for verbatim in the "
                       "bytes of the named built executables"),
            "cli_contains_expected": measured["cli"],
            "worker_contains_expected": measured["worker"],
        },
        "source_state": {
            "note": ("the value is a pure function of iprange-livedb's "
                     "Cargo.toml and src/**.rs content; any change under that "
                     "package regenerates it. Regenerate this file whenever "
                     "the package changes and never hand-edit a digest."),
            "revision": recorded_git_identity(),
        },
        "regenerate_with": (
            "python3 v4/cli/command_sanitize.py --emit-build-ids "
            "v4/cli/evidence/" + BUILD_IDS_ARTIFACT
            + " --built-cli <release iprange> --built-worker <release "
            "iprange-v4-worker>; the digest is recomputed from "
            "v4/rust/iprange-livedb/Cargo.toml and every .rs file under "
            "v4/rust/iprange-livedb/src"),
    }


def emit_build_ids(dest, rust_tree=None, built=None, argv=None):
    """Recompute and commit the build-identity record."""
    package_dir = os.path.join(rust_tree or os.path.join(checkout_root(), "v4",
                                                         "rust"),
                              BUILD_IDS_PACKAGE)
    caller_paths = (("--emit-build-ids", dest),
                    ("--rust-tree", rust_tree),
                    ("--built-cli", (built or {}).get("cli")),
                    ("--built-worker", (built or {}).get("worker")),
                    ("--commit-report", None),
                    ("--commit-report-to", None))
    require_paths_outside_profile(caller_paths)
    document = build_ids_document(package_dir, built)
    written = write_committed_report(
        dest, document, argv=argv, caller_paths=caller_paths, indent=1)
    return written


def commit_report(source, dest, argv=None):
    """Promote a staged report into the evidence directory through the writer.

    The report bytes are the producing gate's; only the identity members are
    added here, by ``write_committed_report``, which is also what refuses a
    personal path or an unsatisfiable provenance before anything is written.
    """
    if not source or not os.path.isfile(source):
        raise SystemExit(f"--commit-report {source}: no such report to promote")
    if same_path(source, dest):
        raise SystemExit("--commit-report and --commit-report-to name the "
                         "same file; promoting a report onto itself would "
                         "hide which writer produced it")
    entry = registry_entry_for(dest)
    if entry is not None and entry is not COMMITTED_REPORT_WRITERS.get(
            _OWNER_MODULE):
        raise SystemExit(
            f"--commit-report-to {dest}: {os.path.basename(dest)} belongs to "
            "another registered writer; promoting it from here would record "
            "this promotion as the measurement that produced the report")
    try:
        with open(source, encoding="utf-8") as stream:
            document = json.load(stream)
    except (OSError, ValueError) as exc:
        raise SystemExit(f"--commit-report {source}: unreadable ({exc})")
    caller_paths = (("--emit-build-ids", None), ("--rust-tree", None),
                    ("--built-cli", None), ("--built-worker", None),
                    ("--commit-report", source),
                    ("--commit-report-to", dest))
    require_paths_outside_profile(caller_paths)
    return write_committed_report(dest, document, argv=argv,
                                 caller_paths=caller_paths, indent=1)


# One entry per module that produces a committed report.  Each entry declares:
#
#   ``artifacts``  the committed files in ``evidence/`` it owns; the artifact
#                  audit enumerates the directory against this table, so a
#                  committed report from an unregistered producer fails exactly
#                  like a registered writer that bypasses the helpers;
#   ``screened``   the command-line options that name a path and must therefore
#                  be refused when they live under the operator's profile.  The
#                  writer records what it screened in ``privacy.checked_inputs``
#                  and the audit requires this set, so dropping the screening of
#                  one option is a gate failure rather than a silent leak; and
#   ``tier``       ``shared-writer`` (must commit through
#                  ``write_committed_report``, no direct ``json.dump``) or
#                  ``legacy-provenance`` (has not migrated yet: the artifact
#                  rules still apply, the call-site rules wait for its owner).
#                  A tier is a named, auditable state, not a skip: the audit
#                  reports which tier each writer is in and refuses an entry
#                  that omits the field or its owning worker.
COMMITTED_REPORT_WRITERS = {
    "run.py": {
        "artifacts": ("matrix-go.json", "matrix-rust.json",
                      "matrix-go_to_rust.json", "matrix-rust_to_go.json"),
        "screened": ("--go", "--rust", "--fixture-tool", "--work-dir",
                     "--cases", "--json-report"),
        "tier": SHARED_TIER,
        "owner": "lead",
    },
    "crash_harness.py": {
        "artifacts": ("crash.json", "crash-negative.json"),
        "screened": ("--producer", "--consumer", "--fixture-tool",
                     "--work-dir", "--json-report"),
        "tier": SHARED_TIER,
        "owner": "gate-c",
    },
    "resource_harness.py": {
        "artifacts": ("resource.json",),
        "screened": ("--binaries", "--work-dir", "--json-report"),
        "tier": SHARED_TIER,
        "owner": "gate-c",
    },
    "throughput_harness.py": {
        "artifacts": ("throughput.json",),
        "screened": ("--go", "--rust", "--work", "--json-report"),
        "tier": SHARED_TIER,
        "owner": "gate-c",
    },
    "coverage_harness.py": {
        "artifacts": ("coverage-go.json",),
        "screened": ("--go-module", "--work", "--rust", "--fixture-tool",
                     "--v4-tree", "--revision", "--json-report"),
        "tier": SHARED_TIER,
        "owner": "gate-c",
    },
    "check_golden.py": {
        "artifacts": ("golden.json",),
        "screened": ("--tree", "--json-report"),
        "tier": SHARED_TIER,
        "owner": "gate-c",
    },
    "check_fifo_surface.py": {
        "artifacts": ("fifo-surface.json",),
        "screened": ("--go", "--rust", "--fixture", "--work", "--json-report"),
        "tier": SHARED_TIER,
        "owner": "gate-c",
    },
    "check_refusal_class_parity.py": {
        "artifacts": ("refusal-class-parity.json",),
        "screened": ("--go", "--rust", "--fixture", "--work", "--json-report"),
        "tier": SHARED_TIER,
        "owner": "lead",
    },
    # The two remaining committed artifacts are not harness measurements of a
    # product: they are identity records, and their producer is this module
    # because this module is what knows how a committed report must identify
    # the tree it names.  ``build-ids.json`` is recomputed from the Rust
    # package's own content by ``--emit-build-ids``; ``battery-manifest.json``
    # is authored by ``check_kind_coverage.py --emit-manifest`` (a gate that is
    # outside this write set and cannot import the writer) and reaches the
    # evidence directory through ``--commit-report``, which adds the identity
    # this module owns.  A promoted artifact's recorded ``command`` therefore
    # names the promoting invocation, so a promotion stays distinguishable from
    # a writer committing its own measurement.
    "command_sanitize.py": {
        "artifacts": (BUILD_IDS_ARTIFACT, BATTERY_MANIFEST_ARTIFACT),
        "screened": PRODUCER_CALLER_PATHS,
        "tier": SHARED_TIER,
        "owner": "lead",
    },
    "sensitivity_gate.py": {
        "artifacts": ("sensitivity.json",),
        "screened": ("--json-report",),
        "tier": SHARED_TIER,
        "owner": "gate-c",
    },
    "windows_guard_harness.py": {
        "artifacts": ("guard-posix.json", "windows-guard.json"),
        "screened": ("--rust", "--go", "--fixture", "--work", "--out",
                     "--provenance"),
        "tier": SHARED_TIER,
        "owner": "gate-c",
    },
    "windows_housekeeping_harness.py": {
        "artifacts": ("windows-housekeeping.json",),
        "screened": ("--binaries", "--work-dir", "--json-report",
                     "--provenance"),
        "tier": SHARED_TIER,
        "owner": "gate-c",
    },
}

# Files in the evidence directory that are not harness measurements and so
# carry no provenance: the known-defects ledger is hand-maintained, and the
# kind gate owns its shape and enforces it in both directions.
COMMITTED_LEDGER_FILES = frozenset({"known-defects.json"})


def shared_tier_writers():
    """Every registered writer that owes the full shared-writer discipline.

    Derived from the registry, so a writer added to
    ``COMMITTED_REPORT_WRITERS`` is covered by the joint audit without anyone
    remembering to extend a list somewhere else.
    """
    return sorted(name for name, entry in COMMITTED_REPORT_WRITERS.items()
                  if entry.get("tier") == SHARED_TIER)


def report_provenance(argv=None):
    """The provenance members every committed report carries.

    ``command`` is the sanitized invocation (never the raw argv) and the
    revision/checkout pair binds the measurement to the tree it measured.
    """
    return {
        "command": sanitized_command(argv),
        "checkout_root": recorded_checkout_root(),
        "git_head": recorded_git_identity(),
    }


def require_paths_outside_profile(labelled_paths):
    """Refuse every caller-supplied path that lives under the profile.

    ``labelled_paths`` is a sequence of ``(option_label, value)``; a None or
    empty value means the option was not supplied and is skipped.  This is the
    input-side net: a report can only carry a personal path if some input
    named it, so refusing the input keeps the record clean instead of relying
    on the write-time scan to notice afterwards.
    """
    for label, value in labelled_paths:
        if not value:
            continue
        # An in-checkout value is not the operator's personal area for this
        # purpose: sanitized_path_value renders it checkout-relative before it
        # can reach an artifact, so the committed record cannot carry the
        # personal prefix.  Only a staged binary or work directory is required
        # to leave the profile, which is what the harnesses documented.
        if _inside_checkout(value):
            continue
        if under_profile(value):
            raise SystemExit(
                f"{label} {value} lives under the operator's profile; stage "
                "all inputs under the authorized scratch area so committed "
                "evidence cannot carry personal paths")


def committed_report_privacy(caller_paths=()):
    """The privacy block derived from the checks actually performed."""
    return {
        "sanitizer": PRIVACY_SANITIZER_NAME,
        # The scan found nothing.  A writer cannot set this member:
        # ``committed_report_problems`` re-runs the same scan over the finished
        # report, so the claim recorded here is re-derivable by any reader.
        "personal_path_in_report": None,
        # Which options the writer handed over for screening.  The audit
        # requires the registry's ``screened`` set, so a writer cannot quietly
        # stop checking the option that names a binary.  Every label the
        # writer passes is recorded, supplied or not: the record describes the
        # writer's code, not the operator's command line, and an optional
        # option left out of the tuple is exactly the omission the audit
        # looks for.
        "checked_inputs": sorted({str(label) for label, _value in caller_paths}),
    }


def committed_report_problems(report, where="report", require_privacy=False,
                              screened=None):
    """Problems in one committed report's provenance and privacy.

    The writers call this before serializing and the verify modes call it on an
    artifact already on disk, so the same rules decide both directions and a
    report that would be rejected after installation can never be installed.
    """
    problems = []
    if not isinstance(report, dict):
        return [f"{where}: {type(report).__name__}, not an object"]
    for member in REPORT_PROVENANCE_MEMBERS:
        if member not in report:
            problems.append(f"{where}: missing provenance member {member!r}")
    command = report.get("command")
    if isinstance(command, list):
        # Idempotence is how a reader proves the recorded command really went
        # through the sanitizer: re-sanitizing must not change it.  A raw argv
        # -- or any spelling the sanitizer would have rewritten -- fails here
        # even when it happens to carry no personal path.
        re_sanitized = sanitized_command(command)
        if re_sanitized != command:
            problems.append(
                f"{where}: command was not produced by sanitized_command() "
                f"({command!r} re-sanitizes to {re_sanitized!r})")
    elif command is not None:
        problems.append(f"{where}: command is not a list: {command!r}")
    git_head = report.get("git_head")
    if git_head is not None and not (
            isinstance(git_head, str) and len(git_head) in (40, 64)
            and all(c in "0123456789abcdef" for c in git_head.lower())):
        problems.append(f"{where}: git_head {git_head!r} is not an object id")
    if report.get("checkout_root"):
        problems.append(
            f"{where}: checkout_root {report.get('checkout_root')!r} must be "
            "null or absent; a recorded directory can only leak the "
            "operator's spelling")
    privacy = report.get(REPORT_PRIVACY_MEMBER)
    if privacy is None:
        if require_privacy:
            problems.append(
                f"{where}: missing the {REPORT_PRIVACY_MEMBER} block written "
                f"by {PRIVACY_SANITIZER_NAME}")
    elif not isinstance(privacy, dict):
        problems.append(f"{where}: {REPORT_PRIVACY_MEMBER} is not an object")
    else:
        if privacy.get("personal_path_in_report") is not None:
            problems.append(
                f"{where}: privacy scan reports "
                f"{privacy.get('personal_path_in_report')!r}")
        if require_privacy:
            if privacy.get("sanitizer") != PRIVACY_SANITIZER_NAME:
                problems.append(
                    f"{where}: privacy block was not written by "
                    f"{PRIVACY_SANITIZER_NAME}: "
                    f"{privacy.get('sanitizer')!r}")
            checked = privacy.get("checked_inputs")
            if screened and isinstance(checked, list):
                absent = sorted(set(screened) - set(checked))
                if absent:
                    problems.append(
                        f"{where}: the writer stopped screening "
                        f"{', '.join(absent)}; that option names a path a "
                        f"personal directory can arrive through")
    personal = personal_path_in_report(report)
    if personal is not None:
        problems.append(f"{where}: carries a personal path: {personal!r}")
    return problems


def committed_report_text(report, indent=2):
    """The canonical serialization of one committed report.

    Sorted keys, one indent level, trailing newline: byte-stable, so a rotated
    artifact diffs against its predecessor only where the measurement changed.
    """
    return json.dumps(report, indent=indent, sort_keys=True) + "\n"


def write_scratch_json(path, value, indent=None):
    # Serializes a NON-committed scratch file (a self-test fixture).
    #
    # Shared-tier writers are forbidden from calling json.dump directly,
    # because that is how an unsanitized report reaches the evidence
    # directory.  Self-tests still need to write doctored fixtures, and
    # forbidding that outright would push them to hand-rolled open/write
    # calls -- the same bypass with a different spelling.  So the outlet
    # is this named helper, and it refuses exactly one destination: the
    # committed evidence directory.  Scratch bytes can go anywhere; a
    # committed report comes only from write_committed_report().
    absolute = os.path.abspath(path)
    evidence = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                            "evidence") + os.sep
    if absolute.startswith(evidence):
        raise SystemExit(
            f"{absolute}: a committed evidence artifact must be written by "
            "write_committed_report(), not write_scratch_json()")
    text = json.dumps(value, indent=indent, sort_keys=True)
    with open(absolute, "w", encoding="utf-8") as stream:
        stream.write(text)
    return text


def registry_entry_for(path):
    """The registry entry that claims one artifact basename, or None.

    The governing entry is derived from the *artifact name*, never from a
    writer's self-declaration: a writer cannot escape the screened-option
    requirement by naming itself something else, and an artifact name that no
    writer claims is refused by ``audit_committed_reports``.
    """
    base = os.path.basename(path or "")
    for entry in COMMITTED_REPORT_WRITERS.values():
        if base in (entry.get("artifacts") or ()):
            return entry
    return None


def write_committed_report(path, report, argv=None, caller_paths=(),
                           indent=2, newline=False):
    """Serialize one committed report through the shared provenance owner.

    The caller hands over its report and the paths its operator supplied; this
    function owns the identity fields and refuses the write -- leaving no file
    created or replaced -- when they cannot be satisfied:

    1. every ``caller_paths`` entry is refused when it lives under the
       operator's profile (the input-side net);
    2. ``command``, ``checkout_root`` and ``git_head`` are set from this
       module, overwriting anything the caller put there, so a record cannot
       carry a raw argv or a hand-written revision;
    3. the ``privacy`` block records the checks performed here; and
    4. the finished report is scanned for a personal path, so a path that
       arrived through a channel this module did not see still cannot reach
       the artifact.

    Returns the text that was written, so a caller that prints or re-reads the
    artifact uses the same bytes it stored.
    """
    require_paths_outside_profile(caller_paths)
    for member in REPORT_PROVENANCE_MEMBERS:
        report.pop(member, None)
    report.update(report_provenance(argv))
    report[REPORT_PRIVACY_MEMBER] = committed_report_privacy(caller_paths)
    entry = registry_entry_for(path)
    problems = committed_report_problems(
        report, require_privacy=True,
        screened=(entry or {}).get("screened"))
    if problems:
        raise SystemExit(
            f"refusing to write committed report {path}: "
            + "; ".join(problems))
    text = committed_report_text(report, indent=indent)
    parent = os.path.dirname(os.path.abspath(path))
    # Battery runs place reports in a staging directory they created for the
    # occasion; a missing parent here is the operator mistyping a path, so it
    # is created rather than raising FileNotFoundError from inside the write.
    # The validation above has already passed, so nothing unscreened can be
    # committed as a side effect of creating the directory.
    os.makedirs(parent, exist_ok=True)
    with open(path, "w", encoding="utf-8",
              newline="" if newline else None) as stream:
        stream.write(text)
    return text


def _committed_evidence_names(evidence_dir):
    """Basenames the repository tracks under the evidence directory.

    The audit's completeness rule is about *committed* reports: a producer that
    installs a JSON file into the tree must be registered.  A file that exists
    only in this shared working tree is another agent's in-flight work, and
    refusing every gate because of it would tie unrelated changes together; the
    rule reaches it unchanged as soon as it is committed.  When git cannot
    answer, every present report is judged instead -- the audit fails toward
    auditing more, never toward skipping.
    """
    try:
        completed = subprocess.run(
            ["git", "-C", os.path.dirname(os.path.dirname(
                os.path.abspath(evidence_dir))),
             "ls-files", "--", evidence_dir],
            capture_output=True, text=True, timeout=10, check=False)
    except (OSError, ValueError, subprocess.SubprocessError):
        return None
    if completed.returncode != 0:
        return None
    return {os.path.basename(line)
            for line in (completed.stdout or "").splitlines() if line.strip()}


_SHARED_DEFENCES = frozenset(
    {"sanitized_command", "under_profile", "personal_path_in_report",
     "require_paths_outside_profile", "write_committed_report",
     "run_shared_self_test"})


def _writer_imports(tree):
    """Names imported from ``command_sanitize`` by one parsed module."""
    imported = set()
    for node in ast.walk(tree):
        if (isinstance(node, ast.ImportFrom)
                and node.module == "command_sanitize"):
            imported.update(alias.name for alias in node.names)
    return imported


def _writer_defences(tree, module_name):
    """Defence names one writer applies: imported, or defined where it owns them.

    ``command_sanitize.py`` is itself a registered writer, because it produces
    the two committed identity records.  Requiring it to *import* the defences
    it implements would ask for a self-import, and accepting a hand-written
    alias would be exactly the bypass the AST audit exists to refuse -- so the
    rule is scoped to the one module that owns these names, and only ever
    counts a top-level definition whose name is a shared defence.  Every other
    writer is still judged on imports alone.
    """
    names = _writer_imports(tree)
    if module_name == os.path.basename(os.path.abspath(__file__)):
        names |= {node.name for node in tree.body
                  if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef))
                  and node.name in _SHARED_DEFENCES}
    return names


def _report_serialization_bypasses(tree):
    """Lines where one module serializes a report outside the sanctioned writer.

    ``write_committed_report`` is the only permitted commit path because it
    owns the provenance and privacy members: it discards what the caller put
    there and writes the identity this module measured.  A direct ``json.dump``
    is therefore the bypass class -- the record can carry a raw argv, a
    hand-written revision, or no privacy scan at all, and nothing downstream
    can tell.  Pinning the call rather than the spelling of a ``"command"``
    record is what makes the pin non-bypassable: the previous lint matched the
    recording expression's text, so binding the value through an intermediate
    (``_raw_cmd = [str(a) for a in sys.argv]``) kept it green while the live
    artifact went unsanitized.

    The rule is deliberately about the serializer call, not about the bytes:
    a harness also writes JSON to a product's stdin, and a rule that looked
    for any ``json.dumps`` reaching a ``write`` would flag that traffic too
    and be waived within a week.  The spelling-independent net is
    ``audit_committed_reports``, which judges the installed artifact whatever
    produced it.
    """
    bad = []
    for node in ast.walk(tree):
        if not isinstance(node, ast.Call):
            continue
        func = node.func
        if (isinstance(func, ast.Attribute) and func.attr == "dump"
                and isinstance(func.value, ast.Name)
                and func.value.id == "json"):
            bad.append(getattr(node, "lineno", 0))
            continue
    return bad


def _commit_call_problems(tree):
    """How one module reaches the sanctioned writer, as problem strings.

    Two bypass classes survive an import-only check:

    * importing ``write_committed_report`` and never calling it, which leaves
      the module free to serialize its report some other way; and
    * calling it without ``caller_paths=``, which makes the derived
      ``privacy.checked_inputs`` record an empty screening list even though the
      writer took path-valued options.  The artifact audit compares that list
      against the registry's screened set, so naming the omission here stops
      the regression at the source instead of after installation.
    """
    calls = [node for node in ast.walk(tree)
             if isinstance(node, ast.Call)
             and ((isinstance(node.func, ast.Attribute)
                   and node.func.attr == "write_committed_report")
                  or (isinstance(node.func, ast.Name)
                      and node.func.id == "write_committed_report"))]
    if not calls:
        return ["imports the shared defences but never calls "
                "write_committed_report()"]
    bad = [getattr(node, "lineno", 0) for node in calls
           if "caller_paths" not in {keyword.arg for keyword in node.keywords
                                     if keyword.arg is not None}
           and len(node.args) < 4]
    if bad:
        return ["calls write_committed_report() without caller_paths= at "
                "line(s) " + ", ".join(str(line) for line in bad) + "; the "
                "privacy block records exactly the options handed over, so an "
                "omitted argument stops screening an input"]
    # A writer can satisfy every import- and call-shape rule and still never
    # execute the shared controls by dropping the call from its --self-test.
    # Requiring the call is what turns the per-file audit into a set-wide one:
    # each writer's self-test then runs the joint audit over all of them.
    shared_calls = [node for node in ast.walk(tree)
                    if isinstance(node, ast.Call)
                    and ((isinstance(node.func, ast.Attribute)
                          and node.func.attr == "run_shared_self_test")
                         or (isinstance(node.func, ast.Name)
                             and node.func.id == "run_shared_self_test"))]
    if not shared_calls:
        return ["never calls run_shared_self_test(); the shared provenance "
                "controls and the joint audit of the whole shared-tier set "
                "would never run for this writer"]
    return []


class _temporary_registry_entry:
    """Register one entry for the duration of a control, then restore.

    The audit reads the live table, so a control that needs a writer on a tier
    the tree no longer uses has to say so explicitly and put the table back --
    including when the control raises.  Leaving the entry behind would make an
    invented writer look like a real committed-report producer to every other
    audit in the process.
    """

    def __init__(self, module_name, entry):
        self.module_name = module_name
        self.entry = entry
        self.previous = COMMITTED_REPORT_WRITERS.get(module_name)
        self.had = module_name in COMMITTED_REPORT_WRITERS

    def __enter__(self):
        COMMITTED_REPORT_WRITERS[self.module_name] = self.entry
        return self

    def __exit__(self, *exc_info):
        if self.had:
            COMMITTED_REPORT_WRITERS[self.module_name] = self.previous
        else:
            COMMITTED_REPORT_WRITERS.pop(self.module_name, None)
        return False


def _registry_problems():
    """Problems in the registry table itself.

    A registry entry is the contract the audit enforces, so a malformed entry
    is not a weaker check but a broken one: the tier must be named, a writer
    left on ``legacy-provenance`` must name its owner, and the screened option
    set must be non-empty (a writer with no screened options is asserting that
    none of its inputs names a path, which is checked against its artifacts).
    """
    problems = []
    for module_name, entry in sorted(COMMITTED_REPORT_WRITERS.items()):
        tier = entry.get("tier")
        if tier not in VALID_TIERS:
            problems.append(f"{module_name}: tier {tier!r} is not one of "
                            f"{', '.join(VALID_TIERS)}")
        if tier == LEGACY_TIER and not entry.get("owner"):
            problems.append(
                f"{module_name}: is on {LEGACY_TIER} without naming the "
                "owner who must migrate it")
        if not entry.get("screened"):
            problems.append(
                f"{module_name}: declares no screened options; every "
                "committed report writer takes at least one path-valued "
                "input")
        if not entry.get("artifacts"):
            problems.append(
                f"{module_name}: declares no committed artifacts")
    return problems


def audit_report_writers(cli_dir=None, writers=None, artifacts=True):
    """Source-level audit of the registered committed-report writers.

    Every rule is evaluated and reported; nothing is skipped because a check
    was inconvenient:

    * the registry itself must be well formed (``_registry_problems``);
    * a registered writer must exist, be readable, and parse;
    * it must import the shared defences it claims to apply;
    * a writer on the ``shared-writer`` tier must import
      ``write_committed_report`` and contain no direct ``json.dump``, which is
      the guarantee the earlier regex-based source pin could not make;
    * it must refuse caller-supplied paths under the operator profile
    * (``under_profile`` or ``require_paths_outside_profile``); and
    * when ``artifacts`` is set, each committed artifact it owns must carry
      the provenance members, the derived privacy block for a shared-writer
      module, the screened-option record, and no personal path.

    ``writers`` restricts the audit to a subset (a harness auditing itself).
    """
    cli_dir = os.path.dirname(os.path.abspath(__file__)) if cli_dir is None \
        else cli_dir
    evidence_dir = os.path.join(cli_dir, "evidence")
    problems = _registry_problems() if writers is None else []
    if writers is None:
        registry = {name: entry
                    for name, entry in COMMITTED_REPORT_WRITERS.items()}
    else:
        registry = {name: COMMITTED_REPORT_WRITERS[name] for name in writers
                    if name in COMMITTED_REPORT_WRITERS}
        problems.extend(
            f"{name}: asked to audit itself but is not registered in "
            f"COMMITTED_REPORT_WRITERS"
            for name in writers if name not in COMMITTED_REPORT_WRITERS)

    claimed = set()
    for module_name, entry in sorted(registry.items()):
        path = os.path.join(cli_dir, module_name)
        if not os.path.isfile(path):
            problems.append(f"{module_name}: registered writer is missing")
            continue
        try:
            with open(path, encoding="utf-8") as stream:
                source = stream.read()
        except OSError as exc:
            problems.append(f"{module_name}: unreadable ({exc})")
            continue
        try:
            tree = ast.parse(source, filename=path)
        except SyntaxError as exc:
            problems.append(f"{module_name}: unparsable ({exc})")
            continue

        imported = _writer_defences(tree, module_name)
        tier = entry.get("tier")
        if tier == SHARED_TIER:
            # Committing through the shared writer *is* the defence: it owns
            # the provenance fields, the derived privacy block, and the
            # personal-path scan.  Requiring the three primitives alongside
            # it would only prove the module can spell an import name, and
            # would push migrated writers into carrying dead imports.
            required = ["write_committed_report"]
        else:
            # A legacy writer still builds its own report dict, so every
            # defence has to be visible at its call site.
            required = ["sanitized_command", "under_profile",
                        "personal_path_in_report"]
        missing = [name for name in required if name not in imported]
        if missing:
            problems.append(
                f"{module_name}: never applies the shared defence(s) "
                f"{', '.join(missing)}() from command_sanitize")
        if "under_profile" not in imported \
                and "require_paths_outside_profile" not in imported:
            problems.append(
                f"{module_name}: never refuses a caller-supplied path under "
                "the operator profile")
        if tier == SHARED_TIER:
            bypasses = _report_serialization_bypasses(tree)
            if bypasses:
                problems.append(
                    f"{module_name}: serializes a report outside "
                    f"write_committed_report() at line(s) "
                    f"{', '.join(str(line) for line in bypasses)}")
            problems.extend(f"{module_name}: {problem}"
                            for problem in _commit_call_problems(tree))

        for artifact in entry.get("artifacts") or ():
            claimed.add(artifact)
            if not artifacts:
                continue
            artifact_path = os.path.join(evidence_dir, artifact)
            if not os.path.isfile(artifact_path):
                # An artifact the battery has not rotated yet is not a
                # provenance defect; audit_committed_reports owns the missing
                # -artifact rule once the writer is registered as producing it.
                continue
            try:
                with open(artifact_path, encoding="utf-8") as stream:
                    report = json.load(stream)
            except (OSError, ValueError) as exc:
                problems.append(f"{artifact}: unreadable ({exc})")
                continue
            problems.extend(committed_report_problems(
                report, where=artifact,
                require_privacy=tier == SHARED_TIER,
                screened=entry.get("screened")))

    if writers is None and os.path.isdir(evidence_dir):
        present = {name for name in os.listdir(evidence_dir)
                   if name.endswith(".json")}
        names = _committed_evidence_names(evidence_dir)
        for name in sorted(present if names is None else names & present):
            if name in COMMITTED_LEDGER_FILES:
                continue
            if name not in claimed:
                problems.append(
                    f"evidence/{name}: committed report from an unregistered "
                    f"writer; add it to COMMITTED_REPORT_WRITERS and route it "
                    f"through write_committed_report()")
    return problems


def audit_committed_reports(cli_dir=None):
    """Artifact-side audit of every committed report in the evidence directory.

    The registry audit proves the *writers* apply the defences; this proves the
    *installed artifacts* carry them.  Both directions matter because they fail
    differently: a writer that skips the helpers still produces a plausible
    artifact until the next rotation, and an artifact that was hand-edited,
    truncated, or copied from an older tree can otherwise sit in the repository
    unnoticed.  Rules, all fail-closed:

    * every committed ``*.json`` must be claimed by a registered writer
      (except the hand-maintained ledger in ``COMMITTED_LEDGER_FILES``);
    * every claimed artifact must carry the provenance members, re-sanitize to
      itself, record the screening of its writer's path-valued options, and
      hold no personal path;
    * an artifact of a ``shared-writer`` module must also carry the derived
      privacy block, which only ``write_committed_report`` can produce; and
    * every artifact a registered writer owns must be present: a report that
      vanished from the evidence directory is not acceptance evidence.
    """
    cli_dir = os.path.dirname(os.path.abspath(__file__)) if cli_dir is None \
        else cli_dir
    evidence_dir = os.path.join(cli_dir, "evidence")
    problems = _registry_problems()
    if not os.path.isdir(evidence_dir):
        return problems + [f"{evidence_dir}: no evidence directory to audit"]

    claimed = {}
    for module_name, entry in sorted(COMMITTED_REPORT_WRITERS.items()):
        for artifact in entry.get("artifacts") or ():
            claimed[artifact] = module_name

    present = {name for name in os.listdir(evidence_dir)
               if name.endswith(".json")}
    tracked = _committed_evidence_names(evidence_dir)
    for name in sorted(present if tracked is None else tracked & present):
        if name in COMMITTED_LEDGER_FILES:
            continue
        producer = claimed.get(name)
        if producer is None:
            problems.append(
                f"evidence/{name}: committed report from an unregistered "
                f"writer; add it to COMMITTED_REPORT_WRITERS and route it "
                f"through write_committed_report()")
            continue
        entry = COMMITTED_REPORT_WRITERS[producer]
        try:
            with open(os.path.join(evidence_dir, name),
                      encoding="utf-8") as stream:
                report = json.load(stream)
        except (OSError, ValueError) as exc:
            problems.append(f"evidence/{name}: unreadable ({exc})")
            continue
        problems.extend(committed_report_problems(
            report, where=f"evidence/{name}",
            require_privacy=entry.get("tier") == SHARED_TIER,
            screened=entry.get("screened")))

    for name in sorted(claimed):
        if not os.path.isfile(os.path.join(evidence_dir, name)):
            problems.append(
                f"evidence/{name}: registered artifact of "
                f"{claimed[name]} is absent from the evidence directory")
    return problems


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
        norm = _normcase(os.path.normpath(os.path.realpath(path)))
    except OSError:
        return False
    checkout_norm = _normcase(os.path.normpath(
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


def _provenance_self_test():
    """Behavioural controls for the shared committed-report writer.

    Each control drives the real ``write_committed_report`` path in a scratch
    directory, so the guarantee is measured rather than asserted.  The count
    of executed controls is pinned: a control that stops running (a dropped
    case, an exception swallowed by a changed guard) fails the self-test with
    the same authority as a control that fails, which is the defect class the
    wave-19.24 review found across the harness self-tests.
    """
    import shutil

    checks = 0
    root = owned_temp_dir("qual-provenance-selftest-")
    profile = profile_path()
    try:
        def expect(label, condition, detail=""):
            nonlocal checks
            checks += 1
            if not condition:
                raise AssertionError(f"{label}: {detail}")

        def write(target, report, caller_paths=(), argv=None):
            return write_committed_report(
                target, report, argv=argv, caller_paths=caller_paths,
                indent=1)

        # 1: a compliant report is written and carries the identity fields.
        good = os.path.join(root, "good.json")
        report = {"schema": "iprange-cli-selftest-v1", "result": "PASS"}
        text = write(good, report, argv=["v4/cli/selftest.py", "--go",
                                         "/tmp/x/iprange"])
        expect("compliant report is written", os.path.isfile(good))
        expect("written text is the stored bytes",
               open(good, encoding="utf-8").read() == text)
        stored = json.loads(text)
        expect("provenance members are present",
               all(member in stored for member in REPORT_PROVENANCE_MEMBERS),
               sorted(stored))
        expect("privacy block is derived",
               stored[REPORT_PRIVACY_MEMBER]["sanitizer"]
               == PRIVACY_SANITIZER_NAME, str(stored.get("privacy")))
        expect("recorded command is the sanitized invocation",
               stored["command"] == ["v4/cli/selftest.py", "--go",
                                     "/tmp/x/iprange"], str(stored["command"]))
        expect("no personal path survives the write",
               personal_path_in_report(stored) is None,
               str(personal_path_in_report(stored)))

        # 2: a caller cannot record the identity it prefers: the sanitizer's
        # values overwrite a hand-written revision, a raw argv, and a
        # pre-fabricated privacy block.
        forged = os.path.join(root, "forged.json")
        write(forged, {"schema": "s",
                       "command": ["/somewhere/iprange", profile or "/dev/null"],
                       "git_head": "0" * 40,
                       "checkout_root": "/home/operator/checkout",
                       "privacy": {"sanitizer": "hand-written",
                                   "personal_path_in_report": None}},
             argv=["v4/cli/selftest.py"])
        stored = json.load(open(forged, encoding="utf-8"))
        expect("a caller-supplied command is replaced by the sanitizer",
               stored["command"] == ["v4/cli/selftest.py"],
               str(stored["command"]))
        expect("a caller-supplied privacy block is replaced",
               stored[REPORT_PRIVACY_MEMBER]["sanitizer"]
               == PRIVACY_SANITIZER_NAME, str(stored["privacy"]))
        expect("a caller-supplied checkout_root cannot survive",
               not stored["checkout_root"], str(stored["checkout_root"]))

        # 3: a caller-supplied path under the profile is refused and leaves no
        # artifact behind.
        refused = os.path.join(root, "refused.json")
        if profile:
            try:
                write(refused, {"schema": "s"},
                      caller_paths=[("--go", os.path.join(profile, "x"))],
                      argv=["v4/cli/selftest.py"])
                rejected = False
            except SystemExit:
                rejected = True
            expect("a profile-rooted input is refused", rejected)
            expect("a refused write leaves no artifact",
                   not os.path.exists(refused))

        # 4: a personal path that arrives through a channel the caller did not
        # declare still cannot reach the artifact.
        leaked = os.path.join(root, "leaked.json")
        if profile:
            try:
                write(leaked, {"schema": "s",
                               "note": f"ran on {profile}/stage/bin"},
                      argv=["v4/cli/selftest.py"])
                scanned = False
            except SystemExit:
                scanned = True
            expect("an undeclared personal path is caught by the scan", scanned)
            expect("the scan refusal left no artifact",
                   not os.path.exists(leaked))

        # 5: the audit is structural, not a spelling match.  A writer that
        # imports every shared helper -- including the sanctioned writer -- and
        # then commits its own JSON is the class the previous regex pin could
        # not see (binding the recorded value through an intermediate kept the
        # lint green while the artifact went unsanitized).  The mutant takes
        # the name of a shared-tier writer, because that tier is what carries
        # the commit-path rule.
        mutant_dir = os.path.join(root, "climut")
        os.makedirs(mutant_dir)
        bypassing = (
            "import json\n"
            "from command_sanitize import (personal_path_in_report,\n"
            "                              sanitized_command,\n"
            "                              under_profile,\n"
            "                              write_committed_report)\n\n"
            "def commit(path, argv):\n"
            '    report = {"schema": "s", "command": [str(a) for a in argv]}\n'
            '    with open(path, "w") as stream:\n'
            "        json.dump(report, stream)\n")
        mutant_writer = "check_golden.py"
        mutant_path = os.path.join(mutant_dir, mutant_writer)
        with open(mutant_path, "w", encoding="utf-8") as stream:
            stream.write(bypassing)
        mutant_problems = audit_report_writers(cli_dir=mutant_dir,
                                               writers=[mutant_writer])
        expect("a direct json.dump bypass is named by the audit",
               any("serializes a report outside write_committed_report"
                   in problem for problem in mutant_problems),
               str(mutant_problems))
        expect("a bypassing writer is named only for the bypass, not for the "
               "defences it does apply",
               not any("never applies the shared defence" in problem
                       for problem in mutant_problems), str(mutant_problems))

        # 6: a writer that never applies the shared defences is named once per
        # missing defence, including the sanctioned commit path: a partial
        # report is not a pass.
        os.remove(mutant_path)
        with open(mutant_path, "w", encoding="utf-8") as stream:
            stream.write('report = {"command": "static"}\n')
        mutant_problems = audit_report_writers(cli_dir=mutant_dir,
                                               writers=[mutant_writer])
        expect("a shared-tier writer that skips the sanctioned writer is named",
               any("write_committed_report" in problem
                   for problem in mutant_problems), str(mutant_problems))
        expect("a shared-tier writer that never screens caller paths is named",
               any("never refuses a caller-supplied path" in problem
                   for problem in mutant_problems), str(mutant_problems))
        # The legacy tier builds its own report dict, so each primitive has to
        # be visible at its call site; naming one defence must not be enough.
        # The example is injected rather than borrowed from a live writer: every
        # registered writer is on the shared tier now, so a control that read its
        # tier from the registry would go red for reasons that have nothing to do
        # with the rule it exists to pin, and the legacy rules would silently
        # lose their only coverage the next time a writer migrates.
        legacy_writer = "legacy_example.py"
        with open(os.path.join(mutant_dir, legacy_writer), "w",
                  encoding="utf-8") as stream:
            stream.write('report = {"command": "static"}\n')
        with _temporary_registry_entry(legacy_writer, {
                "artifacts": ("legacy-example.json",),
                "screened": ("--tree",),
                "tier": LEGACY_TIER,
                "owner": "command_sanitize self-test"}):
            legacy_problems = audit_report_writers(
                cli_dir=mutant_dir, writers=[legacy_writer])
        expect("a legacy writer is reported for every missing primitive",
               all(any(name in problem for problem in legacy_problems)
                   for name in ("sanitized_command", "under_profile",
                                "personal_path_in_report")),
               str(legacy_problems))

        # 6a: importing the sanctioned writer is not the same as using it.  A
        # module that imports it, screens its paths, and then calls it without
        # ``caller_paths=`` records an empty screening list, so the privacy
        # block would claim a screening that never happened.
        with open(mutant_path, "w", encoding="utf-8") as stream:
            stream.write(
                "from command_sanitize import (require_paths_outside_profile,\n"
                "                              write_committed_report)\n\n"
                "def commit(path, tree):\n"
                "    require_paths_outside_profile((('--tree', tree),))\n"
                '    write_committed_report(path, {"schema": "s"})\n')
        mutant_problems = audit_report_writers(cli_dir=mutant_dir,
                                               writers=[mutant_writer])
        expect("a commit that never hands over its path options is named",
               any("without caller_paths=" in problem
                   for problem in mutant_problems), str(mutant_problems))
        expect("a writer that calls the sanctioned writer with its paths is "
               "named for nothing else",
               not any("never calls write_committed_report" in problem
                       for problem in mutant_problems), str(mutant_problems))
        with open(mutant_path, "w", encoding="utf-8") as stream:
            stream.write(
                "from command_sanitize import (require_paths_outside_profile,\n"
                "                              write_committed_report)\n\n"
                "def commit(path, tree):\n"
                "    require_paths_outside_profile((('--tree', tree),))\n"
                "    return write_committed_report\n")
        mutant_problems = audit_report_writers(cli_dir=mutant_dir,
                                               writers=[mutant_writer])
        expect("a writer that imports the sanctioned writer but never calls "
               "it is named",
               any("never calls write_committed_report" in problem
                   for problem in mutant_problems), str(mutant_problems))

        # 6b: the privacy block cannot be satisfied by the writer's own word.
        # A report whose privacy block claims a screening the writer did not do
        # (an option dropped from checked_inputs) is refused by the same rules
        # the writer runs before it serializes.
        unclaimed = {"schema": "s", "command": ["v4/cli/selftest.py"],
                     "checkout_root": None, "git_head": "0" * 40,
                     "privacy": {"sanitizer": PRIVACY_SANITIZER_NAME,
                                 "personal_path_in_report": None,
                                 "checked_inputs": ["--json-report"]}}
        entry = registry_entry_for(os.path.join(root, "golden.json"))
        expect("the registry entry is derived from the artifact name",
               entry is not None and "golden.json" in entry["artifacts"],
               str(entry))
        loose = committed_report_problems(dict(unclaimed), where="t",
                                          require_privacy=True,
                                          screened=entry["screened"])
        expect("a privacy block that omits a screened option is refused",
               any("stopped screening" in problem for problem in loose),
               str(loose))
        strict = committed_report_problems(
            dict(unclaimed, privacy=dict(
                unclaimed["privacy"],
                checked_inputs=sorted(entry["screened"]))),
            where="t", require_privacy=True, screened=entry["screened"])
        expect("the same report passes once every screened option is recorded",
               not strict, str(strict))
        forged = committed_report_problems(
            dict(unclaimed, privacy={"sanitizer": "someone else",
                                     "personal_path_in_report": None,
                                     "checked_inputs": []}),
            where="t", require_privacy=True, screened=entry["screened"])
        expect("a hand-written privacy block is refused",
               any("not written by" in problem for problem in forged),
               str(forged))
        leaked = committed_report_problems(
            dict(unclaimed, note=(profile_path() or "/nonexistent-profile")
                 + "/stage/bin"),
            where="t", require_privacy=True,
            screened=sorted(entry["screened"]))
        expect("a personal path anywhere in the report is refused",
               any("carries a personal path" in problem for problem in leaked),
               str(leaked))

        # 7: the registry itself is never silently empty: asking for a writer
        # that is not registered is an error, not a skip.
        mutant_problems = audit_report_writers(cli_dir=mutant_dir,
                                               writers=["gone.py"])
        expect("a writer outside the registry fails closed",
               any("is not registered in COMMITTED_REPORT_WRITERS" in problem
                   for problem in mutant_problems), str(mutant_problems))

        # 8: the whole shared-tier set is judged together, from the tree that
        # is actually installed.  A writer that deletes the audit call from
        # its own ``--self-test`` still fails, because every *other* writer's
        # self-test runs this control against its source.  This is what makes
        # the provenance rule non-bypassable rather than per-file opt-in.
        shared = shared_tier_writers()
        expect("the shared-tier set is non-empty and fully registered",
               len(shared) >= 9 and all(
                   name in COMMITTED_REPORT_WRITERS for name in shared),
               f"{len(shared)} shared-tier writers")
        expect("every shared-tier writer passes the joint audit",
               not audit_report_writers(writers=shared, artifacts=False),
               str(audit_report_writers(writers=shared, artifacts=False)))
        expect("a shared-tier writer list built from anything else is refused",
               audit_report_writers(writers=["nope.py"],
                                    artifacts=False) != [],
               "the audit accepted an unregistered writer")

        # 9: importing the sanctioned writer and screening paths is still not
        # enough: a writer that drops run_shared_self_test() from its own
        # --self-test never runs the shared provenance controls or the joint
        # audit of the whole set, and nothing else would notice.  Reusing a
        # registered shared-tier name keeps the mutant under the tier that
        # carries this rule.
        silent_writer = "sensitivity_gate.py"
        with open(os.path.join(mutant_dir, silent_writer), "w",
                  encoding="utf-8") as stream:
            stream.write(
                "from command_sanitize import (require_paths_outside_profile,\n"
                "                              write_committed_report)\n\n"
                "def commit(path, report, paths):\n"
                "    require_paths_outside_profile(paths)\n"
                "    write_committed_report(path, report, caller_paths=paths)\n")
        silent_problems = audit_report_writers(cli_dir=mutant_dir,
                                               writers=[silent_writer],
                                               artifacts=False)
        expect("a shared-tier writer that skips run_shared_self_test is named",
               any("run_shared_self_test" in problem
                   for problem in silent_problems), str(silent_problems))
    finally:
        shutil.rmtree(root, ignore_errors=True)
    return checks


# Executed-control count of ``_provenance_self_test``.  A harness self-test
# that only prints "0 failures" cannot tell a passed run from a run in which
# nothing executed, so the count is asserted here and by every harness that
# calls into this module.
PROVENANCE_SELF_TEST_CHECKS = 31


def _self_test():
    """Kernel-resolution pins for the command-path sanitizer.

    The runner executes a binary path through the kernel (symlinks
    resolved before ``..``) and records the effective executable;
    the sanitizer must render the recorded command so it names the
    same file.  Two controls:

    1. symlink-plus-parent traversal: an existing absolute value
       ``<checkout>/link/../go/iprange`` where ``link`` is a symlink
       to a directory OUTSIDE the checkout must render as the
       kernel-resolved outside-checkout executable (the effective
       identity), never as the lexical ``go/iprange`` checkout
       spelling; resolving the rendered value must select the same
       file the kernel would execute.
    2. direct-path control: the same file named directly must keep
       its recorded spelling (outside-checkout values pass through
       verbatim), and the two rendered commands must resolve to the
       same executed file.

    Also pins the checkout-relative rendering invariant for an
    existing value genuinely inside the checkout: it still renders
    checkout-relative.

    The kernel-resolution controls are POSIX-only.  A Windows
    interpreter must still normalise ``link/..`` lexically before the
    kernel sees it (the msys2-mingw64 ntpath collapses ``..`` before
    ``stat``/``realpath``), so the symlink-plus-parent premise cannot
    be exercised there; on Windows the runner and the sanitizer share
    the same ntpath resolution, so their agreement is automatic.
    """

    import tempfile

    global _CHECKOUT
    root = tempfile.mkdtemp(prefix="qual-selftest-checkout-",
                            dir=neutral_temp_root())
    saved_checkout = _CHECKOUT
    try:
        # A scratch checkout rooted OUTSIDE the operator's profile, so
        # the containment and privacy invariants are exercised without
        # depending on the real checkout's location.
        _CHECKOUT = root
        if _IS_POSIX:
            outside = os.path.join(neutral_temp_root(),
                                   "qual-selftest-bin")
            os.makedirs(os.path.join(outside, "rust"), exist_ok=True)
            os.makedirs(os.path.join(outside, "go"), exist_ok=True)
            binary = os.path.join(outside, "go", "iprange")
            with open(binary, "w", encoding="ascii") as stream:
                stream.write("selftest binary\n")
            os.symlink(os.path.join(outside, "rust"),
                       os.path.join(root, "link"))
            traversal = os.path.join(root, "link", "..", "go",
                                     "iprange")

            rendered_traversal = sanitized_path_value(traversal)
            rendered_direct = sanitized_path_value(binary)
        # The traversal value lives lexically under the checkout, so
        # the pre-fix sanitizer recorded ``go/iprange`` and the record
        # resolved to a different (nonexistent) file; the rendered
        # command must name the effective executable instead.
            if rendered_traversal == "go/iprange":
                raise AssertionError(
                    "symlink-plus-parent traversal rendered the "
                    "lexical checkout spelling; the recorded command "
                    "would name a different file than the kernel "
                    "executes")
            if not os.path.isabs(rendered_traversal):
                raise AssertionError(
                    f"symlink-plus-parent traversal rendered a "
                    f"relative spelling {rendered_traversal!r} for an "
                    "outside-checkout effective executable")
            if os.path.realpath(rendered_traversal) != \
                    os.path.realpath(binary):
                raise AssertionError(
                    f"rendered traversal {rendered_traversal!r} does "
                    f"not resolve to the executed binary {binary!r}")
            if rendered_direct != binary:
                raise AssertionError(
                    f"direct outside-checkout value {binary!r} was "
                    f"rewritten to {rendered_direct!r}; it must pass "
                    "through verbatim")
            # The recorded-command resolution (checkout-relative
            # values resolve against the checkout root) must agree
            # with the kernel resolution for both spellings.
            if os.path.realpath(
                    os.path.join(_CHECKOUT, rendered_traversal
                                 if not os.path.isabs(
                                     rendered_traversal)
                                 else rendered_traversal)) != \
                    os.path.realpath(binary):
                raise AssertionError(
                    "recorded-command resolution of the traversal "
                    "spelling does not select the executed file")
        # Existing value inside the checkout keeps the invariant
        # checkout-relative rendering.
        inside = os.path.join(root, "sub", "file.txt")
        os.makedirs(os.path.dirname(inside), exist_ok=True)
        with open(inside, "w", encoding="ascii") as stream:
            stream.write("inside\n")
        rendered_inside = sanitized_path_value(inside)
        if rendered_inside != os.path.join("sub", "file.txt"):
            raise AssertionError(
                f"existing in-checkout value rendered "
                f"{rendered_inside!r}, expected "
                f"{os.path.join('sub', 'file.txt')!r}")
    finally:
        _CHECKOUT = saved_checkout
        import shutil
        shutil.rmtree(root, ignore_errors=True)


def self_test_verbose():
    """Run every shared control and report the pinned executed-check counts.

    The entry point the harness self-tests and ``--self-test`` on a writer
    call, so the shared module's own guarantees are counted in the same
    ``executed=N expected=N`` form the harnesses use.
    """
    _self_test()
    executed = _provenance_self_test()
    if executed != PROVENANCE_SELF_TEST_CHECKS:
        raise AssertionError(
            f"provenance self-test executed {executed} controls, expected "
            f"exactly {PROVENANCE_SELF_TEST_CHECKS}: a dropped control is "
            "the defect this pin exists to catch")
    return executed


def run_shared_self_test(label):
    """Run the shared controls and print them in the counted form.

    Every harness self-test calls this, so the committed-report guarantees
    (the sanctioned commit path, the privacy block, the AST bypass audit) are
    counted inside each gate rather than only in this module.  A harness that
    stops calling it loses that coverage visibly: its own self-test line no
    longer reports the shared controls.
    """
    executed = self_test_verbose()
    print(f"{label}: shared command_sanitize controls executed={executed} "
          f"expected={PROVENANCE_SELF_TEST_CHECKS}")
    return executed


def producer_main(argv=None):
    """The ``--emit-build-ids`` and ``--commit-report`` entry points.

    Both verbs write through ``write_committed_report`` and nothing else, so an
    identity record cannot be produced on a path that skips the provenance and
    privacy rules; each prints what it measured and where, and returns its exit
    status rather than calling ``sys.exit`` so the controls can drive it.
    """
    argv = list(sys.argv if argv is None else argv)
    parser = argparse.ArgumentParser(
        prog=os.path.basename(argv[0]) if argv else "command_sanitize.py",
        description="commit the v4 CLI identity records through the shared "
                    "report writer")
    parser.add_argument("--emit-build-ids", metavar="DEST",
                        help="recompute the iprange-livedb build identity and "
                             "write it to DEST")
    parser.add_argument("--rust-tree", metavar="DIR", default=None,
                        help="root holding iprange-livedb (default: the "
                             "checkout's v4/rust)")
    parser.add_argument("--built-cli", metavar="PATH", default=None,
                        help="built iprange executable to check the digest "
                             "against; without it the record says not measured")
    parser.add_argument("--built-worker", metavar="PATH", default=None,
                        help="built iprange-v4-worker executable to check the "
                             "digest against")
    parser.add_argument("--commit-report", metavar="SRC", default=None,
                        help="staged report to promote into the evidence "
                             "directory")
    parser.add_argument("--commit-report-to", metavar="DEST", default=None,
                        help="committed path for --commit-report")
    args = parser.parse_args(argv[1:])
    if bool(args.emit_build_ids) == bool(args.commit_report):
        parser.error("exactly one of --emit-build-ids or --commit-report is "
                     "required")
    if args.commit_report:
        if not args.commit_report_to:
            parser.error("--commit-report requires --commit-report-to")
        text = commit_report(args.commit_report, args.commit_report_to,
                             argv=argv)
        print(f"promoted {sanitized_path_value(args.commit_report)} -> "
              f"{sanitized_path_value(args.commit_report_to)} "
              f"({len(text)} bytes)")
        return 0
    text = emit_build_ids(args.emit_build_ids, rust_tree=args.rust_tree,
                         built={"cli": args.built_cli,
                                "worker": args.built_worker}, argv=argv)
    document = json.loads(text)
    print(f"emitted {sanitized_path_value(args.emit_build_ids)}: "
          f"{document['package']} build id "
          f"{document['expected_build_id']['linux']} from "
          f"{document['inputs']['source_files']} sources; built products "
          f"cli={document['verified_against_built_products']["cli_contains_expected"]} "
          f"worker={document['verified_against_built_products']["worker_contains_expected"]}")
    return 0


if __name__ == "__main__":
    if "--emit-build-ids" in sys.argv or "--commit-report" in sys.argv:
        sys.exit(producer_main())
    if "--audit-report-writers" in sys.argv or \
            "--audit-committed-reports" in sys.argv:
        # The committed-evidence audit, run over the whole registry.  A
        # harness's own ``--self-test`` audits that harness; this is the
        # battery-step view that no single writer can opt out of.
        found = []
        if "--audit-report-writers" in sys.argv:
            found.extend(audit_report_writers())
        if "--audit-committed-reports" in sys.argv:
            found.extend(audit_committed_reports())
        for problem in found:
            print(f"PROBLEM {problem}")
        print(f"committed-report audit: {len(found)} problem(s)")
        sys.exit(1 if found else 0)
    # The joint audit is run through ``run_shared_self_test`` rather than
    # ``self_test_verbose`` directly: this module is itself a registered writer
    # (it produces the committed identity records), and the audit refuses a
    # writer whose self-test never runs the shared controls and the set-wide
    # audit, which is the only thing that keeps its own entry honest.
    count = run_shared_self_test("command_sanitize")
    print(f"PASS command_sanitize self-test: {count} controls "
          f"(kernel-resolution pins + committed-report provenance)")
