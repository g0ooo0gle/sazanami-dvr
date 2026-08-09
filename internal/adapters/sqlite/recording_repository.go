package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

var (
	// ErrDuplicateReservationは同じ放送番組の未完了予約が既にあることを表す。
	ErrDuplicateReservation = errors.New("sqlite: duplicate active reservation")
	// ErrReservationLimitは未完了予約またはCtrlCmd予約番号の上限を表す。
	ErrReservationLimit = errors.New("sqlite: reservation limit reached")
	// ErrAttemptExistsは対象予約が既に録画処理へ割り当て済みであることを表す。
	ErrAttemptExists = errors.New("sqlite: recording attempt already exists")
	// ErrReservationUnavailableは対象予約が存在しないか、既に終了していることを表す。
	ErrReservationUnavailable = errors.New("sqlite: reservation is not available")
	// ErrAttemptStateは録画処理を要求された状態へ進められないことを表す。
	ErrAttemptState = errors.New("sqlite: recording attempt state conflict")
)

type rowScanner interface {
	Scan(dest ...any) error
}

// CreateReservationは予約時点の番組情報とCtrlCmd予約番号を同じトランザクションで保存する。
func (store *Store) CreateReservation(ctx context.Context, reservation recording.Reservation) (recording.Reservation, error) {
	if store == nil || store.writer == nil || ctx == nil || reservation.ValidateNew() != nil {
		return recording.Reservation{}, errors.New("sqlite: invalid reservation")
	}
	tx, err := store.writer.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return recording.Reservation{}, sanitize("begin-reservation", err)
	}
	defer tx.Rollback()

	reservation, err = createReservationTx(ctx, tx, reservation)
	if err != nil {
		return recording.Reservation{}, err
	}
	if err := tx.Commit(); err != nil {
		return recording.Reservation{}, sanitize("commit-reservation", err)
	}
	return reservation, nil
}

func createReservationTx(ctx context.Context, tx *sql.Tx, reservation recording.Reservation) (recording.Reservation, error) {
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM reservations WHERE state='ACTIVE'`).Scan(&active); err != nil {
		return recording.Reservation{}, sanitize("count-reservations", err)
	}
	if active >= recording.MaxActiveReservations {
		return recording.Reservation{}, ErrReservationLimit
	}
	p := reservation.Program
	var duplicate int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM reservations
		WHERE state='ACTIVE' AND backend_instance_id=? AND network_id=? AND transport_stream_id=?
		  AND service_id=? AND event_id=?`, p.BackendID.Bytes(), p.NetworkID, p.TransportStreamID,
		p.ServiceID, p.EventID).Scan(&duplicate); err != nil {
		return recording.Reservation{}, sanitize("find-duplicate-reservation", err)
	}
	if duplicate != 0 {
		return recording.Reservation{}, ErrDuplicateReservation
	}
	createdMS := reservation.CreatedAt.UnixMilli()
	margins := reservation.EffectiveMargins()
	_, err := tx.ExecContext(ctx, `INSERT INTO reservations(
		id, version, state, program_instance_id, program_revision_id, backend_instance_id,
		provider_service_locator, tuning_target, network_id, transport_stream_id, service_id, event_id,
		title, station_name, start_at_utc_ms, duration_seconds, requested_priority, requested_follow,
		effective_follow, created_at_utc_ms, updated_at_utc_ms, enabled, use_default_margins,
		effective_start_margin_seconds, effective_end_margin_seconds, output_folder, output_template, component_mode)
		VALUES (?, 1, 'ACTIVE', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		reservation.ID.Bytes(), p.ProgramInstanceID.Bytes(), p.ProgramRevisionID.Bytes(), p.BackendID.Bytes(),
		p.ProviderServiceLocator, p.TuningTarget, p.NetworkID, p.TransportStreamID, p.ServiceID, p.EventID,
		p.Title, p.StationName, p.Start.UnixMilli(), int64(p.Duration/time.Second), reservation.Priority,
		reservation.RequestedFollow, createdMS, createdMS, !reservation.Disabled, reservation.Margins == nil,
		int64(margins.Start/time.Second), int64(margins.End/time.Second), reservation.Output.Folder, reservation.Output.Template,
		reservation.Components)
	if err != nil {
		return recording.Reservation{}, sanitize("create-reservation", err)
	}
	var number int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO ctrlcmd_reservation_ids(reservation_id) VALUES (?) RETURNING reserve_id`,
		reservation.ID.Bytes()).Scan(&number); err != nil {
		return recording.Reservation{}, sanitize("allocate-reservation-number", err)
	}
	if number < 1 || number > 1<<31-1 {
		return recording.Reservation{}, ErrReservationLimit
	}
	reservation.Number = int32(number)
	reservation.EffectiveFollow = reservation.RequestedFollow
	if _, _, err := recording.ScheduledOutputPath(reservation); err != nil {
		return recording.Reservation{}, errors.New("sqlite: invalid expanded reservation output")
	}
	return reservation, nil
}

// ActiveReservationsは未完了予約をCtrlCmd番号順に、指定された上限まで返す。
func (store *Store) ActiveReservations(ctx context.Context, limit int, after int32) ([]recording.Reservation, error) {
	if store == nil || store.reader == nil || ctx == nil || limit < 1 || limit > recording.MaxPage || after < 0 {
		return nil, errors.New("sqlite: invalid reservation query")
	}
	rows, err := store.reader.QueryContext(ctx, `SELECT r.id, m.reserve_id, r.version, r.state,
		r.program_instance_id, r.program_revision_id, r.backend_instance_id, r.provider_service_locator,
		r.tuning_target, r.network_id, r.transport_stream_id, r.service_id, r.event_id, r.title,
		r.station_name, r.start_at_utc_ms, r.duration_seconds, r.requested_priority, r.requested_follow,
		r.effective_follow, r.created_at_utc_ms, r.updated_at_utc_ms, r.enabled, r.use_default_margins,
		r.effective_start_margin_seconds, r.effective_end_margin_seconds, r.output_folder, r.output_template, r.component_mode
		FROM ctrlcmd_reservation_ids m JOIN reservations r ON r.id=m.reservation_id
		WHERE r.state='ACTIVE' AND m.reserve_id>? ORDER BY m.reserve_id LIMIT ?`, after, limit)
	if err != nil {
		return nil, sanitize("query-reservations", err)
	}
	defer rows.Close()
	result := make([]recording.Reservation, 0, limit)
	for rows.Next() {
		item, err := scanReservation(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, sanitize("iterate-reservations", err)
	}
	return result, nil
}

// CurrentFollowTargetは最新の完成済み番組表から、指定した番組instanceの現在revisionを返す。
func (store *Store) CurrentFollowTarget(ctx context.Context, backendID, instanceID catalogmodel.ID) (*recording.FollowTarget, error) {
	if store == nil || store.reader == nil || ctx == nil || backendID == (catalogmodel.ID{}) || instanceID == (catalogmodel.ID{}) {
		return nil, errors.New("sqlite: invalid follow target query")
	}
	row := store.reader.QueryRowContext(ctx, `WITH current_sync AS (
		SELECT id FROM catalog_syncs WHERE backend_instance_id=? AND state='COMPLETED'
		ORDER BY finished_at_utc_ms DESC, id DESC LIMIT 1
	)
		SELECT po.program_instance_id, pr.id, pr.start_at_utc_ms, pr.duration_ms
		FROM program_observations po JOIN current_sync cs ON cs.id=po.sync_id
		JOIN program_revisions pr ON pr.id=po.program_revision_id
		WHERE po.program_instance_id=? LIMIT 1`, backendID.Bytes(), instanceID.Bytes())
	var target recording.FollowTarget
	var persistedInstance, revisionID []byte
	var startMS, durationMS int64
	if err := row.Scan(&persistedInstance, &revisionID, &startMS, &durationMS); errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, sanitize("read-follow-target", err)
	}
	if startMS < 0 || durationMS < 60_000 || durationMS > 12*60*60*1_000 || durationMS%1_000 != 0 {
		return nil, nil
	}
	if err := copyExact(target.ProgramInstanceID[:], persistedInstance); err != nil {
		return nil, err
	}
	if err := copyExact(target.ProgramRevisionID[:], revisionID); err != nil {
		return nil, err
	}
	target.Start = time.UnixMilli(startMS).UTC()
	target.Duration = time.Duration(durationMS) * time.Millisecond
	return &target, nil
}

// ApplyReservationFollowは評価時の予約とrevisionが変わっていない場合だけ、時刻を一度で更新する。
func (store *Store) ApplyReservationFollow(ctx context.Context, request recording.ReservationFollowRequest) (bool, error) {
	if store == nil || store.writer == nil || ctx == nil || request.Validate() != nil {
		return false, errors.New("sqlite: invalid reservation follow")
	}
	tx, err := store.writer.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return false, sanitize("begin-reservation-follow", err)
	}
	defer tx.Rollback()
	var startMS, durationMS int64
	err = tx.QueryRowContext(ctx, `SELECT start_at_utc_ms, duration_ms FROM program_revisions WHERE id=?`,
		request.TargetRevisionID.Bytes()).Scan(&startMS, &durationMS)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, sanitize("read-reservation-follow-target", err)
	}
	if startMS < 0 || durationMS < 60_000 || durationMS > 12*60*60*1_000 || durationMS%1_000 != 0 {
		return false, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE reservations SET program_revision_id=?, start_at_utc_ms=?,
		duration_seconds=?, version=version+1, updated_at_utc_ms=?
		WHERE id=? AND version=? AND state='ACTIVE' AND requested_follow=1 AND program_revision_id=?
		AND NOT EXISTS (SELECT 1 FROM recording_attempts WHERE reservation_id=reservations.id)
		AND EXISTS (
			SELECT 1 FROM program_revisions target JOIN program_revisions previous
			  ON previous.id=? AND previous.program_instance_id=target.program_instance_id
			JOIN program_observations po ON po.program_revision_id=target.id
			JOIN catalog_syncs cs ON cs.id=po.sync_id
			WHERE target.id=? AND target.program_instance_id=reservations.program_instance_id
			  AND target.revision_number>previous.revision_number
			  AND cs.backend_instance_id=reservations.backend_instance_id AND cs.state='COMPLETED'
			  AND cs.id=(SELECT id FROM catalog_syncs WHERE backend_instance_id=reservations.backend_instance_id
				AND state='COMPLETED' ORDER BY finished_at_utc_ms DESC, id DESC LIMIT 1)
		)`, request.TargetRevisionID.Bytes(), startMS, durationMS/1_000, request.Now.UnixMilli(),
		request.ReservationID.Bytes(), request.ExpectedVersion, request.ExpectedRevisionID.Bytes(),
		request.ExpectedRevisionID.Bytes(), request.TargetRevisionID.Bytes())
	if err != nil {
		return false, sanitize("apply-reservation-follow", err)
	}
	applied := affected(result) == 1
	if !applied {
		applied, err = applyActiveRecordingExtension(ctx, tx, request, startMS, durationMS)
		if err != nil {
			return false, err
		}
	}
	if !applied {
		return false, nil
	}
	if err := tx.Commit(); err != nil {
		return false, sanitize("commit-reservation-follow", err)
	}
	return true, nil
}

// applyActiveRecordingExtensionは実行中の一件だけについて、予約と録画終了時刻を同時に延ばす。
func applyActiveRecordingExtension(ctx context.Context, tx *sql.Tx, request recording.ReservationFollowRequest,
	targetStartMS, targetDurationMS int64,
) (bool, error) {
	var reservationStartMS, reservationDurationSeconds, endMarginSeconds, attemptEndMS int64
	var attemptID []byte
	var attemptState recording.AttemptState
	err := tx.QueryRowContext(ctx, `SELECT r.start_at_utc_ms, r.duration_seconds, r.effective_end_margin_seconds,
		a.id, a.state, a.planned_end_utc_ms
		FROM reservations r JOIN recording_attempts a ON a.reservation_id=r.id
		WHERE r.id=? AND r.version=? AND r.state='ACTIVE' AND r.requested_follow=1
		  AND r.program_revision_id=?`, request.ReservationID.Bytes(), request.ExpectedVersion,
		request.ExpectedRevisionID.Bytes()).Scan(&reservationStartMS, &reservationDurationSeconds, &endMarginSeconds,
		&attemptID, &attemptState, &attemptEndMS)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, sanitize("read-active-recording-extension", err)
	}
	if reservationStartMS < 0 || reservationDurationSeconds < 1 || targetStartMS != reservationStartMS ||
		targetDurationMS < 60_000 || targetDurationMS > 12*60*60*1_000 || targetDurationMS%1_000 != 0 {
		return false, nil
	}
	targetEndMS := targetStartMS + targetDurationMS + endMarginSeconds*1_000
	reservationEndMS := reservationStartMS + reservationDurationSeconds*1_000 + endMarginSeconds*1_000
	if targetEndMS <= attemptEndMS || reservationEndMS != attemptEndMS {
		return false, nil
	}
	switch attemptState {
	case recording.AttemptClaimed, recording.AttemptStarting, recording.AttemptRecording:
	default:
		return false, nil
	}
	var verified int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM program_revisions target JOIN program_revisions previous
		  ON previous.id=? AND previous.program_instance_id=target.program_instance_id
		JOIN program_observations po ON po.program_revision_id=target.id
		JOIN catalog_syncs cs ON cs.id=po.sync_id
		JOIN reservations r ON r.id=?
		WHERE target.id=? AND target.program_instance_id=r.program_instance_id
		  AND target.revision_number>previous.revision_number
		  AND cs.backend_instance_id=r.backend_instance_id AND cs.state='COMPLETED'
		  AND cs.id=(SELECT id FROM catalog_syncs WHERE backend_instance_id=r.backend_instance_id
			AND state='COMPLETED' ORDER BY finished_at_utc_ms DESC, id DESC LIMIT 1)
	)`, request.ExpectedRevisionID.Bytes(), request.ReservationID.Bytes(),
		request.TargetRevisionID.Bytes()).Scan(&verified); err != nil {
		return false, sanitize("verify-active-recording-extension", err)
	}
	if verified != 1 {
		return false, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE reservations SET program_revision_id=?, duration_seconds=?,
		version=version+1, updated_at_utc_ms=? WHERE id=? AND version=? AND state='ACTIVE'
		AND requested_follow=1 AND program_revision_id=? AND start_at_utc_ms=? AND duration_seconds=?`,
		request.TargetRevisionID.Bytes(), targetDurationMS/1_000, request.Now.UnixMilli(),
		request.ReservationID.Bytes(), request.ExpectedVersion, request.ExpectedRevisionID.Bytes(),
		reservationStartMS, reservationDurationSeconds)
	if err != nil {
		return false, sanitize("update-active-recording-reservation", err)
	}
	if affected(result) != 1 {
		return false, nil
	}
	result, err = tx.ExecContext(ctx, `UPDATE recording_attempts SET planned_end_utc_ms=?,
		state_version=state_version+1, updated_at_utc_ms=?
		WHERE id=? AND state=? AND planned_end_utc_ms=?`, targetEndMS, request.Now.UnixMilli(),
		attemptID, attemptState, attemptEndMS)
	if err != nil {
		return false, sanitize("update-active-recording-end", err)
	}
	return affected(result) == 1, nil
}

// UpdateReservationは変更不可の番組情報を照合し、録画開始前の設定だけを更新する。
func (store *Store) UpdateReservation(ctx context.Context, change recording.ReservationChange, now time.Time) error {
	if store == nil || store.writer == nil || ctx == nil || change.Validate() != nil || now.IsZero() ||
		now.Location() != time.UTC || now.UnixMilli() < 0 {
		return errors.New("sqlite: invalid reservation update")
	}
	request := change.Request
	margins := recording.Reservation{Margins: request.Margins}.EffectiveMargins()
	tx, err := store.writer.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return sanitize("begin-reservation-update", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE reservations SET requested_priority=?, requested_follow=?,
		enabled=?, use_default_margins=?, effective_start_margin_seconds=?, effective_end_margin_seconds=?,
		output_folder=?, output_template=?, component_mode=?, version=version+1, updated_at_utc_ms=? WHERE id=(
			SELECT reservation_id FROM ctrlcmd_reservation_ids WHERE reserve_id=?
		) AND state='ACTIVE' AND network_id=? AND transport_stream_id=? AND service_id=? AND event_id=?
		AND start_at_utc_ms=? AND duration_seconds=? AND NOT EXISTS (
			SELECT 1 FROM recording_attempts WHERE reservation_id=reservations.id
		) AND (?=0 OR start_at_utc_ms+(duration_seconds+?)*1000>?)`,
		request.Priority, request.RequestedFollow, !request.Disabled, request.Margins == nil,
		int64(margins.Start/time.Second), int64(margins.End/time.Second), request.Output.Folder, request.Output.Template,
		request.Components,
		now.UnixMilli(), change.Number, request.NetworkID,
		request.TransportStreamID, request.ServiceID, request.EventID, request.Start.UnixMilli(),
		int64(request.Duration/time.Second), !request.Disabled, int64(margins.End/time.Second), now.UnixMilli())
	if err != nil {
		return sanitize("update-reservation", err)
	}
	if affected(result) != 1 {
		return ErrReservationUnavailable
	}
	var title, station string
	if err := tx.QueryRowContext(ctx, `SELECT r.title, r.station_name FROM reservations r
		JOIN ctrlcmd_reservation_ids m ON m.reservation_id=r.id WHERE m.reserve_id=?`, change.Number).
		Scan(&title, &station); err != nil {
		return sanitize("read-updated-reservation-output", err)
	}
	candidate := recording.Reservation{Number: change.Number, Output: request.Output, Program: recording.ProgramSnapshot{
		NetworkID: request.NetworkID, TransportStreamID: request.TransportStreamID,
		ServiceID: request.ServiceID, EventID: request.EventID, Title: title, StationName: station,
		Start: request.Start, Duration: request.Duration,
	}}
	if _, _, err := recording.ScheduledOutputPath(candidate); err != nil {
		return errors.New("sqlite: invalid expanded reservation output")
	}
	if err := tx.Commit(); err != nil {
		return sanitize("commit-reservation-update", err)
	}
	return nil
}

// StopReservationは録画開始前の取消し、または実行中録画への停止要求を一つのトランザクションで確定する。
// 実行中通知に使う内部予約IDは、DBの確定後だけ呼出し元へ返す。
func (store *Store) StopReservation(ctx context.Context, number int32, now time.Time) (recording.StopResult, error) {
	if store == nil || store.writer == nil || ctx == nil || number < 1 || now.IsZero() ||
		now.Location() != time.UTC || now.UnixMilli() < 0 {
		return recording.StopResult{}, errors.New("sqlite: invalid reservation stop")
	}
	tx, err := store.writer.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return recording.StopResult{}, sanitize("begin-reservation-stop", err)
	}
	defer tx.Rollback()
	var reservationID, attemptID []byte
	var reservationState recording.ReservationState
	var attemptState sql.NullString
	var requested sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT r.id, r.state, a.id, a.state, a.stop_requested_at_utc_ms
		FROM ctrlcmd_reservation_ids m JOIN reservations r ON r.id=m.reservation_id
		LEFT JOIN recording_attempts a ON a.reservation_id=r.id WHERE m.reserve_id=?`, number).
		Scan(&reservationID, &reservationState, &attemptID, &attemptState, &requested)
	if errors.Is(err, sql.ErrNoRows) {
		return recording.StopResult{}, ErrReservationUnavailable
	}
	if err != nil {
		return recording.StopResult{}, sanitize("read-reservation-stop", err)
	}
	if reservationState != recording.ReservationActive {
		return recording.StopResult{}, ErrReservationUnavailable
	}
	nowMS := now.UnixMilli()
	if !attemptState.Valid {
		result, updateErr := tx.ExecContext(ctx, `UPDATE reservations SET state='FINISHED', version=version+1,
			updated_at_utc_ms=?, finished_at_utc_ms=?, terminal_reason='CANCELLED_BY_USER'
			WHERE id=? AND state='ACTIVE'`, nowMS, nowMS, reservationID)
		if updateErr != nil {
			return recording.StopResult{}, sanitize("cancel-reservation", updateErr)
		}
		if affected(result) != 1 {
			return recording.StopResult{}, ErrReservationUnavailable
		}
		if err := tx.Commit(); err != nil {
			return recording.StopResult{}, sanitize("commit-reservation-cancel", err)
		}
		return recording.StopResult{}, nil
	}
	if attemptState.String != string(recording.AttemptClaimed) && attemptState.String != string(recording.AttemptStarting) &&
		attemptState.String != string(recording.AttemptRecording) {
		return recording.StopResult{}, ErrReservationUnavailable
	}
	if !requested.Valid {
		result, updateErr := tx.ExecContext(ctx, `UPDATE recording_attempts SET stop_requested_at_utc_ms=?,
			state_version=state_version+1, updated_at_utc_ms=? WHERE id=? AND state IN ('CLAIMED','STARTING','RECORDING')
			AND stop_requested_at_utc_ms IS NULL`, nowMS, nowMS, attemptID)
		if updateErr != nil {
			return recording.StopResult{}, sanitize("request-recording-stop", updateErr)
		}
		if affected(result) != 1 {
			return recording.StopResult{}, ErrReservationUnavailable
		}
	}
	if err := tx.Commit(); err != nil {
		return recording.StopResult{}, sanitize("commit-recording-stop", err)
	}
	var id catalogmodel.ID
	if err := copyExact(id[:], reservationID); err != nil {
		return recording.StopResult{}, err
	}
	return recording.StopResult{ReservationID: id, Notify: true}, nil
}

// CancelReservationは内部の自動予約取消しから使う、録画開始前だけの互換操作である。
func (store *Store) CancelReservation(ctx context.Context, number int32, now time.Time) error {
	if store == nil || store.writer == nil || ctx == nil || number < 1 || now.IsZero() ||
		now.Location() != time.UTC || now.UnixMilli() < 0 {
		return errors.New("sqlite: invalid reservation cancellation")
	}
	nowMS := now.UnixMilli()
	result, err := store.writer.ExecContext(ctx, `UPDATE reservations SET state='FINISHED', version=version+1,
		updated_at_utc_ms=?, finished_at_utc_ms=?, terminal_reason='CANCELLED_BY_USER' WHERE id=(
			SELECT reservation_id FROM ctrlcmd_reservation_ids WHERE reserve_id=?
		) AND state='ACTIVE' AND NOT EXISTS (
			SELECT 1 FROM recording_attempts WHERE reservation_id=reservations.id
		)`, nowMS, nowMS, number)
	if err != nil {
		return sanitize("cancel-reservation", err)
	}
	if affected(result) != 1 {
		return ErrReservationUnavailable
	}
	return nil
}

// ReservationRecordingは予約に録画中の処理があるかを、DBを変更せずに返す。
func (store *Store) ReservationRecording(ctx context.Context, number int32) (bool, error) {
	if store == nil || store.reader == nil || ctx == nil || number < 1 {
		return false, errors.New("sqlite: invalid recording status query")
	}
	var active int
	err := store.reader.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM ctrlcmd_reservation_ids m
		JOIN reservations r ON r.id=m.reservation_id
		JOIN recording_attempts a ON a.reservation_id=r.id
		WHERE m.reserve_id=? AND r.state='ACTIVE' AND a.state IN ('STARTING','RECORDING','FINALIZING')
	)`, number).Scan(&active)
	if err != nil {
		return false, sanitize("read-recording-status", err)
	}
	return active == 1, nil
}

// ExpireOneDisabledReservationは終了予定を過ぎた無効予約を、一回に一件だけ終了する。
func (store *Store) ExpireOneDisabledReservation(ctx context.Context, now time.Time) (bool, error) {
	if store == nil || store.writer == nil || ctx == nil || now.IsZero() || now.Location() != time.UTC || now.UnixMilli() < 0 {
		return false, errors.New("sqlite: invalid disabled reservation expiration")
	}
	nowMS := now.UnixMilli()
	result, err := store.writer.ExecContext(ctx, `UPDATE reservations SET state='FINISHED', version=version+1,
		updated_at_utc_ms=?, finished_at_utc_ms=?, terminal_reason=?
		WHERE id=(SELECT id FROM reservations WHERE state='ACTIVE' AND enabled=0
		AND start_at_utc_ms + (duration_seconds + effective_end_margin_seconds) * 1000 <= ?
		AND NOT EXISTS (SELECT 1 FROM recording_attempts WHERE reservation_id=reservations.id)
		ORDER BY start_at_utc_ms + (duration_seconds + effective_end_margin_seconds) * 1000, id LIMIT 1)
		AND state='ACTIVE'`, nowMS, nowMS, recording.ReservationReasonDisabledExpired, nowMS)
	if err != nil {
		return false, sanitize("expire-disabled-reservation", err)
	}
	return affected(result) == 1, nil
}

// NextDisabledReservationDeadlineは未来に残る無効予約のうち、最も早い終了予定を返す。
func (store *Store) NextDisabledReservationDeadline(ctx context.Context, now time.Time) (*time.Time, error) {
	if store == nil || store.reader == nil || ctx == nil || now.IsZero() || now.Location() != time.UTC || now.UnixMilli() < 0 {
		return nil, errors.New("sqlite: invalid disabled reservation deadline query")
	}
	var deadlineMS int64
	err := store.reader.QueryRowContext(ctx, `SELECT start_at_utc_ms+(duration_seconds+effective_end_margin_seconds)*1000
		FROM reservations WHERE state='ACTIVE' AND enabled=0
		AND start_at_utc_ms+(duration_seconds+effective_end_margin_seconds)*1000>?
		AND NOT EXISTS (SELECT 1 FROM recording_attempts WHERE reservation_id=reservations.id)
		ORDER BY start_at_utc_ms+(duration_seconds+effective_end_margin_seconds)*1000, id LIMIT 1`, now.UnixMilli()).Scan(&deadlineMS)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, sanitize("read-disabled-reservation-deadline", err)
	}
	deadline := time.UnixMilli(deadlineMS).UTC()
	return &deadline, nil
}

// NextActiveReservationは未割当ての有効予約から、開始済みなら優先度、未来なら開始予定順で一件を返す。
func (store *Store) NextActiveReservation(ctx context.Context, now time.Time) (*recording.Reservation, error) {
	if store == nil || store.reader == nil || ctx == nil || now.IsZero() || now.Location() != time.UTC || now.UnixMilli() < 0 {
		return nil, errors.New("sqlite: invalid next reservation query")
	}
	row := store.reader.QueryRowContext(ctx, `SELECT r.id, m.reserve_id, r.version, r.state,
		r.program_instance_id, r.program_revision_id, r.backend_instance_id, r.provider_service_locator,
		r.tuning_target, r.network_id, r.transport_stream_id, r.service_id, r.event_id, r.title,
		r.station_name, r.start_at_utc_ms, r.duration_seconds, r.requested_priority, r.requested_follow,
		r.effective_follow, r.created_at_utc_ms, r.updated_at_utc_ms, r.enabled, r.use_default_margins,
		r.effective_start_margin_seconds, r.effective_end_margin_seconds, r.output_folder, r.output_template, r.component_mode
		FROM ctrlcmd_reservation_ids m JOIN reservations r ON r.id=m.reservation_id
		WHERE r.state='ACTIVE' AND r.enabled=1 AND NOT EXISTS (
			SELECT 1 FROM recording_attempts a WHERE a.reservation_id=r.id
		)
		ORDER BY CASE WHEN r.start_at_utc_ms-r.effective_start_margin_seconds*1000<=? THEN 0 ELSE 1 END,
		CASE WHEN r.start_at_utc_ms-r.effective_start_margin_seconds*1000<=? THEN r.requested_priority END DESC,
		r.start_at_utc_ms-r.effective_start_margin_seconds*1000, r.id LIMIT 1`, now.UnixMilli(), now.UnixMilli())
	item, err := scanReservation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// ClaimRecordingは予約を一度だけ録画処理へ割り当て、最初のファイル計画も同じトランザクションで保存する。
func (store *Store) ClaimRecording(ctx context.Context, request recording.ClaimRequest) (recording.Attempt, error) {
	if store == nil || store.writer == nil || ctx == nil || request.Validate() != nil {
		return recording.Attempt{}, errors.New("sqlite: invalid recording claim")
	}
	tx, err := store.writer.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return recording.Attempt{}, sanitize("begin-recording-claim", err)
	}
	defer tx.Rollback()

	var state recording.ReservationState
	var startMS, durationSeconds, enabled, startMarginSeconds, endMarginSeconds int64
	err = tx.QueryRowContext(ctx, `SELECT state, start_at_utc_ms, duration_seconds, enabled,
		effective_start_margin_seconds, effective_end_margin_seconds FROM reservations WHERE id=?`,
		request.ReservationID.Bytes()).Scan(&state, &startMS, &durationSeconds, &enabled, &startMarginSeconds, &endMarginSeconds)
	if errors.Is(err, sql.ErrNoRows) {
		return recording.Attempt{}, ErrReservationUnavailable
	}
	if err != nil {
		return recording.Attempt{}, sanitize("read-recording-reservation", err)
	}
	if state != recording.ReservationActive || enabled != 1 {
		return recording.Attempt{}, ErrReservationUnavailable
	}
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM recording_attempts WHERE reservation_id=?`,
		request.ReservationID.Bytes()).Scan(&existing); err != nil {
		return recording.Attempt{}, sanitize("find-recording-attempt", err)
	}
	if existing != 0 {
		return recording.Attempt{}, ErrAttemptExists
	}
	plannedStartMS := startMS - startMarginSeconds*1_000
	endMS := startMS + (durationSeconds+endMarginSeconds)*1_000
	if startMS < 0 || durationSeconds < 1 || plannedStartMS < 0 || endMS <= plannedStartMS || endMS-plannedStartMS > int64((24*time.Hour)/time.Millisecond) {
		return recording.Attempt{}, errors.New("sqlite: corrupt recording time")
	}
	nowMS := request.Now.UnixMilli()
	_, err = tx.ExecContext(ctx, `INSERT INTO recording_attempts(
		id, reservation_id, state, state_version, planned_start_utc_ms, planned_end_utc_ms,
		owner_instance_id, owner_generation, heartbeat_utc_ms, created_at_utc_ms, updated_at_utc_ms)
		VALUES (?, ?, 'CLAIMED', 1, ?, ?, ?, ?, ?, ?, ?)`, request.AttemptID.Bytes(),
		request.ReservationID.Bytes(), plannedStartMS, endMS, request.OwnerID.Bytes(), request.OwnerGeneration,
		nowMS, nowMS, nowMS)
	if err != nil {
		return recording.Attempt{}, sanitize("create-recording-attempt", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO recording_segments(
		id, attempt_id, ordinal, state, relative_partial_path, relative_final_path,
		availability, created_at_utc_ms, updated_at_utc_ms)
		VALUES (?, ?, 0, 'PLANNED', ?, ?, 'PLANNED', ?, ?)`, request.SegmentID.Bytes(),
		request.AttemptID.Bytes(), request.Plan.PartialPath, request.Plan.FinalPath, nowMS, nowMS)
	if err != nil {
		return recording.Attempt{}, sanitize("create-recording-segment", err)
	}
	if err := tx.Commit(); err != nil {
		return recording.Attempt{}, sanitize("commit-recording-claim", err)
	}
	return recording.Attempt{
		ID: request.AttemptID, ReservationID: request.ReservationID, State: recording.AttemptClaimed,
		PlannedStart: time.UnixMilli(plannedStartMS).UTC(), PlannedEnd: time.UnixMilli(endMS).UTC(), Plan: request.Plan,
	}, nil
}

// StartAttemptは割当て済みの処理を、ファイルとストリームの準備段階へ進める。
func (store *Store) StartAttempt(ctx context.Context, attemptID catalogmodel.ID, now time.Time) error {
	return store.transitionAttempt(ctx, attemptID, recording.AttemptClaimed, recording.AttemptStarting, now)
}

// AttemptStopRequestedは一回の録画処理に利用者停止が保存済みかを、副作用なしで返す。
func (store *Store) AttemptStopRequested(ctx context.Context, attemptID catalogmodel.ID) (bool, error) {
	if store == nil || store.reader == nil || ctx == nil || attemptID == (catalogmodel.ID{}) {
		return false, errors.New("sqlite: invalid recording stop query")
	}
	var requested int
	err := store.reader.QueryRowContext(ctx, `SELECT stop_requested_at_utc_ms IS NOT NULL FROM recording_attempts
		WHERE id=? AND state IN ('CLAIMED','STARTING','RECORDING')`, attemptID.Bytes()).Scan(&requested)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrAttemptState
	}
	if err != nil {
		return false, sanitize("read-recording-stop", err)
	}
	return requested == 1, nil
}

// RecordingStartedは録画処理とファイル区間を書込み中へ進め、現在の予定終了時刻を返す。
func (store *Store) RecordingStarted(ctx context.Context, attemptID catalogmodel.ID, now time.Time) (time.Time, error) {
	if err := validateRecordingUpdate(store, ctx, attemptID, now); err != nil {
		return time.Time{}, err
	}
	tx, err := store.writer.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return time.Time{}, sanitize("begin-recording-start", err)
	}
	defer tx.Rollback()
	nowMS := now.UnixMilli()
	result, err := tx.ExecContext(ctx, `UPDATE recording_attempts SET state='RECORDING', state_version=state_version+1,
		actual_start_utc_ms=?, heartbeat_utc_ms=?, updated_at_utc_ms=? WHERE id=? AND state='STARTING'`,
		nowMS, nowMS, nowMS, attemptID.Bytes())
	if err != nil {
		return time.Time{}, sanitize("mark-recording-started", err)
	}
	if affected(result) != 1 {
		return time.Time{}, ErrAttemptState
	}
	result, err = tx.ExecContext(ctx, `UPDATE recording_segments SET state='WRITING', updated_at_utc_ms=?
		WHERE attempt_id=? AND ordinal=0 AND state='PLANNED'`, nowMS, attemptID.Bytes())
	if err != nil {
		return time.Time{}, sanitize("mark-recording-segment-started", err)
	}
	if affected(result) != 1 {
		return time.Time{}, ErrAttemptState
	}
	var plannedEndMS int64
	if err := tx.QueryRowContext(ctx, `SELECT planned_end_utc_ms FROM recording_attempts
		WHERE id=? AND state='RECORDING'`, attemptID.Bytes()).Scan(&plannedEndMS); err != nil {
		return time.Time{}, sanitize("read-recording-start-end", err)
	}
	if plannedEndMS < 0 {
		return time.Time{}, errors.New("sqlite: corrupt recording planned end")
	}
	if err := tx.Commit(); err != nil {
		return time.Time{}, sanitize("commit-recording-start", err)
	}
	return time.UnixMilli(plannedEndMS).UTC(), nil
}

// UpdateRecordingProgressは5秒ごとのバイト数と生存時刻を保存し、現在の予定終了時刻を返す。
func (store *Store) UpdateRecordingProgress(ctx context.Context, attemptID catalogmodel.ID, byteCount int64, now time.Time) (time.Time, error) {
	if byteCount < 0 || validateRecordingUpdate(store, ctx, attemptID, now) != nil {
		return time.Time{}, errors.New("sqlite: invalid recording progress")
	}
	tx, err := store.writer.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return time.Time{}, sanitize("begin-recording-progress", err)
	}
	defer tx.Rollback()
	nowMS := now.UnixMilli()
	result, err := tx.ExecContext(ctx, `UPDATE recording_attempts SET byte_count=?, heartbeat_utc_ms=?,
		updated_at_utc_ms=? WHERE id=? AND state='RECORDING' AND byte_count<=?`,
		byteCount, nowMS, nowMS, attemptID.Bytes(), byteCount)
	if err != nil {
		return time.Time{}, sanitize("update-recording-attempt-progress", err)
	}
	if affected(result) != 1 {
		return time.Time{}, ErrAttemptState
	}
	result, err = tx.ExecContext(ctx, `UPDATE recording_segments SET byte_count=?, updated_at_utc_ms=?
		WHERE attempt_id=? AND ordinal=0 AND state='WRITING' AND byte_count<=?`,
		byteCount, nowMS, attemptID.Bytes(), byteCount)
	if err != nil {
		return time.Time{}, sanitize("update-recording-segment-progress", err)
	}
	if affected(result) != 1 {
		return time.Time{}, ErrAttemptState
	}
	var plannedEndMS int64
	if err := tx.QueryRowContext(ctx, `SELECT planned_end_utc_ms FROM recording_attempts
		WHERE id=? AND state='RECORDING'`, attemptID.Bytes()).Scan(&plannedEndMS); err != nil {
		return time.Time{}, sanitize("read-recording-planned-end", err)
	}
	if plannedEndMS < 0 {
		return time.Time{}, errors.New("sqlite: corrupt recording planned end")
	}
	if err := tx.Commit(); err != nil {
		return time.Time{}, sanitize("commit-recording-progress", err)
	}
	return time.UnixMilli(plannedEndMS).UTC(), nil
}

// BeginFinalizationはファイル同期済みのバイト数とトークンを保存し、完成名を公開できる状態へ進める。
func (store *Store) BeginFinalization(ctx context.Context, request recording.FinalizeRequest) error {
	if store == nil || store.writer == nil || ctx == nil || request.Validate() != nil {
		return errors.New("sqlite: invalid recording finalization")
	}
	tx, err := store.writer.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return sanitize("begin-recording-finalization", err)
	}
	defer tx.Rollback()
	nowMS := request.Now.UnixMilli()
	result, err := tx.ExecContext(ctx, `UPDATE recording_attempts SET state='FINALIZING', state_version=state_version+1,
		byte_count=?, heartbeat_utc_ms=?, finalization_token=?, planned_final_state=?, planned_terminal_reason=?, updated_at_utc_ms=?
		WHERE id=? AND state='RECORDING' AND byte_count<=?
		AND ((?='PARTIAL' AND stop_requested_at_utc_ms IS NOT NULL) OR (?='SUCCEEDED' AND stop_requested_at_utc_ms IS NULL))`,
		request.ByteCount, nowMS, request.Token.Bytes(), request.State, request.Reason, nowMS, request.AttemptID.Bytes(),
		request.ByteCount, request.State, request.State)
	if err != nil {
		return sanitize("mark-recording-finalizing", err)
	}
	if affected(result) != 1 {
		return ErrAttemptState
	}
	result, err = tx.ExecContext(ctx, `UPDATE recording_segments SET state='PARTIAL', byte_count=?, file_synced=1,
		availability='PARTIAL', updated_at_utc_ms=? WHERE attempt_id=? AND ordinal=0 AND state='WRITING'
		AND byte_count<=?`, request.ByteCount, nowMS, request.AttemptID.Bytes(), request.ByteCount)
	if err != nil {
		return sanitize("mark-recording-segment-synced", err)
	}
	if affected(result) != 1 {
		return ErrAttemptState
	}
	if err := tx.Commit(); err != nil {
		return sanitize("commit-recording-finalization", err)
	}
	return nil
}

// MarkFinalPublishedはハードリンクによる完成名の公開を記録する。
func (store *Store) MarkFinalPublished(ctx context.Context, attemptID catalogmodel.ID, now time.Time) error {
	return store.markFinalizationFlag(ctx, attemptID, "final_published", now)
}

// MarkDirectorySyncedは完成名を含むディレクトリの同期を記録する。
func (store *Store) MarkDirectorySynced(ctx context.Context, attemptID catalogmodel.ID, now time.Time) error {
	return store.markFinalizationFlag(ctx, attemptID, "directory_synced", now)
}

// FinishAttemptは録画処理と予約を同じトランザクションで終了状態へ進める。
func (store *Store) FinishAttempt(ctx context.Context, request recording.FinishRequest) error {
	if store == nil || store.writer == nil || ctx == nil || request.Validate() != nil {
		return errors.New("sqlite: invalid recording finish")
	}
	tx, err := store.writer.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return sanitize("begin-recording-finish", err)
	}
	defer tx.Rollback()
	var reservationID []byte
	var current recording.AttemptState
	var actualStart sql.NullInt64
	var plannedState, plannedReason sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT reservation_id, state, actual_start_utc_ms,
		planned_final_state, planned_terminal_reason FROM recording_attempts WHERE id=?`,
		request.AttemptID.Bytes()).Scan(&reservationID, &current, &actualStart, &plannedState, &plannedReason)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAttemptState
	}
	if err != nil {
		return sanitize("read-recording-finish", err)
	}
	if current == recording.AttemptSucceeded || current == recording.AttemptPartial || current == recording.AttemptFailed ||
		current == recording.AttemptCancelled || current == recording.AttemptMissed {
		return ErrAttemptState
	}
	if (request.State == recording.AttemptSucceeded ||
		(request.State == recording.AttemptPartial && request.Reason == recording.ReasonUserRequestedStop)) &&
		current != recording.AttemptFinalizing {
		return ErrAttemptState
	}
	if current == recording.AttemptFinalizing && (request.State == recording.AttemptSucceeded ||
		(request.State == recording.AttemptPartial && request.Reason == recording.ReasonUserRequestedStop)) &&
		(!plannedState.Valid || !plannedReason.Valid ||
			plannedState.String != string(request.State) || plannedReason.String != string(request.Reason)) {
		return ErrAttemptState
	}
	nowMS := request.Now.UnixMilli()
	var actualEnd any
	if actualStart.Valid {
		if nowMS < actualStart.Int64 {
			return errors.New("sqlite: recording finish precedes start")
		}
		actualEnd = nowMS
	}
	result, err := tx.ExecContext(ctx, `UPDATE recording_attempts SET state=?, state_version=state_version+1,
		actual_end_utc_ms=?, byte_count=?, heartbeat_utc_ms=?, terminal_reason=?, recovered=?, updated_at_utc_ms=?
		WHERE id=? AND state=?`, request.State, actualEnd, request.ByteCount, nowMS, request.Reason, request.Recovered,
		nowMS, request.AttemptID.Bytes(), current)
	if err != nil {
		return sanitize("finish-recording-attempt", err)
	}
	if affected(result) != 1 {
		return ErrAttemptState
	}
	if request.State == recording.AttemptSucceeded ||
		(request.State == recording.AttemptPartial && request.Reason == recording.ReasonUserRequestedStop) {
		result, err = tx.ExecContext(ctx, `UPDATE recording_segments SET state='FINALIZED', byte_count=?,
			availability='FINAL', updated_at_utc_ms=? WHERE attempt_id=? AND ordinal=0 AND state='PARTIAL'
			AND file_synced=1 AND final_published=1 AND directory_synced=1`, request.ByteCount, nowMS,
			request.AttemptID.Bytes())
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE recording_segments SET state=CASE WHEN ?>0 THEN 'PARTIAL' ELSE state END,
			byte_count=?, availability=?, updated_at_utc_ms=? WHERE attempt_id=? AND ordinal=0`,
			request.ByteCount, request.ByteCount, request.Availability, nowMS, request.AttemptID.Bytes())
	}
	if err != nil {
		return sanitize("finish-recording-segment", err)
	}
	if affected(result) != 1 {
		return ErrAttemptState
	}
	result, err = tx.ExecContext(ctx, `UPDATE reservations SET state='FINISHED', version=version+1,
		updated_at_utc_ms=?, finished_at_utc_ms=?, terminal_reason='ATTEMPT_FINISHED'
		WHERE id=? AND state='ACTIVE'`, nowMS, nowMS, reservationID)
	if err != nil {
		return sanitize("finish-recording-reservation", err)
	}
	if affected(result) != 1 {
		return ErrAttemptState
	}
	if err := tx.Commit(); err != nil {
		return sanitize("commit-recording-finish", err)
	}
	return nil
}

func scanReservation(scanner rowScanner) (recording.Reservation, error) {
	var item recording.Reservation
	var id, instanceID, revisionID, backendID []byte
	var number, networkID, transportID, serviceID, eventID, startMS, duration, priority int64
	var requestedFollow, effectiveFollow, enabled, useDefaultMargins, startMarginSeconds, endMarginSeconds, componentMode int64
	var createdMS, updatedMS int64
	if err := scanner.Scan(&id, &number, &item.Version, &item.State, &instanceID, &revisionID, &backendID,
		&item.Program.ProviderServiceLocator, &item.Program.TuningTarget, &networkID, &transportID,
		&serviceID, &eventID, &item.Program.Title, &item.Program.StationName, &startMS,
		&duration, &priority, &requestedFollow, &effectiveFollow, &createdMS, &updatedMS,
		&enabled, &useDefaultMargins, &startMarginSeconds, &endMarginSeconds, &item.Output.Folder, &item.Output.Template,
		&componentMode); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return recording.Reservation{}, err
		}
		return recording.Reservation{}, sanitize("scan-reservation", err)
	}
	if number < 1 || number > 1<<31-1 || networkID < 0 || networkID > 65_535 ||
		transportID < 0 || transportID > 65_535 || serviceID < 0 || serviceID > 65_535 ||
		eventID < 0 || eventID > 65_535 || startMS < 0 || duration < 1 || duration > 86_400 ||
		priority < 1 || priority > 5 || requestedFollow < 0 || requestedFollow > 1 || effectiveFollow != 0 ||
		enabled < 0 || enabled > 1 || useDefaultMargins < 0 || useDefaultMargins > 1 ||
		startMarginSeconds < -3600 || startMarginSeconds > 3600 || endMarginSeconds < -3600 || endMarginSeconds > 3600 ||
		startMS-startMarginSeconds*1_000 < 0 ||
		(useDefaultMargins == 1 && (startMarginSeconds != 5 || endMarginSeconds != 2)) ||
		duration+startMarginSeconds+endMarginSeconds < 1 || duration+startMarginSeconds+endMarginSeconds > 86_400 ||
		item.Output.Validate() != nil || componentMode < 0 || componentMode > int64(recording.ComponentBoth) {
		return recording.Reservation{}, errors.New("sqlite: corrupt reservation value")
	}
	if err := copyExact(item.ID[:], id); err != nil {
		return recording.Reservation{}, err
	}
	if err := copyExact(item.Program.ProgramInstanceID[:], instanceID); err != nil {
		return recording.Reservation{}, err
	}
	if err := copyExact(item.Program.ProgramRevisionID[:], revisionID); err != nil {
		return recording.Reservation{}, err
	}
	if err := copyExact(item.Program.BackendID[:], backendID); err != nil {
		return recording.Reservation{}, err
	}
	item.Number = int32(number)
	item.Program.NetworkID = uint16(networkID)
	item.Program.TransportStreamID = uint16(transportID)
	item.Program.ServiceID = uint16(serviceID)
	item.Program.EventID = uint16(eventID)
	item.Program.Duration = time.Duration(duration) * time.Second
	item.Program.Start = time.UnixMilli(startMS).UTC()
	item.Priority = uint8(priority)
	item.RequestedFollow = requestedFollow == 1
	item.EffectiveFollow = item.RequestedFollow
	item.Disabled = enabled == 0
	item.Components = recording.ComponentMode(componentMode)
	if useDefaultMargins == 0 {
		item.Margins = &recording.RecordingMargins{Start: time.Duration(startMarginSeconds) * time.Second,
			End: time.Duration(endMarginSeconds) * time.Second}
	}
	item.CreatedAt = time.UnixMilli(createdMS).UTC()
	item.UpdatedAt = time.UnixMilli(updatedMS).UTC()
	return item, nil
}

func validateRecordingUpdate(store *Store, ctx context.Context, id catalogmodel.ID, now time.Time) error {
	if store == nil || store.writer == nil || ctx == nil || id == (catalogmodel.ID{}) || now.IsZero() ||
		now.Location() != time.UTC || now.UnixMilli() < 0 {
		return errors.New("sqlite: invalid recording update")
	}
	return nil
}

func (store *Store) transitionAttempt(ctx context.Context, id catalogmodel.ID, from, to recording.AttemptState, now time.Time) error {
	if err := validateRecordingUpdate(store, ctx, id, now); err != nil {
		return err
	}
	result, err := store.writer.ExecContext(ctx, `UPDATE recording_attempts SET state=?, state_version=state_version+1,
		heartbeat_utc_ms=?, updated_at_utc_ms=? WHERE id=? AND state=?`, to, now.UnixMilli(), now.UnixMilli(), id.Bytes(), from)
	if err != nil {
		return sanitize("transition-recording-attempt", err)
	}
	if affected(result) != 1 {
		return ErrAttemptState
	}
	return nil
}

func (store *Store) markFinalizationFlag(ctx context.Context, id catalogmodel.ID, column string, now time.Time) error {
	if validateRecordingUpdate(store, ctx, id, now) != nil || (column != "final_published" && column != "directory_synced") {
		return errors.New("sqlite: invalid recording finalization flag")
	}
	tx, err := store.writer.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return sanitize("begin-recording-finalization-flag", err)
	}
	defer tx.Rollback()
	nowMS := now.UnixMilli()
	query := `UPDATE recording_segments SET ` + column + `=1, updated_at_utc_ms=? WHERE attempt_id=? AND ordinal=0
		AND state='PARTIAL' AND file_synced=1`
	if column == "directory_synced" {
		query += ` AND final_published=1`
	}
	result, err := tx.ExecContext(ctx, query, nowMS, id.Bytes())
	if err != nil {
		return sanitize("mark-recording-finalization-flag", err)
	}
	if affected(result) != 1 {
		return ErrAttemptState
	}
	result, err = tx.ExecContext(ctx, `UPDATE recording_attempts SET state_version=state_version+1,
		heartbeat_utc_ms=?, updated_at_utc_ms=? WHERE id=? AND state='FINALIZING'`, nowMS, nowMS, id.Bytes())
	if err != nil {
		return sanitize("touch-recording-finalization", err)
	}
	if affected(result) != 1 {
		return ErrAttemptState
	}
	if err := tx.Commit(); err != nil {
		return sanitize("commit-recording-finalization-flag", err)
	}
	return nil
}

func affected(result sql.Result) int64 {
	if result == nil {
		return -1
	}
	count, err := result.RowsAffected()
	if err != nil {
		return -1
	}
	return count
}
