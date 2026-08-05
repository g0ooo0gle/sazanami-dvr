package sqlite

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

const restoreOperationFormat = "sazanami.catalog-restore-operation"

// RestorePhaseはoffline restoreのdurableな進行段階を表す。
type RestorePhase string

const (
	RestorePrepared         RestorePhase = "PREPARED"
	RestoreQuarantining     RestorePhase = "QUARANTINING"
	RestoreOldQuarantined   RestorePhase = "OLD_QUARANTINED"
	RestoreInstalling       RestorePhase = "INSTALLING"
	RestoreNewInstalled     RestorePhase = "NEW_INSTALLED"
	RestoreVerified         RestorePhase = "VERIFIED"
	RestoreCommitted        RestorePhase = "COMMITTED"
	RestoreRollingBack      RestorePhase = "ROLLING_BACK"
	RestoreRolledBack       RestorePhase = "ROLLED_BACK"
	RestoreFailedNeedsOwner RestorePhase = "FAILED_NEEDS_OPERATOR"
)

// RestoreRequestは1回のoffline restoreを一意に識別する入力である。
type RestoreRequest struct {
	OperationID    catalogmodel.ID
	BackupManifest string
	CreatedAt      time.Time
	Now            func() time.Time
}

// RestoreOperationはprocess kill後の正本判定に使う同期済みmanifest v1である。
type RestoreOperation struct {
	Format                  string       `json:"format"`
	FormatVersion           int          `json:"format_version"`
	OperationID             string       `json:"operation_id"`
	Revision                int          `json:"revision"`
	Phase                   RestorePhase `json:"phase"`
	CreatedAtUTC            string       `json:"created_at_utc"`
	UpdatedAtUTC            string       `json:"updated_at_utc"`
	SourceBackupID          string       `json:"source_backup_id"`
	SourceDatabaseFile      string       `json:"source_database_file"`
	SourceDatabaseSHA256    string       `json:"source_database_sha256"`
	TargetDatabaseFile      string       `json:"target_database_file"`
	StagedDatabaseFile      string       `json:"staged_database_file"`
	StagedDatabaseSHA256    string       `json:"staged_database_sha256"`
	OldDatabasePresent      bool         `json:"old_database_present"`
	OldDatabaseSHA256       *string      `json:"old_database_sha256"`
	OldWALPresent           bool         `json:"old_wal_present"`
	OldSHMPresent           bool         `json:"old_shm_present"`
	InstalledDatabaseSHA256 *string      `json:"installed_database_sha256"`
	FailureReason           *string      `json:"failure_reason"`
}

// RestoreOfflineは検証済みbackupをstageし、旧DB／sidecarをquarantineして原子的に切り替える。
// 呼び出し元は先にdaemonと全SQLite poolを閉じ、単一operatorで実行しなければならない。
func RestoreOffline(ctx context.Context, dataRoot string, request RestoreRequest) (RestoreOperation, error) {
	if ctx == nil || request.Now == nil || request.CreatedAt.IsZero() ||
		filepath.Base(request.BackupManifest) != request.BackupManifest || request.BackupManifest == "" {
		return RestoreOperation{}, errors.New("sqlite: invalid restore request")
	}
	if err := validateDataRootOnly(dataRoot); err != nil {
		return RestoreOperation{}, err
	}
	ownerLock, err := acquireOwnerLock(dataRoot)
	if err != nil {
		return RestoreOperation{}, err
	}
	defer releaseOwnerLock(ownerLock)
	if err := enforceRestoreCaps(dataRoot); err != nil {
		return RestoreOperation{}, err
	}
	backup, err := VerifyBackup(ctx, dataRoot, request.BackupManifest)
	if err != nil {
		return RestoreOperation{}, err
	}
	id := request.OperationID.String()
	if !backupIDPattern.MatchString(id) {
		return RestoreOperation{}, errors.New("sqlite: invalid restore operation id")
	}
	stagedName := ".restore-" + id + ".staged.sqlite3"
	oldName := ".restore-" + id + ".old.sqlite3"
	oldWALName := oldName + "-wal"
	oldSHMName := oldName + "-shm"
	failedName := ".restore-" + id + ".failed.sqlite3"
	sourcePath := filepath.Join(dataRoot, "backups", backup.DatabaseFile)
	stagedPath := filepath.Join(dataRoot, stagedName)
	if err := copyFileExclusive(ctx, sourcePath, stagedPath, backup.DatabaseBytes); err != nil {
		return RestoreOperation{}, err
	}
	stagedHash, stagedSize, err := hashFile(stagedPath)
	if err != nil || stagedSize != backup.DatabaseBytes || hex.EncodeToString(stagedHash[:]) != backup.DatabaseSHA256 {
		return RestoreOperation{}, errors.New("sqlite: staged restore database mismatch")
	}

	targetPath := filepath.Join(dataRoot, databaseFilename)
	oldPresent, oldHash, walPresent, shmPresent, err := inspectCanonicalArtifacts(targetPath)
	if err != nil {
		return RestoreOperation{}, err
	}
	operation := RestoreOperation{
		Format: restoreOperationFormat, FormatVersion: 1, OperationID: id, Revision: 1,
		Phase: RestorePrepared, CreatedAtUTC: request.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAtUTC: request.Now().UTC().Format(time.RFC3339Nano), SourceBackupID: backup.BackupID,
		SourceDatabaseFile: backup.DatabaseFile, SourceDatabaseSHA256: backup.DatabaseSHA256,
		TargetDatabaseFile: databaseFilename, StagedDatabaseFile: stagedName,
		StagedDatabaseSHA256: backup.DatabaseSHA256, OldDatabasePresent: oldPresent,
		OldDatabaseSHA256: oldHash, OldWALPresent: walPresent, OldSHMPresent: shmPresent,
	}
	if err := publishRestoreOperation(dataRoot, operation); err != nil {
		return operation, err
	}
	if err := ctx.Err(); err != nil {
		reason := "cancelled-before-quarantine"
		operation.FailureReason = &reason
		operation.Phase = RestoreRolledBack
		if publishErr := advanceRestoreOperation(dataRoot, request.Now, &operation); publishErr != nil {
			return operation, publishErr
		}
		return operation, errors.New("sqlite: restore cancelled before quarantine")
	}

	operation.Phase = RestoreQuarantining
	if err := advanceRestoreOperation(dataRoot, request.Now, &operation); err != nil {
		return operation, err
	}
	rollback := func(cause error) (RestoreOperation, error) {
		return rollbackRestore(dataRoot, request.Now, &operation, oldName, oldWALName, oldSHMName, failedName, cause)
	}
	if oldPresent {
		if err := os.Rename(targetPath, filepath.Join(dataRoot, oldName)); err != nil {
			return rollback(errors.New("sqlite: quarantine old database"))
		}
	}
	if walPresent {
		if err := os.Rename(targetPath+"-wal", filepath.Join(dataRoot, oldWALName)); err != nil {
			return rollback(errors.New("sqlite: quarantine old WAL"))
		}
	}
	if shmPresent {
		if err := os.Rename(targetPath+"-shm", filepath.Join(dataRoot, oldSHMName)); err != nil {
			return rollback(errors.New("sqlite: quarantine old SHM"))
		}
	}
	if err := syncDirectory(dataRoot); err != nil {
		return rollback(err)
	}
	operation.Phase = RestoreOldQuarantined
	if err := advanceRestoreOperation(dataRoot, request.Now, &operation); err != nil {
		return rollback(err)
	}
	if err := ctx.Err(); err != nil {
		return rollback(errors.New("sqlite: restore cancelled after quarantine"))
	}

	operation.Phase = RestoreInstalling
	if err := advanceRestoreOperation(dataRoot, request.Now, &operation); err != nil {
		return rollback(err)
	}
	if err := os.Rename(stagedPath, targetPath); err != nil {
		return rollback(errors.New("sqlite: install staged database"))
	}
	if err := syncDirectory(dataRoot); err != nil {
		return rollback(err)
	}
	installedHash, installedSize, err := hashFile(targetPath)
	if err != nil || installedSize != backup.DatabaseBytes || hex.EncodeToString(installedHash[:]) != backup.DatabaseSHA256 {
		return rollback(errors.New("sqlite: installed database hash mismatch"))
	}
	installedText := backup.DatabaseSHA256
	operation.InstalledDatabaseSHA256 = &installedText
	operation.Phase = RestoreNewInstalled
	if err := advanceRestoreOperation(dataRoot, request.Now, &operation); err != nil {
		return rollback(err)
	}
	if err := ctx.Err(); err != nil {
		return rollback(errors.New("sqlite: restore cancelled before verification"))
	}
	verified, err := verifyBackupDatabase(ctx, targetPath)
	if err != nil || verified.schemaVersion != backup.SchemaVersion {
		return rollback(errors.New("sqlite: installed database verification failed"))
	}
	operation.Phase = RestoreVerified
	if err := advanceRestoreOperation(dataRoot, request.Now, &operation); err != nil {
		return rollback(err)
	}
	operation.Phase = RestoreCommitted
	if err := advanceRestoreOperation(dataRoot, request.Now, &operation); err != nil {
		return rollback(err)
	}
	return operation, nil
}

// RecoverRestoreOfflineは同期済みoperation manifestとfile hashから、commitまたはrollbackへ再収束させる。
func RecoverRestoreOffline(ctx context.Context, dataRoot, operationBasename string, now func() time.Time) (RestoreOperation, error) {
	if ctx == nil || now == nil || filepath.Base(operationBasename) != operationBasename ||
		!strings.HasPrefix(operationBasename, "restore-") || !strings.HasSuffix(operationBasename, ".operation.json") {
		return RestoreOperation{}, errors.New("sqlite: invalid restore recovery request")
	}
	if err := validateDataRootOnly(dataRoot); err != nil {
		return RestoreOperation{}, err
	}
	ownerLock, err := acquireOwnerLock(dataRoot)
	if err != nil {
		return RestoreOperation{}, err
	}
	defer releaseOwnerLock(ownerLock)
	operation, err := readRestoreOperation(filepath.Join(dataRoot, operationBasename))
	if err != nil {
		return RestoreOperation{}, err
	}
	if operationBasename != "restore-"+operation.OperationID+".operation.json" {
		return operation, errors.New("sqlite: restore operation name mismatch")
	}
	switch operation.Phase {
	case RestoreCommitted, RestoreRolledBack:
		return operation, nil
	case RestoreFailedNeedsOwner:
		return operation, errors.New("sqlite: restore needs operator")
	}
	if err := ctx.Err(); err != nil {
		return operation, errors.New("sqlite: restore recovery cancelled")
	}
	backupManifest := strings.TrimSuffix(operation.SourceDatabaseFile, ".sqlite3") + ".manifest.json"
	backup, err := VerifyBackup(ctx, dataRoot, backupManifest)
	if err != nil || backup.DatabaseSHA256 != operation.SourceDatabaseSHA256 || backup.BackupID != operation.SourceBackupID {
		return failNeedsOperator(dataRoot, now, &operation, errors.New("sqlite: restore source verification failed"))
	}
	id := operation.OperationID
	oldName := ".restore-" + id + ".old.sqlite3"
	oldWALName := oldName + "-wal"
	oldSHMName := oldName + "-shm"
	failedName := ".restore-" + id + ".failed.sqlite3"
	target := filepath.Join(dataRoot, databaseFilename)

	switch operation.Phase {
	case RestorePrepared:
		if canonicalMatchesInitial(target, operation) {
			reason := "recovered-before-quarantine"
			operation.FailureReason = &reason
			operation.Phase = RestoreRolledBack
			if err := advanceRestoreOperation(dataRoot, now, &operation); err != nil {
				return operation, err
			}
			return operation, nil
		}
		return failNeedsOperator(dataRoot, now, &operation, errors.New("sqlite: prepared restore canonical drift"))
	case RestoreQuarantining, RestoreRollingBack:
		return rollbackRestore(dataRoot, now, &operation, oldName, oldWALName, oldSHMName, failedName,
			errors.New("sqlite: recovered restore rolled back"))
	case RestoreOldQuarantined, RestoreInstalling:
		if !quarantineReady(dataRoot, operation, oldName, oldWALName, oldSHMName) {
			return failNeedsOperator(dataRoot, now, &operation, errors.New("sqlite: restore quarantine mismatch"))
		}
		matches, matchErr := fileMatchesHash(target, operation.SourceDatabaseSHA256)
		if matchErr != nil {
			return failNeedsOperator(dataRoot, now, &operation, matchErr)
		}
		if !matches {
			if pathExists(target) {
				return rollbackRestore(dataRoot, now, &operation, oldName, oldWALName, oldSHMName, failedName,
					errors.New("sqlite: unexpected canonical during recovery"))
			}
			staged := filepath.Join(dataRoot, operation.StagedDatabaseFile)
			stagedMatches, stagedErr := fileMatchesHash(staged, operation.SourceDatabaseSHA256)
			if stagedErr != nil || !stagedMatches {
				return rollbackRestore(dataRoot, now, &operation, oldName, oldWALName, oldSHMName, failedName,
					errors.New("sqlite: staged restore missing during recovery"))
			}
			operation.Phase = RestoreInstalling
			if err := advanceRestoreOperation(dataRoot, now, &operation); err != nil {
				return operation, err
			}
			if err := os.Rename(staged, target); err != nil {
				return rollbackRestore(dataRoot, now, &operation, oldName, oldWALName, oldSHMName, failedName, err)
			}
			if err := syncDirectory(dataRoot); err != nil {
				return rollbackRestore(dataRoot, now, &operation, oldName, oldWALName, oldSHMName, failedName, err)
			}
		}
		return finishRecoveredRestore(ctx, dataRoot, now, &operation, backup)
	case RestoreNewInstalled, RestoreVerified:
		if !quarantineReady(dataRoot, operation, oldName, oldWALName, oldSHMName) {
			return failNeedsOperator(dataRoot, now, &operation, errors.New("sqlite: restore quarantine missing"))
		}
		matches, matchErr := fileMatchesHash(target, operation.SourceDatabaseSHA256)
		if matchErr != nil || !matches {
			return rollbackRestore(dataRoot, now, &operation, oldName, oldWALName, oldSHMName, failedName,
				errors.New("sqlite: installed restore hash mismatch"))
		}
		return finishRecoveredRestore(ctx, dataRoot, now, &operation, backup)
	default:
		return failNeedsOperator(dataRoot, now, &operation, errors.New("sqlite: unknown restore phase"))
	}
}

func finishRecoveredRestore(ctx context.Context, dataRoot string, now func() time.Time, operation *RestoreOperation, backup BackupManifest) (RestoreOperation, error) {
	target := filepath.Join(dataRoot, databaseFilename)
	verified, err := verifyBackupDatabase(ctx, target)
	if err != nil || verified.schemaVersion != backup.SchemaVersion {
		return *operation, errors.New("sqlite: recovered database verification failed")
	}
	installed := operation.SourceDatabaseSHA256
	operation.InstalledDatabaseSHA256 = &installed
	if operation.Phase != RestoreNewInstalled && operation.Phase != RestoreVerified {
		operation.Phase = RestoreNewInstalled
		if err := advanceRestoreOperation(dataRoot, now, operation); err != nil {
			return *operation, err
		}
	}
	operation.Phase = RestoreVerified
	if err := advanceRestoreOperation(dataRoot, now, operation); err != nil {
		return *operation, err
	}
	operation.Phase = RestoreCommitted
	if err := advanceRestoreOperation(dataRoot, now, operation); err != nil {
		return *operation, err
	}
	return *operation, nil
}

func canonicalMatchesInitial(target string, operation RestoreOperation) bool {
	if !operation.OldDatabasePresent {
		return !pathExists(target)
	}
	if operation.OldDatabaseSHA256 == nil {
		return false
	}
	matches, err := fileMatchesHash(target, *operation.OldDatabaseSHA256)
	return err == nil && matches
}

func quarantineReady(dataRoot string, operation RestoreOperation, oldName, oldWALName, oldSHMName string) bool {
	if operation.OldDatabasePresent {
		if operation.OldDatabaseSHA256 == nil {
			return false
		}
		matches, err := fileMatchesHash(filepath.Join(dataRoot, oldName), *operation.OldDatabaseSHA256)
		if err != nil || !matches {
			return false
		}
	}
	if operation.OldWALPresent && !pathExists(filepath.Join(dataRoot, oldWALName)) {
		return false
	}
	if operation.OldSHMPresent && !pathExists(filepath.Join(dataRoot, oldSHMName)) {
		return false
	}
	return true
}

func fileMatchesHash(path, expected string) (bool, error) {
	if !pathExists(path) {
		return false, nil
	}
	digest, _, err := hashFile(path)
	if err != nil {
		return false, err
	}
	return hex.EncodeToString(digest[:]) == expected, nil
}

func rollbackRestore(dataRoot string, now func() time.Time, operation *RestoreOperation, oldName, oldWALName, oldSHMName, failedName string, cause error) (RestoreOperation, error) {
	reason := "restore-failed"
	operation.FailureReason = &reason
	operation.Phase = RestoreRollingBack
	if err := advanceRestoreOperation(dataRoot, now, operation); err != nil {
		operation.Phase = RestoreFailedNeedsOwner
		return *operation, errors.Join(cause, err)
	}
	target := filepath.Join(dataRoot, databaseFilename)
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !ownerOnlyRegular(info) {
			return failNeedsOperator(dataRoot, now, operation, cause)
		}
		digest, _, hashErr := hashFile(target)
		if hashErr != nil {
			return failNeedsOperator(dataRoot, now, operation, cause)
		}
		value := hex.EncodeToString(digest[:])
		if operation.OldDatabaseSHA256 != nil && value == *operation.OldDatabaseSHA256 && !pathExists(filepath.Join(dataRoot, oldName)) {
			// 旧DBはまだcanonicalにある。移動しない。
		} else if value == operation.SourceDatabaseSHA256 {
			if err := os.Rename(target, filepath.Join(dataRoot, failedName)); err != nil {
				return failNeedsOperator(dataRoot, now, operation, cause)
			}
		} else {
			return failNeedsOperator(dataRoot, now, operation, cause)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return failNeedsOperator(dataRoot, now, operation, cause)
	}
	if operation.OldDatabasePresent && !pathExists(target) {
		if err := os.Rename(filepath.Join(dataRoot, oldName), target); err != nil {
			return failNeedsOperator(dataRoot, now, operation, cause)
		}
	}
	for _, item := range []struct {
		present bool
		from    string
		to      string
	}{{operation.OldWALPresent, oldWALName, databaseFilename + "-wal"}, {operation.OldSHMPresent, oldSHMName, databaseFilename + "-shm"}} {
		if item.present && !pathExists(filepath.Join(dataRoot, item.to)) {
			if err := os.Rename(filepath.Join(dataRoot, item.from), filepath.Join(dataRoot, item.to)); err != nil {
				return failNeedsOperator(dataRoot, now, operation, cause)
			}
		}
	}
	if err := syncDirectory(dataRoot); err != nil {
		return failNeedsOperator(dataRoot, now, operation, cause)
	}
	if operation.OldDatabasePresent {
		digest, _, err := hashFile(target)
		if err != nil || operation.OldDatabaseSHA256 == nil || hex.EncodeToString(digest[:]) != *operation.OldDatabaseSHA256 {
			return failNeedsOperator(dataRoot, now, operation, cause)
		}
	}
	operation.Phase = RestoreRolledBack
	if err := advanceRestoreOperation(dataRoot, now, operation); err != nil {
		return *operation, errors.Join(cause, err)
	}
	return *operation, cause
}

func failNeedsOperator(dataRoot string, now func() time.Time, operation *RestoreOperation, cause error) (RestoreOperation, error) {
	if operation.FailureReason == nil {
		reason := "operator-action-required"
		operation.FailureReason = &reason
	}
	operation.Phase = RestoreFailedNeedsOwner
	if err := advanceRestoreOperation(dataRoot, now, operation); err != nil {
		return *operation, errors.Join(cause, err)
	}
	return *operation, cause
}

func inspectCanonicalArtifacts(target string) (bool, *string, bool, bool, error) {
	present := false
	var oldHash *string
	info, err := os.Lstat(target)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !ownerOnlyRegular(info) {
			return false, nil, false, false, errors.New("sqlite: canonical database is not owner-only")
		}
		digest, _, hashErr := hashFile(target)
		if hashErr != nil {
			return false, nil, false, false, hashErr
		}
		text := hex.EncodeToString(digest[:])
		oldHash = &text
		present = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, nil, false, false, errors.New("sqlite: inspect canonical database")
	}
	wal, err := validateOptionalSidecar(target + "-wal")
	if err != nil {
		return false, nil, false, false, err
	}
	shm, err := validateOptionalSidecar(target + "-shm")
	if err != nil {
		return false, nil, false, false, err
	}
	return present, oldHash, wal, shm, nil
}

func validateOptionalSidecar(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !ownerOnlyRegular(info) {
		return false, errors.New("sqlite: invalid database sidecar")
	}
	return true, nil
}

func copyFileExclusive(ctx context.Context, source, destination string, expectedSize int64) error {
	if expectedSize < 1 {
		return errors.New("sqlite: invalid restore source size")
	}
	input, err := os.Open(source)
	if err != nil {
		return errors.New("sqlite: open restore source")
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.New("sqlite: create staged restore database")
	}
	closed := false
	defer func() {
		if !closed {
			_ = output.Close()
		}
	}()
	buffer := make([]byte, 64*1024)
	var copied int64
	for copied < expectedSize {
		if err := ctx.Err(); err != nil {
			return errors.New("sqlite: restore copy cancelled")
		}
		limit := len(buffer)
		if remaining := expectedSize - copied; remaining < int64(limit) {
			limit = int(remaining)
		}
		read, readErr := input.Read(buffer[:limit])
		if read > 0 {
			written, writeErr := output.Write(buffer[:read])
			if writeErr != nil || written != read {
				return errors.New("sqlite: write staged restore database")
			}
			copied += int64(written)
		}
		if readErr != nil && readErr != io.EOF {
			return errors.New("sqlite: read restore source")
		}
		if readErr == io.EOF && copied != expectedSize {
			return errors.New("sqlite: short restore source")
		}
	}
	var extra [1]byte
	if read, _ := input.Read(extra[:]); read != 0 {
		return errors.New("sqlite: restore source exceeds manifest size")
	}
	if err := output.Sync(); err != nil {
		return errors.New("sqlite: sync staged restore database")
	}
	if err := output.Close(); err != nil {
		return errors.New("sqlite: close staged restore database")
	}
	closed = true
	return nil
}

func publishRestoreOperation(dataRoot string, operation RestoreOperation) error {
	if err := validateRestoreOperation(operation); err != nil {
		return errors.New("sqlite: invalid restore operation manifest")
	}
	partial := filepath.Join(dataRoot, ".restore-"+operation.OperationID+".operation.partial.json")
	final := filepath.Join(dataRoot, "restore-"+operation.OperationID+".operation.json")
	file, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.New("sqlite: create restore operation manifest")
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(operation); err != nil {
		_ = file.Close()
		return errors.New("sqlite: encode restore operation manifest")
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return errors.New("sqlite: sync restore operation manifest")
	}
	if err := file.Close(); err != nil {
		return errors.New("sqlite: close restore operation manifest")
	}
	if err := os.Rename(partial, final); err != nil {
		return errors.New("sqlite: publish restore operation manifest")
	}
	return syncDirectory(dataRoot)
}

func advanceRestoreOperation(dataRoot string, now func() time.Time, operation *RestoreOperation) error {
	operation.Revision++
	operation.UpdatedAtUTC = now().UTC().Format(time.RFC3339Nano)
	return publishRestoreOperation(dataRoot, *operation)
}

func enforceRestoreCaps(dataRoot string) error {
	directory, err := os.Open(dataRoot)
	if err != nil {
		return errors.New("sqlite: open data root")
	}
	defer directory.Close()
	names, err := directory.Readdirnames(257)
	if err != nil && err != io.EOF {
		return errors.New("sqlite: scan restore operations")
	}
	if len(names) > 256 {
		return errors.New("sqlite: data root entry scan cap reached")
	}
	operations := 0
	for _, name := range names {
		if strings.HasPrefix(name, "restore-") && strings.HasSuffix(name, ".operation.json") {
			operations++
			operation, readErr := readRestoreOperation(filepath.Join(dataRoot, name))
			if readErr != nil {
				return readErr
			}
			if operation.Phase != RestoreCommitted && operation.Phase != RestoreRolledBack {
				return errors.New("sqlite: nonterminal restore operation exists")
			}
		}
	}
	if operations >= 4 {
		return errors.New("sqlite: restore artifact cap reached")
	}
	return nil
}

func readRestoreOperation(path string) (RestoreOperation, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !ownerOnlyRegular(info) || info.Size() < 1 || info.Size() > 64*1024 {
		return RestoreOperation{}, errors.New("sqlite: invalid restore operation file")
	}
	file, err := os.Open(path)
	if err != nil {
		return RestoreOperation{}, errors.New("sqlite: open restore operation")
	}
	defer file.Close()
	var operation RestoreOperation
	if err := decodeStrictJSONObject(file, &operation); err != nil {
		return RestoreOperation{}, errors.New("sqlite: decode restore operation")
	}
	if err := validateRestoreOperation(operation); err != nil {
		return RestoreOperation{}, errors.New("sqlite: invalid restore operation")
	}
	return operation, nil
}

func validateRestoreOperation(operation RestoreOperation) error {
	if operation.Format != restoreOperationFormat || operation.FormatVersion != 1 ||
		!backupIDPattern.MatchString(operation.OperationID) || operation.Revision < 1 ||
		!backupIDPattern.MatchString(operation.SourceBackupID) {
		return errors.New("sqlite: invalid restore identity")
	}
	validPhase := false
	for _, phase := range []RestorePhase{
		RestorePrepared, RestoreQuarantining, RestoreOldQuarantined, RestoreInstalling, RestoreNewInstalled,
		RestoreVerified, RestoreCommitted, RestoreRollingBack, RestoreRolledBack, RestoreFailedNeedsOwner,
	} {
		if operation.Phase == phase {
			validPhase = true
			break
		}
	}
	if !validPhase {
		return errors.New("sqlite: invalid restore phase")
	}
	created, err := time.Parse(time.RFC3339Nano, operation.CreatedAtUTC)
	if err != nil || created.Location() != time.UTC {
		return errors.New("sqlite: invalid restore creation time")
	}
	updated, err := time.Parse(time.RFC3339Nano, operation.UpdatedAtUTC)
	if err != nil || updated.Location() != time.UTC || updated.Before(created) {
		return errors.New("sqlite: invalid restore update time")
	}
	if filepath.Base(operation.SourceDatabaseFile) != operation.SourceDatabaseFile ||
		!strings.HasPrefix(operation.SourceDatabaseFile, "catalog-") ||
		!strings.HasSuffix(operation.SourceDatabaseFile, ".sqlite3") ||
		operation.TargetDatabaseFile != databaseFilename ||
		operation.StagedDatabaseFile != ".restore-"+operation.OperationID+".staged.sqlite3" {
		return errors.New("sqlite: invalid restore file reference")
	}
	if !validSHA256Text(operation.SourceDatabaseSHA256) ||
		operation.StagedDatabaseSHA256 != operation.SourceDatabaseSHA256 {
		return errors.New("sqlite: invalid restore source hash")
	}
	if operation.OldDatabasePresent != (operation.OldDatabaseSHA256 != nil) ||
		(operation.OldDatabaseSHA256 != nil && !validSHA256Text(*operation.OldDatabaseSHA256)) {
		return errors.New("sqlite: invalid restore old database hash")
	}
	if operation.InstalledDatabaseSHA256 != nil && *operation.InstalledDatabaseSHA256 != operation.SourceDatabaseSHA256 {
		return errors.New("sqlite: invalid installed restore hash")
	}
	if operation.Phase == RestoreNewInstalled || operation.Phase == RestoreVerified || operation.Phase == RestoreCommitted {
		if operation.InstalledDatabaseSHA256 == nil {
			return errors.New("sqlite: missing installed restore hash")
		}
	}
	if operation.FailureReason != nil {
		reason := *operation.FailureReason
		if len(reason) < 1 || len(reason) > 96 {
			return errors.New("sqlite: invalid restore failure reason")
		}
		for _, character := range reason {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return errors.New("sqlite: invalid restore failure reason")
			}
		}
	}
	switch operation.Phase {
	case RestoreCommitted:
		if operation.FailureReason != nil {
			return errors.New("sqlite: committed restore has failure reason")
		}
	case RestoreRollingBack, RestoreRolledBack, RestoreFailedNeedsOwner:
		if operation.FailureReason == nil {
			return errors.New("sqlite: restore failure phase lacks reason")
		}
	}
	return nil
}

func validateDataRootOnly(dataRoot string) error {
	if !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot {
		return errors.New("sqlite: data root must be canonical absolute path")
	}
	info, err := os.Lstat(dataRoot)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !ownerOnlyDirectory(info) {
		return errors.New("sqlite: data root is not owner-only")
	}
	return validateLocalFilesystem(dataRoot)
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// RestoreOperationBasenameはoperation IDから公開manifestのbasenameを決定的に返す。
func RestoreOperationBasename(id catalogmodel.ID) string {
	return fmt.Sprintf("restore-%s.operation.json", id.String())
}
