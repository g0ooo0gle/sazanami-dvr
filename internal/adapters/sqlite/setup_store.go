package sqlite

import (
	"context"
	"errors"
	"time"
)

// ErrSetupDatabaseNotCurrentはsetupが既存のcurrent schema以外を扱わず停止したことを表す。
var ErrSetupDatabaseNotCurrent = errors.New("sqlite: current database required")

// SetupStoreErrorはsetupを継続できないDB状態を固定理由で返すエラーである。
// StateはCLIが診断を分類するために使い、Error文字列は外部へ出してよい固定値だけを返す。
type SetupStoreError struct {
	State DatabaseState
}

// Errorは外部へ出してよい固定理由だけを返す。
func (err *SetupStoreError) Error() string { return ErrSetupDatabaseNotCurrent.Error() }

// UnwrapはDB状態に依存しない固定分類を返す。
func (err *SetupStoreError) Unwrap() error { return ErrSetupDatabaseNotCurrent }

// OpenStoreForSetupはsetup専用のStoreを開く。
//
// owner lockはDB状態の確認からStore.Closeまで一度だけ保持する。EMPTYだけは同じlock内で
// embedded migrationを適用し、CURRENTはschemaを変更せず開く。それ以外の状態はDBをwriterで
// 開かず、固定理由のErrSetupDatabaseNotCurrentを返す。
func OpenStoreForSetup(ctx context.Context, dataRoot string, appliedAt time.Time) (*Store, Inspection, error) {
	if ctx == nil || appliedAt.IsZero() {
		return nil, Inspection{State: StateUnreadable}, errors.New("sqlite: invalid setup context")
	}
	ownerLock, err := acquireOwnerLock(dataRoot)
	if err != nil {
		return nil, Inspection{State: StateUnreadable}, err
	}

	inspection, err := InspectDatabase(ctx, dataRoot)
	if err != nil {
		_ = releaseOwnerLock(ownerLock)
		return nil, inspection, setupStateError(inspection)
	}
	if inspection.State != StateEmpty && inspection.State != StateCurrent {
		_ = releaseOwnerLock(ownerLock)
		return nil, inspection, setupStateError(inspection)
	}

	writer, err := openWriter(ctx, dataRoot)
	if err != nil {
		_ = releaseOwnerLock(ownerLock)
		return nil, inspection, err
	}
	closeWriter := func() {
		_ = writer.Close()
		_ = releaseOwnerLock(ownerLock)
	}

	// Migrationの直前にも、同じowner lock内のwriterから状態を確認する。
	inspection, err = inspect(ctx, writer)
	if err != nil {
		closeWriter()
		return nil, inspection, setupStateError(inspection)
	}
	switch inspection.State {
	case StateEmpty:
		inspection, err = migrate(ctx, writer, appliedAt.UTC().UnixMilli())
		if err != nil {
			closeWriter()
			return nil, inspection, errors.New("sqlite: setup database initialization failed")
		}
	case StateCurrent:
		// Existing CURRENT databases are opened without schema changes.
	default:
		closeWriter()
		return nil, inspection, setupStateError(inspection)
	}
	if inspection.State != StateCurrent {
		closeWriter()
		return nil, inspection, setupStateError(inspection)
	}

	store, err := openStorePools(ctx, dataRoot, writer, ownerLock)
	if err != nil {
		closeWriter()
		return nil, inspection, err
	}
	return store, inspection, nil
}

func setupStateError(inspection Inspection) error {
	state := inspection.State
	if state == "" {
		state = StateUnreadable
	}
	return &SetupStoreError{State: state}
}
