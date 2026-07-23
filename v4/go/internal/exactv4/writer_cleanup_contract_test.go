package exactv4

import "testing"

func succeedPrivateWriterCleanup(
	_ privateWriterCleanupObligation,
	_ *privateWriterCleanupRetryAuthority,
) privateWriterCleanupError {
	return privateWriterCleanupError{}
}

func failOddPrivateWriterCleanup(
	obligation privateWriterCleanupObligation,
	authority *privateWriterCleanupRetryAuthority,
) privateWriterCleanupError {
	authority.state++
	if obligation.id%2 != 0 {
		return privateWriterCleanupError{
			code:         privateWriterCleanupErrExecutionFailed,
			obligationID: obligation.id,
			detail:       100 + obligation.id,
		}
	}
	return privateWriterCleanupError{}
}

func panicPrivateWriterCleanup(
	_ privateWriterCleanupObligation,
	authority *privateWriterCleanupRetryAuthority,
) privateWriterCleanupError {
	authority.state++
	panic("private writer cleanup executor panic")
}

func recoverPrivateWriterCleanupPanic(ledger *privateWriterCleanupLedger) (recovered any) {
	defer func() {
		recovered = recover()
	}()
	ledger.retry(panicPrivateWriterCleanup)
	return nil
}

func TestPrivateWriterCleanupLedgerExactCapacityAndOwnerBinding(t *testing.T) {
	var obligations [2]privateWriterCleanupObligation
	var owners [2]privateWriterCleanupOwner
	var ledger privateWriterCleanupLedger
	if problem := initPrivateWriterCleanupLedger(
		&ledger, obligations[:], owners[:],
	); problem.failed() {
		t.Fatal(problem)
	}
	for id := uint64(1); id <= 2; id++ {
		if problem := ledger.append(
			privateWriterCleanupObligation{id: id},
			privateWriterCleanupOwner{obligationID: id},
		); problem.failed() {
			t.Fatal(problem)
		}
	}
	if problem := ledger.append(
		privateWriterCleanupObligation{id: 3},
		privateWriterCleanupOwner{obligationID: 3},
	); problem.code != privateWriterCleanupErrLedgerFull ||
		problem.obligationID != 3 || problem.detail != 2 || ledger.length != 2 {
		t.Fatalf("one-over append = ledger %#v error %+v", ledger, problem)
	}
	if problem := ledger.append(
		privateWriterCleanupObligation{id: 4},
		privateWriterCleanupOwner{obligationID: 5},
	); problem.code != privateWriterCleanupErrOwnerMismatch || ledger.length != 2 {
		t.Fatalf("owner mismatch = ledger %#v error %+v", ledger, problem)
	}
}

func TestPrivateWriterCleanupLedgerRetriesAllAndRetainsExactFailuresInOrder(t *testing.T) {
	var obligations [5]privateWriterCleanupObligation
	var owners [5]privateWriterCleanupOwner
	var ledger privateWriterCleanupLedger
	if problem := initPrivateWriterCleanupLedger(
		&ledger, obligations[:], owners[:],
	); problem.failed() {
		t.Fatal(problem)
	}
	for id := uint64(1); id <= 5; id++ {
		if problem := ledger.append(
			privateWriterCleanupObligation{id: id},
			privateWriterCleanupOwner{obligationID: id},
		); problem.failed() {
			t.Fatal(problem)
		}
	}

	result := ledger.retry(failOddPrivateWriterCleanup)
	if result.attempted != 5 || result.provenClean != 2 || result.retained != 3 ||
		result.firstCause != (privateWriterCleanupError{
			code:         privateWriterCleanupErrExecutionFailed,
			obligationID: 1,
			detail:       101,
		}) {
		t.Fatalf("retry result = %#v", result)
	}
	for index, id := range []uint64{1, 3, 5} {
		wantError := privateWriterCleanupError{
			code:         privateWriterCleanupErrExecutionFailed,
			obligationID: id,
			detail:       100 + id,
		}
		if ledger.obligations[index].id != id ||
			ledger.owners[index].obligationID != id ||
			ledger.owners[index].authority.state != 1 ||
			ledger.owners[index].lastError != wantError ||
			ledger.owners[index].provenClean {
			t.Fatalf(
				"retained[%d] = record %#v owner %#v",
				index, ledger.obligations[index], ledger.owners[index],
			)
		}
	}
	if ledger.obligations[3] != (privateWriterCleanupObligation{}) ||
		ledger.owners[3] != (privateWriterCleanupOwner{}) ||
		ledger.obligations[4] != (privateWriterCleanupObligation{}) ||
		ledger.owners[4] != (privateWriterCleanupOwner{}) {
		t.Fatalf("proven-clean tail not cleared: records %#v owners %#v", obligations, owners)
	}
	beforeNilRetry := owners
	result = ledger.retry(nil)
	if result.attempted != 0 || result.provenClean != 0 || result.retained != 3 ||
		result.firstCause != (privateWriterCleanupError{
			code: privateWriterCleanupErrExecutorRequired,
		}) ||
		owners != beforeNilRetry {
		t.Fatalf(
			"nil retry changed retained failures = result %#v owners %#v",
			result, owners,
		)
	}
	result = ledger.retry(succeedPrivateWriterCleanup)
	if result.attempted != 3 || result.provenClean != 3 || result.retained != 0 ||
		result.firstCause.failed() || ledger.length != 0 {
		t.Fatalf("successful retry = result %#v ledger %#v", result, ledger)
	}
}

func TestPrivateWriterCleanupLedgerNilExecutorRetainsEverything(t *testing.T) {
	var obligations [1]privateWriterCleanupObligation
	var owners [1]privateWriterCleanupOwner
	var ledger privateWriterCleanupLedger
	if problem := initPrivateWriterCleanupLedger(
		&ledger, obligations[:], owners[:],
	); problem.failed() {
		t.Fatal(problem)
	}
	if problem := ledger.append(
		privateWriterCleanupObligation{id: 9},
		privateWriterCleanupOwner{obligationID: 9},
	); problem.failed() {
		t.Fatal(problem)
	}
	result := ledger.retry(nil)
	if result.attempted != 0 || result.provenClean != 0 || result.retained != 1 ||
		result.firstCause != (privateWriterCleanupError{
			code: privateWriterCleanupErrExecutorRequired,
		}) ||
		ledger.length != 1 || ledger.obligations[0].id != 9 ||
		ledger.owners[0].obligationID != 9 ||
		ledger.owners[0].lastError.failed() ||
		ledger.owners[0].provenClean {
		t.Fatalf("nil executor = result %#v ledger %#v", result, ledger)
	}
}

func TestPrivateWriterCleanupLedgerNormalizesMismatchedExecutorErrors(t *testing.T) {
	var obligations [2]privateWriterCleanupObligation
	var owners [2]privateWriterCleanupOwner
	var ledger privateWriterCleanupLedger
	if problem := initPrivateWriterCleanupLedger(
		&ledger, obligations[:], owners[:],
	); problem.failed() {
		t.Fatal(problem)
	}
	for id := uint64(1); id <= 2; id++ {
		if problem := ledger.append(
			privateWriterCleanupObligation{id: id},
			privateWriterCleanupOwner{obligationID: id},
		); problem.failed() {
			t.Fatal(problem)
		}
	}
	result := ledger.retry(func(
		obligation privateWriterCleanupObligation,
		authority *privateWriterCleanupRetryAuthority,
	) privateWriterCleanupError {
		authority.state++
		if obligation.id == 1 {
			return privateWriterCleanupError{
				code: privateWriterCleanupErrExecutionFailed,
			}
		}
		return privateWriterCleanupError{
			code:         privateWriterCleanupErrExecutionFailed,
			obligationID: 99,
		}
	})
	if result.attempted != 2 || result.provenClean != 0 || result.retained != 2 ||
		result.firstCause != (privateWriterCleanupError{
			code:         privateWriterCleanupErrOwnerMismatch,
			obligationID: 1,
			semanticCode: privateWriterStableErrorWrongState,
		}) ||
		!ledger.valid() {
		t.Fatalf("mismatched errors = result %#v ledger %#v", result, ledger)
	}
	for index, want := range []privateWriterCleanupError{
		{
			code:         privateWriterCleanupErrOwnerMismatch,
			obligationID: 1,
			semanticCode: privateWriterStableErrorWrongState,
		},
		{
			code:         privateWriterCleanupErrOwnerMismatch,
			obligationID: 2,
			detail:       99,
			semanticCode: privateWriterStableErrorWrongState,
		},
	} {
		if ledger.owners[index].lastError != want ||
			ledger.owners[index].provenClean ||
			ledger.owners[index].authority.state != 1 {
			t.Fatalf("normalized owner[%d] = %#v, want %+v", index, ledger.owners[index], want)
		}
	}
	result = ledger.retry(succeedPrivateWriterCleanup)
	if result.attempted != 2 || result.provenClean != 2 || result.retained != 0 ||
		result.firstCause.failed() || ledger.length != 0 || !ledger.valid() {
		t.Fatalf("repair after mismatched errors = result %#v ledger %#v", result, ledger)
	}
}

func TestPrivateWriterCleanupLedgerCannotBeReinitializedWithOwnedObligations(t *testing.T) {
	var obligations [1]privateWriterCleanupObligation
	var owners [1]privateWriterCleanupOwner
	var replacementObligations [1]privateWriterCleanupObligation
	var replacementOwners [1]privateWriterCleanupOwner
	var ledger privateWriterCleanupLedger
	if problem := initPrivateWriterCleanupLedger(
		&ledger, obligations[:], owners[:],
	); problem.failed() {
		t.Fatal(problem)
	}
	if problem := ledger.append(
		privateWriterCleanupObligation{id: 13},
		privateWriterCleanupOwner{obligationID: 13},
	); problem.failed() {
		t.Fatal(problem)
	}
	if problem := initPrivateWriterCleanupLedger(
		&ledger, replacementObligations[:], replacementOwners[:],
	); problem.code != privateWriterCleanupErrInvalidState {
		t.Fatalf("owned ledger reinit = %+v", problem)
	}
	if ledger.length != 1 || ledger.obligations[0].id != 13 ||
		ledger.owners[0].obligationID != 13 {
		t.Fatalf("reinit lost obligation: %#v", ledger)
	}
}

func TestPrivateWriterCleanupLedgerRejectsExecutorReentryAndPreservesBinding(t *testing.T) {
	var obligations [2]privateWriterCleanupObligation
	var owners [2]privateWriterCleanupOwner
	var ledger privateWriterCleanupLedger
	if problem := initPrivateWriterCleanupLedger(
		&ledger, obligations[:], owners[:],
	); problem.failed() {
		t.Fatal(problem)
	}
	if problem := ledger.append(
		privateWriterCleanupObligation{id: 11},
		privateWriterCleanupOwner{obligationID: 11},
	); problem.failed() {
		t.Fatal(problem)
	}
	executor := func(
		obligation privateWriterCleanupObligation,
		authority *privateWriterCleanupRetryAuthority,
	) privateWriterCleanupError {
		if problem := ledger.append(
			privateWriterCleanupObligation{id: 12},
			privateWriterCleanupOwner{obligationID: 12},
		); problem.code != privateWriterCleanupErrBusy {
			t.Fatalf("reentrant append = %+v", problem)
		}
		if nested := ledger.retry(succeedPrivateWriterCleanup); nested.firstCause.code != privateWriterCleanupErrBusy ||
			nested.retained != 1 {
			t.Fatalf("reentrant retry = %#v", nested)
		}
		authority.state = 77
		return privateWriterCleanupError{
			code:         privateWriterCleanupErrExecutionFailed,
			obligationID: obligation.id,
			detail:       23,
		}
	}
	result := ledger.retry(executor)
	if result.firstCause != (privateWriterCleanupError{
		code:         privateWriterCleanupErrExecutionFailed,
		obligationID: 11,
		detail:       23,
	}) || result.retained != 1 || ledger.retrying || !ledger.valid() ||
		ledger.owners[0].obligationID != 11 ||
		ledger.owners[0].authority.state != 77 ||
		ledger.owners[0].lastError != result.firstCause ||
		ledger.owners[0].provenClean {
		t.Fatalf("outer retry = result %#v ledger %#v", result, ledger)
	}
}

func TestPrivateWriterCleanupLedgerPanicRetainsExactRemainingAuthority(t *testing.T) {
	for _, panicID := range []uint64{1, 2, 3} {
		t.Run(string(rune('0'+panicID)), func(t *testing.T) {
			var obligations [3]privateWriterCleanupObligation
			var owners [3]privateWriterCleanupOwner
			var ledger privateWriterCleanupLedger
			if problem := initPrivateWriterCleanupLedger(
				&ledger, obligations[:], owners[:],
			); problem.failed() {
				t.Fatal(problem)
			}
			for id := uint64(1); id <= 3; id++ {
				if problem := ledger.append(
					privateWriterCleanupObligation{id: id},
					privateWriterCleanupOwner{obligationID: id},
				); problem.failed() {
					t.Fatal(problem)
				}
			}

			panicValue := struct{ id uint64 }{id: panicID}
			var firstPassIDs [3]uint64
			firstPassCount := 0
			func() {
				defer func() {
					if recovered := recover(); recovered != panicValue {
						t.Fatalf("panic = %#v, want %#v", recovered, panicValue)
					}
				}()
				ledger.retry(func(
					obligation privateWriterCleanupObligation,
					authority *privateWriterCleanupRetryAuthority,
				) privateWriterCleanupError {
					firstPassIDs[firstPassCount] = obligation.id
					firstPassCount++
					authority.state++
					if obligation.id == panicID {
						panic(panicValue)
					}
					return privateWriterCleanupError{}
				})
				t.Fatal("retry returned after executor panic")
			}()

			if firstPassCount != int(panicID) {
				t.Fatalf("first pass count = %d, want %d", firstPassCount, panicID)
			}
			for index := 0; index < firstPassCount; index++ {
				if firstPassIDs[index] != uint64(index+1) {
					t.Fatalf("first pass IDs = %#v", firstPassIDs[:firstPassCount])
				}
			}
			if ledger.retrying || !ledger.valid() || ledger.length != 3 {
				t.Fatalf("ledger after panic = %#v", ledger)
			}
			for index := 0; index < 3; index++ {
				id := uint64(index + 1)
				owner := ledger.owners[index]
				switch {
				case id < panicID:
					if !owner.provenClean || owner.lastError.failed() ||
						owner.authority.state != 1 {
						t.Fatalf("completed owner %d = %#v", id, owner)
					}
				case id == panicID:
					if owner.provenClean ||
						owner.lastError != (privateWriterCleanupError{
							code:         privateWriterCleanupErrExecutorPanicked,
							obligationID: id,
							semanticCode: privateWriterStableErrorPanic,
						}) ||
						owner.authority.state != 1 {
						t.Fatalf("panicked owner %d = %#v", id, owner)
					}
				default:
					if owner.provenClean || owner.lastError.failed() ||
						owner.authority.state != 0 {
						t.Fatalf("unattempted owner %d = %#v", id, owner)
					}
				}
			}

			var retryIDs [3]uint64
			retryCount := 0
			result := ledger.retry(func(
				obligation privateWriterCleanupObligation,
				authority *privateWriterCleanupRetryAuthority,
			) privateWriterCleanupError {
				retryIDs[retryCount] = obligation.id
				retryCount++
				authority.state++
				return privateWriterCleanupError{}
			})
			wantRetryCount := 4 - int(panicID)
			if result.attempted != uint64(wantRetryCount) ||
				result.provenClean != uint64(wantRetryCount) ||
				result.retained != 0 || result.firstCause.failed() ||
				retryCount != wantRetryCount || ledger.length != 0 ||
				ledger.retrying || !ledger.valid() {
				t.Fatalf(
					"retry after panic = result %#v IDs %#v ledger %#v",
					result, retryIDs[:retryCount], ledger,
				)
			}
			for index := 0; index < retryCount; index++ {
				if retryIDs[index] != panicID+uint64(index) {
					t.Fatalf("retry IDs = %#v", retryIDs[:retryCount])
				}
			}
		})
	}
}

func TestPrivateWriterCleanupStateIsDerivedFromBothObligationClasses(t *testing.T) {
	var obligations [1]privateWriterCleanupObligation
	var owners [1]privateWriterCleanupOwner
	var ledger privateWriterCleanupLedger
	if problem := initPrivateWriterCleanupLedger(
		&ledger, obligations[:], owners[:],
	); problem.failed() {
		t.Fatal(problem)
	}
	var none privateWriterCoordinationCleanup
	if problem := initPrivateWriterNoCoordinationCleanup(&none); problem.failed() {
		t.Fatal(problem)
	}
	if state := derivePrivateWriterCleanupState(&ledger, &none); state != privateWriterCleanupClean {
		t.Fatalf("empty/none state = %d", state)
	}
	if problem := ledger.append(
		privateWriterCleanupObligation{id: 1},
		privateWriterCleanupOwner{obligationID: 1},
	); problem.failed() {
		t.Fatal(problem)
	}
	if state := derivePrivateWriterCleanupState(&ledger, &none); state != privateWriterCleanupResiduePossible {
		t.Fatalf("artifact residue state = %d", state)
	}
	if result := ledger.retry(succeedPrivateWriterCleanup); result.firstCause.failed() {
		t.Fatal(result)
	}
	var retained privateWriterCoordinationCleanup
	if problem := initPrivateWriterRetainedCoordinationCleanup(
		&retained, privateWriterCoordinationRetainedWriterCloseRequired,
	); problem.failed() {
		t.Fatal(problem)
	}
	if state := derivePrivateWriterCleanupState(
		&ledger, &retained,
	); state != privateWriterCleanupResiduePossible {
		t.Fatalf("coordination residue state = %d", state)
	}
}

func TestPrivateWriterCleanupGuardRejectsCopiedOwnerAndTransfersOnce(t *testing.T) {
	var state privateWriterCleanupGuardState
	var cleanup privateWriterCoordinationCleanup
	if problem := armPrivateWriterCleanupGuard(&cleanup, &state); problem.failed() {
		t.Fatal(problem)
	}
	copied := cleanup
	var copiedOutput privateWriterTakenCleanupGuard
	if problem := copied.takeGuard(
		&copiedOutput,
	); problem.code != privateWriterCoordinationErrInvalidState {
		t.Fatalf("copied owner took guard: %+v", problem)
	}
	if problem := cleanup.destroy(); problem.code != privateWriterCoordinationErrGuardBusy {
		t.Fatalf("destroy before take = %+v", problem)
	}
	var guard privateWriterTakenCleanupGuard
	problem := cleanup.takeGuard(&guard)
	if problem.failed() || !guard.valid() {
		t.Fatalf("original take = guard %#v error %+v", guard, problem)
	}
	copiedGuard := guard
	if problem = copiedGuard.resolve(); problem.code != privateWriterCoordinationErrStaleGuard ||
		!guard.valid() {
		t.Fatalf("copied taken guard changed authority: guard %#v error %+v", guard, problem)
	}
	var duplicate privateWriterTakenCleanupGuard
	if problem = cleanup.takeGuard(
		&duplicate,
	); problem.code != privateWriterCoordinationErrGuardAlreadyTaken {
		t.Fatalf("second take = %+v", problem)
	}
	if problem = cleanup.destroy(); problem.failed() {
		t.Fatalf("destroy after take = %+v", problem)
	}
	if cleanup != (privateWriterCoordinationCleanup{}) {
		t.Fatalf("successful destroy retained result state: %#v", cleanup)
	}

	var replacement privateWriterCoordinationCleanup
	if problem = armPrivateWriterCleanupGuard(
		&replacement, &state,
	); problem.code != privateWriterCoordinationErrGuardBusy || !guard.valid() {
		t.Fatalf("live taken guard state reused: guard %#v error %+v", guard, problem)
	}
	if problem = guard.resolve(); problem.failed() {
		t.Fatalf("resolve taken guard = %+v", problem)
	}
	if problem = guard.resolve(); problem.code != privateWriterCoordinationErrGuardAlreadyResolved {
		t.Fatalf("second resolve = %+v", problem)
	}
	if guard.valid() {
		t.Fatal("resolved guard remained live")
	}
	if problem = armPrivateWriterCleanupGuard(&replacement, &state); problem.failed() {
		t.Fatal(problem)
	}
	var replacementGuard privateWriterTakenCleanupGuard
	if problem = replacement.takeGuard(&replacementGuard); problem.failed() {
		t.Fatal(problem)
	}
	if problem = replacementGuard.resolve(); problem.failed() {
		t.Fatal(problem)
	}
}

func TestPrivateWriterCleanupResultStorageMayBeReusedAfterGuardTransfer(t *testing.T) {
	var state privateWriterCleanupGuardState
	var cleanup privateWriterCoordinationCleanup
	if problem := armPrivateWriterCleanupGuard(&cleanup, &state); problem.failed() {
		t.Fatal(problem)
	}
	var guard privateWriterTakenCleanupGuard
	if problem := cleanup.takeGuard(&guard); problem.failed() {
		t.Fatal(problem)
	}
	if problem := cleanup.destroy(); problem.failed() ||
		cleanup != (privateWriterCoordinationCleanup{}) {
		t.Fatalf("destroy transferred result = cleanup %#v error %+v", cleanup, problem)
	}
	if problem := initPrivateWriterNoCoordinationCleanup(&cleanup); problem.failed() {
		t.Fatalf("result storage reuse after transfer = %+v", problem)
	}
	if !guard.valid() || !cleanup.validShape() ||
		cleanup.disposition != privateWriterCoordinationNone {
		t.Fatalf("result reuse changed guard/result: guard %#v cleanup %#v", guard, cleanup)
	}
	if problem := guard.resolve(); problem.failed() {
		t.Fatal(problem)
	}
}

func TestPrivateWriterCleanupGuardIncarnationRejectsStaleResultAndGuard(t *testing.T) {
	var state privateWriterCleanupGuardState
	var oldResult privateWriterCoordinationCleanup
	if problem := armPrivateWriterCleanupGuard(&oldResult, &state); problem.failed() {
		t.Fatal(problem)
	}
	var oldGuard privateWriterTakenCleanupGuard
	if problem := oldResult.takeGuard(&oldGuard); problem.failed() {
		t.Fatal(problem)
	}
	if problem := oldGuard.resolve(); problem.failed() {
		t.Fatal(problem)
	}
	var replacement privateWriterCoordinationCleanup
	if problem := armPrivateWriterCleanupGuard(&replacement, &state); problem.failed() {
		t.Fatal(problem)
	}
	var staleOutput privateWriterTakenCleanupGuard
	if problem := oldResult.takeGuard(
		&staleOutput,
	); problem.code != privateWriterCoordinationErrStaleGuard {
		t.Fatalf("old result survived state reuse: %+v", problem)
	}
	if problem := oldGuard.resolve(); problem.code != privateWriterCoordinationErrStaleGuard {
		t.Fatalf("old guard survived state reuse: %+v", problem)
	}
	var replacementGuard privateWriterTakenCleanupGuard
	if problem := replacement.takeGuard(&replacementGuard); problem.failed() {
		t.Fatal(problem)
	}
	if problem := replacementGuard.resolve(); problem.failed() {
		t.Fatal(problem)
	}
}

func TestPrivateWriterCleanupGuardCannotBeOverwrittenBeforeTake(t *testing.T) {
	var firstState privateWriterCleanupGuardState
	var secondState privateWriterCleanupGuardState
	var cleanup privateWriterCoordinationCleanup
	if problem := armPrivateWriterCleanupGuard(&cleanup, &firstState); problem.failed() {
		t.Fatal(problem)
	}
	before := cleanup
	if problem := armPrivateWriterCleanupGuard(
		&cleanup, &secondState,
	); problem.code != privateWriterCoordinationErrGuardBusy {
		t.Fatalf("second arm = %+v", problem)
	}
	if cleanup != before || firstState.status.Load() != privateWriterCleanupGuardAvailable ||
		secondState.status.Load() != privateWriterCleanupGuardVacant {
		t.Fatalf(
			"second arm changed authority: cleanup %#v first=%d/%d second=%d/%d",
			cleanup,
			firstState.status.Load(), firstState.epoch.Load(),
			secondState.status.Load(), secondState.epoch.Load(),
		)
	}
	if problem := initPrivateWriterNoCoordinationCleanup(
		&cleanup,
	); problem.code != privateWriterCoordinationErrGuardBusy || cleanup != before {
		t.Fatalf("guard overwritten by none: cleanup %#v error %+v", cleanup, problem)
	}
	var guard privateWriterTakenCleanupGuard
	if problem := cleanup.takeGuard(&guard); problem.failed() {
		t.Fatal(problem)
	}
	if problem := guard.resolve(); problem.failed() {
		t.Fatal(problem)
	}
}

func TestPrivateWriterRetainedCoordinationCarriesNoGuard(t *testing.T) {
	for _, disposition := range []privateWriterCoordinationDisposition{
		privateWriterCoordinationRetainedReaderCloseRequired,
		privateWriterCoordinationRetainedWriterCloseRequired,
	} {
		var cleanup privateWriterCoordinationCleanup
		if problem := initPrivateWriterRetainedCoordinationCleanup(
			&cleanup, disposition,
		); problem.failed() {
			t.Fatal(problem)
		}
		if cleanup.guard != nil || cleanup.guardEpoch != 0 || !cleanup.validShape() {
			t.Fatalf("retained disposition owns guard: %#v", cleanup)
		}
		var guard privateWriterTakenCleanupGuard
		if problem := cleanup.takeGuard(
			&guard,
		); problem.code != privateWriterCoordinationErrInvalidState {
			t.Fatalf("retained disposition transferred guard: %+v", problem)
		}
		if problem := cleanup.destroy(); problem.failed() {
			t.Fatalf("retained disposition destroy = %+v", problem)
		}
		if cleanup != (privateWriterCoordinationCleanup{}) {
			t.Fatalf("retained destroy did not consume result: %#v", cleanup)
		}
	}
}

func TestPrivateWriterCleanupGuardIncarnationExhaustionIsAtomic(t *testing.T) {
	saved := privateWriterCleanupGuardIncarnation.Load()
	defer privateWriterCleanupGuardIncarnation.Store(saved)
	privateWriterCleanupGuardIncarnation.Store(^uint64(0))

	var state privateWriterCleanupGuardState
	var cleanup privateWriterCoordinationCleanup
	if problem := armPrivateWriterCleanupGuard(
		&cleanup, &state,
	); problem.code != privateWriterCoordinationErrIncarnationExhausted {
		t.Fatalf("terminal incarnation = %+v", problem)
	}
	if state.status.Load() != privateWriterCleanupGuardVacant ||
		state.epoch.Load() != 0 || cleanup != (privateWriterCoordinationCleanup{}) {
		t.Fatalf(
			"exhausted arm changed state/result: state=%d/%d cleanup %#v",
			state.status.Load(), state.epoch.Load(), cleanup,
		)
	}
}

func TestPrivateWriterCleanupPreparedOperationsAllocateNothing(t *testing.T) {
	var obligations [2]privateWriterCleanupObligation
	var owners [2]privateWriterCleanupOwner
	var ledger privateWriterCleanupLedger
	var guardState privateWriterCleanupGuardState
	var coordination privateWriterCoordinationCleanup
	var guard privateWriterTakenCleanupGuard
	if problem := initPrivateWriterCleanupLedger(
		&ledger, obligations[:], owners[:],
	); problem.failed() {
		t.Fatal(problem)
	}
	allocations := testing.AllocsPerRun(1000, func() {
		if problem := ledger.append(
			privateWriterCleanupObligation{id: 1},
			privateWriterCleanupOwner{obligationID: 1},
		); problem.failed() {
			panic(problem)
		}
		if result := ledger.retry(succeedPrivateWriterCleanup); result.firstCause.failed() {
			panic(result)
		}
		if problem := armPrivateWriterCleanupGuard(
			&coordination, &guardState,
		); problem.failed() {
			panic(problem)
		}
		guard = privateWriterTakenCleanupGuard{}
		if problem := coordination.takeGuard(&guard); problem.failed() {
			panic(problem)
		}
		if problem := coordination.destroy(); problem.failed() {
			panic(problem)
		}
		if problem := guard.resolve(); problem.failed() {
			panic(problem)
		}
	})
	if allocations != 0 {
		t.Fatalf("cleanup contract allocations = %f", allocations)
	}
}

func TestPrivateWriterCleanupFailureAndRecoveryPathsAllocateNothing(t *testing.T) {
	assertZero := func(name string, operation func()) {
		t.Helper()
		if allocations := testing.AllocsPerRun(1000, operation); allocations != 0 {
			t.Fatalf("%s allocations = %f", name, allocations)
		}
	}

	var fullObligations [1]privateWriterCleanupObligation
	var fullOwners [1]privateWriterCleanupOwner
	var fullLedger privateWriterCleanupLedger
	if problem := initPrivateWriterCleanupLedger(
		&fullLedger, fullObligations[:], fullOwners[:],
	); problem.failed() {
		t.Fatal(problem)
	}
	if problem := fullLedger.append(
		privateWriterCleanupObligation{id: 1},
		privateWriterCleanupOwner{obligationID: 1},
	); problem.failed() {
		t.Fatal(problem)
	}
	var fullProblem privateWriterCleanupError
	assertZero("full ledger", func() {
		fullProblem = fullLedger.append(
			privateWriterCleanupObligation{id: 2},
			privateWriterCleanupOwner{obligationID: 2},
		)
	})
	if fullProblem.code != privateWriterCleanupErrLedgerFull {
		t.Fatalf("full-ledger result = %+v", fullProblem)
	}

	if result := fullLedger.retry(failOddPrivateWriterCleanup); !result.firstCause.failed() {
		t.Fatal("failure fixture unexpectedly succeeded")
	}
	failedOwner := fullLedger.owners[0]
	var nilResult privateWriterCleanupRetryResult
	assertZero("nil executor", func() {
		nilResult = fullLedger.retry(nil)
	})
	if nilResult.firstCause.code != privateWriterCleanupErrExecutorRequired ||
		fullLedger.owners[0] != failedOwner {
		t.Fatalf("nil retry = result %#v owner %#v", nilResult, fullLedger.owners[0])
	}

	var mixedObligations [3]privateWriterCleanupObligation
	var mixedOwners [3]privateWriterCleanupOwner
	var mixedLedger privateWriterCleanupLedger
	if problem := initPrivateWriterCleanupLedger(
		&mixedLedger, mixedObligations[:], mixedOwners[:],
	); problem.failed() {
		t.Fatal(problem)
	}
	for id := uint64(1); id <= 3; id++ {
		if problem := mixedLedger.append(
			privateWriterCleanupObligation{id: id},
			privateWriterCleanupOwner{obligationID: id},
		); problem.failed() {
			t.Fatal(problem)
		}
	}
	mixedObligationsFixture := mixedObligations
	mixedOwnersFixture := mixedOwners
	var mixedResult privateWriterCleanupRetryResult
	assertZero("mixed failure compaction", func() {
		mixedObligations = mixedObligationsFixture
		mixedOwners = mixedOwnersFixture
		mixedLedger.length = 3
		mixedLedger.retrying = false
		mixedResult = mixedLedger.retry(failOddPrivateWriterCleanup)
	})
	if mixedResult.attempted != 3 || mixedResult.provenClean != 1 ||
		mixedResult.retained != 2 || mixedLedger.length != 2 ||
		mixedLedger.obligations[0].id != 1 || mixedLedger.obligations[1].id != 3 {
		t.Fatalf("mixed retry = result %#v ledger %#v", mixedResult, mixedLedger)
	}

	var panicObligations [1]privateWriterCleanupObligation
	var panicOwners [1]privateWriterCleanupOwner
	var panicLedger privateWriterCleanupLedger
	if problem := initPrivateWriterCleanupLedger(
		&panicLedger, panicObligations[:], panicOwners[:],
	); problem.failed() {
		t.Fatal(problem)
	}
	if problem := panicLedger.append(
		privateWriterCleanupObligation{id: 1},
		privateWriterCleanupOwner{obligationID: 1},
	); problem.failed() {
		t.Fatal(problem)
	}
	if recovered := recoverPrivateWriterCleanupPanic(&panicLedger); recovered == nil {
		t.Fatal("panic fixture did not panic")
	}
	if panicLedger.retrying || !panicLedger.valid() ||
		panicLedger.owners[0].lastError.code != privateWriterCleanupErrExecutorPanicked {
		t.Fatalf("panic fixture = %#v", panicLedger)
	}
	panicObligationsFixture := panicObligations
	panicOwnersFixture := panicOwners
	var panicRecoveryResult privateWriterCleanupRetryResult
	assertZero("post-panic retry", func() {
		panicObligations = panicObligationsFixture
		panicOwners = panicOwnersFixture
		panicLedger.length = 1
		panicLedger.retrying = false
		panicRecoveryResult = panicLedger.retry(succeedPrivateWriterCleanup)
	})
	if panicRecoveryResult.attempted != 1 || panicRecoveryResult.provenClean != 1 ||
		panicRecoveryResult.retained != 0 || panicRecoveryResult.firstCause.failed() {
		t.Fatalf("post-panic retry = %#v", panicRecoveryResult)
	}

	var copiedState privateWriterCleanupGuardState
	var copiedCleanup privateWriterCoordinationCleanup
	if problem := armPrivateWriterCleanupGuard(
		&copiedCleanup, &copiedState,
	); problem.failed() {
		t.Fatal(problem)
	}
	copiedOwner := copiedCleanup
	var copiedOutput privateWriterTakenCleanupGuard
	var copiedProblem privateWriterCoordinationError
	assertZero("copied guard owner rejection", func() {
		copiedOutput = privateWriterTakenCleanupGuard{}
		copiedProblem = copiedOwner.takeGuard(&copiedOutput)
	})
	if copiedProblem.code != privateWriterCoordinationErrInvalidState {
		t.Fatalf("copied-owner result = %+v", copiedProblem)
	}

	var takenState privateWriterCleanupGuardState
	var takenCleanup privateWriterCoordinationCleanup
	if problem := armPrivateWriterCleanupGuard(
		&takenCleanup, &takenState,
	); problem.failed() {
		t.Fatal(problem)
	}
	var takenGuard privateWriterTakenCleanupGuard
	if problem := takenCleanup.takeGuard(&takenGuard); problem.failed() {
		t.Fatal(problem)
	}
	var secondOutput privateWriterTakenCleanupGuard
	var secondTakeProblem privateWriterCoordinationError
	assertZero("second guard take", func() {
		secondOutput = privateWriterTakenCleanupGuard{}
		secondTakeProblem = takenCleanup.takeGuard(&secondOutput)
	})
	if secondTakeProblem.code != privateWriterCoordinationErrGuardAlreadyTaken {
		t.Fatalf("second-take result = %+v", secondTakeProblem)
	}

	var untakenState privateWriterCleanupGuardState
	var untakenCleanup privateWriterCoordinationCleanup
	if problem := armPrivateWriterCleanupGuard(
		&untakenCleanup, &untakenState,
	); problem.failed() {
		t.Fatal(problem)
	}
	var destroyProblem privateWriterCoordinationError
	assertZero("destroy before take", func() {
		destroyProblem = untakenCleanup.destroy()
	})
	if destroyProblem.code != privateWriterCoordinationErrGuardBusy {
		t.Fatalf("destroy-before-take result = %+v", destroyProblem)
	}

	var replacementObligations [1]privateWriterCleanupObligation
	var replacementOwners [1]privateWriterCleanupOwner
	var reinitProblem privateWriterCleanupError
	assertZero("cleanup-ledger reinitialization rejection", func() {
		reinitProblem = initPrivateWriterCleanupLedger(
			&fullLedger, replacementObligations[:], replacementOwners[:],
		)
	})
	if reinitProblem.code != privateWriterCleanupErrInvalidState {
		t.Fatalf("reinitialization result = %+v", reinitProblem)
	}
}
