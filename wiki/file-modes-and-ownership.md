# File modes and ownership (v4 engines)

The v4 Rust and Go engines create their artifacts with the process
default permissions, unless creator-only mode is requested.

## The process default

Without creator-only, every artifact the engine creates is created with
mode `0666` (before the process umask): main files, reader tables,
publication temporaries, authorized scratch, and adapter outputs alike.
The umask applies normally — under `umask 022` an unprotected file lands
`0644`; under `umask 077` it lands `0600`. An unprotected artifact the
process tree re-opens for writing always keeps at least owner read and
write (the owner-bit floor); group and other follow the umask.

## Creator-only mode

Creator-only mode is an opt-in for the creating process:

- **Rust CLI**: set `IPRANGE_CREATOR_ONLY=1` in the environment.
- **Go CLI**: the same variable, read at create time.
- **C ABI**: pass a non-zero `creator_only` argument to
  `iprange_v4_abi1_create_live`. Zero is unprotected; any non-zero
  value requests the proof.

On POSIX, creator-only artifacts carry exactly mode `0600` with no
other access entries (the ACLs a creator-only create would inherit are
removed). On Windows, a creator-only artifact records a protected
security descriptor (a zero additional-access commitment). This is not
a floor: the protected state is exact, and a file that does not satisfy
it cannot pass the proof a Protected database's later opens demand.

## A database governs its own files

The process switch is advisory. A database that recorded a creator-only
choice at creation time governs its **own** files: later processes —
with or without the switch — preserve that recorded choice when they
write, recover, or republish the same database, and the lifecycle
transitions (initialize, reset) publish coordination matching the
recorded choice. This is the "creator-only follows database" rule: the
recorded choice outlives the creating process.

When coordination is absent or unreadable (an immutable source being
initialized, or a reset after coordination loss), the transition
derives the compatible policy from the main file's own state: it
records Protected only when the main itself already satisfies the
protected contract, and Unprotected otherwise. A transition never
silently changes an existing file's access.

Practical effect: to keep a shared database world-readable, do not
create it in creator-only mode; to keep a private database private,
create it in creator-only mode once — every later writer honors it.

## No-source artifacts

Some coordination artifacts have no recorded database choice to
follow: recovery scratch a process creates for its own validation,
and the worker control file. These follow the process switch directly
— with `IPRANGE_CREATOR_ONLY=1` the worker control file is created
secured (0600); without it, the process default applies. The
"follows database" rule governs the database's durable artifacts;
no-source artifacts are the switch's own surface.
