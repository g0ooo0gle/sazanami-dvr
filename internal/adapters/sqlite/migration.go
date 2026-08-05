// Package sqliteはcatalogのSQLite永続化、明示migration、backup／restore境界を実装する。
package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	sqlitedriver "github.com/ncruces/go-sqlite3/driver"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

const (
	maxMigrationCount  = 128
	maxMigrationBytes  = 8 * 1024 * 1024
	maxSingleMigration = 1 * 1024 * 1024
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

var migrationNamePattern = regexp.MustCompile(`^([0-9]{4})_([a-z][a-z0-9_]{0,63})\.sql$`)

// DatabaseStateはembedded migrationに対するDBの互換状態を表す。
type DatabaseState string

const (
	StateEmpty               DatabaseState = "EMPTY"
	StateCurrent             DatabaseState = "CURRENT"
	StateBehind              DatabaseState = "BEHIND"
	StateFuture              DatabaseState = "FUTURE"
	StateDrifted             DatabaseState = "DRIFTED"
	StateGapped              DatabaseState = "GAPPED"
	StateUnversionedNonempty DatabaseState = "UNVERSIONED_NONEMPTY"
	StateUnreadable          DatabaseState = "UNREADABLE"
)

// Inspectionはschema authorityのread-onlyな判定結果を返す。
type Inspection struct {
	State          DatabaseState
	CurrentVersion int
	TargetVersion  int
	Reason         string
}

// MigrationRequestは既存DBの更新前backupへ必要な再現性情報をまとめる。
// 空DBではbackupを作らないが、同じ明示command入力を使う。
type MigrationRequest struct {
	AppliedAt      time.Time
	BackupID       catalogmodel.ID
	ProductVersion string
	ProductCommit  string
	Now            func() time.Time
}

// MigrationResultは更新後のDB状態と、既存DBで作成した事前backupを返す。
type MigrationResult struct {
	Inspection Inspection
	Backup     *BackupManifest
}

type migration struct {
	version  int
	name     string
	content  string
	checksum [32]byte
}

func embeddedMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("sqlite: read embedded migrations: %w", err)
	}
	if len(entries) == 0 || len(entries) > maxMigrationCount {
		return nil, errors.New("sqlite: migration count outside accepted limit")
	}
	result := make([]migration, 0, len(entries))
	total := 0
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, errors.New("sqlite: migration directory is not allowed")
		}
		match := migrationNamePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, fmt.Errorf("sqlite: invalid migration name %q", entry.Name())
		}
		var version int
		if _, err := fmt.Sscanf(match[1], "%d", &version); err != nil {
			return nil, errors.New("sqlite: invalid migration version")
		}
		content, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("sqlite: read migration: %w", err)
		}
		if len(content) == 0 || len(content) > maxSingleMigration {
			return nil, errors.New("sqlite: migration size outside accepted limit")
		}
		if strings.HasPrefix(string(content), "\ufeff") || strings.Contains(string(content), "\r") {
			return nil, errors.New("sqlite: migration encoding is not canonical")
		}
		total += len(content)
		if total > maxMigrationBytes {
			return nil, errors.New("sqlite: total migration size outside accepted limit")
		}
		result = append(result, migration{version: version, name: match[2], content: string(content), checksum: sha256.Sum256(content)})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].version < result[j].version })
	for i := range result {
		if result[i].version != i+1 {
			return nil, errors.New("sqlite: embedded migration versions are not contiguous")
		}
	}
	return result, nil
}

func inspect(ctx context.Context, database *sql.DB) (Inspection, error) {
	migrations, err := embeddedMigrations()
	if err != nil {
		return Inspection{State: StateUnreadable}, err
	}
	result := Inspection{TargetVersion: migrations[len(migrations)-1].version}
	var migrationTable int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='schema_migrations'`).Scan(&migrationTable); err != nil {
		result.State = StateUnreadable
		return result, fmt.Errorf("sqlite: inspect schema metadata: %w", err)
	}
	if migrationTable == 0 {
		var userObjects int
		if err := database.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`).Scan(&userObjects); err != nil {
			result.State = StateUnreadable
			return result, fmt.Errorf("sqlite: inspect user objects: %w", err)
		}
		if userObjects == 0 {
			result.State = StateEmpty
			return result, nil
		}
		result.State = StateUnversionedNonempty
		result.Reason = "migration-table-missing"
		return result, nil
	}

	rows, err := database.QueryContext(ctx, `SELECT version, name, checksum, applied_at_utc_ms FROM schema_migrations ORDER BY version`)
	if err != nil {
		result.State = StateUnreadable
		return result, fmt.Errorf("sqlite: inspect migrations: %w", err)
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		var version int
		var name string
		var checksum []byte
		var appliedAtUTCMS int64
		if err := rows.Scan(&version, &name, &checksum, &appliedAtUTCMS); err != nil {
			result.State = StateUnreadable
			return result, fmt.Errorf("sqlite: scan migration: %w", err)
		}
		index++
		result.CurrentVersion = version
		if version != index {
			result.State = StateGapped
			result.Reason = "non-contiguous-applied-version"
			return result, nil
		}
		if appliedAtUTCMS < 0 {
			result.State = StateGapped
			result.Reason = "invalid-applied-timestamp"
			return result, nil
		}
		if version > len(migrations) {
			result.State = StateFuture
			result.Reason = "applied-version-newer-than-binary"
			return result, nil
		}
		expected := migrations[version-1]
		if name != expected.name || !bytesEqual(checksum, expected.checksum[:]) {
			result.State = StateDrifted
			result.Reason = "migration-name-or-checksum-drift"
			return result, nil
		}
	}
	if err := rows.Err(); err != nil {
		result.State = StateUnreadable
		return result, fmt.Errorf("sqlite: iterate migrations: %w", err)
	}
	if index == len(migrations) {
		result.State = StateCurrent
	} else {
		result.State = StateBehind
	}
	return result, nil
}

// InspectDatabaseはDB fileを作成・変更せず、指定data rootのschema状態をread-onlyで返す。
func InspectDatabase(ctx context.Context, dataRoot string) (Inspection, error) {
	migrations, err := embeddedMigrations()
	if err != nil {
		return Inspection{State: StateUnreadable}, err
	}
	if err := validateDataRootOnly(dataRoot); err != nil {
		return Inspection{State: StateUnreadable, TargetVersion: migrations[len(migrations)-1].version}, err
	}
	path := filepath.Join(dataRoot, databaseFilename)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Inspection{State: StateEmpty, TargetVersion: migrations[len(migrations)-1].version}, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !ownerOnlyRegular(info) {
		return Inspection{State: StateUnreadable, TargetVersion: migrations[len(migrations)-1].version}, errors.New("sqlite: invalid database file")
	}
	database, err := sqlitedriver.Open(buildReaderDSN(path))
	if err != nil {
		return Inspection{State: StateUnreadable, TargetVersion: migrations[len(migrations)-1].version}, sanitize("open-inspector", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	defer database.Close()
	inspection, inspectErr := inspect(ctx, database)
	if inspectErr != nil {
		return inspection, errors.New("sqlite: inspect database failed")
	}
	return inspection, nil
}

// MigrateDatabaseはdata rootの単一owner lockを保持し、明示command用writerだけでmigrationを実行する。
func MigrateDatabase(ctx context.Context, dataRoot string, appliedAtUTCMS int64) (Inspection, error) {
	if ctx == nil || appliedAtUTCMS < 0 {
		return Inspection{State: StateUnreadable}, errors.New("sqlite: invalid migration operation")
	}
	ownerLock, err := acquireOwnerLock(dataRoot)
	if err != nil {
		return Inspection{State: StateUnreadable}, err
	}
	defer releaseOwnerLock(ownerLock)
	database, err := openWriter(ctx, dataRoot)
	if err != nil {
		return Inspection{State: StateUnreadable}, err
	}
	defer database.Close()
	inspection, err := migrate(ctx, database, appliedAtUTCMS)
	if err != nil {
		if inspection.State == StateBehind {
			return inspection, errors.New("sqlite: pre-migration backup required")
		}
		return inspection, errors.New("sqlite: migration operation failed")
	}
	if err := verifyPragmas(ctx, database, false); err != nil {
		return inspection, errors.New("sqlite: migration pragma readback failed")
	}
	return inspection, nil
}

// MigrateDatabaseWithBackupはBEHIND状態のDBを、検証済みbackupの作成後だけ更新する。
// 通常起動からは呼ばず、明示的なdb migrate commandだけが利用する。
func MigrateDatabaseWithBackup(ctx context.Context, dataRoot string, request MigrationRequest) (MigrationResult, error) {
	var result MigrationResult
	if ctx == nil || request.AppliedAt.IsZero() || request.Now == nil {
		return result, errors.New("sqlite: invalid migration operation")
	}
	ownerLock, err := acquireOwnerLock(dataRoot)
	if err != nil {
		return result, err
	}
	defer releaseOwnerLock(ownerLock)
	database, err := openWriter(ctx, dataRoot)
	if err != nil {
		return result, err
	}
	defer database.Close()

	before, err := inspect(ctx, database)
	if err != nil {
		return result, errors.New("sqlite: migration inspection failed")
	}
	allowBehind := false
	if before.State == StateBehind {
		from, to := before.CurrentVersion, before.TargetVersion
		manifest, backupErr := createBackup(ctx, dataRoot, database, BackupRequest{
			ID: request.BackupID, Purpose: "pre_migration", MigrationFromSchema: &from, MigrationToSchema: &to,
			StartedAt: request.AppliedAt.UTC(), ProductVersion: request.ProductVersion,
			ProductCommit: request.ProductCommit, Now: request.Now,
		})
		if backupErr != nil {
			return result, errors.New("sqlite: pre-migration backup failed")
		}
		result.Backup = &manifest
		allowBehind = true
	}
	inspection, err := migrateKnown(ctx, database, request.AppliedAt.UTC().UnixMilli(), allowBehind)
	result.Inspection = inspection
	if err != nil {
		return result, errors.New("sqlite: migration operation failed")
	}
	if err := verifyPragmas(ctx, database, false); err != nil {
		return result, errors.New("sqlite: migration pragma readback failed")
	}
	return result, nil
}

// migrateは空DBだけを、全pending migrationを1 transactionにまとめてCURRENTへ進める。
// 既存DBの更新はMigrateDatabaseWithBackupを経由しなければ拒否する。
func migrate(ctx context.Context, database *sql.DB, appliedAtUTCMS int64) (Inspection, error) {
	return migrateKnown(ctx, database, appliedAtUTCMS, false)
}

// migrateKnownはbackup検証済みの場合だけBEHINDからの更新を許可する。
func migrateKnown(ctx context.Context, database *sql.DB, appliedAtUTCMS int64, allowBehind bool) (Inspection, error) {
	if ctx == nil || database == nil || appliedAtUTCMS < 0 {
		return Inspection{State: StateUnreadable}, errors.New("sqlite: invalid migration input")
	}
	before, err := inspect(ctx, database)
	if err != nil {
		return before, err
	}
	switch before.State {
	case StateCurrent, StateEmpty:
		// Fresh databaseだけはbackup対象がない。
	case StateBehind:
		if allowBehind {
			break
		}
		return before, errors.New("sqlite: pre-migration backup is required before upgrading")
	default:
		return before, fmt.Errorf("sqlite: migration refused for state %s", before.State)
	}
	if err := verifyDatabaseHealth(ctx, database); err != nil {
		return before, err
	}
	if before.State == StateCurrent {
		return before, nil
	}
	var backendCountBefore, syncCountBefore int
	if before.State == StateBehind {
		if err := database.QueryRowContext(ctx, `SELECT count(*) FROM backend_instances`).Scan(&backendCountBefore); err != nil {
			return before, errors.New("sqlite: count pre-migration backends")
		}
		if err := database.QueryRowContext(ctx, `SELECT count(*) FROM catalog_syncs`).Scan(&syncCountBefore); err != nil {
			return before, errors.New("sqlite: count pre-migration syncs")
		}
	}
	migrations, err := embeddedMigrations()
	if err != nil {
		return before, err
	}
	tx, err := database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return before, fmt.Errorf("sqlite: begin migration: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	for _, item := range migrations {
		if item.version <= before.CurrentVersion {
			continue
		}
		if _, err := tx.ExecContext(ctx, item.content); err != nil {
			return before, fmt.Errorf("sqlite: apply migration %04d: %w", item.version, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, name, checksum, applied_at_utc_ms) VALUES (?, ?, ?, ?)`,
			item.version, item.name, item.checksum[:], appliedAtUTCMS); err != nil {
			return before, fmt.Errorf("sqlite: record migration %04d: %w", item.version, err)
		}
	}
	if before.State == StateBehind {
		var backendCountAfter, syncCountAfter, violations int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM backend_instances`).Scan(&backendCountAfter); err != nil || backendCountAfter != backendCountBefore {
			return before, errors.New("sqlite: backend count changed during migration")
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM catalog_syncs`).Scan(&syncCountAfter); err != nil || syncCountAfter != syncCountBefore {
			return before, errors.New("sqlite: sync count changed during migration")
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
			return before, errors.New("sqlite: migration foreign key check failed")
		}
	}
	if err := tx.Commit(); err != nil {
		return before, fmt.Errorf("sqlite: commit migration: %w", err)
	}
	committed = true
	after, err := inspect(ctx, database)
	if err != nil {
		return after, err
	}
	if after.State != StateCurrent {
		return after, fmt.Errorf("sqlite: migration readback state %s", after.State)
	}
	if err := verifyDatabaseHealth(ctx, database); err != nil {
		return after, err
	}
	return after, nil
}

func verifyDatabaseHealth(ctx context.Context, database *sql.DB) error {
	var integrity string
	if err := database.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		return errors.New("sqlite: database integrity check failed")
	}
	var violations int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		return errors.New("sqlite: database foreign key check failed")
	}
	return nil
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var different byte
	for i := range left {
		different |= left[i] ^ right[i]
	}
	return different == 0
}
