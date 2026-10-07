#!/bin/bash
# Regenerate the round-7 detector transcripts with the discipline the
# round-6 reviews demanded: one command per entry, the correct working
# directory, rc captured from the TEST COMMAND (not a downstream rg),
# and every entry verified to contain named results and rc=0 before the
# artifact is accepted.
set -u
OUT="$1"
REPO="$(git rev-parse --show-toplevel)"
{
echo "# Round-7 detector transcripts (verified generation)"
echo "# Convention per entry: cwd, the exact command, its named results,"
echo "# then the command's own rc. The generator refuses to stage an"
echo "# entry whose rc is nonzero or whose output names no tests."
echo

entry() {  # entry <heading> <cwd> <command...>
  local heading="$1"; local cwd="$2"; shift 2
  echo "## $heading"
  echo "cwd: $cwd"
  printf 'command:'
  printf ' %q' "$@"
  echo
  local out rc
  out=$(cd "$cwd" && "$@" 2>&1); rc=$?
  echo "$out" | rg "^test |^--- |test result|^ok |^PASS|^FAIL" | head -60
  echo "rc=$rc"
  if [ "$rc" -ne 0 ]; then
    echo "GENERATOR-REFUSED: rc=$rc — this entry is NOT attestable" >&2
    exit 1
  fi
  if ! echo "$out" | rg -q "^test |^--- |^ok "; then
    echo "GENERATOR-REFUSED: no named results — this entry is NOT attestable" >&2
    exit 1
  fi
  echo
}

entry "Rust lib unit detectors (classifier crafted header; merged control-file mode test)" \
  "$REPO" nice cargo test --manifest-path v4/rust/Cargo.toml -p iprange-livedb --offline --lib -v -- classifier_rejects_a_zero_identity_header control_file_follows_the_process_switch

entry "Rust source_policy integration (sidecar record + watchdog FIFO)" \
  "$REPO" nice cargo test --manifest-path v4/rust/Cargo.toml -p iprange-livedb --offline --test source_policy -v

entry "Rust recovery twins (end-to-end, switch-ON, hostile umask)" \
  "$REPO" nice cargo test --manifest-path v4/rust/Cargo.toml -p iprange-livedb --offline --test recovery_follows_source -v

entry "Rust feed and algebra publish switch pins" \
  "$REPO" nice cargo test --manifest-path v4/rust/Cargo.toml -p iprange-livedb --offline --test immutable_feed --test membership_algebra -v -- follows_the_process_switch

entry "iprange-cli export detectors (source-following; re-exec hostile-umask child)" \
  "$REPO" nice cargo test --manifest-path v4/rust/Cargo.toml -p iprange-cli --offline --bins -v -- output_follows_the_source_database protected_output

entry "Go root detectors (recovery follows-source set)" \
  "$REPO/v4/go" env GOFLAGS=-buildvcs=false go test -count=1 -v -run TestRecoveryFollowsSource .

entry "Go root detector (recovery survives hostile umask)" \
  "$REPO/v4/go" env GOFLAGS=-buildvcs=false go test -count=1 -v -run TestRecoverySurvivesAHostileUmask .

entry "Go recovery source-policy detector (sidecar record, exact umask 0)" \
  "$REPO/v4/go" env GOFLAGS=-buildvcs=false go test -count=1 -v -run TestSourcePolicyFollowsTheSidecarRecord ./internal/recovery/

entry "Go internal/live detectors (zero-identity crafted header + watchdog FIFO)" \
  "$REPO/v4/go" env GOFLAGS=-buildvcs=false go test -count=1 -v -run TestSourceCreatorOnly ./internal/live/

entry "Go publish switch pins (immutable feed; publish set)" \
  "$REPO/v4/go" env GOFLAGS=-buildvcs=false go test -count=1 -v -run "TestImmutableFeedPublishFollowsTheProcessSwitch|TestPublishSetFollowsTheProcessSwitch" .

entry "Go export follows-source twin (umask 0200 hostile arm)" \
  "$REPO/v4/go" env GOFLAGS=-buildvcs=false go test -count=1 -v -run TestExportWriterFollowsSourceDatabase ./internal/cli/fileio/

entry "Go control-file mode twin" \
  "$REPO/v4/go" env GOFLAGS=-buildvcs=false go test -count=1 -v -run TestCreateParentModeIndependentOfUmask ./internal/worker/

echo "## create-mode shape gate self-test (verifier controls)"
echo "cwd: $REPO"
echo "command: python3 v4/cli/check_create_mode_shape.py --self-test"
nice python3 "$REPO/v4/cli/check_create_mode_shape.py" --self-test 2>&1 | rg "self-test|PASS|controls"
rc=$?
echo "rc=$rc"
[ "$rc" -eq 0 ] || { echo "GENERATOR-REFUSED: create-mode self-test rc=$rc" >&2; exit 1; }
} > "$OUT"
echo "staged $OUT"
