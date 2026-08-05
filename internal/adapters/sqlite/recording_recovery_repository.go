package sqlite

import (
	"context"
	"errors"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

// RecoveryAttemptsは再起動時に照合が必要な未完了処理と成功済み処理を、ID順に100件まで返す。
func (store *Store) RecoveryAttempts(ctx context.Context, limit int, after catalogmodel.ID) ([]recording.RecoveryItem, error) {
	if store == nil || store.reader == nil || ctx == nil || limit < 1 || limit > recording.MaxRecoveryPage {
		return nil, errors.New("sqlite: invalid recovery query")
	}
	rows, err := store.reader.QueryContext(ctx, `SELECT a.id, a.reservation_id, a.state,
		a.planned_start_utc_ms, a.planned_end_utc_ms, a.byte_count, a.finalization_token, a.recovered,
		s.relative_partial_path, s.relative_final_path, s.file_synced, s.final_published,
		s.directory_synced, s.availability
		FROM recording_attempts a JOIN recording_segments s ON s.attempt_id=a.id AND s.ordinal=0
		WHERE a.id>? AND a.state IN ('CLAIMED','STARTING','RECORDING','FINALIZING','SUCCEEDED')
		ORDER BY a.id LIMIT ?`, after.Bytes(), limit)
	if err != nil {
		return nil, sanitize("query-recording-recovery", err)
	}
	defer rows.Close()
	items := make([]recording.RecoveryItem, 0, limit)
	for rows.Next() {
		var item recording.RecoveryItem
		var id, reservationID, token []byte
		var startMS, endMS, byteCount int64
		var recovered, fileSynced, finalPublished, directorySynced int64
		if err := rows.Scan(&id, &reservationID, &item.State, &startMS, &endMS, &byteCount, &token,
			&recovered, &item.Plan.PartialPath, &item.Plan.FinalPath, &fileSynced, &finalPublished,
			&directorySynced, &item.Availability); err != nil {
			return nil, sanitize("scan-recording-recovery", err)
		}
		if err := validateRecoveryValues(item, startMS, endMS, byteCount, recovered, fileSynced,
			finalPublished, directorySynced, len(token)); err != nil {
			return nil, err
		}
		if err := copyExact(item.ID[:], id); err != nil {
			return nil, err
		}
		if err := copyExact(item.ReservationID[:], reservationID); err != nil {
			return nil, err
		}
		if len(token) != 0 {
			if err := copyExact(item.FinalizationToken[:], token); err != nil {
				return nil, err
			}
		}
		item.PlannedStart = time.UnixMilli(startMS).UTC()
		item.PlannedEnd = time.UnixMilli(endMS).UTC()
		item.ByteCount = byteCount
		item.Recovered = recovered == 1
		item.FileSynced = fileSynced == 1
		item.FinalPublished = finalPublished == 1
		item.DirectorySynced = directorySynced == 1
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, sanitize("iterate-recording-recovery", err)
	}
	return items, nil
}

// SetRecordingAvailabilityは成功履歴を変えず、完成ファイルの現在の利用状態だけを更新する。
func (store *Store) SetRecordingAvailability(ctx context.Context, attemptID catalogmodel.ID, availability recording.Availability, reason recording.TerminalReason, now time.Time) error {
	if store == nil || store.writer == nil || ctx == nil || attemptID == (catalogmodel.ID{}) ||
		now.IsZero() || now.Location() != time.UTC || now.UnixMilli() < 0 {
		return errors.New("sqlite: invalid recording availability update")
	}
	var integrity any
	switch availability {
	case recording.AvailabilityFinal:
		if reason != "" {
			return errors.New("sqlite: final recording must not have integrity reason")
		}
	case recording.AvailabilityMissing:
		if reason != recording.ReasonFileMissing {
			return errors.New("sqlite: missing recording requires stable reason")
		}
		integrity = reason
	case recording.AvailabilityMismatched:
		if reason != recording.ReasonFileIntegrityMismatch {
			return errors.New("sqlite: mismatched recording requires stable reason")
		}
		integrity = reason
	default:
		return errors.New("sqlite: invalid successful recording availability")
	}
	integrityText := ""
	if integrity != nil {
		integrityText = string(reason)
	}
	result, err := store.writer.ExecContext(ctx, `UPDATE recording_segments SET availability=?, integrity_reason=?,
		updated_at_utc_ms=? WHERE attempt_id=? AND ordinal=0 AND state='FINALIZED'
		AND EXISTS (SELECT 1 FROM recording_attempts a WHERE a.id=? AND a.state='SUCCEEDED')
		AND (availability<>? OR COALESCE(integrity_reason,'')<>?)`, availability, integrity, now.UnixMilli(),
		attemptID.Bytes(), attemptID.Bytes(), availability, integrityText)
	if err != nil {
		return sanitize("update-recording-availability", err)
	}
	if count := affected(result); count < 0 || count > 1 {
		return errors.New("sqlite: recording availability update count mismatch")
	}
	return nil
}

func validateRecoveryValues(item recording.RecoveryItem, startMS, endMS, byteCount, recovered, fileSynced, finalPublished, directorySynced int64, tokenBytes int) error {
	if startMS < 0 || endMS <= startMS || byteCount < 0 || item.Plan.Validate() != nil ||
		(recovered != 0 && recovered != 1) || (fileSynced != 0 && fileSynced != 1) ||
		(finalPublished != 0 && finalPublished != 1) || (directorySynced != 0 && directorySynced != 1) {
		return errors.New("sqlite: corrupt recording recovery value")
	}
	switch item.State {
	case recording.AttemptClaimed, recording.AttemptStarting, recording.AttemptRecording,
		recording.AttemptFinalizing, recording.AttemptSucceeded:
	default:
		return errors.New("sqlite: invalid recording recovery state")
	}
	switch item.Availability {
	case recording.AvailabilityPlanned, recording.AvailabilityPartial, recording.AvailabilityFinal,
		recording.AvailabilityMissing, recording.AvailabilityMismatched:
	default:
		return errors.New("sqlite: invalid recording availability")
	}
	if (item.State == recording.AttemptFinalizing || item.State == recording.AttemptSucceeded) &&
		(fileSynced != 1 || tokenBytes != 16 || byteCount < 188) {
		return errors.New("sqlite: completed recording lacks finalization evidence")
	}
	if (directorySynced == 1 && finalPublished != 1) || (finalPublished == 1 && fileSynced != 1) {
		return errors.New("sqlite: recording publication flags are inconsistent")
	}
	if item.State == recording.AttemptSucceeded && (finalPublished != 1 || directorySynced != 1) {
		return errors.New("sqlite: successful recording lacks publication evidence")
	}
	if item.State == recording.AttemptFinalizing && item.Availability != recording.AvailabilityPartial {
		return errors.New("sqlite: finalizing recording is not partial")
	}
	if item.State == recording.AttemptSucceeded && item.Availability != recording.AvailabilityFinal &&
		item.Availability != recording.AvailabilityMissing && item.Availability != recording.AvailabilityMismatched {
		return errors.New("sqlite: successful recording has invalid availability")
	}
	if item.State != recording.AttemptFinalizing && item.State != recording.AttemptSucceeded &&
		(tokenBytes != 0 || fileSynced != 0 || finalPublished != 0 || directorySynced != 0) {
		return errors.New("sqlite: early recording has finalization evidence")
	}
	return nil
}
