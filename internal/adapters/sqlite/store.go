package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
)

const databaseFilename = "catalog.sqlite3"

// Storeは用途を分離したwriter／reader poolを所有するSQLite adapterである。
type Store struct {
	writer           *sql.DB
	reader           *sql.DB
	root             string
	ownerLock        *os.File
	reservationPower sync.Mutex
}

// LockPostRecordingPowerDecisionは次予約の最終確認から電源動作開始まで、予約変更を待たせる。
func (store *Store) LockPostRecordingPowerDecision() {
	store.reservationPower.Lock()
}

// UnlockPostRecordingPowerDecisionは待機中の予約変更を再開する。
func (store *Store) UnlockPostRecordingPowerDecision() {
	store.reservationPower.Unlock()
}

func openWriter(ctx context.Context, dataRoot string) (*sql.DB, error) {
	path, err := prepareDatabasePath(dataRoot)
	if err != nil {
		return nil, err
	}
	if err := restrictDatabaseSidecars(path); err != nil {
		return nil, err
	}
	database, err := sqlitedriver.Open(buildWriterDSN(path))
	if err != nil {
		return nil, sanitize("open-writer", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, sanitize("ping-writer", err)
	}
	if err := verifyPragmas(ctx, database, false); err != nil {
		_ = database.Close()
		return nil, err
	}
	if err := restrictDatabaseSidecars(path); err != nil {
		_ = database.Close()
		return nil, err
	}
	if err := verifyDatabaseMode(path); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

// OpenStoreはCURRENT schemaだけを受理し、writerの後にread-only reader poolを開く。
func OpenStore(ctx context.Context, dataRoot string) (*Store, error) {
	ownerLock, err := acquireOwnerLock(dataRoot)
	if err != nil {
		return nil, err
	}
	writer, err := openWriter(ctx, dataRoot)
	if err != nil {
		_ = releaseOwnerLock(ownerLock)
		return nil, err
	}
	inspection, err := inspect(ctx, writer)
	if err != nil || inspection.State != StateCurrent {
		_ = writer.Close()
		_ = releaseOwnerLock(ownerLock)
		if err != nil {
			return nil, errors.New("sqlite: schema inspection failed")
		}
		return nil, fmt.Errorf("sqlite: schema state %s is not ready", inspection.State)
	}
	path := filepath.Join(dataRoot, databaseFilename)
	reader, err := sqlitedriver.Open(buildReaderDSN(path))
	if err != nil {
		_ = writer.Close()
		_ = releaseOwnerLock(ownerLock)
		return nil, sanitize("open-reader", err)
	}
	reader.SetMaxOpenConns(4)
	reader.SetMaxIdleConns(1)
	if err := reader.PingContext(ctx); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		_ = releaseOwnerLock(ownerLock)
		return nil, sanitize("ping-reader", err)
	}
	if err := verifyPragmas(ctx, reader, true); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		_ = releaseOwnerLock(ownerLock)
		return nil, err
	}
	return &Store{writer: writer, reader: reader, root: dataRoot, ownerLock: ownerLock}, nil
}

// Closeはreader、writerの順にpoolを閉じ、最初のエラーを返す。
func (store *Store) Close() error {
	if store == nil {
		return nil
	}
	var first error
	if store.reader != nil {
		first = store.reader.Close()
	}
	if store.writer != nil {
		if err := store.writer.Close(); first == nil {
			first = err
		}
	}
	if store.ownerLock != nil {
		if err := releaseOwnerLock(store.ownerLock); first == nil {
			first = err
		}
		store.ownerLock = nil
	}
	return first
}

func prepareDatabasePath(dataRoot string) (string, error) {
	if dataRoot == "" || !filepath.IsAbs(dataRoot) {
		return "", errors.New("sqlite: data root must be an absolute path")
	}
	clean := filepath.Clean(dataRoot)
	if clean != dataRoot {
		return "", errors.New("sqlite: data root must be canonical")
	}
	if err := os.MkdirAll(clean, 0o700); err != nil {
		return "", errors.New("sqlite: create data root")
	}
	info, err := os.Lstat(clean)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !ownerOnlyDirectory(info) {
		return "", errors.New("sqlite: data root is not an owner-only directory")
	}
	if err := validateLocalFilesystem(clean); err != nil {
		return "", err
	}
	path := filepath.Join(clean, databaseFilename)
	info, err = os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		file, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if createErr != nil {
			return "", errors.New("sqlite: create database file")
		}
		if closeErr := file.Close(); closeErr != nil {
			return "", errors.New("sqlite: close new database file")
		}
		return path, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !ownerOnlyRegular(info) {
		return "", errors.New("sqlite: database is not an owner-only regular file")
	}
	return path, nil
}

func buildWriterDSN(path string) string {
	query := url.Values{}
	query.Set("_txlock", "immediate")
	query["_pragma"] = []string{"busy_timeout(5000)", "foreign_keys(1)", "journal_mode(wal)", "synchronous(full)"}
	return (&url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}).String()
}

func buildReaderDSN(path string) string {
	query := url.Values{}
	query.Set("mode", "ro")
	query["_pragma"] = []string{"busy_timeout(5000)", "foreign_keys(1)", "synchronous(full)", "query_only(1)"}
	return (&url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}).String()
}

func verifyPragmas(ctx context.Context, database *sql.DB, reader bool) error {
	checks := []struct {
		query string
		want  string
	}{
		{"PRAGMA busy_timeout", "5000"},
		{"PRAGMA foreign_keys", "1"},
		{"PRAGMA journal_mode", "wal"},
		{"PRAGMA synchronous", "2"},
	}
	if reader {
		checks = append(checks, struct{ query, want string }{"PRAGMA query_only", "1"})
	}
	for _, check := range checks {
		var got string
		if err := database.QueryRowContext(ctx, check.query).Scan(&got); err != nil {
			return sanitize("pragma-readback", err)
		}
		if got != check.want {
			return fmt.Errorf("sqlite: pragma readback mismatch")
		}
	}
	return nil
}

func verifyDatabaseMode(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !ownerOnlyRegular(info) {
		return errors.New("sqlite: database mode is not 0600")
	}
	return nil
}

// restrictDatabaseSidecarsはSQLiteがumaskを使って作ったWALとSHMを0600へ狭め、同じ条件を読み戻す。
func restrictDatabaseSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		sidecar := path + suffix
		info, err := os.Lstat(sidecar)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("sqlite: invalid database sidecar")
		}
		if err := os.Chmod(sidecar, 0o600); err != nil {
			return errors.New("sqlite: restrict database sidecar")
		}
		info, err = os.Lstat(sidecar)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !ownerOnlyRegular(info) {
			return errors.New("sqlite: database sidecar is not owner-only")
		}
	}
	return nil
}

func sanitize(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("sqlite: %s failed", operation)
}
