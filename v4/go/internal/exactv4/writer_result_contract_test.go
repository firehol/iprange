package exactv4

import (
	"math"
	"testing"
)

func testPrivateWriterIdentity(kind localIdentityKind, seed byte) privateWriterLocalIdentity {
	var value [32]byte
	value[0] = seed
	switch kind {
	case localIdentityPOSIX:
		value[8] = seed + 1
	case localIdentityWindows:
		value[16] = seed + 1
	}
	return privateWriterLocalIdentity{kind: kind, value: value}
}

func testPrivateWriterSecurity(
	kind localIdentityKind,
) privateWriterOptionalCreationSecurity {
	var commitment [32]byte
	commitment[0] = 1
	return privateWriterOptionalCreationSecurity{
		present:    true,
		kind:       privateWriterCreationSecurityKind(kind),
		commitment: commitment,
	}
}

func testPrivateWriterTail() privateWriterOptionalUnpublishedTail {
	return privateWriterOptionalUnpublishedTail{
		present: true, expectedDatabaseID: [16]byte{1},
		committedTargetTxnID: 7, committedTargetNonce: [16]byte{2},
		committedTargetLength:    2 * PageSize,
		observedTailEndExclusive: 3 * PageSize,
	}
}

func testPrivateWriterArtifactInputs(
	kind privateWriterCleanupArtifactKind,
	identityKind localIdentityKind,
) (
	privateWriterCleanupDirectoryRole,
	privateWriterLocalIdentity,
	privateWriterOptionalLocalIdentity,
	privateWriterOptionalCreationSecurity,
	privateWriterOptionalUnpublishedTail,
) {
	role := privateWriterDirectoryDestination
	identity := privateWriterOptionalLocalIdentity{
		present:  true,
		identity: testPrivateWriterIdentity(identityKind, 3),
	}
	security := testPrivateWriterSecurity(identityKind)
	tail := privateWriterOptionalUnpublishedTail{}
	switch kind {
	case privateWriterArtifactAuthorizedScratch:
		role = privateWriterDirectoryScratch
	case privateWriterArtifactUnpublishedMainTail:
		role = privateWriterDirectoryMainFile
		security = privateWriterOptionalCreationSecurity{}
		tail = testPrivateWriterTail()
	}
	return role, testPrivateWriterIdentity(identityKind, 1), identity, security, tail
}

func makeTestPrivateWriterArtifact(
	t *testing.T,
	arena *privateWriterBasenameArena,
	kind privateWriterCleanupArtifactKind,
	role privateWriterCleanupDirectoryRole,
	name []byte,
) privateWriterCleanupArtifactEvidence {
	t.Helper()
	_, directory, identity, security, tail := testPrivateWriterArtifactInputs(
		kind, localIdentityPOSIX,
	)
	evidence, problem := makePrivateWriterCleanupArtifactEvidence(
		arena, kind, role, directory, basenamePOSIXBytes, name,
		identity, security, tail,
	)
	if problem.failed() {
		t.Fatalf("artifact construction failed: %+v", problem)
	}
	return evidence
}

func testPrivateWriterCommitAttempt() privateWriterCommitAttempt {
	return privateWriterCommitAttempt{
		attemptedDatabaseID: [16]byte{1},
		directory:           testPrivateWriterIdentity(localIdentityPOSIX, 1),
		main:                testPrivateWriterIdentity(localIdentityPOSIX, 3),
		attemptedTxnID:      9, attemptedCommitNonce: [16]byte{4},
	}
}

type privateWriterResultFixture struct {
	storage     [256]byte
	arena       privateWriterBasenameArena
	obligations [4]privateWriterCleanupObligation
	owners      [4]privateWriterCleanupOwner
	ledger      privateWriterCleanupLedger
}

func newPrivateWriterResultFixture(t *testing.T) *privateWriterResultFixture {
	t.Helper()
	fixture := new(privateWriterResultFixture)
	if problem := initPrivateWriterBasenameArena(
		&fixture.arena, fixture.storage[:],
	); problem.failed() {
		t.Fatal(problem)
	}
	if problem := initPrivateWriterCleanupLedger(
		&fixture.ledger, fixture.obligations[:], fixture.owners[:],
	); problem.failed() {
		t.Fatal(problem)
	}
	return fixture
}

func (fixture *privateWriterResultFixture) addFailedArtifact(
	t *testing.T,
	id uint64,
	kind privateWriterCleanupArtifactKind,
	role privateWriterCleanupDirectoryRole,
	name []byte,
	code privateWriterStableErrorCode,
) {
	t.Helper()
	evidence := makeTestPrivateWriterArtifact(t, &fixture.arena, kind, role, name)
	if problem := fixture.ledger.append(
		privateWriterCleanupObligation{id: id, artifact: evidence},
		privateWriterCleanupOwner{obligationID: id},
	); problem.failed() {
		t.Fatal(problem)
	}
	result := fixture.ledger.retry(func(
		obligation privateWriterCleanupObligation,
		_ *privateWriterCleanupRetryAuthority,
	) privateWriterCleanupError {
		return privateWriterCleanupError{
			code:         privateWriterCleanupErrExecutionFailed,
			obligationID: obligation.id, semanticCode: code,
		}
	})
	if result.retained != 1 || !result.firstCause.failed() {
		t.Fatalf("cleanup failure fixture = %#v", result)
	}
}

func TestPrivateWriterBasenameArenaExactStorageAndValidation(t *testing.T) {
	rawPOSIX := []byte{0xff, 'x'}
	windows := []byte{'a', 0, 0x3d, 0xd8, 0x00, 0xde}
	storage := make([]byte, len(rawPOSIX)+len(windows))
	var arena privateWriterBasenameArena
	if problem := initPrivateWriterBasenameArena(&arena, storage); problem.failed() {
		t.Fatal(problem)
	}
	posixRef, problem := arena.append(basenamePOSIXBytes, rawPOSIX)
	if problem.failed() {
		t.Fatal(problem)
	}
	windowsRef, problem := arena.append(basenameWindowsUTF16, windows)
	if problem.failed() {
		t.Fatal(problem)
	}
	if arena.used != uint64(len(storage)) {
		t.Fatalf("arena used = %d, want %d", arena.used, len(storage))
	}
	for _, test := range []struct {
		reference privateWriterBasenameRef
		want      []byte
	}{
		{posixRef, rawPOSIX},
		{windowsRef, windows},
	} {
		got, lookupProblem := arena.bytes(test.reference)
		if lookupProblem.failed() || string(got) != string(test.want) {
			t.Fatalf("basename lookup = %x error %+v", got, lookupProblem)
		}
	}
	beforeUsed, beforeStorage := arena.used, string(storage)
	if _, problem = arena.append(basenamePOSIXBytes, []byte("z")); problem.code != privateWriterResultErrBasenameArenaFull ||
		arena.used != beforeUsed || string(storage) != beforeStorage {
		t.Fatalf("one-over append = arena %#v error %+v", arena, problem)
	}
	if _, problem = arena.bytes(privateWriterBasenameRef{
		encoding: basenamePOSIXBytes, offset: math.MaxUint64, length: 2,
	}); problem.code != privateWriterResultErrArithmeticOverflow {
		t.Fatalf("offset overflow = %+v", problem)
	}
}

func TestPrivateWriterBasenameArenaRejectsMalformedAndReinitialization(t *testing.T) {
	var storage [32]byte
	var arena privateWriterBasenameArena
	if problem := initPrivateWriterBasenameArena(&arena, storage[:]); problem.failed() {
		t.Fatal(problem)
	}
	tests := []struct {
		encoding basenameEncoding
		name     []byte
	}{
		{0, []byte("x")},
		{basenamePOSIXBytes, nil},
		{basenamePOSIXBytes, []byte(".")},
		{basenamePOSIXBytes, []byte("a/b")},
		{basenamePOSIXBytes, []byte{'a', 0}},
		{basenameWindowsUTF16, []byte{'a'}},
		{basenameWindowsUTF16, []byte{0x00, 0xd8}},
		{basenameWindowsUTF16, []byte{'/', 0}},
	}
	for index, test := range tests {
		before := arena.used
		if _, problem := arena.append(test.encoding, test.name); problem.code != privateWriterResultErrInvalidBasename ||
			arena.used != before {
			t.Fatalf("case %d = arena %#v error %+v", index, arena, problem)
		}
	}
	var replacement [8]byte
	if problem := initPrivateWriterBasenameArena(
		&arena, replacement[:],
	); problem.code != privateWriterResultErrInvalidState ||
		arena.storage[0] != storage[0] || len(arena.storage) != len(storage) {
		t.Fatalf("arena reinitialization = arena %#v error %+v", arena, problem)
	}
}

func TestPrivateWriterCleanupArtifactKindRoleMatrix(t *testing.T) {
	tests := []struct {
		name string
		kind privateWriterCleanupArtifactKind
		role privateWriterCleanupDirectoryRole
		ok   bool
	}{
		{"output destination", privateWriterArtifactPrivateOutput, privateWriterDirectoryDestination, true},
		{"output main", privateWriterArtifactPrivateOutput, privateWriterDirectoryMainFile, false},
		{"reservation destination", privateWriterArtifactPrivateReservation, privateWriterDirectoryDestination, true},
		{"reservation scratch", privateWriterArtifactPrivateReservation, privateWriterDirectoryScratch, false},
		{"coordination destination", privateWriterArtifactOwnedCoordination, privateWriterDirectoryDestination, true},
		{"coordination main", privateWriterArtifactOwnedCoordination, privateWriterDirectoryMainFile, true},
		{"coordination scratch", privateWriterArtifactOwnedCoordination, privateWriterDirectoryScratch, false},
		{"scratch scratch", privateWriterArtifactAuthorizedScratch, privateWriterDirectoryScratch, true},
		{"scratch destination", privateWriterArtifactAuthorizedScratch, privateWriterDirectoryDestination, false},
		{"tail main", privateWriterArtifactUnpublishedMainTail, privateWriterDirectoryMainFile, true},
		{"tail destination", privateWriterArtifactUnpublishedMainTail, privateWriterDirectoryDestination, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var storage [64]byte
			var arena privateWriterBasenameArena
			if problem := initPrivateWriterBasenameArena(
				&arena, storage[:],
			); problem.failed() {
				t.Fatal(problem)
			}
			_, directory, identity, security, tail :=
				testPrivateWriterArtifactInputs(test.kind, localIdentityPOSIX)
			before := arena.used
			evidence, problem := makePrivateWriterCleanupArtifactEvidence(
				&arena, test.kind, test.role, directory,
				basenamePOSIXBytes, []byte(".iprange-test.tmp"),
				identity, security, tail,
			)
			if test.ok {
				if problem.failed() || !evidence.valid(&arena) {
					t.Fatalf("valid artifact = %#v error %+v", evidence, problem)
				}
			} else if problem.code != privateWriterResultErrInvalidArtifact ||
				arena.used != before {
				t.Fatalf("invalid artifact = arena %#v evidence %#v error %+v", arena, evidence, problem)
			}
		})
	}
}

func TestPrivateWriterCleanupArtifactOptionalGroupMatrix(t *testing.T) {
	tests := []struct {
		name   string
		kind   privateWriterCleanupArtifactKind
		mutate func(
			*privateWriterOptionalLocalIdentity,
			*privateWriterOptionalCreationSecurity,
			*privateWriterOptionalUnpublishedTail,
		)
	}{
		{
			"absent identity has payload", privateWriterArtifactPrivateOutput,
			func(identity *privateWriterOptionalLocalIdentity, _ *privateWriterOptionalCreationSecurity, _ *privateWriterOptionalUnpublishedTail) {
				identity.present = false
			},
		},
		{
			"created artifact lacks security", privateWriterArtifactPrivateOutput,
			func(_ *privateWriterOptionalLocalIdentity, security *privateWriterOptionalCreationSecurity, _ *privateWriterOptionalUnpublishedTail) {
				*security = privateWriterOptionalCreationSecurity{}
			},
		},
		{
			"absent security has payload", privateWriterArtifactPrivateOutput,
			func(_ *privateWriterOptionalLocalIdentity, security *privateWriterOptionalCreationSecurity, _ *privateWriterOptionalUnpublishedTail) {
				security.present = false
			},
		},
		{
			"created artifact has tail", privateWriterArtifactAuthorizedScratch,
			func(_ *privateWriterOptionalLocalIdentity, _ *privateWriterOptionalCreationSecurity, tail *privateWriterOptionalUnpublishedTail) {
				*tail = testPrivateWriterTail()
			},
		},
		{
			"tail lacks identity", privateWriterArtifactUnpublishedMainTail,
			func(identity *privateWriterOptionalLocalIdentity, _ *privateWriterOptionalCreationSecurity, _ *privateWriterOptionalUnpublishedTail) {
				*identity = privateWriterOptionalLocalIdentity{}
			},
		},
		{
			"tail has security", privateWriterArtifactUnpublishedMainTail,
			func(_ *privateWriterOptionalLocalIdentity, security *privateWriterOptionalCreationSecurity, _ *privateWriterOptionalUnpublishedTail) {
				*security = testPrivateWriterSecurity(localIdentityPOSIX)
			},
		},
		{
			"absent tail has payload", privateWriterArtifactPrivateReservation,
			func(_ *privateWriterOptionalLocalIdentity, _ *privateWriterOptionalCreationSecurity, tail *privateWriterOptionalUnpublishedTail) {
				tail.expectedDatabaseID = [16]byte{1}
			},
		},
		{
			"tail database zero", privateWriterArtifactUnpublishedMainTail,
			func(_ *privateWriterOptionalLocalIdentity, _ *privateWriterOptionalCreationSecurity, tail *privateWriterOptionalUnpublishedTail) {
				tail.expectedDatabaseID = [16]byte{}
			},
		},
		{
			"tail transaction zero", privateWriterArtifactUnpublishedMainTail,
			func(_ *privateWriterOptionalLocalIdentity, _ *privateWriterOptionalCreationSecurity, tail *privateWriterOptionalUnpublishedTail) {
				tail.committedTargetTxnID = 0
			},
		},
		{
			"tail nonce zero", privateWriterArtifactUnpublishedMainTail,
			func(_ *privateWriterOptionalLocalIdentity, _ *privateWriterOptionalCreationSecurity, tail *privateWriterOptionalUnpublishedTail) {
				tail.committedTargetNonce = [16]byte{}
			},
		},
		{
			"tail target unaligned", privateWriterArtifactUnpublishedMainTail,
			func(_ *privateWriterOptionalLocalIdentity, _ *privateWriterOptionalCreationSecurity, tail *privateWriterOptionalUnpublishedTail) {
				tail.committedTargetLength++
			},
		},
		{
			"tail target below database minimum", privateWriterArtifactUnpublishedMainTail,
			func(_ *privateWriterOptionalLocalIdentity, _ *privateWriterOptionalCreationSecurity, tail *privateWriterOptionalUnpublishedTail) {
				tail.committedTargetLength = PageSize
			},
		},
		{
			"tail end not greater", privateWriterArtifactUnpublishedMainTail,
			func(_ *privateWriterOptionalLocalIdentity, _ *privateWriterOptionalCreationSecurity, tail *privateWriterOptionalUnpublishedTail) {
				tail.observedTailEndExclusive = tail.committedTargetLength
			},
		},
		{
			"tail end unaligned", privateWriterArtifactUnpublishedMainTail,
			func(_ *privateWriterOptionalLocalIdentity, _ *privateWriterOptionalCreationSecurity, tail *privateWriterOptionalUnpublishedTail) {
				tail.observedTailEndExclusive++
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var storage [64]byte
			var arena privateWriterBasenameArena
			if problem := initPrivateWriterBasenameArena(
				&arena, storage[:],
			); problem.failed() {
				t.Fatal(problem)
			}
			role, directory, identity, security, tail :=
				testPrivateWriterArtifactInputs(test.kind, localIdentityPOSIX)
			test.mutate(&identity, &security, &tail)
			before := arena.used
			if _, problem := makePrivateWriterCleanupArtifactEvidence(
				&arena, test.kind, role, directory, basenamePOSIXBytes,
				[]byte(".iprange-test.tmp"), identity, security, tail,
			); problem.code != privateWriterResultErrInvalidArtifact ||
				arena.used != before {
				t.Fatalf("invalid group = arena %#v error %+v", arena, problem)
			}
		})
	}
}

func TestPrivateWriterCleanupArtifactIdentityAndEncodingMatrix(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(
			*privateWriterLocalIdentity,
			*basenameEncoding,
			*privateWriterOptionalLocalIdentity,
			*privateWriterOptionalCreationSecurity,
		)
	}{
		{
			"invalid directory padding",
			func(directory *privateWriterLocalIdentity, _ *basenameEncoding, _ *privateWriterOptionalLocalIdentity, _ *privateWriterOptionalCreationSecurity) {
				directory.value[31] = 1
			},
		},
		{
			"encoding mismatch",
			func(_ *privateWriterLocalIdentity, encoding *basenameEncoding, _ *privateWriterOptionalLocalIdentity, _ *privateWriterOptionalCreationSecurity) {
				*encoding = basenameWindowsUTF16
			},
		},
		{
			"artifact identity kind mismatch",
			func(_ *privateWriterLocalIdentity, _ *basenameEncoding, identity *privateWriterOptionalLocalIdentity, _ *privateWriterOptionalCreationSecurity) {
				identity.identity = testPrivateWriterIdentity(localIdentityWindows, 2)
			},
		},
		{
			"security kind mismatch",
			func(_ *privateWriterLocalIdentity, _ *basenameEncoding, _ *privateWriterOptionalLocalIdentity, security *privateWriterOptionalCreationSecurity) {
				security.kind = privateWriterCreationSecurityWindows
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var storage [64]byte
			var arena privateWriterBasenameArena
			if problem := initPrivateWriterBasenameArena(
				&arena, storage[:],
			); problem.failed() {
				t.Fatal(problem)
			}
			role, directory, identity, security, tail :=
				testPrivateWriterArtifactInputs(
					privateWriterArtifactPrivateOutput, localIdentityPOSIX,
				)
			encoding := basenamePOSIXBytes
			test.mutate(&directory, &encoding, &identity, &security)
			if _, problem := makePrivateWriterCleanupArtifactEvidence(
				&arena, privateWriterArtifactPrivateOutput, role, directory,
				encoding, []byte(".iprange-test.tmp"), identity, security, tail,
			); problem.code != privateWriterResultErrInvalidArtifact {
				t.Fatalf("identity/encoding error = %+v", problem)
			}
		})
	}

	var storage [32]byte
	var arena privateWriterBasenameArena
	if problem := initPrivateWriterBasenameArena(&arena, storage[:]); problem.failed() {
		t.Fatal(problem)
	}
	role, directory, _, security, tail := testPrivateWriterArtifactInputs(
		privateWriterArtifactPrivateOutput, localIdentityPOSIX,
	)
	presentZero := privateWriterOptionalLocalIdentity{
		present: true,
		identity: privateWriterLocalIdentity{
			kind: localIdentityPOSIX,
		},
	}
	evidence, problem := makePrivateWriterCleanupArtifactEvidence(
		&arena, privateWriterArtifactPrivateOutput, role, directory,
		basenamePOSIXBytes, []byte("x"), presentZero, security, tail,
	)
	if problem.failed() || !evidence.valid(&arena) {
		t.Fatalf("present all-zero identity rejected: evidence %#v error %+v", evidence, problem)
	}
}

func TestPrivateWriterCleanupArtifactExactFullArenaValidationIsPure(t *testing.T) {
	name := []byte(".iprange-full.tmp")
	storage := make([]byte, len(name))
	var arena privateWriterBasenameArena
	if problem := initPrivateWriterBasenameArena(&arena, storage); problem.failed() {
		t.Fatal(problem)
	}
	evidence := makeTestPrivateWriterArtifact(
		t, &arena, privateWriterArtifactPrivateOutput,
		privateWriterDirectoryDestination, name,
	)
	beforeUsed, beforeStorage := arena.used, string(storage)
	for iteration := 0; iteration < 10; iteration++ {
		if !evidence.valid(&arena) {
			t.Fatal("exact-full evidence rejected")
		}
	}
	if arena.used != beforeUsed || string(storage) != beforeStorage {
		t.Fatalf("validation mutated exact-full arena: %#v %x", arena, storage)
	}
	storage[0] = 'x'
	if evidence.valid(&arena) {
		t.Fatal("same-length valid basename mutation remained bound")
	}
}

func TestPrivateWriterCommitAttemptInvariantMatrix(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*privateWriterCommitAttempt)
	}{
		{"database zero", func(attempt *privateWriterCommitAttempt) {
			attempt.attemptedDatabaseID = [16]byte{}
		}},
		{"directory kind zero", func(attempt *privateWriterCommitAttempt) {
			attempt.directory.kind = 0
		}},
		{"directory padding", func(attempt *privateWriterCommitAttempt) {
			attempt.directory.value[31] = 1
		}},
		{"main kind zero", func(attempt *privateWriterCommitAttempt) {
			attempt.main.kind = 0
		}},
		{"main padding", func(attempt *privateWriterCommitAttempt) {
			attempt.main.value[31] = 1
		}},
		{"identity kind mismatch", func(attempt *privateWriterCommitAttempt) {
			attempt.main = testPrivateWriterIdentity(localIdentityWindows, 3)
		}},
		{"transaction zero", func(attempt *privateWriterCommitAttempt) {
			attempt.attemptedTxnID = 0
		}},
		{"nonce zero", func(attempt *privateWriterCommitAttempt) {
			attempt.attemptedCommitNonce = [16]byte{}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPrivateWriterResultFixture(t)
			attempt := testPrivateWriterCommitAttempt()
			test.mutate(&attempt)
			var result privateWriterCommitResult
			if problem := initPrivateWriterCommitResult(
				&result, attempt, privateWriterCommitted,
				&fixture.ledger, &fixture.arena,
				privateWriterCoordinationNone, nil,
				privateWriterOptionalStableError{},
			); problem.code != privateWriterResultErrInvalidCommitAttempt ||
				result != (privateWriterCommitResult{}) {
				t.Fatalf("invalid attempt = result %#v error %+v", result, problem)
			}
		})
	}
}

func TestPrivateWriterCommitDurabilityCleanupInvariantMatrix(t *testing.T) {
	tests := []struct {
		name        string
		durability  privateWriterCommitDurability
		disposition privateWriterCoordinationDisposition
		artifact    bool
		ok          bool
		wantState   privateWriterCleanupState
	}{
		{"not committed clean", privateWriterNotCommitted, privateWriterCoordinationNone, false, true, privateWriterCleanupClean},
		{"not committed artifact", privateWriterNotCommitted, privateWriterCoordinationNone, true, true, privateWriterCleanupResiduePossible},
		{"not committed guard", privateWriterNotCommitted, privateWriterCoordinationCleanupGuard, false, true, privateWriterCleanupResiduePossible},
		{"not committed writer", privateWriterNotCommitted, privateWriterCoordinationRetainedWriterCloseRequired, false, true, privateWriterCleanupResiduePossible},
		{"not committed reader", privateWriterNotCommitted, privateWriterCoordinationRetainedReaderCloseRequired, false, false, 0},
		{"committed clean", privateWriterCommitted, privateWriterCoordinationNone, false, true, privateWriterCleanupClean},
		{"committed artifact", privateWriterCommitted, privateWriterCoordinationNone, true, true, privateWriterCleanupResiduePossible},
		{"committed guard", privateWriterCommitted, privateWriterCoordinationCleanupGuard, false, true, privateWriterCleanupResiduePossible},
		{"committed writer", privateWriterCommitted, privateWriterCoordinationRetainedWriterCloseRequired, false, true, privateWriterCleanupResiduePossible},
		{"committed reader", privateWriterCommitted, privateWriterCoordinationRetainedReaderCloseRequired, false, false, 0},
		{"unknown writer", privateWriterOutcomeUnknown, privateWriterCoordinationRetainedWriterCloseRequired, false, true, privateWriterCleanupResiduePossible},
		{"unknown writer artifact", privateWriterOutcomeUnknown, privateWriterCoordinationRetainedWriterCloseRequired, true, true, privateWriterCleanupResiduePossible},
		{"unknown none", privateWriterOutcomeUnknown, privateWriterCoordinationNone, false, false, 0},
		{"unknown artifact only", privateWriterOutcomeUnknown, privateWriterCoordinationNone, true, false, 0},
		{"unknown guard", privateWriterOutcomeUnknown, privateWriterCoordinationCleanupGuard, false, false, 0},
		{"unknown reader", privateWriterOutcomeUnknown, privateWriterCoordinationRetainedReaderCloseRequired, false, false, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPrivateWriterResultFixture(t)
			if test.artifact {
				fixture.addFailedArtifact(
					t, 1, privateWriterArtifactPrivateOutput,
					privateWriterDirectoryDestination, []byte(".iprange-output.tmp"),
					privateWriterStableErrorCleanupConflict,
				)
			}
			var guardState privateWriterCleanupGuardState
			var guardStatePointer *privateWriterCleanupGuardState
			if test.disposition == privateWriterCoordinationCleanupGuard {
				guardStatePointer = &guardState
			}
			var result privateWriterCommitResult
			problem := initPrivateWriterCommitResult(
				&result, testPrivateWriterCommitAttempt(), test.durability,
				&fixture.ledger, &fixture.arena, test.disposition,
				guardStatePointer, privateWriterOptionalStableError{},
			)
			if test.ok {
				if problem.failed() || !result.valid() ||
					result.cleanupState() != test.wantState {
					t.Fatalf("valid matrix = result %#v error %+v", result, problem)
				}
				if destroyProblem := result.destroy(); destroyProblem.failed() &&
					destroyProblem.code != privateWriterResultErrHandleBusy {
					t.Fatalf("destroy = %+v", destroyProblem)
				}
				if test.disposition == privateWriterCoordinationCleanupGuard {
					var guard privateWriterTakenCleanupGuard
					if problem := result.takeCleanupGuard(&guard); problem.failed() {
						t.Fatalf("take guard after busy destroy = %+v", problem)
					}
					if problem := result.destroy(); problem.failed() {
						t.Fatalf("destroy after guard take = %+v", problem)
					}
					if problem := guard.resolve(); problem.failed() {
						t.Fatalf("resolve guard = %+v", problem)
					}
				}
				return
			}
			if !problem.failed() || result != (privateWriterCommitResult{}) {
				t.Fatalf("invalid matrix = result %#v error %+v", result, problem)
			}
			if guardState.status.Load() != privateWriterCleanupGuardVacant ||
				guardState.epoch.Load() != 0 {
				t.Fatalf(
					"invalid result consumed guard state: %d/%d",
					guardState.status.Load(), guardState.epoch.Load(),
				)
			}
		})
	}
}

func TestPrivateWriterResultSemanticEnumValues(t *testing.T) {
	if privateWriterArtifactPrivateOutput != 1 ||
		privateWriterArtifactPrivateReservation != 2 ||
		privateWriterArtifactOwnedCoordination != 3 ||
		privateWriterArtifactAuthorizedScratch != 4 ||
		privateWriterArtifactUnpublishedMainTail != 5 ||
		privateWriterDirectoryDestination != 1 ||
		privateWriterDirectoryScratch != 2 ||
		privateWriterDirectoryMainFile != 3 ||
		privateWriterCreationSecurityPOSIX != 1 ||
		privateWriterCreationSecurityWindows != 2 ||
		privateWriterNotCommitted != 1 ||
		privateWriterCommitted != 2 ||
		privateWriterOutcomeUnknown != 3 ||
		privateWriterAborted != 1 ||
		privateWriterAbortIncomplete != 2 {
		t.Fatal("private semantic enum values drifted")
	}
}

func TestPrivateWriterResultRejectsUnknownDurabilityAndAbortOutcome(t *testing.T) {
	for _, durability := range []privateWriterCommitDurability{
		0, privateWriterOutcomeUnknown + 1,
	} {
		fixture := newPrivateWriterResultFixture(t)
		var result privateWriterCommitResult
		if problem := initPrivateWriterCommitResult(
			&result, testPrivateWriterCommitAttempt(), durability,
			&fixture.ledger, &fixture.arena, privateWriterCoordinationNone,
			nil, privateWriterOptionalStableError{},
		); problem.code != privateWriterResultErrInvalidCommitAttempt ||
			result != (privateWriterCommitResult{}) {
			t.Fatalf("durability %d = result %#v error %+v", durability, result, problem)
		}
	}
	for _, outcome := range []privateWriterAbortOutcome{
		0, privateWriterAbortIncomplete + 1,
	} {
		fixture := newPrivateWriterResultFixture(t)
		var result privateWriterAbortResult
		if problem := initPrivateWriterAbortResult(
			&result, outcome, &fixture.ledger, &fixture.arena,
			privateWriterCoordinationNone, privateWriterOptionalStableError{},
		); problem.code != privateWriterResultErrInvalidAbortResult ||
			result != (privateWriterAbortResult{}) {
			t.Fatalf("abort outcome %d = result %#v error %+v", outcome, result, problem)
		}
	}
}

func TestPrivateWriterCommitCauseIsCanonicalAndDoesNotDetermineFacts(t *testing.T) {
	tests := []struct {
		name       string
		cause      privateWriterOptionalStableError
		ok         bool
		durability privateWriterCommitDurability
	}{
		{"absent", privateWriterOptionalStableError{}, true, privateWriterCommitted},
		{
			"present",
			privateWriterOptionalStableError{
				present: true, code: privateWriterStableErrorIO,
			},
			true, privateWriterCommitted,
		},
		{
			"absent with payload",
			privateWriterOptionalStableError{code: privateWriterStableErrorIO},
			false, privateWriterCommitted,
		},
		{
			"present zero",
			privateWriterOptionalStableError{present: true},
			false, privateWriterNotCommitted,
		},
		{
			"present unknown",
			privateWriterOptionalStableError{
				present: true, code: privateWriterStableErrorCleanupInProgress + 1,
			},
			false, privateWriterNotCommitted,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPrivateWriterResultFixture(t)
			var result privateWriterCommitResult
			problem := initPrivateWriterCommitResult(
				&result, testPrivateWriterCommitAttempt(), test.durability,
				&fixture.ledger, &fixture.arena,
				privateWriterCoordinationNone, nil, test.cause,
			)
			if test.ok {
				if problem.failed() || result.durability != test.durability ||
					result.cleanupState() != privateWriterCleanupClean {
					t.Fatalf("cause changed facts: result %#v error %+v", result, problem)
				}
			} else if !problem.failed() || result != (privateWriterCommitResult{}) {
				t.Fatalf("invalid cause = result %#v error %+v", result, problem)
			}
		})
	}
}

func TestPrivateWriterAbortOutcomeCleanupInvariantMatrix(t *testing.T) {
	tests := []struct {
		name        string
		outcome     privateWriterAbortOutcome
		disposition privateWriterCoordinationDisposition
		artifact    bool
		ok          bool
		wantState   privateWriterCleanupState
	}{
		{"aborted clean", privateWriterAborted, privateWriterCoordinationNone, false, true, privateWriterCleanupClean},
		{"aborted writer", privateWriterAborted, privateWriterCoordinationRetainedWriterCloseRequired, false, true, privateWriterCleanupResiduePossible},
		{"aborted artifact writer", privateWriterAborted, privateWriterCoordinationRetainedWriterCloseRequired, true, true, privateWriterCleanupResiduePossible},
		{"aborted artifact none", privateWriterAborted, privateWriterCoordinationNone, true, false, 0},
		{"aborted reader", privateWriterAborted, privateWriterCoordinationRetainedReaderCloseRequired, false, false, 0},
		{"aborted guard", privateWriterAborted, privateWriterCoordinationCleanupGuard, false, false, 0},
		{"incomplete writer", privateWriterAbortIncomplete, privateWriterCoordinationRetainedWriterCloseRequired, false, true, privateWriterCleanupResiduePossible},
		{"incomplete artifact writer", privateWriterAbortIncomplete, privateWriterCoordinationRetainedWriterCloseRequired, true, true, privateWriterCleanupResiduePossible},
		{"incomplete none", privateWriterAbortIncomplete, privateWriterCoordinationNone, false, false, 0},
		{"incomplete reader", privateWriterAbortIncomplete, privateWriterCoordinationRetainedReaderCloseRequired, false, false, 0},
		{"incomplete guard", privateWriterAbortIncomplete, privateWriterCoordinationCleanupGuard, false, false, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPrivateWriterResultFixture(t)
			if test.artifact {
				fixture.addFailedArtifact(
					t, 1, privateWriterArtifactUnpublishedMainTail,
					privateWriterDirectoryMainFile, []byte("main"),
					privateWriterStableErrorCleanupConflict,
				)
			}
			var result privateWriterAbortResult
			problem := initPrivateWriterAbortResult(
				&result, test.outcome, &fixture.ledger, &fixture.arena,
				test.disposition,
				privateWriterOptionalStableError{
					present: true, code: privateWriterStableErrorIO,
				},
			)
			if test.ok {
				if problem.failed() || !result.valid() ||
					result.cleanupState() != test.wantState {
					t.Fatalf("valid abort = result %#v error %+v", result, problem)
				}
				if destroyProblem := result.destroy(); destroyProblem.failed() {
					t.Fatalf("abort destroy = %+v", destroyProblem)
				}
			} else if !problem.failed() || result != (privateWriterAbortResult{}) {
				t.Fatalf("invalid abort = result %#v error %+v", result, problem)
			}
		})
	}
}

func TestPrivateWriterArtifactViewPreservesEvidenceAndExactErrorAfterCompaction(t *testing.T) {
	fixture := newPrivateWriterResultFixture(t)
	names := [][]byte{
		[]byte(".iprange-one.tmp"),
		[]byte(".iprange-two.tmp"),
		[]byte(".iprange-three.tmp"),
	}
	for index, name := range names {
		id := uint64(index + 1)
		evidence := makeTestPrivateWriterArtifact(
			t, &fixture.arena, privateWriterArtifactPrivateOutput,
			privateWriterDirectoryDestination, name,
		)
		if problem := fixture.ledger.append(
			privateWriterCleanupObligation{id: id, artifact: evidence},
			privateWriterCleanupOwner{obligationID: id},
		); problem.failed() {
			t.Fatal(problem)
		}
	}
	beforeFirst := fixture.ledger.obligations[0].artifact
	beforeThird := fixture.ledger.obligations[2].artifact
	retryResult := fixture.ledger.retry(func(
		obligation privateWriterCleanupObligation,
		_ *privateWriterCleanupRetryAuthority,
	) privateWriterCleanupError {
		if obligation.id == 2 {
			return privateWriterCleanupError{}
		}
		code := privateWriterStableErrorCleanupConflict
		if obligation.id == 3 {
			code = privateWriterStableErrorIO
		}
		return privateWriterCleanupError{
			code:         privateWriterCleanupErrExecutionFailed,
			obligationID: obligation.id, semanticCode: code,
		}
	})
	if retryResult.attempted != 3 || retryResult.provenClean != 1 ||
		retryResult.retained != 2 {
		t.Fatalf("mixed retry = %#v", retryResult)
	}
	if fixture.ledger.obligations[0].artifact != beforeFirst ||
		fixture.ledger.obligations[1].artifact != beforeThird {
		t.Fatalf("artifact evidence changed during compaction: %#v", fixture.ledger.obligations)
	}
	var result privateWriterCommitResult
	if problem := initPrivateWriterCommitResult(
		&result, testPrivateWriterCommitAttempt(), privateWriterCommitted,
		&fixture.ledger, &fixture.arena, privateWriterCoordinationNone,
		nil, privateWriterOptionalStableError{},
	); problem.failed() {
		t.Fatal(problem)
	}
	for index, want := range []struct {
		name []byte
		code privateWriterStableErrorCode
	}{
		{names[0], privateWriterStableErrorCleanupConflict},
		{names[2], privateWriterStableErrorIO},
	} {
		view, problem := result.terminal.artifact(uint64(index))
		if problem.failed() ||
			view.cleanupState != privateWriterCleanupResiduePossible ||
			view.cleanupError != want.code {
			t.Fatalf("artifact view %d = %#v error %+v", index, view, problem)
		}
		gotName, basenameProblem := fixture.arena.bytes(view.evidence.basename)
		if basenameProblem.failed() || string(gotName) != string(want.name) {
			t.Fatalf("artifact basename %d = %x error %+v", index, gotName, basenameProblem)
		}
	}
	if _, problem := result.terminal.artifact(2); problem.code != privateWriterResultErrArtifactOutOfBounds {
		t.Fatalf("artifact one-over = %+v", problem)
	}
}

func TestPrivateWriterTerminalResultRequiresExactStableArtifactErrors(t *testing.T) {
	fixture := newPrivateWriterResultFixture(t)
	evidence := makeTestPrivateWriterArtifact(
		t, &fixture.arena, privateWriterArtifactPrivateOutput,
		privateWriterDirectoryDestination, []byte(".iprange-output.tmp"),
	)
	if problem := fixture.ledger.append(
		privateWriterCleanupObligation{id: 1, artifact: evidence},
		privateWriterCleanupOwner{obligationID: 1},
	); problem.failed() {
		t.Fatal(problem)
	}
	if result := fixture.ledger.retry(func(
		obligation privateWriterCleanupObligation,
		_ *privateWriterCleanupRetryAuthority,
	) privateWriterCleanupError {
		return privateWriterCleanupError{
			code:         privateWriterCleanupErrExecutionFailed,
			obligationID: obligation.id,
		}
	}); result.retained != 1 {
		t.Fatalf("failure fixture = %#v", result)
	}
	var commit privateWriterCommitResult
	if problem := initPrivateWriterCommitResult(
		&commit, testPrivateWriterCommitAttempt(), privateWriterCommitted,
		&fixture.ledger, &fixture.arena, privateWriterCoordinationNone,
		nil, privateWriterOptionalStableError{},
	); problem.code != privateWriterResultErrInvalidState ||
		commit != (privateWriterCommitResult{}) {
		t.Fatalf("missing semantic cleanup error = result %#v error %+v", commit, problem)
	}
}

func TestPrivateWriterCommitResultGuardTakeDestroyAndCopySemantics(t *testing.T) {
	fixture := newPrivateWriterResultFixture(t)
	var state privateWriterCleanupGuardState
	var result privateWriterCommitResult
	if problem := initPrivateWriterCommitResult(
		&result, testPrivateWriterCommitAttempt(), privateWriterCommitted,
		&fixture.ledger, &fixture.arena, privateWriterCoordinationCleanupGuard,
		&state, privateWriterOptionalStableError{},
	); problem.failed() {
		t.Fatal(problem)
	}
	before := result
	if problem := result.destroy(); problem.code != privateWriterResultErrHandleBusy ||
		result != before || state.status.Load() != privateWriterCleanupGuardAvailable {
		t.Fatalf("destroy before take = result %#v state %d error %+v", result, state.status.Load(), problem)
	}

	copied := result
	var copiedGuard privateWriterTakenCleanupGuard
	if problem := copied.takeCleanupGuard(
		&copiedGuard,
	); problem.code != privateWriterResultErrInvalidState ||
		copiedGuard != (privateWriterTakenCleanupGuard{}) {
		t.Fatalf("copied take = guard %#v error %+v", copiedGuard, problem)
	}
	if problem := copied.destroy(); problem.code != privateWriterResultErrInvalidState {
		t.Fatalf("copied destroy = %+v", problem)
	}

	var guard privateWriterTakenCleanupGuard
	if problem := result.takeCleanupGuard(&guard); problem.failed() ||
		!guard.valid() ||
		result.terminal.coordination.disposition != privateWriterCoordinationCleanupGuard ||
		result.cleanupState() != privateWriterCleanupResiduePossible {
		t.Fatalf("guard take = result %#v guard %#v error %+v", result, guard, problem)
	}
	var second privateWriterTakenCleanupGuard
	if problem := result.takeCleanupGuard(
		&second,
	); problem.code != privateWriterResultErrCleanupGuardUnavailable ||
		second != (privateWriterTakenCleanupGuard{}) {
		t.Fatalf("second take = guard %#v error %+v", second, problem)
	}
	if problem := result.destroy(); problem.failed() ||
		result != (privateWriterCommitResult{}) || !guard.valid() {
		t.Fatalf("destroy after take = result %#v guard %#v error %+v", result, guard, problem)
	}
	if problem := guard.resolve(); problem.failed() {
		t.Fatalf("transferred guard resolution = %+v", problem)
	}
}

func TestPrivateWriterResultInitializationFailureReturnsGuardAuthority(t *testing.T) {
	fixture := newPrivateWriterResultFixture(t)
	var state privateWriterCleanupGuardState
	var result privateWriterCommitResult
	if problem := initPrivateWriterCommitResult(
		&result, testPrivateWriterCommitAttempt(), privateWriterOutcomeUnknown,
		&fixture.ledger, &fixture.arena, privateWriterCoordinationCleanupGuard,
		&state, privateWriterOptionalStableError{},
	); problem.code != privateWriterResultErrInvalidCommitResult ||
		result != (privateWriterCommitResult{}) ||
		state.status.Load() != privateWriterCleanupGuardVacant ||
		state.epoch.Load() != 0 {
		t.Fatalf(
			"failed construction consumed authority: result %#v state %d/%d error %+v",
			result, state.status.Load(), state.epoch.Load(), problem,
		)
	}
	if fixture.ledger.sealedBy != nil || fixture.arena.sealedBy != nil {
		t.Fatal("failed construction sealed caller storage")
	}
}

func TestPrivateWriterTerminalResultSealsAndReleasesCallerStorage(t *testing.T) {
	fixture := newPrivateWriterResultFixture(t)
	var result privateWriterCommitResult
	if problem := initPrivateWriterCommitResult(
		&result, testPrivateWriterCommitAttempt(), privateWriterCommitted,
		&fixture.ledger, &fixture.arena, privateWriterCoordinationNone,
		nil, privateWriterOptionalStableError{},
	); problem.failed() || !result.valid() {
		t.Fatalf("result construction = %#v error %+v", result, problem)
	}
	if fixture.ledger.sealedBy != &result.terminal ||
		fixture.arena.sealedBy != &result.terminal {
		t.Fatal("terminal did not own both caller stores")
	}
	if problem := fixture.ledger.append(
		privateWriterCleanupObligation{id: 1},
		privateWriterCleanupOwner{obligationID: 1},
	); problem.code != privateWriterCleanupErrBusy {
		t.Fatalf("append through sealed ledger = %+v", problem)
	}
	if retry := fixture.ledger.retry(
		failPrivateWriterCleanupWithStableConflict,
	); retry.firstCause.code != privateWriterCleanupErrBusy {
		t.Fatalf("retry through sealed ledger = %#v", retry)
	}
	if _, problem := fixture.arena.append(
		basenamePOSIXBytes, []byte("x"),
	); problem.code != privateWriterResultErrInvalidState {
		t.Fatalf("append through sealed arena = %+v", problem)
	}
	var replacementObligations [1]privateWriterCleanupObligation
	var replacementOwners [1]privateWriterCleanupOwner
	if problem := initPrivateWriterCleanupLedger(
		&fixture.ledger, replacementObligations[:], replacementOwners[:],
	); problem.code != privateWriterCleanupErrInvalidState {
		t.Fatalf("reinitialize sealed ledger = %+v", problem)
	}
	var replacementStorage [1]byte
	if problem := initPrivateWriterBasenameArena(
		&fixture.arena, replacementStorage[:],
	); problem.code != privateWriterResultErrInvalidState {
		t.Fatalf("reinitialize sealed arena = %+v", problem)
	}

	copied := result
	if problem := copied.destroy(); problem.code != privateWriterResultErrInvalidState ||
		fixture.ledger.sealedBy != &result.terminal ||
		fixture.arena.sealedBy != &result.terminal {
		t.Fatalf("copied result released stores: error %+v", problem)
	}
	if problem := result.destroy(); problem.failed() {
		t.Fatalf("owner destroy = %+v", problem)
	}
	if fixture.ledger.sealedBy != nil || fixture.arena.sealedBy != nil {
		t.Fatal("owner destroy did not release caller stores")
	}
	if _, problem := fixture.arena.append(
		basenamePOSIXBytes, []byte("x"),
	); problem.failed() {
		t.Fatalf("arena append after release = %+v", problem)
	}
	if problem := fixture.ledger.append(
		privateWriterCleanupObligation{id: 1},
		privateWriterCleanupOwner{obligationID: 1},
	); problem.failed() {
		t.Fatalf("ledger append after release = %+v", problem)
	}
	if retry := fixture.ledger.retry(func(
		privateWriterCleanupObligation,
		*privateWriterCleanupRetryAuthority,
	) privateWriterCleanupError {
		return privateWriterCleanupError{}
	}); retry.attempted != 1 || retry.provenClean != 1 ||
		retry.retained != 0 || retry.firstCause.failed() {
		t.Fatalf("ledger retry after release = %#v", retry)
	}
}

func TestPrivateWriterArtifactEnumerationScalesLinearly(t *testing.T) {
	measure := func(count int) testing.BenchmarkResult {
		t.Helper()
		storage := make([]byte, count)
		obligations := make([]privateWriterCleanupObligation, count)
		owners := make([]privateWriterCleanupOwner, count)
		var arena privateWriterBasenameArena
		if problem := initPrivateWriterBasenameArena(
			&arena, storage,
		); problem.failed() {
			t.Fatal(problem)
		}
		var ledger privateWriterCleanupLedger
		if problem := initPrivateWriterCleanupLedger(
			&ledger, obligations, owners,
		); problem.failed() {
			t.Fatal(problem)
		}
		for index := 0; index < count; index++ {
			evidence := makeTestPrivateWriterArtifact(
				t, &arena, privateWriterArtifactPrivateOutput,
				privateWriterDirectoryDestination, []byte("x"),
			)
			id := uint64(index + 1)
			if problem := ledger.append(
				privateWriterCleanupObligation{id: id, artifact: evidence},
				privateWriterCleanupOwner{obligationID: id},
			); problem.failed() {
				t.Fatal(problem)
			}
		}
		if retry := ledger.retry(
			failPrivateWriterCleanupWithStableConflict,
		); retry.retained != uint64(count) {
			t.Fatalf("failure fixture retained %d, want %d", retry.retained, count)
		}
		var result privateWriterCommitResult
		if problem := initPrivateWriterCommitResult(
			&result, testPrivateWriterCommitAttempt(), privateWriterCommitted,
			&ledger, &arena, privateWriterCoordinationNone, nil,
			privateWriterOptionalStableError{},
		); problem.failed() {
			t.Fatal(problem)
		}
		measurement := testing.Benchmark(func(benchmark *testing.B) {
			var observed privateWriterStableErrorCode
			benchmark.ReportAllocs()
			benchmark.ResetTimer()
			for iteration := 0; iteration < benchmark.N; iteration++ {
				for index := 0; index < count; index++ {
					view, problem := result.terminal.artifact(uint64(index))
					if problem.failed() {
						benchmark.Fatal(problem)
					}
					observed = view.cleanupError
				}
			}
			if observed != privateWriterStableErrorCleanupConflict {
				benchmark.Fatal("enumeration result was not observed")
			}
		})
		if problem := result.destroy(); problem.failed() {
			t.Fatal(problem)
		}
		return measurement
	}

	const (
		smallCount = 64
		largeCount = 1024
	)
	small := measure(smallCount)
	large := measure(largeCount)
	smallPerArtifact := float64(small.NsPerOp()) / smallCount
	largePerArtifact := float64(large.NsPerOp()) / largeCount
	if small.AllocsPerOp() != 0 || large.AllocsPerOp() != 0 {
		t.Fatalf(
			"enumeration allocated: small %d large %d",
			small.AllocsPerOp(), large.AllocsPerOp(),
		)
	}
	if largePerArtifact > smallPerArtifact*8 {
		t.Fatalf(
			"enumeration is not linear: %.1f ns/artifact at %d, %.1f at %d",
			smallPerArtifact, smallCount, largePerArtifact, largeCount,
		)
	}
}

func failPrivateWriterCleanupWithStableConflict(
	obligation privateWriterCleanupObligation,
	_ *privateWriterCleanupRetryAuthority,
) privateWriterCleanupError {
	return privateWriterCleanupError{
		code:         privateWriterCleanupErrExecutionFailed,
		obligationID: obligation.id,
		semanticCode: privateWriterStableErrorCleanupConflict,
	}
}

func TestPrivateWriterResultContractsAllocateNothingWhenPrepared(t *testing.T) {
	var arenaStorage [64]byte
	var arena privateWriterBasenameArena
	if problem := initPrivateWriterBasenameArena(
		&arena, arenaStorage[:],
	); problem.failed() {
		t.Fatal(problem)
	}
	var obligations [1]privateWriterCleanupObligation
	var owners [1]privateWriterCleanupOwner
	var ledger privateWriterCleanupLedger
	if problem := initPrivateWriterCleanupLedger(
		&ledger, obligations[:], owners[:],
	); problem.failed() {
		t.Fatal(problem)
	}
	role, directory, identity, security, tail := testPrivateWriterArtifactInputs(
		privateWriterArtifactPrivateOutput, localIdentityPOSIX,
	)
	name := []byte(".iprange-output.tmp")
	var evidence privateWriterCleanupArtifactEvidence
	var constructionProblem privateWriterResultContractError
	allocations := testing.AllocsPerRun(1000, func() {
		arena.used = 0
		evidence, constructionProblem = makePrivateWriterCleanupArtifactEvidence(
			&arena, privateWriterArtifactPrivateOutput, role, directory,
			basenamePOSIXBytes, name, identity, security, tail,
		)
	})
	if allocations != 0 || constructionProblem.failed() || !evidence.valid(&arena) {
		t.Fatalf("artifact construction allocations = %f error %+v", allocations, constructionProblem)
	}

	ledger.length = 0
	clear(obligations[:])
	clear(owners[:])
	if problem := ledger.append(
		privateWriterCleanupObligation{id: 1, artifact: evidence},
		privateWriterCleanupOwner{obligationID: 1},
	); problem.failed() {
		t.Fatal(problem)
	}
	if result := ledger.retry(
		failPrivateWriterCleanupWithStableConflict,
	); result.retained != 1 {
		t.Fatalf("failure fixture = %#v", result)
	}
	var commit privateWriterCommitResult
	var resultProblem privateWriterResultContractError
	allocations = testing.AllocsPerRun(1000, func() {
		commit = privateWriterCommitResult{}
		resultProblem = initPrivateWriterCommitResult(
			&commit, testPrivateWriterCommitAttempt(), privateWriterCommitted,
			&ledger, &arena, privateWriterCoordinationNone, nil,
			privateWriterOptionalStableError{},
		)
		if !resultProblem.failed() {
			resultProblem = commit.destroy()
		}
	})
	if allocations != 0 || resultProblem.failed() {
		t.Fatalf("result lifecycle allocations = %f error %+v", allocations, resultProblem)
	}

	var fullStorage [1]byte
	var fullArena privateWriterBasenameArena
	if problem := initPrivateWriterBasenameArena(
		&fullArena, fullStorage[:],
	); problem.failed() {
		t.Fatal(problem)
	}
	if _, problem := fullArena.append(
		basenamePOSIXBytes, []byte("x"),
	); problem.failed() {
		t.Fatal(problem)
	}
	allocations = testing.AllocsPerRun(1000, func() {
		_, constructionProblem = fullArena.append(basenamePOSIXBytes, []byte("y"))
	})
	if allocations != 0 ||
		constructionProblem.code != privateWriterResultErrBasenameArenaFull {
		t.Fatalf("arena failure allocations = %f error %+v", allocations, constructionProblem)
	}
}
