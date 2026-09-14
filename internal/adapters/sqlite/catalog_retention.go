package sqlite

import (
	"context"
	"database/sql"
	"errors"
)

const catalogPruneBatchSize = 1000

// CatalogPruneResultは一回の番組表GCで削除した行数と、全段階が空だったかを表す。
type CatalogPruneResult struct {
	ProgramObservationsDeleted int
	ServiceObservationsDeleted int
	CatalogSyncsDeleted        int
	ProgramRevisionsDeleted    int
	ProgramInstancesDeleted    int
	ServicesDeleted            int
	AllEmpty                   bool
}

// PruneCatalogBatchは削除順の最初の非空段階を、1 transactionで最大1,000行整理する。
func (store *Store) PruneCatalogBatch(ctx context.Context, cutoffUTCMS int64) (CatalogPruneResult, error) {
	var result CatalogPruneResult
	if store == nil || store.writer == nil || ctx == nil || cutoffUTCMS < 0 {
		return result, errors.New("sqlite: invalid catalog prune operation")
	}
	tx, err := store.writer.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return result, sanitize("begin-catalog-prune", err)
	}
	defer tx.Rollback()

	count, err := pruneCatalogRows(ctx, tx, pruneProgramObservationsSQL)
	if err != nil {
		return result, err
	}
	if count != 0 {
		result.ProgramObservationsDeleted = count
		return commitCatalogPrune(ctx, tx, result)
	}

	count, err = pruneCatalogRows(ctx, tx, pruneServiceObservationsSQL)
	if err != nil {
		return result, err
	}
	if count != 0 {
		result.ServiceObservationsDeleted = count
		return commitCatalogPrune(ctx, tx, result)
	}

	count, err = pruneCatalogRows(ctx, tx, pruneCatalogSyncsSQL, cutoffUTCMS)
	if err != nil {
		return result, err
	}
	if count != 0 {
		result.CatalogSyncsDeleted = count
		return commitCatalogPrune(ctx, tx, result)
	}

	count, err = pruneCatalogRows(ctx, tx, pruneProgramRevisionsSQL, cutoffUTCMS)
	if err != nil {
		return result, err
	}
	if count != 0 {
		result.ProgramRevisionsDeleted = count
		return commitCatalogPrune(ctx, tx, result)
	}

	count, err = pruneCatalogRows(ctx, tx, pruneProgramInstancesSQL, cutoffUTCMS)
	if err != nil {
		return result, err
	}
	if count != 0 {
		result.ProgramInstancesDeleted = count
		return commitCatalogPrune(ctx, tx, result)
	}

	count, err = pruneCatalogRows(ctx, tx, pruneServicesSQL, cutoffUTCMS)
	if err != nil {
		return result, err
	}
	if count != 0 {
		result.ServicesDeleted = count
		return commitCatalogPrune(ctx, tx, result)
	}

	result.AllEmpty = true
	return commitCatalogPrune(ctx, tx, result)
}

func pruneCatalogRows(ctx context.Context, tx *sql.Tx, statement string, arguments ...any) (int, error) {
	result, err := tx.ExecContext(ctx, statement, arguments...)
	if err != nil {
		return 0, sanitize("catalog-prune-stage", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count < 0 || count > catalogPruneBatchSize {
		return 0, errors.New("sqlite: invalid catalog prune result")
	}
	return int(count), nil
}

func commitCatalogPrune(ctx context.Context, tx *sql.Tx, result CatalogPruneResult) (CatalogPruneResult, error) {
	if err := ctx.Err(); err != nil {
		return CatalogPruneResult{}, sanitize("catalog-prune-stage", err)
	}
	if err := tx.Commit(); err != nil {
		return CatalogPruneResult{}, sanitize("commit-catalog-prune", err)
	}
	return result, nil
}

const pruneProgramObservationsSQL = `
WITH candidate_syncs AS (
	SELECT cs.id, cs.finished_at_utc_ms
	FROM catalog_syncs AS cs INDEXED BY catalog_gc_sync_terminal_idx
	WHERE cs.state IN ('COMPLETED', 'FAILED')
	  AND (cs.state = 'FAILED' OR (
		SELECT count(*)
		FROM catalog_syncs AS newer INDEXED BY catalog_syncs_completed_backend_idx
		WHERE newer.backend_instance_id = cs.backend_instance_id
		  AND newer.state = 'COMPLETED'
		  AND (newer.finished_at_utc_ms > cs.finished_at_utc_ms OR
		       (newer.finished_at_utc_ms = cs.finished_at_utc_ms AND newer.id > cs.id))
	  ) >= 3)
	ORDER BY cs.finished_at_utc_ms ASC, cs.id ASC
), candidate AS (
	SELECT po.sequence
	FROM candidate_syncs AS cs
	JOIN program_observations AS po INDEXED BY program_observations_sync_instance_idx
	  ON po.sync_id = cs.id
	ORDER BY cs.finished_at_utc_ms ASC, cs.id ASC, po.sequence ASC
	LIMIT 1000
)
DELETE FROM program_observations
WHERE sequence IN (SELECT sequence FROM candidate)`

const pruneServiceObservationsSQL = `
WITH candidate_syncs AS (
	SELECT cs.id, cs.finished_at_utc_ms
	FROM catalog_syncs AS cs INDEXED BY catalog_gc_sync_terminal_idx
	WHERE cs.state IN ('COMPLETED', 'FAILED')
	  AND (cs.state = 'FAILED' OR (
		SELECT count(*)
		FROM catalog_syncs AS newer INDEXED BY catalog_syncs_completed_backend_idx
		WHERE newer.backend_instance_id = cs.backend_instance_id
		  AND newer.state = 'COMPLETED'
		  AND (newer.finished_at_utc_ms > cs.finished_at_utc_ms OR
		       (newer.finished_at_utc_ms = cs.finished_at_utc_ms AND newer.id > cs.id))
	  ) >= 3)
	ORDER BY cs.finished_at_utc_ms ASC, cs.id ASC
), candidate AS (
	SELECT so.sequence
	FROM candidate_syncs AS cs
	JOIN service_observations AS so INDEXED BY service_observations_sync_service_idx
	  ON so.sync_id = cs.id
	ORDER BY cs.finished_at_utc_ms ASC, cs.id ASC, so.sequence ASC
	LIMIT 1000
)
DELETE FROM service_observations
WHERE sequence IN (SELECT sequence FROM candidate)`

const pruneCatalogSyncsSQL = `
WITH candidate AS (
	SELECT cs.id
	FROM catalog_syncs AS cs INDEXED BY catalog_gc_sync_terminal_idx
	WHERE cs.state IN ('COMPLETED', 'FAILED')
	  AND cs.finished_at_utc_ms < ?
	  AND (cs.state = 'FAILED' OR (
		SELECT count(*)
		FROM catalog_syncs AS newer INDEXED BY catalog_syncs_completed_backend_idx
		WHERE newer.backend_instance_id = cs.backend_instance_id
		  AND newer.state = 'COMPLETED'
		  AND (newer.finished_at_utc_ms > cs.finished_at_utc_ms OR
		       (newer.finished_at_utc_ms = cs.finished_at_utc_ms AND newer.id > cs.id))
	  ) >= 3)
	  AND NOT EXISTS (
		SELECT 1
		FROM service_observations AS so INDEXED BY service_observations_sync_service_idx
		WHERE so.sync_id = cs.id
	  )
	  AND NOT EXISTS (
		SELECT 1
		FROM program_observations AS po INDEXED BY program_observations_sync_instance_idx
		WHERE po.sync_id = cs.id
	  )
	ORDER BY cs.finished_at_utc_ms ASC, cs.id ASC
	LIMIT 1000
)
DELETE FROM catalog_syncs
WHERE id IN (SELECT id FROM candidate)`

const pruneProgramRevisionsSQL = `
WITH eligible_instances AS (
	SELECT pi.id, pi.last_seen_at_utc_ms
	FROM program_instances AS pi INDEXED BY catalog_gc_program_instance_cutoff_idx
	WHERE pi.last_seen_at_utc_ms < ?
	  AND NOT EXISTS (
		SELECT 1
		FROM program_observations AS po INDEXED BY catalog_gc_program_observation_instance_idx
		WHERE po.program_instance_id = pi.id
	  )
	  AND NOT EXISTS (
		SELECT 1
		FROM program_observations AS po INDEXED BY catalog_gc_program_observation_revision_idx
		JOIN program_revisions AS referenced ON referenced.id = po.program_revision_id
		WHERE referenced.program_instance_id = pi.id
	  )
	  AND NOT EXISTS (
		SELECT 1
		FROM reservations AS r INDEXED BY catalog_gc_reservation_instance_idx
		WHERE r.program_instance_id = pi.id
	  )
	  AND NOT EXISTS (
		SELECT 1
		FROM reservations AS r INDEXED BY catalog_gc_reservation_revision_idx
		JOIN program_revisions AS referenced ON referenced.id = r.program_revision_id
		WHERE referenced.program_instance_id = pi.id
	  )
	  AND NOT EXISTS (
		SELECT 1
		FROM automatic_reservation_matches AS arm
		WHERE arm.program_instance_id = pi.id
	  )
), candidate AS (
	SELECT pr.id
	FROM eligible_instances AS pi
	JOIN program_revisions AS pr INDEXED BY program_revisions_instance_number_idx
	  ON pr.program_instance_id = pi.id
	ORDER BY pi.last_seen_at_utc_ms ASC, pi.id ASC, pr.revision_number ASC, pr.id ASC
	LIMIT 1000
)
DELETE FROM program_revisions
WHERE id IN (SELECT id FROM candidate)`

const pruneProgramInstancesSQL = `
WITH candidate AS (
	SELECT pi.id
	FROM program_instances AS pi INDEXED BY catalog_gc_program_instance_cutoff_idx
	WHERE pi.last_seen_at_utc_ms < ?
	  AND NOT EXISTS (
		SELECT 1
		FROM program_revisions AS pr INDEXED BY program_revisions_instance_number_idx
		WHERE pr.program_instance_id = pi.id
	  )
	  AND NOT EXISTS (
		SELECT 1
		FROM program_observations AS po INDEXED BY catalog_gc_program_observation_instance_idx
		WHERE po.program_instance_id = pi.id
	  )
	  AND NOT EXISTS (
		SELECT 1
		FROM reservations AS r INDEXED BY catalog_gc_reservation_instance_idx
		WHERE r.program_instance_id = pi.id
	  )
	  AND NOT EXISTS (
		SELECT 1
		FROM automatic_reservation_matches AS arm
		WHERE arm.program_instance_id = pi.id
	  )
	ORDER BY pi.last_seen_at_utc_ms ASC, pi.id ASC
	LIMIT 1000
)
DELETE FROM program_instances
WHERE id IN (SELECT id FROM candidate)`

const pruneServicesSQL = `
WITH candidate AS (
	SELECT s.id
	FROM services AS s INDEXED BY catalog_gc_service_cutoff_idx
	WHERE s.last_seen_at_utc_ms < ?
	  AND NOT EXISTS (
		SELECT 1
		FROM service_observations AS so INDEXED BY catalog_gc_service_observation_service_idx
		WHERE so.service_id = s.id
	  )
	  AND NOT EXISTS (
		SELECT 1
		FROM program_instances AS pi INDEXED BY program_instances_service_event_idx
		WHERE pi.service_id = s.id
	  )
	ORDER BY s.last_seen_at_utc_ms ASC, s.id ASC
	LIMIT 1000
)
DELETE FROM services
WHERE id IN (SELECT id FROM candidate)`
