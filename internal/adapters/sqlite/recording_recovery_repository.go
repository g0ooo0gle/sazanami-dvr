package sqlite

import (
	"context"
	"database/sql"
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
		a.planned_start_utc_ms, a.planned_end_utc_ms, a.byte_count, a.finalization_token,
		a.planned_final_state, a.planned_terminal_reason, a.recovered,
		m.relative_partial_path, m.relative_final_path, m.state, m.byte_count, m.file_synced, m.final_published,
		m.directory_synced, m.availability, COALESCE(m.integrity_reason,''),
		o.relative_partial_path, o.relative_final_path, o.state, o.byte_count, o.file_synced, o.final_published,
		o.directory_synced, o.availability, o.integrity_reason,
		(SELECT count(*) FROM recording_segments c WHERE c.attempt_id=a.id),
		(SELECT count(*) FROM recording_segments c WHERE c.attempt_id=a.id AND c.ordinal NOT IN (0,1))
		FROM recording_attempts a
		LEFT JOIN recording_segments m ON m.attempt_id=a.id AND m.ordinal=0
		LEFT JOIN recording_segments o ON o.attempt_id=a.id AND o.ordinal=1
		WHERE a.id>? AND (a.state IN ('CLAIMED','STARTING','RECORDING','FINALIZING','SUCCEEDED') OR
			(a.state='PARTIAL' AND a.terminal_reason='USER_REQUESTED_STOP'))
		ORDER BY a.id LIMIT ?`, after.Bytes(), limit)
	if err != nil {
		return nil, sanitize("query-recording-recovery", err)
	}
	defer rows.Close()
	items := make([]recording.RecoveryItem, 0, limit)
	for rows.Next() {
		var item recording.RecoveryItem
		var id, reservationID, token []byte
		var startMS, endMS, byteCount, mainByteCount int64
		var plannedState, plannedReason sql.NullString
		var recovered, fileSynced, finalPublished, directorySynced int64
		var mainPartial, mainFinal sql.NullString
		var onePartial, oneFinal, oneState, oneAvailability, oneIntegrity sql.NullString
		var oneByteCount, oneFileSynced, oneFinalPublished, oneDirectorySynced sql.NullInt64
		var segmentCount, invalidOrdinalCount int
		if err := rows.Scan(&id, &reservationID, &item.State, &startMS, &endMS, &byteCount, &token,
			&plannedState, &plannedReason, &recovered, &mainPartial, &mainFinal, &item.SegmentState, &mainByteCount,
			&fileSynced, &finalPublished, &directorySynced, &item.Availability, &item.IntegrityReason,
			&onePartial, &oneFinal, &oneState, &oneByteCount, &oneFileSynced, &oneFinalPublished,
			&oneDirectorySynced, &oneAvailability, &oneIntegrity, &segmentCount, &invalidOrdinalCount); err != nil {
			return nil, sanitize("scan-recording-recovery", err)
		}
		if !mainPartial.Valid || !mainFinal.Valid || segmentCount < 1 || segmentCount > 2 || invalidOrdinalCount != 0 ||
			(segmentCount == 1) != (!onePartial.Valid && !oneFinal.Valid && !oneState.Valid && !oneByteCount.Valid &&
				!oneFileSynced.Valid && !oneFinalPublished.Valid && !oneDirectorySynced.Valid && !oneAvailability.Valid &&
				!oneIntegrity.Valid) {
			return nil, errors.New("sqlite: corrupt recording segment set")
		}
		item.Plan = recording.FilePlan{PartialPath: mainPartial.String, FinalPath: mainFinal.String}
		if plannedState.Valid {
			item.PlannedState = recording.AttemptState(plannedState.String)
		}
		if plannedReason.Valid {
			item.PlannedReason = recording.TerminalReason(plannedReason.String)
		}
		if err := validateRecoveryValues(item, startMS, endMS, byteCount, mainByteCount, recovered, fileSynced,
			finalPublished, directorySynced, len(token)); err != nil {
			return nil, err
		}
		if segmentCount == 2 {
			if !onePartial.Valid || !oneFinal.Valid || !oneState.Valid || !oneByteCount.Valid || !oneFileSynced.Valid ||
				!oneFinalPublished.Valid || !oneDirectorySynced.Valid || !oneAvailability.Valid {
				return nil, errors.New("sqlite: incomplete one-seg recovery segment")
			}
			oneSeg := &recording.RecoverySegment{
				Plan:      recording.FilePlan{PartialPath: onePartial.String, FinalPath: oneFinal.String},
				ByteCount: oneByteCount.Int64, State: recording.SegmentState(oneState.String),
				FileSynced: oneFileSynced.Int64 == 1, FinalPublished: oneFinalPublished.Int64 == 1,
				DirectorySynced: oneDirectorySynced.Int64 == 1,
				Availability:    recording.Availability(oneAvailability.String),
			}
			if oneIntegrity.Valid {
				oneSeg.IntegrityReason = recording.TerminalReason(oneIntegrity.String)
			}
			if err := validateRecoverySegment(*oneSeg, oneFileSynced.Int64, oneFinalPublished.Int64,
				oneDirectorySynced.Int64); err != nil {
				return nil, err
			}
			item.OneSeg = oneSeg
			plan := oneSeg.Plan
			item.OneSegPlan = &plan
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

// SetRecordingAvailabilityは録画結果を変えず、公開済みファイルの現在の利用状態だけを更新する。
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
		AND EXISTS (SELECT 1 FROM recording_attempts a WHERE a.id=? AND
			(a.state='SUCCEEDED' OR (a.state='PARTIAL' AND a.terminal_reason='USER_REQUESTED_STOP')))
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

// SetOneSegAvailabilityは終了済み録画のordinal 1だけを再照合結果へ更新する。
func (store *Store) SetOneSegAvailability(ctx context.Context, attemptID catalogmodel.ID,
	availability recording.Availability, reason recording.TerminalReason, now time.Time,
) error {
	if store == nil || store.writer == nil || ctx == nil || attemptID == (catalogmodel.ID{}) ||
		now.IsZero() || now.Location() != time.UTC || now.UnixMilli() < 0 {
		return errors.New("sqlite: invalid one-seg availability update")
	}
	var integrity any
	switch availability {
	case recording.AvailabilityFinal:
		if reason != "" {
			return errors.New("sqlite: final one-seg must not have integrity reason")
		}
	case recording.AvailabilityMissing:
		if reason != recording.ReasonFileMissing {
			return errors.New("sqlite: missing one-seg requires stable reason")
		}
		integrity = reason
	case recording.AvailabilityMismatched:
		if reason != recording.ReasonFileIntegrityMismatch {
			return errors.New("sqlite: mismatched one-seg requires stable reason")
		}
		integrity = reason
	default:
		return errors.New("sqlite: invalid completed one-seg availability")
	}
	integrityText := ""
	if integrity != nil {
		integrityText = string(reason)
	}
	result, err := store.writer.ExecContext(ctx, `UPDATE recording_segments SET availability=?, integrity_reason=?,
		updated_at_utc_ms=? WHERE attempt_id=? AND ordinal=1 AND state IN ('PARTIAL','FINALIZED')
		AND EXISTS (SELECT 1 FROM recording_attempts a WHERE a.id=? AND
			(a.state='SUCCEEDED' OR (a.state='PARTIAL' AND a.terminal_reason='USER_REQUESTED_STOP')))
		AND (?<>'FINAL' OR state='FINALIZED')
		AND (availability<>? OR COALESCE(integrity_reason,'')<>?)`, availability, integrity, now.UnixMilli(),
		attemptID.Bytes(), attemptID.Bytes(), availability, availability, integrityText)
	if err != nil {
		return sanitize("update-one-seg-availability", err)
	}
	if count := affected(result); count < 0 || count > 1 {
		return errors.New("sqlite: one-seg availability update count mismatch")
	}
	return nil
}

func validateRecoveryValues(item recording.RecoveryItem, startMS, endMS, byteCount, mainByteCount, recovered,
	fileSynced, finalPublished, directorySynced int64, tokenBytes int,
) error {
	if startMS < 0 || endMS <= startMS || byteCount < 0 || mainByteCount < 0 || byteCount != mainByteCount ||
		item.Plan.Validate() != nil ||
		(recovered != 0 && recovered != 1) || (fileSynced != 0 && fileSynced != 1) ||
		(finalPublished != 0 && finalPublished != 1) || (directorySynced != 0 && directorySynced != 1) {
		return errors.New("sqlite: corrupt recording recovery value")
	}
	switch item.State {
	case recording.AttemptClaimed, recording.AttemptStarting, recording.AttemptRecording,
		recording.AttemptFinalizing, recording.AttemptSucceeded, recording.AttemptPartial:
	default:
		return errors.New("sqlite: invalid recording recovery state")
	}
	switch item.Availability {
	case recording.AvailabilityPlanned, recording.AvailabilityPartial, recording.AvailabilityFinal,
		recording.AvailabilityMissing, recording.AvailabilityMismatched:
	default:
		return errors.New("sqlite: invalid recording availability")
	}
	switch item.SegmentState {
	case recording.SegmentPlanned, recording.SegmentWriting, recording.SegmentPartial, recording.SegmentFinalized:
	default:
		return errors.New("sqlite: invalid main recovery segment state")
	}
	if (item.State == recording.AttemptFinalizing || item.State == recording.AttemptSucceeded || item.State == recording.AttemptPartial) &&
		(fileSynced != 1 || tokenBytes != 16 || byteCount < 188) {
		return errors.New("sqlite: completed recording lacks finalization evidence")
	}
	if (directorySynced == 1 && finalPublished != 1) || (finalPublished == 1 && fileSynced != 1) {
		return errors.New("sqlite: recording publication flags are inconsistent")
	}
	if (item.State == recording.AttemptSucceeded || item.State == recording.AttemptPartial) &&
		(finalPublished != 1 || directorySynced != 1) {
		return errors.New("sqlite: successful recording lacks publication evidence")
	}
	if item.State == recording.AttemptFinalizing && item.Availability != recording.AvailabilityPartial {
		return errors.New("sqlite: finalizing recording is not partial")
	}
	if (item.State == recording.AttemptSucceeded || item.State == recording.AttemptPartial) && item.Availability != recording.AvailabilityFinal &&
		item.Availability != recording.AvailabilityMissing && item.Availability != recording.AvailabilityMismatched {
		return errors.New("sqlite: successful recording has invalid availability")
	}
	if item.State != recording.AttemptFinalizing && item.State != recording.AttemptSucceeded && item.State != recording.AttemptPartial &&
		(tokenBytes != 0 || fileSynced != 0 || finalPublished != 0 || directorySynced != 0) {
		return errors.New("sqlite: early recording has finalization evidence")
	}
	if item.State == recording.AttemptFinalizing || item.State == recording.AttemptSucceeded || item.State == recording.AttemptPartial {
		validNormal := item.PlannedState == recording.AttemptSucceeded &&
			(item.PlannedReason == recording.ReasonCompleted || item.PlannedReason == recording.ReasonCompletedAfterReconnect)
		validStopped := item.PlannedState == recording.AttemptPartial && item.PlannedReason == recording.ReasonUserRequestedStop
		if !validNormal && !validStopped {
			return errors.New("sqlite: recording finalization plan is invalid")
		}
		if item.State == recording.AttemptSucceeded && !validNormal || item.State == recording.AttemptPartial && !validStopped {
			return errors.New("sqlite: terminal recording differs from finalization plan")
		}
	}
	return nil
}

func validateRecoverySegment(segment recording.RecoverySegment, fileSynced, finalPublished,
	directorySynced int64,
) error {
	if segment.Plan.Validate() != nil || segment.ByteCount < 0 || (fileSynced != 0 && fileSynced != 1) ||
		(finalPublished != 0 && finalPublished != 1) || (directorySynced != 0 && directorySynced != 1) {
		return errors.New("sqlite: corrupt one-seg recovery value")
	}
	switch segment.State {
	case recording.SegmentPlanned, recording.SegmentWriting, recording.SegmentPartial, recording.SegmentFinalized:
	default:
		return errors.New("sqlite: invalid one-seg recovery state")
	}
	switch segment.Availability {
	case recording.AvailabilityPlanned, recording.AvailabilityPartial, recording.AvailabilityFinal,
		recording.AvailabilityMissing, recording.AvailabilityMismatched:
	default:
		return errors.New("sqlite: invalid one-seg recovery availability")
	}
	if (directorySynced == 1 && finalPublished != 1) || (finalPublished == 1 && fileSynced != 1) {
		return errors.New("sqlite: one-seg publication flags are inconsistent")
	}
	if segment.Availability == recording.AvailabilityFinal && (segment.State != recording.SegmentFinalized ||
		fileSynced != 1 || finalPublished != 1 || directorySynced != 1 || segment.IntegrityReason != "") {
		return errors.New("sqlite: final one-seg lacks publication evidence")
	}
	if segment.Availability == recording.AvailabilityPlanned &&
		(segment.State != recording.SegmentPlanned && segment.State != recording.SegmentWriting) {
		return errors.New("sqlite: planned one-seg state is inconsistent")
	}
	publishCandidate := segment.Availability == recording.AvailabilityPartial && segment.State == recording.SegmentPartial &&
		fileSynced == 1 && segment.IntegrityReason == ""
	if segment.Availability != recording.AvailabilityPlanned && segment.Availability != recording.AvailabilityFinal &&
		!publishCandidate && !segment.IntegrityReason.Valid() {
		return errors.New("sqlite: one-seg outcome lacks stable reason")
	}
	return nil
}
