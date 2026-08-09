package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

const historyColumns = `m.reserve_id, a.state, a.terminal_reason, r.title, r.station_name,
	r.network_id, r.transport_stream_id, r.service_id, r.event_id,
	a.planned_start_utc_ms, a.planned_end_utc_ms, a.actual_start_utc_ms, a.actual_end_utc_ms, a.byte_count,
	s.relative_partial_path, s.relative_final_path, s.state, s.availability,
	s.file_synced, s.final_published, s.directory_synced`

const historyFrom = ` FROM ctrlcmd_reservation_ids m
	JOIN reservations r ON r.id=m.reservation_id
	JOIN recording_attempts a ON a.reservation_id=r.id
	JOIN recording_segments s ON s.attempt_id=a.id AND s.ordinal=0`

// RecordingHistoryは終了済み録画を新しい番号から指定上限まで返す。beforeが0なら最新から読む。
func (store *Store) RecordingHistory(ctx context.Context, limit int, before int32) ([]recording.HistoryItem, error) {
	if store == nil || store.reader == nil || ctx == nil || limit < 1 || limit > recording.MaxHistoryPage || before < 0 {
		return nil, errors.New("sqlite: invalid recording history query")
	}
	upper := int64(1 << 31)
	if before > 0 {
		upper = int64(before)
	}
	query := `SELECT ` + historyColumns + historyFrom + ` WHERE a.state IN
		('SUCCEEDED','PARTIAL','FAILED','CANCELLED','MISSED') AND m.reserve_id<?
		ORDER BY m.reserve_id DESC LIMIT ?`
	return store.readHistory(ctx, query, upper, limit)
}

// CompletedRecordingsは再生可能な完成録画を番号順に返す。CtrlCmdの逐次出力に使用する。
func (store *Store) CompletedRecordings(ctx context.Context, limit int, after int32) ([]recording.HistoryItem, error) {
	if store == nil || store.reader == nil || ctx == nil || limit < 1 || limit > recording.MaxHistoryPage || after < 0 {
		return nil, errors.New("sqlite: invalid completed recording query")
	}
	query := `SELECT ` + historyColumns + historyFrom + ` WHERE (
		(a.state='SUCCEEDED' AND a.terminal_reason IN ('COMPLETED','COMPLETED_AFTER_RECONNECT')) OR
		(a.state='PARTIAL' AND a.terminal_reason='USER_REQUESTED_STOP'))
		AND s.state='FINALIZED' AND s.availability='FINAL' AND s.file_synced=1
		AND s.final_published=1 AND s.directory_synced=1 AND a.byte_count>=188 AND m.reserve_id>?
		ORDER BY m.reserve_id LIMIT ?`
	return store.readHistory(ctx, query, after, limit)
}

// RecordingHistoryItemは指定番号の終了済み録画を返す。存在しない場合はnilを返す。
func (store *Store) RecordingHistoryItem(ctx context.Context, number int32) (*recording.HistoryItem, error) {
	if store == nil || store.reader == nil || ctx == nil || number < 1 {
		return nil, errors.New("sqlite: invalid recording history item query")
	}
	query := `SELECT ` + historyColumns + historyFrom + ` WHERE a.state IN
		('SUCCEEDED','PARTIAL','FAILED','CANCELLED','MISSED') AND m.reserve_id=? LIMIT 1`
	row := store.reader.QueryRowContext(ctx, query, number)
	item, err := scanHistory(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (store *Store) readHistory(ctx context.Context, query string, argument any, limit int) ([]recording.HistoryItem, error) {
	rows, err := store.reader.QueryContext(ctx, query, argument, limit)
	if err != nil {
		return nil, sanitize("query-recording-history", err)
	}
	defer rows.Close()
	items := make([]recording.HistoryItem, 0, limit)
	for rows.Next() {
		item, err := scanHistory(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, sanitize("iterate-recording-history", err)
	}
	return items, nil
}

func scanHistory(scanner rowScanner) (recording.HistoryItem, error) {
	var item recording.HistoryItem
	var networkID, transportID, serviceID, eventID int64
	var plannedStart, plannedEnd int64
	var actualStart, actualEnd sql.NullInt64
	var fileSynced, finalPublished, directorySynced int64
	if err := scanner.Scan(&item.Number, &item.State, &item.Reason, &item.Title, &item.StationName,
		&networkID, &transportID, &serviceID, &eventID, &plannedStart, &plannedEnd, &actualStart, &actualEnd,
		&item.ByteCount, &item.Plan.PartialPath, &item.Plan.FinalPath, &item.SegmentState, &item.Availability,
		&fileSynced, &finalPublished, &directorySynced); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return recording.HistoryItem{}, err
		}
		return recording.HistoryItem{}, sanitize("scan-recording-history", err)
	}
	if networkID < 0 || networkID > 65535 || transportID < 0 || transportID > 65535 ||
		serviceID < 0 || serviceID > 65535 || eventID < 0 || eventID > 65535 || plannedStart < 0 ||
		plannedEnd <= plannedStart || fileSynced < 0 || fileSynced > 1 || finalPublished < 0 ||
		finalPublished > 1 || directorySynced < 0 || directorySynced > 1 {
		return recording.HistoryItem{}, errors.New("sqlite: corrupt recording history value")
	}
	item.NetworkID, item.TransportStreamID = uint16(networkID), uint16(transportID)
	item.ServiceID, item.EventID = uint16(serviceID), uint16(eventID)
	item.PlannedStart, item.PlannedEnd = time.UnixMilli(plannedStart).UTC(), time.UnixMilli(plannedEnd).UTC()
	if actualStart.Valid {
		value := time.UnixMilli(actualStart.Int64).UTC()
		item.ActualStart = &value
	}
	if actualEnd.Valid {
		value := time.UnixMilli(actualEnd.Int64).UTC()
		item.ActualEnd = &value
	}
	item.FileSynced, item.FinalPublished, item.DirectorySynced = fileSynced == 1, finalPublished == 1, directorySynced == 1
	if err := item.Validate(); err != nil {
		return recording.HistoryItem{}, errors.New("sqlite: corrupt recording history item")
	}
	return item, nil
}
