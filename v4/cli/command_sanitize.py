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
import re
import sys
import tempfile
import unicodedata

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
    return value


def _resolve(value):
    """One value as an absolute normalized spelling.

    Relative spellings resolve against the checkout root so the
    sanitized record is invariant to the invocation directory.
    A doubled leading separator (``//home/...``) is collapsed to one
    on POSIX: ``ntpath`` folds it, the kernel resolves it as the
    root, and APFS does too (macOS repeated-separator handling), so
    every comparison path must see the same single-root spelling or
    a case-varied, ``//``-prefixed checkout spelling falls out of
    containment and renders as a ``..``-walk repeating the account
    path.
    """
    if os.path.isabs(value):
        norm = os.path.normpath(value)
    else:
        norm = os.path.normpath(os.path.join(_CHECKOUT, value))
    if os.sep == "/" and norm.startswith("//") and not norm.startswith("///"):
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


def profile_path():
    """Normcased operator profile root, or an empty string when the
    platform cannot determine it (never match in that case)."""
    home = os.path.expanduser("~")
    if not home:
        return ""
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
    norm = value.replace("/", os.sep).replace("\\", os.sep)
    if "%" in norm or "$" in norm:
        norm = _expand_env_vars(norm)
    if os.name == "nt":
        norm = _strip_device_prefix(norm)
    out = [_normcase(norm)]
    resolved = _normcase(os.path.normpath(norm)) if norm else norm
    if os.sep == "/" and resolved.startswith("//"):
        resolved = "/" + resolved.lstrip("/")
    if resolved != out[0]:
        out.append(resolved)
    if os.name == "nt" and _is_drive_relative(norm):
        anchored = _normcase(os.path.normpath(os.path.abspath(norm)))
        if anchored not in out:
            out.append(anchored)
    if os.name == "nt":
        # re.sub interprets backslashes in a string replacement as
        # escapes, so the two-separator root is supplied through a
        # function replacement.
        inline = _DEVICE_INLINE_UNC_RE.sub(
            lambda _match: _BS + _BS, norm)
        inline = _DEVICE_INLINE_RE.sub("", inline)
        inline = _normcase(inline)
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
    return any(spelling == form or spelling.startswith(form + os.sep)
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
    if os.sep == "/" and norm.startswith("//") and not norm.startswith("///"):
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
        real = _normcase(os.path.normpath(os.path.realpath(path)))
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
