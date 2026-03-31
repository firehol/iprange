# TL;DR

Purpose: make the repository easier to understand and maintain by turning the README into a strong project landing page based on the current wiki content, and by thinning the repository root so source code lives under `src/` and packaging-related files live under `packaging/` where practical.

User requirements:
- Make the README the page that exists in the wiki.
- Improve that content so it works well as a README, not just as a wiki page copy.
- Ideally move code into `src/` and packaging into `packaging/` so the repo root stays thin.
- Publish this work on a branch with a commit and GitHub PR.

# Analysis

Initial status:
- The repository root was heavy:
  - all authored C and header files lived at the top level
  - the RPM spec template and Gentoo ebuild also lived at the top level
  - the root also carried build/test runners, autotools files, tests, packaging helpers, and many generated local artifacts
- The existing `README.md` was minimal and did not explain the tool meaningfully.
- The GitHub wiki for this repo currently contains a single page, `Home.md`, which documents the feature set in much more detail than the README.
- The autotools build referenced every source file directly from the root in `Makefile.am`, and `configure.ac` used `AC_CONFIG_SRCDIR([iprange.c])`.
- The CMake file also referenced the source files directly from the root.
- The packaging helper logic still assumed the RPM spec template lived at the repository root.
- Several test and sanitizer harnesses hardcoded direct `../../ipset*.c` source paths.
- The build-copy regression harnesses only excluded root-level object/spec artifacts; after a `src/` move they would need to exclude `src/` build artifacts too.

Areas to inspect before implementation:
- Current repository root layout and which files are build-critical.
- Existing README content and how it differs from the wiki landing page.
- Current wiki page content and whether it maps cleanly to a README.
- Build, CI, packaging, and test scripts that assume the current root layout.
- Documentation and packaging files that would need path updates if code moves under `src/`.
- Official GNU automake/autoconf behavior for subdirectory sources and configured files, to avoid breaking the current build flow.

# Decisions

User decisions already made:
- Promote the wiki page into the README and improve it.
- Aim for a thin repository root with source under `src/` and packaging under `packaging/`.

Pending decisions:
- None. The traced path assumptions were concrete enough to implement the move directly without pausing for a consumer-facing design choice.

# Plan

1. Inspect the current repository layout, build system, CI, tests, and packaging paths.
2. Fetch and review the current wiki page content that should become the README.
3. Compare the wiki page to the existing README and define the README rewrite scope.
4. Determine whether the `src/` and `packaging/` reorganization can be done cleanly in this task or whether a staged move is safer.
5. Implement the README update and any repository layout changes that are justified by the analysis.
6. Update build/test/packaging/documentation references affected by the layout changes.
7. Run the relevant verification paths and summarize residual risks.
8. Publish the finished work on git with a branch, commit, push, and PR update/creation as appropriate for the current repo state.

Implemented changes:
- Rewrote `README.md` from the wiki content into a proper landing page with:
  - feature overview
  - supported input forms
  - main operating modes
  - quick examples
  - build/test instructions
  - repository layout summary
- Moved all authored C and header files into `src/`.
- Moved `iprange.spec.in` and `iprange-9999.ebuild` into `packaging/`.
- Updated autotools to build from `src/` and generate `packaging/iprange.spec`.
- Updated the stale-object VPATH protection to track `src/*.o` under `subdir-objects`.
- Updated CMake source and include paths to the new layout.
- Updated packaging helper logic to read `packaging/iprange.spec.in`.
- Updated unit/sanitizer/build harnesses and snapshot excludes for the new `src/` and `packaging/` paths.
- Updated `.gitignore` so the configured spec path under `packaging/` is ignored, along with generated `.plist` artifacts.

# Implied Decisions

- The README should be optimized for first-time repository visitors, not just for existing users of the wiki.
- Any repository layout change must preserve the existing build, test, and packaging behavior.
- Path churn should be justified by a cleaner long-term structure, not by cosmetic movement alone.
- The `src/` move should not force a recursive automake redesign when the existing non-recursive build can be kept working cleanly with explicit source paths and `subdir-objects`.

# Testing Requirements

- Verify the project still configures/builds/tests after any path moves.
- Verify scripts, Makefiles, and CI-facing entry points still resolve the moved files correctly.
- Verify the README references valid paths after the reorganization.
- Verify fresh out-of-tree `make check` and `make check-sanitizers`, not just ad-hoc scripts, because those are the maintainer/CI entry points.

# Documentation Updates Required

- README rewrite based on the wiki landing page.
- Any path references in docs/build scripts that change because of a `src/` or `packaging/` move.

Verification completed:
- `./run-build-tests.sh`
- `./run-sanitizer-tests.sh`
- fresh out-of-tree snapshot:
  - `autoreconf -fi`
  - `../src/configure --disable-man`
  - `make -j1`
  - `IPRANGE_BIN=\"$build/iprange\" ./run-tests.sh`
  - `make check`
  - `make check-sanitizers`

Residual notes:
- The repository root is much thinner for authored files, but autotools still necessarily keeps top-level build metadata such as `configure.ac`, `Makefile.am`, `configure`, and generated support files.
- Existing local generated artifacts in this checkout (`build-default/`, old `*.plist`, `local-build-objects.stamp`, and configured-tree files) were not deleted as part of this task.
