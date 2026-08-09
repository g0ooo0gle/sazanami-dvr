package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

// EnsureBackendはcredentialを含まないbackend identityを作成または観測時刻だけ更新する。
func (store *Store) EnsureBackend(ctx context.Context, backend catalogmodel.Backend) error {
	if (backend.Kind != "FAKE" && backend.Kind != "MIRAKURUN") || backend.ObservedAtMS < 0 ||
		!validOptionalText(backend.ReportedVersion, 128) || !validOptionalText(backend.SourceRef, 256) {
		return errors.New("sqlite: invalid backend input")
	}
	_, err := store.writer.ExecContext(ctx, `
		INSERT INTO backend_instances(id, provider_kind, identity_hash, reported_version, source_ref, created_at_utc_ms, last_seen_at_utc_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET last_seen_at_utc_ms=excluded.last_seen_at_utc_ms
		WHERE backend_instances.identity_hash=excluded.identity_hash
		  AND backend_instances.provider_kind=excluded.provider_kind
		  AND excluded.last_seen_at_utc_ms >= backend_instances.last_seen_at_utc_ms`,
		backend.ID.Bytes(), backend.Kind, backend.IdentityHash[:], backend.ReportedVersion, backend.SourceRef,
		backend.ObservedAtMS, backend.ObservedAtMS)
	if err != nil {
		return sanitize("ensure-backend", err)
	}
	var kind string
	var identityHash []byte
	if err := store.writer.QueryRowContext(ctx, `SELECT provider_kind, identity_hash FROM backend_instances WHERE id=?`, backend.ID.Bytes()).Scan(&kind, &identityHash); err != nil {
		return sanitize("readback-backend", err)
	}
	if kind != backend.Kind || !bytesEqual(identityHash, backend.IdentityHash[:]) {
		return errors.New("sqlite: backend identity conflict")
	}
	return nil
}

// BeginSyncは新しいRUNNING generationを作り、既存IDとの衝突を拒否する。
func (store *Store) BeginSync(ctx context.Context, sync catalogmodel.Sync) error {
	if !validCorrelation(sync.CorrelationID) {
		return errors.New("sqlite: invalid sync correlation")
	}
	_, err := store.writer.ExecContext(ctx, `
		INSERT INTO catalog_syncs(id, backend_instance_id, state, started_at_utc_ms, correlation_id)
		VALUES (?, ?, 'RUNNING', ?, ?)`, sync.ID.Bytes(), sync.BackendID.Bytes(), sync.StartedAtMS, sync.CorrelationID)
	if err != nil {
		return sanitize("begin-sync", err)
	}
	return nil
}

// StoreServicesは最大100件のservice観測を1つの短いtransactionで保存する。
func (store *Store) StoreServices(ctx context.Context, syncID catalogmodel.ID, observations []catalogmodel.ServiceObservation) error {
	if len(observations) == 0 || len(observations) > catalogmodel.MaxWriteBatch {
		return errors.New("sqlite: service batch outside accepted limit")
	}
	tx, err := store.writer.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return sanitize("begin-service-batch", err)
	}
	defer tx.Rollback()
	backendID, err := runningBackend(ctx, tx, syncID)
	if err != nil {
		return err
	}
	for _, observation := range observations {
		if err := storeService(ctx, tx, syncID, backendID, observation); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return sanitize("commit-service-batch", err)
	}
	return nil
}

// StoreProgramsは最大100件を保存し、same content、verified successor、ambiguousを分類する。
func (store *Store) StorePrograms(ctx context.Context, syncID catalogmodel.ID, verifiedFakeLineage bool, observations []catalogmodel.ProgramObservation) error {
	if len(observations) == 0 || len(observations) > catalogmodel.MaxWriteBatch {
		return errors.New("sqlite: program batch outside accepted limit")
	}
	tx, err := store.writer.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return sanitize("begin-program-batch", err)
	}
	defer tx.Rollback()
	backendID, err := runningBackend(ctx, tx, syncID)
	if err != nil {
		return err
	}
	providerKind, err := runningBackendKind(ctx, tx, syncID)
	if err != nil {
		return err
	}
	for _, observation := range observations {
		if err := storeProgram(ctx, tx, syncID, backendID, providerKind, verifiedFakeLineage, observation); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return sanitize("commit-program-batch", err)
	}
	return nil
}

// CompleteSyncはRUNNING generationを一度だけCOMPLETEDへCASし、件数を確定する。
func (store *Store) CompleteSync(ctx context.Context, syncID catalogmodel.ID, finishedAtMS int64, serviceCount, programCount int) error {
	if serviceCount < 0 || serviceCount > 10_000 || programCount < 0 || programCount > 262_144 {
		return errors.New("sqlite: sync count outside accepted limit")
	}
	tx, err := store.writer.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return sanitize("begin-complete-sync", err)
	}
	defer tx.Rollback()
	var state string
	var startedAtMS int64
	var persistedServices, persistedPrograms int
	if err := tx.QueryRowContext(ctx, `SELECT state, started_at_utc_ms,
		(SELECT count(*) FROM service_observations WHERE sync_id=catalog_syncs.id),
		(SELECT count(*) FROM program_observations WHERE sync_id=catalog_syncs.id)
		FROM catalog_syncs WHERE id=?`, syncID.Bytes()).Scan(&state, &startedAtMS, &persistedServices, &persistedPrograms); err != nil {
		return sanitize("readback-sync-counts", err)
	}
	if state != "RUNNING" || finishedAtMS < startedAtMS || persistedServices != serviceCount || persistedPrograms != programCount {
		return errors.New("sqlite: sync completion facts mismatch")
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE catalog_syncs SET state='COMPLETED', finished_at_utc_ms=?, service_count=?, program_count=?
		WHERE id=? AND state='RUNNING'`, finishedAtMS, serviceCount, programCount, syncID.Bytes())
	if err != nil {
		return sanitize("complete-sync", err)
	}
	if err := requireOneRow(result, "complete-sync-conflict"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return sanitize("commit-complete-sync", err)
	}
	return nil
}

// FailSyncはRUNNING generationをbounded reason付きFAILEDへ閉じ、既存completed catalogを維持する。
func (store *Store) FailSync(ctx context.Context, syncID catalogmodel.ID, finishedAtMS int64, reason string) error {
	if !validStableReason(reason) {
		reason = "sync-failed"
	}
	result, err := store.writer.ExecContext(ctx, `
		UPDATE catalog_syncs SET state='FAILED', finished_at_utc_ms=?, failure_reason=?
		WHERE id=? AND state='RUNNING' AND started_at_utc_ms<=?`, finishedAtMS, reason, syncID.Bytes(), finishedAtMS)
	if err != nil {
		return sanitize("fail-sync", err)
	}
	return requireOneRow(result, "fail-sync-conflict")
}

// ReconcileRunningSyncsはprocess終了で残ったRUNNING generationを一括してFAILEDへ閉じる。
func (store *Store) ReconcileRunningSyncs(ctx context.Context, finishedAtMS int64) (int, error) {
	if store == nil || store.writer == nil || finishedAtMS < 0 {
		return 0, errors.New("sqlite: invalid sync reconciliation")
	}
	tx, err := store.writer.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return 0, sanitize("begin-sync-reconciliation", err)
	}
	defer tx.Rollback()
	var future int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM catalog_syncs
		WHERE state='RUNNING' AND started_at_utc_ms > ?`, finishedAtMS).Scan(&future); err != nil {
		return 0, sanitize("check-sync-reconciliation-time", err)
	}
	if future != 0 {
		return 0, errors.New("sqlite: reconciliation clock precedes running sync")
	}
	result, err := tx.ExecContext(ctx, `UPDATE catalog_syncs
		SET state='FAILED', finished_at_utc_ms=?, failure_reason='process-interrupted'
		WHERE state='RUNNING'`, finishedAtMS)
	if err != nil {
		return 0, sanitize("reconcile-running-syncs", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count < 0 || count > int64(^uint(0)>>1) {
		return 0, errors.New("sqlite: invalid reconciliation result")
	}
	if err := tx.Commit(); err != nil {
		return 0, sanitize("commit-sync-reconciliation", err)
	}
	return int(count), nil
}

// LatestCompletedGenerationは指定backendで最後に完了した番組表世代を返す。
func (store *Store) LatestCompletedGeneration(ctx context.Context, backendID catalogmodel.ID) (catalogmodel.ID, error) {
	var value []byte
	err := store.reader.QueryRowContext(ctx, `SELECT id FROM catalog_syncs
		WHERE backend_instance_id=? AND state='COMPLETED'
		ORDER BY finished_at_utc_ms DESC, id DESC LIMIT 1`, backendID.Bytes()).Scan(&value)
	if err != nil {
		return catalogmodel.ID{}, sanitize("query-latest-completed-generation", err)
	}
	var result catalogmodel.ID
	if err := copyExact(result[:], value); err != nil {
		return catalogmodel.ID{}, err
	}
	return result, nil
}

// ServicesForGenerationは指定backendの一つの世代からserviceをopaque keysetで読む。
// RUNNINGは完了前のチャンネル照合にだけ使い、COMPLETEDは公開済みスナップショットに使う。
func (store *Store) ServicesForGeneration(ctx context.Context, backendID, generationID catalogmodel.ID,
	state catalogmodel.GenerationState, limit int, after catalogmodel.ID,
) ([]catalogmodel.CurrentService, error) {
	if limit < 1 || limit > catalogmodel.MaxQueryPage {
		return nil, errors.New("sqlite: service query limit outside accepted range")
	}
	stateText, err := generationStateText(state)
	if err != nil {
		return nil, err
	}
	rows, err := store.reader.QueryContext(ctx, `
		WITH selected_sync AS (
			SELECT id FROM catalog_syncs WHERE id=? AND backend_instance_id=? AND state=?
		)
		SELECT s.id, so.provider_locator, so.display_name, so.network_id,
		       so.transport_stream_id, so.service_number, so.broadcast_kind, so.validation_state
		FROM service_observations so
		JOIN selected_sync cs ON cs.id=so.sync_id
		JOIN services s ON s.id=so.service_id
		WHERE s.id > ?
		ORDER BY s.id LIMIT ?`, generationID.Bytes(), backendID.Bytes(), stateText, after.Bytes(), limit)
	if err != nil {
		return nil, sanitize("query-generation-services", err)
	}
	defer rows.Close()
	return scanCurrentServices(rows, limit)
}

// ProgramsByServiceForGenerationは公開済みの一世代をserviceとeventの順に読む。
func (store *Store) ProgramsByServiceForGeneration(ctx context.Context, backendID, generationID catalogmodel.ID,
	limit int, after catalogmodel.ProgramCursor,
) ([]catalogmodel.CurrentProgram, error) {
	if limit < 1 || limit > catalogmodel.MaxQueryPage ||
		(after.ServiceLocator == "") != (after.EventLocator == "") ||
		!validText(after.ServiceLocator, 0, 256) || !validText(after.EventLocator, 0, 256) {
		return nil, errors.New("sqlite: program cursor outside accepted range")
	}
	rows, err := store.reader.QueryContext(ctx, `
		WITH selected_sync AS (
			SELECT id FROM catalog_syncs WHERE id=? AND backend_instance_id=? AND state='COMPLETED'
		)
		SELECT pi.id, pr.id, po.provider_service_locator, po.provider_event_locator, po.raw_event_id,
		       pr.revision_number, pr.content_hash, pr.start_at_utc_ms, pr.duration_ms,
		       pr.title, pr.description, pr.free_access, pr.validation_state, pr.metadata, po.classification
		FROM program_observations po
		JOIN selected_sync cs ON cs.id=po.sync_id
		JOIN program_instances pi ON pi.id=po.program_instance_id
		JOIN program_revisions pr ON pr.id=po.program_revision_id
		WHERE (?='' OR po.provider_service_locator>? OR
		      (po.provider_service_locator=? AND po.provider_event_locator>?))
		ORDER BY po.provider_service_locator, po.provider_event_locator LIMIT ?`,
		generationID.Bytes(), backendID.Bytes(), after.ServiceLocator, after.ServiceLocator,
		after.ServiceLocator, after.EventLocator, limit)
	if err != nil {
		return nil, sanitize("query-generation-programs-by-service", err)
	}
	defer rows.Close()
	return scanCurrentPrograms(rows, limit)
}

// ProgramsForServiceForGenerationは公開済みの一世代から一サービスの番組を読む。
func (store *Store) ProgramsForServiceForGeneration(ctx context.Context, backendID, generationID catalogmodel.ID,
	serviceLocator string, limit int, afterEvent string,
) ([]catalogmodel.CurrentProgram, error) {
	if limit < 1 || limit > catalogmodel.MaxQueryPage ||
		!validText(serviceLocator, 1, 256) || !validText(afterEvent, 0, 256) {
		return nil, errors.New("sqlite: service program cursor outside accepted range")
	}
	rows, err := store.reader.QueryContext(ctx, `
		WITH selected_sync AS (
			SELECT id FROM catalog_syncs WHERE id=? AND backend_instance_id=? AND state='COMPLETED'
		)
		SELECT pi.id, pr.id, po.provider_service_locator, po.provider_event_locator, po.raw_event_id,
		       pr.revision_number, pr.content_hash, pr.start_at_utc_ms, pr.duration_ms,
		       pr.title, pr.description, pr.free_access, pr.validation_state, pr.metadata, po.classification
		FROM program_observations po
		JOIN selected_sync cs ON cs.id=po.sync_id
		JOIN program_instances pi ON pi.id=po.program_instance_id
		JOIN program_revisions pr ON pr.id=po.program_revision_id
		WHERE po.provider_service_locator=? AND (?='' OR po.provider_event_locator>?)
		ORDER BY po.provider_event_locator LIMIT ?`, generationID.Bytes(), backendID.Bytes(),
		serviceLocator, afterEvent, afterEvent, limit)
	if err != nil {
		return nil, sanitize("query-generation-programs-for-service", err)
	}
	defer rows.Close()
	return scanCurrentPrograms(rows, limit)
}

// ProgramsMatchingGenerationは公開済みの一世代から予約対象に完全一致する候補を最大2件返す。
func (store *Store) ProgramsMatchingGeneration(ctx context.Context, backendID, generationID catalogmodel.ID,
	serviceLocator string, rawEventID, startUTCMS, durationMS int64,
) ([]catalogmodel.CurrentProgram, error) {
	if !validText(serviceLocator, 1, 256) || rawEventID < 0 || rawEventID > 65_535 || startUTCMS < 0 ||
		durationMS < 1_000 || durationMS > int64((24*time.Hour)/time.Millisecond) {
		return nil, errors.New("sqlite: program match outside accepted range")
	}
	rows, err := store.reader.QueryContext(ctx, `
		WITH selected_sync AS (
			SELECT id FROM catalog_syncs WHERE id=? AND backend_instance_id=? AND state='COMPLETED'
		)
		SELECT pi.id, pr.id, po.provider_service_locator, po.provider_event_locator, po.raw_event_id,
		       pr.revision_number, pr.content_hash, pr.start_at_utc_ms, pr.duration_ms,
		       pr.title, pr.description, pr.free_access, pr.validation_state, pr.metadata, po.classification
		FROM program_observations po
		JOIN selected_sync cs ON cs.id=po.sync_id
		JOIN program_instances pi ON pi.id=po.program_instance_id
		JOIN program_revisions pr ON pr.id=po.program_revision_id
		WHERE po.provider_service_locator=? AND po.raw_event_id=?
		  AND pr.start_at_utc_ms=? AND pr.duration_ms=?
		ORDER BY pi.id LIMIT 2`, generationID.Bytes(), backendID.Bytes(), serviceLocator,
		rawEventID, startUTCMS, durationMS)
	if err != nil {
		return nil, sanitize("query-generation-program-match", err)
	}
	defer rows.Close()
	return scanCurrentPrograms(rows, 2)
}

// CurrentProgramsは指定backendの最後に完了したgenerationだけをopaque keysetで読む。
func (store *Store) CurrentPrograms(ctx context.Context, backendID catalogmodel.ID, limit int, after catalogmodel.ID) ([]catalogmodel.CurrentProgram, error) {
	if limit < 1 || limit > catalogmodel.MaxQueryPage {
		return nil, errors.New("sqlite: query limit outside accepted range")
	}
	rows, err := store.reader.QueryContext(ctx, `
		WITH current_sync AS (
			SELECT id FROM catalog_syncs
			WHERE backend_instance_id=? AND state='COMPLETED'
			ORDER BY finished_at_utc_ms DESC, id DESC LIMIT 1
		)
		SELECT pi.id, pr.id, po.provider_service_locator, po.provider_event_locator, po.raw_event_id,
		       pr.revision_number, pr.content_hash, pr.start_at_utc_ms, pr.duration_ms,
		       pr.title, pr.description, pr.free_access, pr.validation_state, pr.metadata, po.classification
		FROM program_observations po
		JOIN current_sync cs ON cs.id=po.sync_id
		JOIN program_instances pi ON pi.id=po.program_instance_id
		JOIN program_revisions pr ON pr.id=po.program_revision_id
		WHERE pi.id > ?
		ORDER BY pi.id LIMIT ?`, backendID.Bytes(), after.Bytes(), limit)
	if err != nil {
		return nil, sanitize("query-current-programs", err)
	}
	defer rows.Close()
	return scanCurrentPrograms(rows, limit)
}

// CurrentProgramsByServiceは最新の完成済み番組表を放送サービスとeventの順に256件以下で読む。
func (store *Store) CurrentProgramsByService(ctx context.Context, backendID catalogmodel.ID, limit int, after catalogmodel.ProgramCursor) ([]catalogmodel.CurrentProgram, error) {
	if limit < 1 || limit > catalogmodel.MaxQueryPage ||
		(after.ServiceLocator == "") != (after.EventLocator == "") ||
		!validText(after.ServiceLocator, 0, 256) || !validText(after.EventLocator, 0, 256) {
		return nil, errors.New("sqlite: program cursor outside accepted range")
	}
	rows, err := store.reader.QueryContext(ctx, `
		WITH current_sync AS (
			SELECT id FROM catalog_syncs
			WHERE backend_instance_id=? AND state='COMPLETED'
			ORDER BY finished_at_utc_ms DESC, id DESC LIMIT 1
		)
		SELECT pi.id, pr.id, po.provider_service_locator, po.provider_event_locator, po.raw_event_id,
		       pr.revision_number, pr.content_hash, pr.start_at_utc_ms, pr.duration_ms,
		       pr.title, pr.description, pr.free_access, pr.validation_state, pr.metadata, po.classification
		FROM program_observations po
		JOIN current_sync cs ON cs.id=po.sync_id
		JOIN program_instances pi ON pi.id=po.program_instance_id
		JOIN program_revisions pr ON pr.id=po.program_revision_id
		WHERE (?='' OR po.provider_service_locator>? OR
		      (po.provider_service_locator=? AND po.provider_event_locator>?))
		ORDER BY po.provider_service_locator, po.provider_event_locator LIMIT ?`,
		backendID.Bytes(), after.ServiceLocator, after.ServiceLocator, after.ServiceLocator, after.EventLocator, limit)
	if err != nil {
		return nil, sanitize("query-current-programs-by-service", err)
	}
	defer rows.Close()
	return scanCurrentPrograms(rows, limit)
}

// CurrentProgramsForServiceは、最新の完成済み番組表から指定したサービスの番組だけを、イベント識別子順に最大256件読む。
func (store *Store) CurrentProgramsForService(ctx context.Context, backendID catalogmodel.ID, serviceLocator string, limit int, afterEvent string) ([]catalogmodel.CurrentProgram, error) {
	if limit < 1 || limit > catalogmodel.MaxQueryPage ||
		!validText(serviceLocator, 1, 256) || !validText(afterEvent, 0, 256) {
		return nil, errors.New("sqlite: service program cursor outside accepted range")
	}
	rows, err := store.reader.QueryContext(ctx, `
		WITH current_sync AS (
			SELECT id FROM catalog_syncs
			WHERE backend_instance_id=? AND state='COMPLETED'
			ORDER BY finished_at_utc_ms DESC, id DESC LIMIT 1
		)
		SELECT pi.id, pr.id, po.provider_service_locator, po.provider_event_locator, po.raw_event_id,
		       pr.revision_number, pr.content_hash, pr.start_at_utc_ms, pr.duration_ms,
		       pr.title, pr.description, pr.free_access, pr.validation_state, pr.metadata, po.classification
		FROM program_observations po
		JOIN current_sync cs ON cs.id=po.sync_id
		JOIN program_instances pi ON pi.id=po.program_instance_id
		JOIN program_revisions pr ON pr.id=po.program_revision_id
		WHERE po.provider_service_locator=? AND (?='' OR po.provider_event_locator>?)
		ORDER BY po.provider_event_locator LIMIT ?`,
		backendID.Bytes(), serviceLocator, afterEvent, afterEvent, limit)
	if err != nil {
		return nil, sanitize("query-current-programs-for-service", err)
	}
	defer rows.Close()
	return scanCurrentPrograms(rows, limit)
}

// CurrentProgramsMatchingは最新の完成済み番組表から予約対象に完全一致する候補を最大2件返す。
func (store *Store) CurrentProgramsMatching(ctx context.Context, backendID catalogmodel.ID, serviceLocator string, rawEventID, startUTCMS, durationMS int64) ([]catalogmodel.CurrentProgram, error) {
	if !validText(serviceLocator, 1, 256) || rawEventID < 0 || rawEventID > 65_535 || startUTCMS < 0 ||
		durationMS < 1_000 || durationMS > int64((24*time.Hour)/time.Millisecond) {
		return nil, errors.New("sqlite: program match outside accepted range")
	}
	rows, err := store.reader.QueryContext(ctx, `
		WITH current_sync AS (
			SELECT id FROM catalog_syncs
			WHERE backend_instance_id=? AND state='COMPLETED'
			ORDER BY finished_at_utc_ms DESC, id DESC LIMIT 1
		)
		SELECT pi.id, pr.id, po.provider_service_locator, po.provider_event_locator, po.raw_event_id,
		       pr.revision_number, pr.content_hash, pr.start_at_utc_ms, pr.duration_ms,
		       pr.title, pr.description, pr.free_access, pr.validation_state, pr.metadata, po.classification
		FROM program_observations po
		JOIN current_sync cs ON cs.id=po.sync_id
		JOIN program_instances pi ON pi.id=po.program_instance_id
		JOIN program_revisions pr ON pr.id=po.program_revision_id
		WHERE po.provider_service_locator=? AND po.raw_event_id=?
		  AND pr.start_at_utc_ms=? AND pr.duration_ms=?
		ORDER BY pi.id LIMIT 2`, backendID.Bytes(), serviceLocator, rawEventID, startUTCMS, durationMS)
	if err != nil {
		return nil, sanitize("query-current-program-match", err)
	}
	defer rows.Close()
	return scanCurrentPrograms(rows, 2)
}

// CurrentBackendsはCOMPLETED generationを持つbackendだけをopaque keysetで読む。
func (store *Store) CurrentBackends(ctx context.Context, limit int, after catalogmodel.ID) ([]catalogmodel.CurrentBackend, error) {
	if limit < 1 || limit > 16 {
		return nil, errors.New("sqlite: backend query limit outside accepted range")
	}
	rows, err := store.reader.QueryContext(ctx, `
		SELECT b.id, b.provider_kind, b.reported_version, b.last_seen_at_utc_ms
		FROM backend_instances b
		WHERE b.id > ? AND EXISTS (
			SELECT 1 FROM catalog_syncs cs
			WHERE cs.backend_instance_id=b.id AND cs.state='COMPLETED'
		)
		ORDER BY b.id LIMIT ?`, after.Bytes(), limit)
	if err != nil {
		return nil, sanitize("query-current-backends", err)
	}
	defer rows.Close()
	result := make([]catalogmodel.CurrentBackend, 0, limit)
	for rows.Next() {
		var item catalogmodel.CurrentBackend
		var id []byte
		var version sql.NullString
		if err := rows.Scan(&id, &item.Kind, &version, &item.LastSeenAtMS); err != nil {
			return nil, sanitize("scan-current-backend", err)
		}
		if err := copyExact(item.ID[:], id); err != nil {
			return nil, err
		}
		if version.Valid {
			item.ReportedVersion = &version.String
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, sanitize("iterate-current-backends", err)
	}
	return result, nil
}

// CurrentServicesは指定backendの最後に完了したgenerationのserviceだけを読む。
func (store *Store) CurrentServices(ctx context.Context, backendID catalogmodel.ID, limit int, after catalogmodel.ID) ([]catalogmodel.CurrentService, error) {
	if limit < 1 || limit > catalogmodel.MaxQueryPage {
		return nil, errors.New("sqlite: service query limit outside accepted range")
	}
	rows, err := store.reader.QueryContext(ctx, `
		WITH current_sync AS (
			SELECT id FROM catalog_syncs
			WHERE backend_instance_id=? AND state='COMPLETED'
			ORDER BY finished_at_utc_ms DESC, id DESC LIMIT 1
		)
		SELECT s.id, so.provider_locator, so.display_name, so.network_id,
		       so.transport_stream_id, so.service_number, so.broadcast_kind, so.validation_state
		FROM service_observations so
		JOIN current_sync cs ON cs.id=so.sync_id
		JOIN services s ON s.id=so.service_id
		WHERE s.id > ?
		ORDER BY s.id LIMIT ?`, backendID.Bytes(), after.Bytes(), limit)
	if err != nil {
		return nil, sanitize("query-current-services", err)
	}
	defer rows.Close()
	return scanCurrentServices(rows, limit)
}

func scanCurrentServices(rows *sql.Rows, capacity int) ([]catalogmodel.CurrentService, error) {
	result := make([]catalogmodel.CurrentService, 0, capacity)
	for rows.Next() {
		var item catalogmodel.CurrentService
		var id []byte
		var network, transport, service sql.NullInt64
		var broadcast sql.NullString
		var validation string
		if err := rows.Scan(&id, &item.ProviderLocator, &item.DisplayName, &network, &transport,
			&service, &broadcast, &validation); err != nil {
			return nil, sanitize("scan-current-service", err)
		}
		if err := copyExact(item.ID[:], id); err != nil {
			return nil, err
		}
		item.NetworkID = nullableInt64(network)
		item.TransportID = nullableInt64(transport)
		item.ServiceID = nullableInt64(service)
		if broadcast.Valid {
			item.BroadcastKind = &broadcast.String
		}
		item.Validation = validationFromSQL(validation)
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, sanitize("iterate-current-services", err)
	}
	return result, nil
}

// CurrentProgramsInWindowはcompleted catalogからstart時刻範囲内の番組を最大limit+1件だけ読む。
func (store *Store) CurrentProgramsInWindow(ctx context.Context, backendID catalogmodel.ID, fromUTCMS, toUTCMS int64, limit int) ([]catalogmodel.CurrentProgram, bool, error) {
	const maximumWindowMS = int64((8 * 24 * time.Hour) / time.Millisecond)
	if limit < 1 || limit > catalogmodel.MaxQueryPage || toUTCMS <= fromUTCMS || toUTCMS-fromUTCMS > maximumWindowMS {
		return nil, false, errors.New("sqlite: program window outside accepted range")
	}
	rows, err := store.reader.QueryContext(ctx, `
		WITH current_sync AS (
			SELECT id FROM catalog_syncs
			WHERE backend_instance_id=? AND state='COMPLETED'
			ORDER BY finished_at_utc_ms DESC, id DESC LIMIT 1
		)
		SELECT pi.id, pr.id, po.provider_service_locator, po.provider_event_locator, po.raw_event_id,
		       pr.revision_number, pr.content_hash, pr.start_at_utc_ms, pr.duration_ms,
		       pr.title, pr.description, pr.free_access, pr.validation_state, pr.metadata, po.classification
		FROM program_observations po
		JOIN current_sync cs ON cs.id=po.sync_id
		JOIN program_instances pi ON pi.id=po.program_instance_id
		JOIN program_revisions pr ON pr.id=po.program_revision_id
		WHERE pr.start_at_utc_ms >= ? AND pr.start_at_utc_ms < ?
		ORDER BY pr.start_at_utc_ms, pi.id LIMIT ?`, backendID.Bytes(), fromUTCMS, toUTCMS, limit+1)
	if err != nil {
		return nil, false, sanitize("query-current-program-window", err)
	}
	defer rows.Close()
	result, err := scanCurrentPrograms(rows, limit+1)
	if err != nil {
		return nil, false, err
	}
	truncated := len(result) > limit
	if truncated {
		result = result[:limit]
	}
	return result, truncated, nil
}

func scanCurrentPrograms(rows *sql.Rows, capacity int) ([]catalogmodel.CurrentProgram, error) {
	result := make([]catalogmodel.CurrentProgram, 0, capacity)
	for rows.Next() {
		var item catalogmodel.CurrentProgram
		var instanceID, revisionID, hash, metadata []byte
		var rawEventID, start, duration sql.NullInt64
		var title, description sql.NullString
		var free sql.NullInt64
		var validation string
		if err := rows.Scan(&instanceID, &revisionID, &item.ServiceLocator, &item.EventLocator, &rawEventID,
			&item.RevisionNumber, &hash, &start, &duration, &title, &description, &free,
			&validation, &metadata, &item.Classification); err != nil {
			return nil, sanitize("scan-current-program", err)
		}
		if err := copyExact(item.InstanceID[:], instanceID); err != nil {
			return nil, err
		}
		if err := copyExact(item.RevisionID[:], revisionID); err != nil {
			return nil, err
		}
		if err := copyExact(item.Hash[:], hash); err != nil {
			return nil, err
		}
		item.RawEventID = nullableInt64(rawEventID)
		material, err := materialFromSQL(start, duration, title, description, free, validation, metadata)
		if err != nil {
			return nil, err
		}
		item.Material = material
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, sanitize("iterate-current-programs", err)
	}
	return result, nil
}

func nullableInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

func validationFromSQL(value string) catalogmodel.Validation {
	switch value {
	case "VALID":
		return catalogmodel.ValidationValid
	case "PROVISIONAL":
		return catalogmodel.ValidationProvisional
	default:
		return catalogmodel.ValidationInvalid
	}
}

func generationStateText(state catalogmodel.GenerationState) (string, error) {
	switch state {
	case catalogmodel.GenerationRunning:
		return "RUNNING", nil
	case catalogmodel.GenerationCompleted:
		return "COMPLETED", nil
	default:
		return "", errors.New("sqlite: invalid generation state")
	}
}

func runningBackend(ctx context.Context, tx *sql.Tx, syncID catalogmodel.ID) ([]byte, error) {
	var backend []byte
	if err := tx.QueryRowContext(ctx, `SELECT backend_instance_id FROM catalog_syncs WHERE id=? AND state='RUNNING'`, syncID.Bytes()).Scan(&backend); err != nil {
		return nil, sanitize("read-running-sync", err)
	}
	return backend, nil
}

func runningBackendKind(ctx context.Context, tx *sql.Tx, syncID catalogmodel.ID) (string, error) {
	var kind string
	if err := tx.QueryRowContext(ctx, `SELECT b.provider_kind FROM catalog_syncs s
		JOIN backend_instances b ON b.id=s.backend_instance_id
		WHERE s.id=? AND s.state='RUNNING'`, syncID.Bytes()).Scan(&kind); err != nil {
		return "", sanitize("read-running-backend-kind", err)
	}
	return kind, nil
}

func storeService(ctx context.Context, tx *sql.Tx, syncID catalogmodel.ID, backendID []byte, observation catalogmodel.ServiceObservation) error {
	if !validText(observation.ProviderLocator, 1, 256) || !validText(observation.DisplayName, 0, 4096) ||
		!validOptionalText(observation.BroadcastKind, 32) || !validOptionalText(observation.TuningTarget, 256) ||
		!validStableReasonPointer(observation.Reason) {
		return errors.New("sqlite: invalid service observation")
	}
	serviceID, err := catalogmodel.NewID()
	if err != nil {
		return errors.New("sqlite: generate service id")
	}
	identity := string(catalogmodel.IdentityProvisional)
	if observation.TransportID != nil {
		identity = string(catalogmodel.IdentityVerified)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO services(id, backend_instance_id, provider_locator, identity_state, created_at_utc_ms, last_seen_at_utc_ms)
		SELECT ?, ?, ?, ?, s.started_at_utc_ms, s.started_at_utc_ms FROM catalog_syncs s WHERE s.id=?
		ON CONFLICT(backend_instance_id, provider_locator) DO UPDATE SET last_seen_at_utc_ms=excluded.last_seen_at_utc_ms`,
		serviceID.Bytes(), backendID, observation.ProviderLocator, identity, syncID.Bytes())
	if err != nil {
		return sanitize("upsert-service", err)
	}
	var persistedID []byte
	if err := tx.QueryRowContext(ctx, `SELECT id FROM services WHERE backend_instance_id=? AND provider_locator=?`, backendID, observation.ProviderLocator).Scan(&persistedID); err != nil {
		return sanitize("read-service-id", err)
	}
	if err := copyExact(serviceID[:], persistedID); err != nil {
		return err
	}
	hash := hashService(observation)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO service_observations(sync_id, service_id, provider_locator, network_id,
		 transport_stream_id, service_number, broadcast_kind, display_name, tuning_target,
		 validation_state, validation_reason, observation_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		syncID.Bytes(), serviceID.Bytes(), observation.ProviderLocator, observation.NetworkID,
		observation.TransportID, observation.ServiceID, observation.BroadcastKind, observation.DisplayName,
		observation.TuningTarget, validationText(observation.Validation), observation.Reason, hash[:])
	if err != nil {
		return sanitize("insert-service-observation", err)
	}
	return nil
}

func storeProgram(ctx context.Context, tx *sql.Tx, syncID catalogmodel.ID, backendID []byte, providerKind string,
	verifiedFakeLineage bool, observation catalogmodel.ProgramObservation,
) error {
	if !validText(observation.ServiceLocator, 1, 256) || !validText(observation.EventLocator, 1, 256) ||
		!validStableReasonPointer(observation.Reason) {
		return errors.New("sqlite: invalid program observation")
	}
	var serviceID catalogmodel.ID
	var serviceIDBytes []byte
	if err := tx.QueryRowContext(ctx, `SELECT id FROM services WHERE backend_instance_id=? AND provider_locator=?`, backendID, observation.ServiceLocator).Scan(&serviceIDBytes); err != nil {
		return sanitize("resolve-program-service", err)
	}
	if err := copyExact(serviceID[:], serviceIDBytes); err != nil {
		return err
	}
	hash, hashErr := catalogmodel.HashRevision(observation.Material)
	if hashErr != nil || observation.Material.Validation == catalogmodel.ValidationInvalid {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO program_observations(sync_id, provider_service_locator, provider_event_locator,
			 raw_event_id, classification, validation_reason) VALUES (?, ?, ?, ?, 'INVALID', ?)`,
			syncID.Bytes(), observation.ServiceLocator, observation.EventLocator, observation.RawEventID, stableReason(observation.Reason, "invalid-material"))
		if err != nil {
			return sanitize("insert-invalid-program", err)
		}
		return nil
	}

	var instanceID catalogmodel.ID
	var instanceIDBytes []byte
	var previousEventID sql.NullInt64
	var previousSeenMS int64
	err := tx.QueryRowContext(ctx, `SELECT id, raw_event_id, last_seen_at_utc_ms FROM program_instances
		WHERE service_id=? AND provider_event_locator=?`, serviceID.Bytes(), observation.EventLocator).
		Scan(&instanceIDBytes, &previousEventID, &previousSeenMS)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return insertNewProgram(ctx, tx, syncID, serviceID, verifiedFakeLineage, observation, hash)
	case err != nil:
		return sanitize("find-program-instance", err)
	}
	if err := copyExact(instanceID[:], instanceIDBytes); err != nil {
		return err
	}

	var revisionID catalogmodel.ID
	var revisionIDBytes []byte
	var latestHash, previousMetadata []byte
	var revisionNumber int64
	var previousStart, previousDuration, previousFree sql.NullInt64
	var previousTitle, previousDescription sql.NullString
	var previousValidation string
	if err := tx.QueryRowContext(ctx, `
		SELECT id, content_hash, revision_number, start_at_utc_ms, duration_ms, title, description,
		       free_access, validation_state, metadata FROM program_revisions
		WHERE program_instance_id=? ORDER BY revision_number DESC LIMIT 1`, instanceID.Bytes()).
		Scan(&revisionIDBytes, &latestHash, &revisionNumber, &previousStart, &previousDuration,
			&previousTitle, &previousDescription, &previousFree, &previousValidation, &previousMetadata); err != nil {
		return sanitize("read-latest-revision", err)
	}
	if err := copyExact(revisionID[:], revisionIDBytes); err != nil {
		return err
	}
	observedAt, err := syncStartedAt(ctx, tx, syncID)
	if err != nil {
		return err
	}
	if bytesEqual(latestHash, hash[:]) {
		if err := touchProgramInstance(ctx, tx, instanceID, observedAt); err != nil {
			return err
		}
		return insertProgramObservation(ctx, tx, syncID, observation, hash, instanceID, revisionID, catalogmodel.SameContent)
	}
	previousMaterial, err := materialFromSQL(previousStart, previousDuration, previousTitle, previousDescription,
		previousFree, previousValidation, previousMetadata)
	if err != nil {
		return err
	}
	var previousEventPointer *int64
	if previousEventID.Valid {
		previousEventPointer = &previousEventID.Int64
	}
	verified := verifiedFakeLineage || providerKind == "MIRAKURUN" && catalogmodel.MirakurunSuccessor(
		previousMaterial, previousEventPointer, previousSeenMS, observation.Material, observation.RawEventID, observedAt)
	if !verified {
		if err := touchProgramInstance(ctx, tx, instanceID, observedAt); err != nil {
			return err
		}
		return insertProgramObservation(ctx, tx, syncID, observation, hash, instanceID, revisionID, catalogmodel.Ambiguous)
	}
	newRevisionID, err := catalogmodel.NewID()
	if err != nil {
		return errors.New("sqlite: generate revision id")
	}
	if err := insertRevision(ctx, tx, newRevisionID, instanceID, revisionNumber+1, hash, observation.Material, observedAt); err != nil {
		return err
	}
	if err := touchProgramInstance(ctx, tx, instanceID, observedAt); err != nil {
		return err
	}
	return insertProgramObservation(ctx, tx, syncID, observation, hash, instanceID, newRevisionID, catalogmodel.VerifiedSuccessor)
}

func touchProgramInstance(ctx context.Context, tx *sql.Tx, instanceID catalogmodel.ID, observedAt int64) error {
	result, err := tx.ExecContext(ctx, `UPDATE program_instances SET last_seen_at_utc_ms=?
		WHERE id=? AND last_seen_at_utc_ms<=?`, observedAt, instanceID.Bytes(), observedAt)
	if err != nil {
		return sanitize("touch-program-instance", err)
	}
	return requireOneRow(result, "touch-program-instance-conflict")
}

func insertNewProgram(ctx context.Context, tx *sql.Tx, syncID, serviceID catalogmodel.ID, verified bool, observation catalogmodel.ProgramObservation, hash [32]byte) error {
	instanceID, err := catalogmodel.NewID()
	if err != nil {
		return errors.New("sqlite: generate program instance id")
	}
	revisionID, err := catalogmodel.NewID()
	if err != nil {
		return errors.New("sqlite: generate revision id")
	}
	createdAt, err := syncStartedAt(ctx, tx, syncID)
	if err != nil {
		return err
	}
	identity := catalogmodel.IdentityProvisional
	if verified {
		identity = catalogmodel.IdentityVerified
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO program_instances(id, service_id, provider_event_locator, raw_event_id, identity_state,
		 created_at_utc_ms, last_seen_at_utc_ms) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		instanceID.Bytes(), serviceID.Bytes(), observation.EventLocator, observation.RawEventID, string(identity), createdAt, createdAt)
	if err != nil {
		return sanitize("insert-program-instance", err)
	}
	if err := insertRevision(ctx, tx, revisionID, instanceID, 1, hash, observation.Material, createdAt); err != nil {
		return err
	}
	return insertProgramObservation(ctx, tx, syncID, observation, hash, instanceID, revisionID, catalogmodel.NewInstance)
}

func insertRevision(ctx context.Context, tx *sql.Tx, revisionID, instanceID catalogmodel.ID, number int64, hash [32]byte, material catalogmodel.RevisionMaterial, createdAt int64) error {
	var free any
	switch material.FreeAccess {
	case catalogmodel.FreeNo:
		free = 0
	case catalogmodel.FreeYes:
		free = 1
	}
	metadata, err := catalogmodel.EncodeMetadataV1(material.Metadata)
	if err != nil {
		return errors.New("sqlite: invalid program metadata")
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO program_revisions(id, program_instance_id, revision_number, content_hash,
		 start_at_utc_ms, duration_ms, title, description, free_access, validation_state, created_at_utc_ms, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, revisionID.Bytes(), instanceID.Bytes(), number, hash[:],
		material.StartUTCMS, material.DurationMS, material.Title, material.Description, free,
		validationText(material.Validation), createdAt, nullableBytes(metadata))
	if err != nil {
		return sanitize("insert-program-revision", err)
	}
	return nil
}

func insertProgramObservation(ctx context.Context, tx *sql.Tx, syncID catalogmodel.ID, observation catalogmodel.ProgramObservation, hash [32]byte, instanceID, revisionID catalogmodel.ID, classification catalogmodel.Classification) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO program_observations(sync_id, provider_service_locator, provider_event_locator,
		 raw_event_id, content_hash, program_instance_id, program_revision_id, classification, validation_reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, syncID.Bytes(), observation.ServiceLocator,
		observation.EventLocator, observation.RawEventID, hash[:], instanceID.Bytes(), revisionID.Bytes(),
		string(classification), observation.Reason)
	if err != nil {
		return sanitize("insert-program-observation", err)
	}
	return nil
}

func syncStartedAt(ctx context.Context, tx *sql.Tx, syncID catalogmodel.ID) (int64, error) {
	var value int64
	if err := tx.QueryRowContext(ctx, `SELECT started_at_utc_ms FROM catalog_syncs WHERE id=? AND state='RUNNING'`, syncID.Bytes()).Scan(&value); err != nil {
		return 0, sanitize("read-sync-time", err)
	}
	return value, nil
}

func requireOneRow(result sql.Result, reason string) error {
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return fmt.Errorf("sqlite: %s", reason)
	}
	return nil
}

func validText(value string, minimum, maximum int) bool {
	return len(value) >= minimum && len(value) <= maximum && utf8.ValidString(value)
}

func validOptionalText(value *string, maximum int) bool {
	return value == nil || validText(*value, 0, maximum)
}

func validCorrelation(value string) bool {
	if !validText(value, 1, 128) {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '-' && character != '_' && character != '.' && character != ':' {
			return false
		}
	}
	return true
}

func validStableReason(value string) bool {
	if len(value) < 1 || len(value) > 96 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func validStableReasonPointer(value *string) bool {
	return value == nil || validStableReason(*value)
}

func hashService(observation catalogmodel.ServiceObservation) [32]byte {
	hash := sha256.New()
	writeHashField(hash, observation.ProviderLocator)
	writeHashField(hash, observation.DisplayName)
	if observation.NetworkID != nil {
		var raw [8]byte
		binary.BigEndian.PutUint64(raw[:], uint64(*observation.NetworkID))
		hash.Write(raw[:])
	}
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result
}

type hashWriter interface{ Write([]byte) (int, error) }

func writeHashField(destination hashWriter, value string) {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(value)))
	_, _ = destination.Write(size[:])
	_, _ = destination.Write([]byte(value))
}

func validationText(value catalogmodel.Validation) string {
	switch value {
	case catalogmodel.ValidationValid:
		return "VALID"
	case catalogmodel.ValidationProvisional:
		return "PROVISIONAL"
	default:
		return "INVALID"
	}
}

func materialFromSQL(start, duration sql.NullInt64, title, description sql.NullString, free sql.NullInt64, validation string, metadata []byte) (catalogmodel.RevisionMaterial, error) {
	material := catalogmodel.RevisionMaterial{Validation: catalogmodel.ValidationInvalid}
	if start.Valid {
		material.StartUTCMS = &start.Int64
	}
	if duration.Valid {
		material.DurationMS = &duration.Int64
	}
	if title.Valid {
		material.Title = &title.String
	}
	if description.Valid {
		material.Description = &description.String
	}
	if free.Valid && free.Int64 == 0 {
		material.FreeAccess = catalogmodel.FreeNo
	} else if free.Valid {
		material.FreeAccess = catalogmodel.FreeYes
	}
	if validation == "VALID" {
		material.Validation = catalogmodel.ValidationValid
	} else if validation == "PROVISIONAL" {
		material.Validation = catalogmodel.ValidationProvisional
	}
	decoded, err := catalogmodel.DecodeMetadataV1(metadata)
	if err != nil {
		return catalogmodel.RevisionMaterial{}, errors.New("sqlite: corrupt program metadata")
	}
	material.Metadata = decoded
	return material, nil
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func copyExact(destination, source []byte) error {
	if len(destination) != len(source) {
		return errors.New("sqlite: corrupt fixed-width value")
	}
	copy(destination, source)
	return nil
}

func stableReason(value *string, fallback string) string {
	if value == nil || *value == "" || len(*value) > 96 {
		return fallback
	}
	return *value
}
