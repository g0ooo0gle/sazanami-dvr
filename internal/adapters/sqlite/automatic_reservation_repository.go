package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/autoreservation"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

const maxAutomaticRuleJSON = 256 * 1024

var (
	// ErrAutomaticRuleLimitは保存済み条件が固定上限へ達したことを表す。
	ErrAutomaticRuleLimit = errors.New("sqlite: automatic reservation rule limit reached")
	// ErrAutomaticRuleUnavailableは変更または削除する条件が存在しないことを表す。
	ErrAutomaticRuleUnavailable = errors.New("sqlite: automatic reservation rule unavailable")
	// ErrAutomaticReservationDuplicateは同じ番組に予約履歴があることを表す。
	ErrAutomaticReservationDuplicate = errors.New("sqlite: automatic reservation already exists")
)

// CreateAutomaticRuleは条件と単調増加するCtrlCmd番号を一つのtransactionで保存する。
func (store *Store) CreateAutomaticRule(ctx context.Context, rule autoreservation.Rule) (autoreservation.Rule, error) {
	if store == nil || store.writer == nil || ctx == nil || rule.ValidateNew() != nil {
		return autoreservation.Rule{}, errors.New("sqlite: invalid automatic reservation rule")
	}
	searchJSON, recordingJSON, err := encodeAutomaticRule(rule)
	if err != nil {
		return autoreservation.Rule{}, err
	}
	tx, err := store.writer.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return autoreservation.Rule{}, sanitize("begin-automatic-rule", err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM automatic_reservation_rules`).Scan(&count); err != nil {
		return autoreservation.Rule{}, sanitize("count-automatic-rules", err)
	}
	if count >= autoreservation.MaxRules {
		return autoreservation.Rule{}, ErrAutomaticRuleLimit
	}
	var number int64
	err = tx.QueryRowContext(ctx, `INSERT INTO automatic_reservation_rules(
		id, version, search_json, recording_json, created_at_utc_ms, updated_at_utc_ms)
		VALUES (?, 1, ?, ?, ?, ?) RETURNING number`, rule.ID.Bytes(), searchJSON, recordingJSON,
		rule.CreatedAtUTCMS, rule.UpdatedAtUTCMS).Scan(&number)
	if err != nil {
		return autoreservation.Rule{}, sanitize("create-automatic-rule", err)
	}
	if number < 1 || number > 1<<31-1 {
		return autoreservation.Rule{}, ErrAutomaticRuleLimit
	}
	if err := tx.Commit(); err != nil {
		return autoreservation.Rule{}, sanitize("commit-automatic-rule", err)
	}
	rule.Number = int32(number)
	return rule, nil
}

// AutomaticRulesは条件をCtrlCmd番号順に上限付きで返す。
func (store *Store) AutomaticRules(ctx context.Context, limit int, after int32) ([]autoreservation.Rule, error) {
	if store == nil || store.reader == nil || ctx == nil || limit < 1 || limit > autoreservation.MaxPage || after < 0 {
		return nil, errors.New("sqlite: invalid automatic rule query")
	}
	rows, err := store.reader.QueryContext(ctx, `SELECT r.id, r.number, r.version, r.search_json, r.recording_json,
		(SELECT count(*) FROM automatic_reservation_matches m WHERE m.rule_id=r.id),
		r.created_at_utc_ms, r.updated_at_utc_ms
		FROM automatic_reservation_rules r WHERE r.number>? ORDER BY r.number LIMIT ?`, after, limit)
	if err != nil {
		return nil, sanitize("query-automatic-rules", err)
	}
	defer rows.Close()
	result := make([]autoreservation.Rule, 0, limit)
	for rows.Next() {
		var rule autoreservation.Rule
		var id []byte
		var searchJSON, recordingJSON string
		if err := rows.Scan(&id, &rule.Number, &rule.Version, &searchJSON, &recordingJSON,
			&rule.ReservationCount, &rule.CreatedAtUTCMS, &rule.UpdatedAtUTCMS); err != nil {
			return nil, sanitize("scan-automatic-rule", err)
		}
		if err := copyExact(rule.ID[:], id); err != nil {
			return nil, err
		}
		if err := decodeAutomaticJSON(searchJSON, &rule.Search); err != nil {
			return nil, err
		}
		if err := decodeAutomaticJSON(recordingJSON, &rule.Recording); err != nil {
			return nil, err
		}
		if err := rule.ValidateStored(); err != nil {
			return nil, errors.New("sqlite: invalid stored automatic rule")
		}
		result = append(result, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, sanitize("iterate-automatic-rules", err)
	}
	return result, nil
}

// UpdateAutomaticRuleは存在する番号の条件だけを一度に置き換える。
func (store *Store) UpdateAutomaticRule(ctx context.Context, number int32, search autoreservation.SearchCondition,
	settings autoreservation.RecordingSettings, updatedAtUTCMS int64,
) error {
	if store == nil || store.writer == nil || ctx == nil || updatedAtUTCMS < 0 ||
		autoreservation.ValidateChange(number, search, settings) != nil {
		return errors.New("sqlite: invalid automatic rule change")
	}
	rule := autoreservation.Rule{Search: search, Recording: settings}
	searchJSON, recordingJSON, err := encodeAutomaticRule(rule)
	if err != nil {
		return err
	}
	result, err := store.writer.ExecContext(ctx, `UPDATE automatic_reservation_rules
		SET version=version+1, search_json=?, recording_json=?, updated_at_utc_ms=?
		WHERE number=? AND updated_at_utc_ms<=?`, searchJSON, recordingJSON, updatedAtUTCMS, number, updatedAtUTCMS)
	if err != nil {
		return sanitize("update-automatic-rule", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrAutomaticRuleUnavailable
	}
	return nil
}

// DeleteAutomaticRuleは条件と対応表だけを削除し、作成済み予約を維持する。
func (store *Store) DeleteAutomaticRule(ctx context.Context, number int32) error {
	if store == nil || store.writer == nil || ctx == nil || number < 1 {
		return errors.New("sqlite: invalid automatic rule delete")
	}
	result, err := store.writer.ExecContext(ctx, `DELETE FROM automatic_reservation_rules WHERE number=?`, number)
	if err != nil {
		return sanitize("delete-automatic-rule", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrAutomaticRuleUnavailable
	}
	return nil
}

// CreateAutomaticReservationは規則、番組、予約の対応を通常予約と同じtransactionで保存する。
func (store *Store) CreateAutomaticReservation(ctx context.Context, ruleNumber int32,
	reservation recording.Reservation,
) (recording.Reservation, error) {
	if store == nil || store.writer == nil || ctx == nil || ruleNumber < 1 || reservation.ValidateNew() != nil {
		return recording.Reservation{}, errors.New("sqlite: invalid automatic reservation")
	}
	store.reservationPower.Lock()
	defer store.reservationPower.Unlock()
	tx, err := store.writer.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return recording.Reservation{}, sanitize("begin-automatic-reservation", err)
	}
	defer tx.Rollback()
	var ruleID []byte
	if err := tx.QueryRowContext(ctx, `SELECT id FROM automatic_reservation_rules WHERE number=?`, ruleNumber).Scan(&ruleID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return recording.Reservation{}, ErrAutomaticRuleUnavailable
		}
		return recording.Reservation{}, sanitize("find-automatic-rule", err)
	}
	var previous int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM reservations WHERE program_instance_id=?`,
		reservation.Program.ProgramInstanceID.Bytes()).Scan(&previous); err != nil {
		return recording.Reservation{}, sanitize("find-automatic-duplicate", err)
	}
	if previous != 0 {
		return recording.Reservation{}, ErrAutomaticReservationDuplicate
	}
	created, err := createReservationTx(ctx, tx, reservation)
	if err != nil {
		return recording.Reservation{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO automatic_reservation_matches(
		rule_id, program_instance_id, reservation_id, created_at_utc_ms) VALUES (?, ?, ?, ?)`,
		ruleID, reservation.Program.ProgramInstanceID.Bytes(), created.ID.Bytes(), reservation.CreatedAt.UnixMilli()); err != nil {
		return recording.Reservation{}, sanitize("create-automatic-match", err)
	}
	if err := tx.Commit(); err != nil {
		return recording.Reservation{}, sanitize("commit-automatic-reservation", err)
	}
	return created, nil
}

// DisableAutomaticReservationは同じ番組へ自動予約が作られている場合だけ、録画開始前に無効へ変える。
// 手動予約、終了予約、録画処理を開始した予約は変更しない。
func (store *Store) DisableAutomaticReservation(ctx context.Context, programID catalogmodel.ID,
	now time.Time,
) (bool, error) {
	if store == nil || store.writer == nil || ctx == nil || programID == (catalogmodel.ID{}) ||
		now.IsZero() || now.Location() != time.UTC || now.UnixMilli() < 0 {
		return false, errors.New("sqlite: invalid automatic reservation disable")
	}
	store.reservationPower.Lock()
	defer store.reservationPower.Unlock()
	result, err := store.writer.ExecContext(ctx, `UPDATE reservations SET enabled=0,
		version=version+1, updated_at_utc_ms=? WHERE id=(
			SELECT reservation_id FROM automatic_reservation_matches WHERE program_instance_id=?
		) AND state='ACTIVE' AND enabled=1 AND NOT EXISTS (
			SELECT 1 FROM recording_attempts WHERE recording_attempts.reservation_id=reservations.id
		)`, now.UnixMilli(), programID.Bytes())
	if err != nil {
		return false, sanitize("disable-automatic-reservation", err)
	}
	return affected(result) == 1, nil
}

func encodeAutomaticRule(rule autoreservation.Rule) (string, string, error) {
	searchJSON, err := json.Marshal(rule.Search)
	if err != nil {
		return "", "", errors.New("sqlite: encode automatic search")
	}
	recordingJSON, err := json.Marshal(rule.Recording)
	if err != nil || len(searchJSON)+len(recordingJSON) > maxAutomaticRuleJSON {
		return "", "", errors.New("sqlite: automatic rule too large")
	}
	return string(searchJSON), string(recordingJSON), nil
}

func decodeAutomaticJSON(value string, destination any) error {
	if len(value) == 0 || len(value) > maxAutomaticRuleJSON {
		return errors.New("sqlite: invalid automatic rule json")
	}
	scanner := json.NewDecoder(bytes.NewReader([]byte(value)))
	scanner.UseNumber()
	if err := scanUniqueJSONValue(scanner); err != nil {
		return errors.New("sqlite: invalid automatic rule json")
	}
	if _, err := scanner.Token(); err != io.EOF {
		return errors.New("sqlite: invalid automatic rule json")
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(value)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("sqlite: invalid automatic rule json")
	}
	return nil
}
