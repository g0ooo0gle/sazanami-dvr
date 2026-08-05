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
	_, err = tx.ExecContext(ctx, `INSERT INTO reservations(
		id, version, state, program_instance_id, program_revision_id, backend_instance_id,
		provider_service_locator, tuning_target, network_id, transport_stream_id, service_id, event_id,
		title, station_name, start_at_utc_ms, duration_seconds, requested_priority, requested_follow,
		effective_follow, created_at_utc_ms, updated_at_utc_ms)
		VALUES (?, 1, 'ACTIVE', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		reservation.ID.Bytes(), p.ProgramInstanceID.Bytes(), p.ProgramRevisionID.Bytes(), p.BackendID.Bytes(),
		p.ProviderServiceLocator, p.TuningTarget, p.NetworkID, p.TransportStreamID, p.ServiceID, p.EventID,
		p.Title, p.StationName, p.Start.UnixMilli(), int64(p.Duration/time.Second), reservation.Priority,
		reservation.RequestedFollow, createdMS, createdMS)
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
	if err := tx.Commit(); err != nil {
		return recording.Reservation{}, sanitize("commit-reservation", err)
	}
	reservation.Number = int32(number)
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
		r.effective_follow, r.created_at_utc_ms, r.updated_at_utc_ms
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

// UpdateReservationは変更不可の番組情報を照合し、録画開始前の設定だけを更新する。
func (store *Store) UpdateReservation(ctx context.Context, change recording.ReservationChange, now time.Time) error {
	if store == nil || store.writer == nil || ctx == nil || change.Validate() != nil || now.IsZero() ||
		now.Location() != time.UTC || now.UnixMilli() < 0 {
		return errors.New("sqlite: invalid reservation update")
	}
	request := change.Request
	result, err := store.writer.ExecContext(ctx, `UPDATE reservations SET requested_priority=?, requested_follow=?,
		version=version+1, updated_at_utc_ms=? WHERE id=(
			SELECT reservation_id FROM ctrlcmd_reservation_ids WHERE reserve_id=?
		) AND state='ACTIVE' AND network_id=? AND transport_stream_id=? AND service_id=? AND event_id=?
		AND start_at_utc_ms=? AND duration_seconds=? AND NOT EXISTS (
			SELECT 1 FROM recording_attempts WHERE reservation_id=reservations.id
		)`, request.Priority, request.RequestedFollow, now.UnixMilli(), change.Number, request.NetworkID,
		request.TransportStreamID, request.ServiceID, request.EventID, request.Start.UnixMilli(),
		int64(request.Duration/time.Second))
	if err != nil {
		return sanitize("update-reservation", err)
	}
	if affected(result) != 1 {
		return ErrReservationUnavailable
	}
	return nil
}

// CancelReservationは録画開始前の予約だけを終了状態へ進め、取消理由を残す。
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

// NextActiveReservationはまだ録画処理へ割り当てていない予約を開始時刻順に一件だけ返す。
func (store *Store) NextActiveReservation(ctx context.Context) (*recording.Reservation, error) {
	if store == nil || store.reader == nil || ctx == nil {
		return nil, errors.New("sqlite: invalid next reservation query")
	}
	row := store.reader.QueryRowContext(ctx, `SELECT r.id, m.reserve_id, r.version, r.state,
		r.program_instance_id, r.program_revision_id, r.backend_instance_id, r.provider_service_locator,
		r.tuning_target, r.network_id, r.transport_stream_id, r.service_id, r.event_id, r.title,
		r.station_name, r.start_at_utc_ms, r.duration_seconds, r.requested_priority, r.requested_follow,
		r.effective_follow, r.created_at_utc_ms, r.updated_at_utc_ms
		FROM ctrlcmd_reservation_ids m JOIN reservations r ON r.id=m.reservation_id
		WHERE r.state='ACTIVE' AND NOT EXISTS (
			SELECT 1 FROM recording_attempts a WHERE a.reservation_id=r.id
		)
		ORDER BY r.start_at_utc_ms, r.id LIMIT 1`)
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
	var startMS, durationSeconds int64
	err = tx.QueryRowContext(ctx, `SELECT state, start_at_utc_ms, duration_seconds FROM reservations WHERE id=?`,
		request.ReservationID.Bytes()).Scan(&state, &startMS, &durationSeconds)
	if errors.Is(err, sql.ErrNoRows) {
		return recording.Attempt{}, ErrReservationUnavailable
	}
	if err != nil {
		return recording.Attempt{}, sanitize("read-recording-reservation", err)
	}
	if state != recording.ReservationActive {
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
	endMS := startMS + durationSeconds*1_000
	if startMS < 0 || durationSeconds < 1 || endMS <= startMS {
		return recording.Attempt{}, errors.New("sqlite: corrupt recording time")
	}
	nowMS := request.Now.UnixMilli()
	_, err = tx.ExecContext(ctx, `INSERT INTO recording_attempts(
		id, reservation_id, state, state_version, planned_start_utc_ms, planned_end_utc_ms,
		owner_instance_id, owner_generation, heartbeat_utc_ms, created_at_utc_ms, updated_at_utc_ms)
		VALUES (?, ?, 'CLAIMED', 1, ?, ?, ?, ?, ?, ?, ?)`, request.AttemptID.Bytes(),
		request.ReservationID.Bytes(), startMS, endMS, request.OwnerID.Bytes(), request.OwnerGeneration,
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
		PlannedStart: time.UnixMilli(startMS).UTC(), PlannedEnd: time.UnixMilli(endMS).UTC(), Plan: request.Plan,
	}, nil
}

// StartAttemptは割当て済みの処理を、ファイルとストリームの準備段階へ進める。
func (store *Store) StartAttempt(ctx context.Context, attemptID catalogmodel.ID, now time.Time) error {
	return store.transitionAttempt(ctx, attemptID, recording.AttemptClaimed, recording.AttemptStarting, now)
}

// RecordingStartedはストリーム接続後に、録画処理とファイル区間を一緒に書込み中へ進める。
func (store *Store) RecordingStarted(ctx context.Context, attemptID catalogmodel.ID, now time.Time) error {
	if err := validateRecordingUpdate(store, ctx, attemptID, now); err != nil {
		return err
	}
	tx, err := store.writer.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return sanitize("begin-recording-start", err)
	}
	defer tx.Rollback()
	nowMS := now.UnixMilli()
	result, err := tx.ExecContext(ctx, `UPDATE recording_attempts SET state='RECORDING', state_version=state_version+1,
		actual_start_utc_ms=?, heartbeat_utc_ms=?, updated_at_utc_ms=? WHERE id=? AND state='STARTING'`,
		nowMS, nowMS, nowMS, attemptID.Bytes())
	if err != nil {
		return sanitize("mark-recording-started", err)
	}
	if affected(result) != 1 {
		return ErrAttemptState
	}
	result, err = tx.ExecContext(ctx, `UPDATE recording_segments SET state='WRITING', updated_at_utc_ms=?
		WHERE attempt_id=? AND ordinal=0 AND state='PLANNED'`, nowMS, attemptID.Bytes())
	if err != nil {
		return sanitize("mark-recording-segment-started", err)
	}
	if affected(result) != 1 {
		return ErrAttemptState
	}
	if err := tx.Commit(); err != nil {
		return sanitize("commit-recording-start", err)
	}
	return nil
}

// UpdateRecordingProgressは5秒ごとのバイト数と生存時刻を、短いトランザクションで保存する。
func (store *Store) UpdateRecordingProgress(ctx context.Context, attemptID catalogmodel.ID, byteCount int64, now time.Time) error {
	if byteCount < 0 || validateRecordingUpdate(store, ctx, attemptID, now) != nil {
		return errors.New("sqlite: invalid recording progress")
	}
	tx, err := store.writer.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return sanitize("begin-recording-progress", err)
	}
	defer tx.Rollback()
	nowMS := now.UnixMilli()
	result, err := tx.ExecContext(ctx, `UPDATE recording_attempts SET byte_count=?, heartbeat_utc_ms=?,
		updated_at_utc_ms=? WHERE id=? AND state='RECORDING' AND byte_count<=?`,
		byteCount, nowMS, nowMS, attemptID.Bytes(), byteCount)
	if err != nil {
		return sanitize("update-recording-attempt-progress", err)
	}
	if affected(result) != 1 {
		return ErrAttemptState
	}
	result, err = tx.ExecContext(ctx, `UPDATE recording_segments SET byte_count=?, updated_at_utc_ms=?
		WHERE attempt_id=? AND ordinal=0 AND state='WRITING' AND byte_count<=?`,
		byteCount, nowMS, attemptID.Bytes(), byteCount)
	if err != nil {
		return sanitize("update-recording-segment-progress", err)
	}
	if affected(result) != 1 {
		return ErrAttemptState
	}
	if err := tx.Commit(); err != nil {
		return sanitize("commit-recording-progress", err)
	}
	return nil
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
		byte_count=?, heartbeat_utc_ms=?, finalization_token=?, updated_at_utc_ms=?
		WHERE id=? AND state='RECORDING' AND byte_count<=?`, request.ByteCount, nowMS, request.Token.Bytes(),
		nowMS, request.AttemptID.Bytes(), request.ByteCount)
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
	err = tx.QueryRowContext(ctx, `SELECT reservation_id, state, actual_start_utc_ms FROM recording_attempts WHERE id=?`,
		request.AttemptID.Bytes()).Scan(&reservationID, &current, &actualStart)
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
	if request.State == recording.AttemptSucceeded && current != recording.AttemptFinalizing {
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
	if request.State == recording.AttemptSucceeded {
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
	var requestedFollow, effectiveFollow int64
	var createdMS, updatedMS int64
	if err := scanner.Scan(&id, &number, &item.Version, &item.State, &instanceID, &revisionID, &backendID,
		&item.Program.ProviderServiceLocator, &item.Program.TuningTarget, &networkID, &transportID,
		&serviceID, &eventID, &item.Program.Title, &item.Program.StationName, &startMS,
		&duration, &priority, &requestedFollow, &effectiveFollow, &createdMS, &updatedMS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return recording.Reservation{}, err
		}
		return recording.Reservation{}, sanitize("scan-reservation", err)
	}
	if number < 1 || number > 1<<31-1 || networkID < 0 || networkID > 65_535 ||
		transportID < 0 || transportID > 65_535 || serviceID < 0 || serviceID > 65_535 ||
		eventID < 0 || eventID > 65_535 || startMS < 0 || duration < 1 || duration > 86_400 ||
		priority < 1 || priority > 5 || requestedFollow < 0 || requestedFollow > 1 || effectiveFollow != 0 {
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
	item.EffectiveFollow = false
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
