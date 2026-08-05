package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	sqlite3 "github.com/ncruces/go-sqlite3"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

const (
	backupManifestFormat = "sazanami.catalog-backup-manifest"
	backupDriverModule   = "github.com/ncruces/go-sqlite3"
	backupDriverVersion  = "v0.35.2"
	backupDriverSum      = "h1:YOoumI7tkxMIm1MBrkucRb1qtvAPEh8RrZtv6U+2aLs="
	backupPageStep       = 128
	maxCompletedBackups  = 32
	maxBackupEntries     = 64
)

var productCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var backupIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// BackupRequestは1回のbackup artifactへ固定する再現性情報を保持する。
type BackupRequest struct {
	ID                  catalogmodel.ID
	Purpose             string
	MigrationFromSchema *int
	MigrationToSchema   *int
	StartedAt           time.Time
	ProductVersion      string
	ProductCommit       string
	Now                 func() time.Time
}

// BackupManifestはDB fileと対応するstrict JSON manifest v1である。
type BackupManifest struct {
	Format               string `json:"format"`
	FormatVersion        int    `json:"format_version"`
	BackupID             string `json:"backup_id"`
	State                string `json:"state"`
	Purpose              string `json:"purpose"`
	MigrationFromSchema  *int   `json:"migration_from_schema"`
	MigrationToSchema    *int   `json:"migration_to_schema"`
	StartedAtUTC         string `json:"started_at_utc"`
	CompletedAtUTC       string `json:"completed_at_utc"`
	ProductVersion       string `json:"product_version"`
	ProductCommit        string `json:"product_commit"`
	DatabaseFile         string `json:"database_file"`
	DatabaseSHA256       string `json:"database_sha256"`
	DatabaseBytes        int64  `json:"database_bytes"`
	SchemaVersion        int    `json:"schema_version"`
	SQLiteVersion        string `json:"sqlite_version"`
	PageSize             int    `json:"page_size"`
	PageCount            int    `json:"page_count"`
	JournalMode          string `json:"journal_mode"`
	Synchronous          string `json:"synchronous"`
	ForeignKeys          bool   `json:"foreign_keys"`
	IntegrityCheck       string `json:"integrity_check"`
	ForeignKeyViolations int    `json:"foreign_key_violations"`
	DriverModule         string `json:"driver_module"`
	DriverVersion        string `json:"driver_version"`
	DriverModuleSum      string `json:"driver_module_sum"`
	CGOEnabled           bool   `json:"cgo_enabled"`
}

// CreateBackupはincremental online backupを検証し、DBの後にmanifestを原子的にpublishする。
func (store *Store) CreateBackup(ctx context.Context, request BackupRequest) (BackupManifest, error) {
	if store == nil || store.writer == nil || store.root == "" {
		return BackupManifest{}, errors.New("sqlite: backup store is not open")
	}
	return createBackup(ctx, store.root, store.writer, request)
}

// createBackupはowner lockを持つ呼び出し元が指定したwriterから、検証済みbackupを作る。
// 明示migrationはStoreを開けないBEHIND状態でも同じbackup実装を再利用する。
func createBackup(ctx context.Context, dataRoot string, database *sql.DB, request BackupRequest) (BackupManifest, error) {
	if ctx == nil || database == nil || dataRoot == "" {
		return BackupManifest{}, errors.New("sqlite: invalid backup source")
	}
	if err := validateBackupRequest(request); err != nil {
		return BackupManifest{}, err
	}
	backupRoot, err := prepareBackupRoot(dataRoot)
	if err != nil {
		return BackupManifest{}, err
	}
	if err := enforceBackupCaps(backupRoot); err != nil {
		return BackupManifest{}, err
	}

	stamp := request.StartedAt.UTC().Format("20060102T150405.000000000Z")
	id := request.ID.String()
	finalDatabase := fmt.Sprintf("catalog-%s-%s.sqlite3", stamp, id)
	finalManifest := fmt.Sprintf("catalog-%s-%s.manifest.json", stamp, id)
	partialDatabase := ".catalog-" + id + ".partial.sqlite3"
	partialManifest := ".catalog-" + id + ".partial.manifest.json"
	source, err := readSourceMetadata(ctx, database)
	if err != nil {
		return BackupManifest{}, err
	}
	if err := requireBackupFreeSpace(backupRoot, source.pageSize, source.pageCount); err != nil {
		return BackupManifest{}, err
	}
	partialPath := filepath.Join(backupRoot, partialDatabase)
	if err := createExclusiveEmpty(partialPath); err != nil {
		return BackupManifest{}, err
	}
	if err := runIncrementalBackup(ctx, database, partialPath); err != nil {
		return BackupManifest{}, err
	}
	if err := finalizeBackupDatabase(ctx, partialPath); err != nil {
		return BackupManifest{}, err
	}
	if err := syncRegularFile(partialPath); err != nil {
		return BackupManifest{}, err
	}
	verified, err := verifyBackupDatabase(ctx, partialPath)
	if err != nil {
		return BackupManifest{}, err
	}
	if request.Purpose == "pre_migration" && verified.schemaVersion != *request.MigrationFromSchema {
		return BackupManifest{}, errors.New("sqlite: migration backup schema mismatch")
	}
	digest, size, err := hashFile(partialPath)
	if err != nil {
		return BackupManifest{}, err
	}
	completed := request.Now().UTC()
	if completed.Before(request.StartedAt.UTC()) {
		completed = request.StartedAt.UTC()
	}
	manifest := BackupManifest{
		Format: backupManifestFormat, FormatVersion: 1, BackupID: id, State: "complete",
		Purpose: request.Purpose, MigrationFromSchema: request.MigrationFromSchema,
		MigrationToSchema: request.MigrationToSchema, StartedAtUTC: request.StartedAt.UTC().Format(time.RFC3339Nano),
		CompletedAtUTC: completed.Format(time.RFC3339Nano), ProductVersion: request.ProductVersion,
		ProductCommit: request.ProductCommit, DatabaseFile: finalDatabase, DatabaseSHA256: hex.EncodeToString(digest[:]),
		DatabaseBytes: size, SchemaVersion: verified.schemaVersion, SQLiteVersion: verified.sqliteVersion,
		PageSize: verified.pageSize, PageCount: verified.pageCount, JournalMode: source.journalMode,
		Synchronous: source.synchronous, ForeignKeys: source.foreignKeys, IntegrityCheck: "ok",
		ForeignKeyViolations: 0, DriverModule: backupDriverModule, DriverVersion: backupDriverVersion,
		DriverModuleSum: backupDriverSum, CGOEnabled: false,
	}
	if err := writeManifestExclusive(filepath.Join(backupRoot, partialManifest), manifest); err != nil {
		return BackupManifest{}, err
	}
	if err := os.Rename(partialPath, filepath.Join(backupRoot, finalDatabase)); err != nil {
		return BackupManifest{}, errors.New("sqlite: publish backup database")
	}
	if err := syncDirectory(backupRoot); err != nil {
		return BackupManifest{}, err
	}
	if err := os.Rename(filepath.Join(backupRoot, partialManifest), filepath.Join(backupRoot, finalManifest)); err != nil {
		return BackupManifest{}, errors.New("sqlite: publish backup manifest")
	}
	if err := syncDirectory(backupRoot); err != nil {
		return BackupManifest{}, err
	}
	if _, err := VerifyBackup(ctx, dataRoot, finalManifest); err != nil {
		return BackupManifest{}, err
	}
	return manifest, nil
}

// VerifyBackupはfinal manifestをstrict decodeし、basename、hash、size、schema、integrity、FKを再確認する。
func VerifyBackup(ctx context.Context, dataRoot, manifestBasename string) (BackupManifest, error) {
	if filepath.Base(manifestBasename) != manifestBasename || !strings.HasSuffix(manifestBasename, ".manifest.json") {
		return BackupManifest{}, errors.New("sqlite: invalid backup manifest basename")
	}
	backupRoot, err := existingBackupRoot(dataRoot)
	if err != nil {
		return BackupManifest{}, err
	}
	manifestPath := filepath.Join(backupRoot, manifestBasename)
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil || manifestInfo.Mode()&os.ModeSymlink != 0 || !ownerOnlyRegular(manifestInfo) ||
		manifestInfo.Size() < 1 || manifestInfo.Size() > 64*1024 {
		return BackupManifest{}, errors.New("sqlite: invalid backup manifest file")
	}
	file, err := os.Open(manifestPath)
	if err != nil {
		return BackupManifest{}, errors.New("sqlite: open backup manifest")
	}
	defer file.Close()
	var manifest BackupManifest
	if err := decodeStrictJSONObject(file, &manifest); err != nil {
		return BackupManifest{}, errors.New("sqlite: decode backup manifest")
	}
	if err := validateManifest(manifest, manifestBasename); err != nil {
		return BackupManifest{}, err
	}
	databasePath := filepath.Join(backupRoot, manifest.DatabaseFile)
	digest, size, err := hashFile(databasePath)
	if err != nil {
		return BackupManifest{}, err
	}
	if hex.EncodeToString(digest[:]) != manifest.DatabaseSHA256 || size != manifest.DatabaseBytes {
		return BackupManifest{}, errors.New("sqlite: backup hash or size mismatch")
	}
	verified, err := verifyBackupDatabase(ctx, databasePath)
	if err != nil {
		return BackupManifest{}, err
	}
	if verified.schemaVersion != manifest.SchemaVersion || verified.sqliteVersion != manifest.SQLiteVersion ||
		verified.pageSize != manifest.PageSize || verified.pageCount != manifest.PageCount {
		return BackupManifest{}, errors.New("sqlite: backup metadata mismatch")
	}
	return manifest, nil
}

// FindBackupManifestはUUIDv4 backup IDに一致するfinal manifestをbounded scanで一意に解決する。
func FindBackupManifest(dataRoot string, id catalogmodel.ID) (string, error) {
	root, err := existingBackupRoot(dataRoot)
	if err != nil {
		return "", err
	}
	suffix := "-" + id.String() + ".manifest.json"
	directory, err := os.Open(root)
	if err != nil {
		return "", errors.New("sqlite: open backup root")
	}
	defer directory.Close()
	names, err := directory.Readdirnames(maxBackupEntries + 1)
	if err != nil && err != io.EOF {
		return "", errors.New("sqlite: scan backup root")
	}
	if len(names) > maxBackupEntries {
		return "", errors.New("sqlite: backup entry cap exceeded")
	}
	match := ""
	for _, name := range names {
		if strings.HasPrefix(name, "catalog-") && strings.HasSuffix(name, suffix) {
			if match != "" {
				return "", errors.New("sqlite: duplicate backup id")
			}
			match = name
		}
	}
	if match == "" {
		return "", errors.New("sqlite: backup id not found")
	}
	return match, nil
}

type sourceMetadata struct {
	journalMode string
	synchronous string
	foreignKeys bool
	pageSize    int
	pageCount   int
}

type verifiedDatabase struct {
	schemaVersion int
	sqliteVersion string
	pageSize      int
	pageCount     int
}

func runIncrementalBackup(ctx context.Context, database interface {
	Conn(context.Context) (*sql.Conn, error)
}, destination string) error {
	connection, err := database.Conn(ctx)
	if err != nil {
		return sanitize("acquire-backup-connection", err)
	}
	defer connection.Close()
	destinationURI := (&url.URL{Scheme: "file", Path: destination}).String()
	return connection.Raw(func(raw any) error {
		driverConnection, ok := raw.(sqlitedriver.Conn)
		if !ok {
			return errors.New("sqlite: unexpected driver connection")
		}
		backup, err := driverConnection.Raw().BackupInit("main", destinationURI)
		if err != nil {
			return sanitize("initialize-backup", err)
		}
		closed := false
		defer func() {
			if !closed {
				_ = backup.Close()
			}
		}()
		busyRetries := 0
		for {
			if err := ctx.Err(); err != nil {
				return errors.New("sqlite: backup cancelled")
			}
			done, stepErr := backup.Step(backupPageStep)
			if stepErr != nil {
				if errors.Is(stepErr, sqlite3.BUSY) && busyRetries < 3 {
					busyRetries++
					timer := time.NewTimer(100 * time.Millisecond)
					select {
					case <-ctx.Done():
						timer.Stop()
						return errors.New("sqlite: backup cancelled")
					case <-timer.C:
					}
					continue
				}
				return sanitize("step-backup", stepErr)
			}
			if done {
				if err := backup.Close(); err != nil {
					return sanitize("close-backup", err)
				}
				closed = true
				return nil
			}
		}
	})
}

func readSourceMetadata(ctx context.Context, database *sql.DB) (sourceMetadata, error) {
	var result sourceMetadata
	var foreign int
	if err := database.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&result.journalMode); err != nil {
		return result, sanitize("read-backup-journal", err)
	}
	var synchronous int
	if err := database.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&synchronous); err != nil {
		return result, sanitize("read-backup-synchronous", err)
	}
	if synchronous != 2 {
		return result, errors.New("sqlite: backup source is not synchronous FULL")
	}
	result.synchronous = "full"
	if err := database.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreign); err != nil {
		return result, sanitize("read-backup-foreign-keys", err)
	}
	result.foreignKeys = foreign == 1
	if result.journalMode != "wal" || !result.foreignKeys {
		return result, errors.New("sqlite: backup source pragma mismatch")
	}
	if err := database.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&result.pageSize); err != nil {
		return result, sanitize("read-backup-page-size", err)
	}
	if err := database.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&result.pageCount); err != nil {
		return result, sanitize("read-backup-page-count", err)
	}
	if result.pageSize < 512 || result.pageSize > 65536 || result.pageCount < 1 {
		return result, errors.New("sqlite: backup source page values invalid")
	}
	return result, nil
}

func verifyBackupDatabase(ctx context.Context, path string) (verifiedDatabase, error) {
	var result verifiedDatabase
	query := url.Values{"mode": {"ro"}, "_pragma": {"foreign_keys(1)", "query_only(1)"}}
	database, err := sqlitedriver.Open((&url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}).String())
	if err != nil {
		return result, sanitize("open-backup-readonly", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	defer database.Close()
	var integrity string
	if err := database.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		return result, errors.New("sqlite: backup integrity check failed")
	}
	var violations int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		return result, errors.New("sqlite: backup foreign key check failed")
	}
	inspection, err := inspect(ctx, database)
	if err != nil || (inspection.State != StateCurrent && inspection.State != StateBehind) {
		return result, errors.New("sqlite: backup schema is not a known complete version")
	}
	result.schemaVersion = inspection.CurrentVersion
	if err := database.QueryRowContext(ctx, `SELECT sqlite_version()`).Scan(&result.sqliteVersion); err != nil {
		return result, sanitize("read-backup-sqlite-version", err)
	}
	if err := database.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&result.pageSize); err != nil {
		return result, sanitize("read-backup-page-size", err)
	}
	if err := database.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&result.pageCount); err != nil {
		return result, sanitize("read-backup-page-count", err)
	}
	if result.pageSize < 512 || result.pageSize > 65536 || result.pageSize&(result.pageSize-1) != 0 || result.pageCount < 1 {
		return result, errors.New("sqlite: backup page metadata invalid")
	}
	return result, nil
}

func finalizeBackupDatabase(ctx context.Context, path string) error {
	query := url.Values{"_pragma": {"busy_timeout(5000)", "foreign_keys(1)", "journal_mode(delete)", "synchronous(full)"}}
	database, err := sqlitedriver.Open((&url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}).String())
	if err != nil {
		return sanitize("open-backup-finalizer", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	var mode string
	if err := database.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "delete" {
		_ = database.Close()
		return errors.New("sqlite: backup journal finalization failed")
	}
	if err := database.Close(); err != nil {
		return sanitize("close-backup-finalizer", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		sidecar := path + suffix
		info, statErr := os.Lstat(sidecar)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil || !ownerOnlyRegular(info) || info.Size() != 0 {
			return errors.New("sqlite: backup sidecar is not empty")
		}
		if err := os.Remove(sidecar); err != nil {
			return errors.New("sqlite: remove empty backup sidecar")
		}
	}
	return nil
}

func validateBackupRequest(request BackupRequest) error {
	if request.Purpose != "manual" && request.Purpose != "pre_migration" {
		return errors.New("sqlite: invalid backup purpose")
	}
	if request.StartedAt.IsZero() || request.Now == nil || len(request.ProductVersion) == 0 || len(request.ProductVersion) > 128 || !productCommitPattern.MatchString(request.ProductCommit) {
		return errors.New("sqlite: invalid backup metadata")
	}
	if request.Purpose == "manual" && (request.MigrationFromSchema != nil || request.MigrationToSchema != nil) {
		return errors.New("sqlite: manual backup has migration metadata")
	}
	if request.Purpose == "pre_migration" && (request.MigrationFromSchema == nil || request.MigrationToSchema == nil || *request.MigrationFromSchema < 0 || *request.MigrationToSchema <= *request.MigrationFromSchema) {
		return errors.New("sqlite: invalid migration backup metadata")
	}
	return nil
}

func validateManifest(manifest BackupManifest, basename string) error {
	if manifest.Format != backupManifestFormat || manifest.FormatVersion != 1 || manifest.State != "complete" ||
		manifest.DriverModule != backupDriverModule || manifest.DriverVersion != backupDriverVersion ||
		manifest.DriverModuleSum != backupDriverSum || manifest.CGOEnabled || manifest.IntegrityCheck != "ok" ||
		manifest.ForeignKeyViolations != 0 || !manifest.ForeignKeys || manifest.DatabaseBytes < 1 ||
		!productCommitPattern.MatchString(manifest.ProductCommit) || !backupIDPattern.MatchString(manifest.BackupID) ||
		manifest.JournalMode != "wal" || manifest.Synchronous != "full" {
		return errors.New("sqlite: invalid backup manifest fields")
	}
	if filepath.Base(manifest.DatabaseFile) != manifest.DatabaseFile || strings.Contains(manifest.DatabaseFile, string(filepath.Separator)) ||
		!strings.HasPrefix(manifest.DatabaseFile, "catalog-") || !strings.HasSuffix(manifest.DatabaseFile, ".sqlite3") ||
		!validSHA256Text(manifest.DatabaseSHA256) {
		return errors.New("sqlite: invalid backup database reference")
	}
	wantPrefix := strings.TrimSuffix(basename, ".manifest.json")
	if strings.TrimSuffix(manifest.DatabaseFile, ".sqlite3") != wantPrefix || !strings.HasSuffix(wantPrefix, "-"+manifest.BackupID) {
		return errors.New("sqlite: backup pair name mismatch")
	}
	if manifest.Purpose == "pre_migration" &&
		(manifest.MigrationFromSchema == nil || manifest.SchemaVersion != *manifest.MigrationFromSchema) {
		return errors.New("sqlite: migration backup schema mismatch")
	}
	started, err := time.Parse(time.RFC3339Nano, manifest.StartedAtUTC)
	if err != nil || started.Location() != time.UTC {
		return errors.New("sqlite: invalid backup start time")
	}
	completed, err := time.Parse(time.RFC3339Nano, manifest.CompletedAtUTC)
	if err != nil || completed.Location() != time.UTC || completed.Before(started) {
		return errors.New("sqlite: invalid backup completion time")
	}
	return validateBackupRequest(BackupRequest{
		Purpose: manifest.Purpose, MigrationFromSchema: manifest.MigrationFromSchema,
		MigrationToSchema: manifest.MigrationToSchema, StartedAt: started, ProductVersion: manifest.ProductVersion,
		ProductCommit: manifest.ProductCommit, Now: time.Now,
	})
}

func validSHA256Text(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func prepareBackupRoot(dataRoot string) (string, error) {
	if _, err := prepareDatabasePath(dataRoot); err != nil {
		return "", err
	}
	root := filepath.Join(dataRoot, "backups")
	if err := os.Mkdir(root, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", errors.New("sqlite: create backup root")
	}
	return existingBackupRoot(dataRoot)
}

func existingBackupRoot(dataRoot string) (string, error) {
	if !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot {
		return "", errors.New("sqlite: invalid data root")
	}
	root := filepath.Join(dataRoot, "backups")
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !ownerOnlyDirectory(info) {
		return "", errors.New("sqlite: backup root is not owner-only")
	}
	return root, nil
}

func requireBackupFreeSpace(root string, pageSize, pageCount int) error {
	if pageSize < 1 || pageCount < 0 {
		return errors.New("sqlite: invalid backup capacity input")
	}
	if pageCount != 0 && uint64(pageSize) > ^uint64(0)/uint64(pageCount) {
		return errors.New("sqlite: backup capacity overflow")
	}
	required := uint64(pageSize) * uint64(pageCount)
	const margin = uint64(64 * 1024 * 1024)
	if required > ^uint64(0)-margin {
		return errors.New("sqlite: backup capacity overflow")
	}
	available, err := availableFilesystemBytes(root)
	if err != nil {
		return err
	}
	if available < required+margin {
		return errors.New("sqlite: insufficient backup free space")
	}
	return nil
}

func enforceBackupCaps(root string) error {
	directory, err := os.Open(root)
	if err != nil {
		return errors.New("sqlite: open backup root")
	}
	defer directory.Close()
	names, err := directory.Readdirnames(maxBackupEntries + 1)
	if err != nil && err != io.EOF {
		return errors.New("sqlite: scan backup root")
	}
	if len(names) >= maxBackupEntries {
		return errors.New("sqlite: backup entry cap reached")
	}
	completed := 0
	for _, name := range names {
		if strings.HasPrefix(name, "catalog-") && strings.HasSuffix(name, ".manifest.json") {
			completed++
		}
	}
	if completed >= maxCompletedBackups {
		return errors.New("sqlite: completed backup cap reached")
	}
	return nil
}

func createExclusiveEmpty(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return errors.New("sqlite: create partial backup")
	}
	return file.Close()
}

func writeManifestExclusive(path string, manifest BackupManifest) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.New("sqlite: create partial manifest")
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(manifest); err != nil {
		_ = file.Close()
		return errors.New("sqlite: encode backup manifest")
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return errors.New("sqlite: sync backup manifest")
	}
	if err := file.Close(); err != nil {
		return errors.New("sqlite: close backup manifest")
	}
	return nil
}

func syncRegularFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return errors.New("sqlite: open backup for sync")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !ownerOnlyRegular(info) {
		return errors.New("sqlite: backup file mode mismatch")
	}
	if err := file.Sync(); err != nil {
		return errors.New("sqlite: sync backup database")
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return errors.New("sqlite: open directory for sync")
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return errors.New("sqlite: sync backup directory")
	}
	return nil
}

func hashFile(path string) ([32]byte, int64, error) {
	lstat, err := os.Lstat(path)
	if err != nil || lstat.Mode()&os.ModeSymlink != 0 || !ownerOnlyRegular(lstat) || lstat.Size() < 1 {
		return [32]byte{}, 0, errors.New("sqlite: invalid backup file")
	}
	file, err := os.Open(path)
	if err != nil {
		return [32]byte{}, 0, errors.New("sqlite: open backup for hash")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !ownerOnlyRegular(info) || info.Size() < 1 {
		return [32]byte{}, 0, errors.New("sqlite: invalid backup file")
	}
	hash := sha256.New()
	written, err := io.CopyBuffer(hash, file, make([]byte, 64*1024))
	if err != nil || written != info.Size() {
		return [32]byte{}, 0, errors.New("sqlite: hash backup file")
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest, written, nil
}
