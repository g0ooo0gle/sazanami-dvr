// Package recordingは予約、録画処理、録画ファイルの中核状態を定義する。
package recording

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

const (
	// MaxActiveReservationsはCtrlCmd 2011が列挙できる未完了予約の上限である。
	MaxActiveReservations = 16_384
	// MaxPageは予約を一度にDBから読む最大件数である。
	MaxPage = 256
	// MaxRecoveryPageは再起動時にDBから一度に照合する録画処理の最大件数である。
	MaxRecoveryPage = 100
	// MaxHistoryPageは録画履歴を一度にDBから読む最大件数である。
	MaxHistoryPage = 256
	// MaxHistoryItemsはCtrlCmdへ列挙できる録画履歴の最大件数である。
	MaxHistoryItems = 16_384
)

var (
	// ErrFinalExistsは完成ファイル名が既に使われており、上書きしなかったことを表す。
	ErrFinalExists = errors.New("recording: final file already exists")
)

// ReservationStateは一回限りの予約が実行前か終了済みかを表す。
type ReservationState string

const (
	// ReservationActiveはまだ最終的な録画結果を持たない予約である。
	ReservationActive ReservationState = "ACTIVE"
	// ReservationFinishedは録画試行がterminalになった予約である。
	ReservationFinished ReservationState = "FINISHED"
)

// ProgramSnapshotは予約時点で固定した番組と放送サービスの情報である。
type ProgramSnapshot struct {
	ProgramInstanceID      catalogmodel.ID
	ProgramRevisionID      catalogmodel.ID
	BackendID              catalogmodel.ID
	ProviderServiceLocator string
	TuningTarget           string
	NetworkID              uint16
	TransportStreamID      uint16
	ServiceID              uint16
	EventID                uint16
	Title                  string
	StationName            string
	Start                  time.Time
	Duration               time.Duration
}

// ReservationRequestは番組表から一回限りの予約を作るための照合条件である。
type ReservationRequest struct {
	NetworkID         uint16
	TransportStreamID uint16
	ServiceID         uint16
	EventID           uint16
	Start             time.Time
	Duration          time.Duration
	Priority          uint8
	RequestedFollow   bool
}

// ReservationChangeはKonomiTVが返す完全な予約情報から、変更対象と照合条件を分離した値である。
type ReservationChange struct {
	Number  int32
	Request ReservationRequest
}

// Validateは保存済み予約へ安全に照合できる変更要求かを検証する。
func (change ReservationChange) Validate() error {
	if change.Number < 1 || change.Request.Validate() != nil {
		return errors.New("recording: invalid reservation change")
	}
	return nil
}

// Validateは番組表の候補照合へ使用できる値かを検証する。
func (request ReservationRequest) Validate() error {
	if request.Start.IsZero() || request.Start.Location() != time.UTC || request.Start.UnixMilli() < 0 ||
		request.Duration < time.Second || request.Duration > 24*time.Hour || request.Duration%time.Second != 0 ||
		request.Priority < 1 || request.Priority > 5 {
		return errors.New("recording: invalid reservation request")
	}
	return nil
}

// ReservationはKonomiTVの番号と分離した内部予約を表す。
type Reservation struct {
	ID              catalogmodel.ID
	Number          int32
	Version         int64
	State           ReservationState
	Program         ProgramSnapshot
	Priority        uint8
	RequestedFollow bool
	EffectiveFollow bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
	FinishedAt      *time.Time
}

// FollowTargetは最新の完成済み番組表から選んだ、同じ番組の追従先revisionである。
type FollowTarget struct {
	ProgramInstanceID catalogmodel.ID
	ProgramRevisionID catalogmodel.ID
	Start             time.Time
	Duration          time.Duration
}

// ReservationFollowRequestは予約と追従先が評価後も同じことをCASで確認する入力である。
type ReservationFollowRequest struct {
	ReservationID      catalogmodel.ID
	ExpectedVersion    int64
	ExpectedRevisionID catalogmodel.ID
	TargetRevisionID   catalogmodel.ID
	Now                time.Time
}

// Validateは一回の録画開始前追従に必要な値が揃っているかを検証する。
func (request ReservationFollowRequest) Validate() error {
	zeroID := catalogmodel.ID{}
	if request.ReservationID == zeroID || request.ExpectedRevisionID == zeroID || request.TargetRevisionID == zeroID ||
		request.ExpectedRevisionID == request.TargetRevisionID || request.ExpectedVersion < 1 || request.Now.IsZero() ||
		request.Now.Location() != time.UTC || request.Now.UnixMilli() < 0 {
		return errors.New("recording: invalid reservation follow")
	}
	return nil
}

// ValidateNewは番号割当て前の固定番組予約を保存できるか検証する。
func (reservation Reservation) ValidateNew() error {
	program := reservation.Program
	if reservation.Version != 1 || reservation.State != ReservationActive || reservation.Number != 0 ||
		reservation.Priority < 1 || reservation.Priority > 5 || reservation.EffectiveFollow || reservation.FinishedAt != nil {
		return errors.New("recording: invalid reservation state")
	}
	zeroID := catalogmodel.ID{}
	if reservation.ID == zeroID || program.ProgramInstanceID == zeroID || program.ProgramRevisionID == zeroID ||
		program.BackendID == zeroID {
		return errors.New("recording: invalid reservation id")
	}
	if !validText(program.ProviderServiceLocator, 1, 256) || !validText(program.TuningTarget, 1, 256) ||
		!validText(program.Title, 0, 4096) || !validText(program.StationName, 0, 4096) {
		return errors.New("recording: invalid reservation text")
	}
	if program.Start.IsZero() || program.Start.Location() != time.UTC || program.Start.UnixMilli() < 0 ||
		program.Duration < time.Second || program.Duration > 24*time.Hour || program.Duration%time.Second != 0 {
		return errors.New("recording: invalid reservation time")
	}
	if reservation.CreatedAt.IsZero() || reservation.CreatedAt.Location() != time.UTC ||
		reservation.UpdatedAt != reservation.CreatedAt || reservation.CreatedAt.UnixMilli() < 0 {
		return errors.New("recording: invalid reservation timestamp")
	}
	return nil
}

// AttemptStateは一回の録画処理がどの段階にあるかを表す。
type AttemptState string

const (
	// AttemptClaimedはDB内で録画担当を一意に確保した状態である。
	AttemptClaimed AttemptState = "CLAIMED"
	// AttemptStartingはstream接続と部分ファイルの準備中である。
	AttemptStarting AttemptState = "STARTING"
	// AttemptRecordingはstreamを部分ファイルへ書いている状態である。
	AttemptRecording AttemptState = "RECORDING"
	// AttemptFinalizingは書込みを終えて完成ファイルを確定している状態である。
	AttemptFinalizing AttemptState = "FINALIZING"
	// AttemptSucceededは完成ファイルを保存できた終了状態である。
	AttemptSucceeded AttemptState = "SUCCEEDED"
	// AttemptPartialは一部だけ保存して終了した状態である。
	AttemptPartial AttemptState = "PARTIAL"
	// AttemptFailedは録画データを保存できずに終了した状態である。
	AttemptFailed AttemptState = "FAILED"
	// AttemptCancelledはプロセス終了などで中止した状態である。
	AttemptCancelled AttemptState = "CANCELLED"
	// AttemptMissedは録画枠を確保できずstreamを開かなかった状態である。
	AttemptMissed AttemptState = "MISSED"
)

// SegmentStateは受信データを一つのファイルへ書き込む処理の保存状態を表す。
type SegmentState string

const (
	// SegmentPlannedは書込み前のファイル計画である。
	SegmentPlanned SegmentState = "PLANNED"
	// SegmentWritingは部分ファイルへ書き込んでいる状態である。
	SegmentWriting SegmentState = "WRITING"
	// SegmentPartialは完成前に書込みが終わった状態である。
	SegmentPartial SegmentState = "PARTIAL"
	// SegmentFinalizedは完成ファイルを公開できた状態である。
	SegmentFinalized SegmentState = "FINALIZED"
)

// TerminalReasonは録画処理を終了した理由を、変更しない識別子としてDBへ残す値である。
type TerminalReason string

const (
	// ReasonCompletedは完成ファイルとDBの確定が終わった正常終了を表す。
	ReasonCompleted TerminalReason = "COMPLETED"
	// ReasonCompletedAfterReconnectは録画ストリームを開き直した後の正常終了を表す。
	ReasonCompletedAfterReconnect TerminalReason = "COMPLETED_AFTER_RECONNECT"
	// ReasonLateStartExpiredは開始猶予を過ぎて録画を始めなかったことを表す。
	ReasonLateStartExpired TerminalReason = "LATE_START_EXPIRED"
	// ReasonRecordingSlotUnavailableは一件だけの録画枠を取得できなかったことを表す。
	ReasonRecordingSlotUnavailable TerminalReason = "RECORDING_SLOT_UNAVAILABLE"
	// ReasonStreamNotFoundはプロバイダーに対象サービスがなかったことを表す。
	ReasonStreamNotFound TerminalReason = "STREAM_NOT_FOUND"
	// ReasonStreamUnavailableはプロバイダーがストリームを提供できなかったことを表す。
	ReasonStreamUnavailable TerminalReason = "STREAM_UNAVAILABLE"
	// ReasonStreamTimeoutは接続または読み込みが期限内に進まなかったことを表す。
	ReasonStreamTimeout TerminalReason = "STREAM_TIMEOUT"
	// ReasonStreamEndedEarlyは予約終了前にストリームが終了したことを表す。
	ReasonStreamEndedEarly TerminalReason = "STREAM_ENDED_EARLY"
	// ReasonStreamCancelledはコンテキストによってストリームが停止したことを表す。
	ReasonStreamCancelled TerminalReason = "STREAM_CANCELLED"
	// ReasonStreamReconnectExhaustedは上限の3回まで開き直してもストリームが回復しなかったことを表す。
	ReasonStreamReconnectExhausted TerminalReason = "STREAM_RECONNECT_EXHAUSTED"
	// ReasonFileCreateFailedは安全な部分ファイルを作れなかったことを表す。
	ReasonFileCreateFailed TerminalReason = "FILE_CREATE_FAILED"
	// ReasonFileWriteFailedは部分ファイルへ完全に書き込めなかったことを表す。
	ReasonFileWriteFailed TerminalReason = "FILE_WRITE_FAILED"
	// ReasonFileSyncFailedはファイルまたはディレクトリを永続化できなかったことを表す。
	ReasonFileSyncFailed TerminalReason = "FILE_SYNC_FAILED"
	// ReasonFinalNameConflictは完成ファイル名が既に使われていたことを表す。
	ReasonFinalNameConflict TerminalReason = "FINAL_NAME_CONFLICT"
	// ReasonFinalPublicationFailedは完成名の公開または部分名の除去に失敗したことを表す。
	ReasonFinalPublicationFailed TerminalReason = "FINAL_PUBLICATION_FAILED"
	// ReasonFinalDatabaseFailedは完成ファイル公開後のDB確定に失敗したことを表す。
	ReasonFinalDatabaseFailed TerminalReason = "FINAL_DATABASE_FAILED"
	// ReasonProcessInterruptedは前回プロセスの未完了処理を再起動時に検出したことを表す。
	ReasonProcessInterrupted TerminalReason = "PROCESS_INTERRUPTED"
	// ReasonProcessShutdownは明示停止によって録画を終了したことを表す。
	ReasonProcessShutdown TerminalReason = "PROCESS_SHUTDOWN"
	// ReasonFileMissingはDBが期待する録画ファイルを確認できないことを表す。
	ReasonFileMissing TerminalReason = "FILE_MISSING"
	// ReasonFileIntegrityMismatchはファイルの種類、サイズ、リンク関係がDBと一致しないことを表す。
	ReasonFileIntegrityMismatch TerminalReason = "FILE_INTEGRITY_MISMATCH"
)

// ValidはDBへ保存できる固定の終了理由かを返す。
func (reason TerminalReason) Valid() bool {
	switch reason {
	case ReasonCompleted, ReasonCompletedAfterReconnect, ReasonLateStartExpired, ReasonRecordingSlotUnavailable,
		ReasonStreamNotFound, ReasonStreamUnavailable, ReasonStreamTimeout,
		ReasonStreamEndedEarly, ReasonStreamCancelled, ReasonStreamReconnectExhausted, ReasonFileCreateFailed,
		ReasonFileWriteFailed, ReasonFileSyncFailed, ReasonFinalNameConflict,
		ReasonFinalPublicationFailed, ReasonFinalDatabaseFailed,
		ReasonProcessInterrupted, ReasonProcessShutdown, ReasonFileMissing, ReasonFileIntegrityMismatch:
		return true
	default:
		return false
	}
}

func (reason TerminalReason) successful() bool {
	return reason == ReasonCompleted || reason == ReasonCompletedAfterReconnect
}

// AvailabilityはDBから見た録画ファイルの現在状態である。
type Availability string

const (
	// AvailabilityPlannedはまだ録画ファイルを作っていない状態である。
	AvailabilityPlanned Availability = "PLANNED"
	// AvailabilityPartialは完成前の録画データが残っている状態である。
	AvailabilityPartial Availability = "PARTIAL"
	// AvailabilityFinalは完成ファイルを利用できる状態である。
	AvailabilityFinal Availability = "FINAL"
	// AvailabilityMissingはDBが期待するファイルを確認できない状態である。
	AvailabilityMissing Availability = "MISSING"
	// AvailabilityMismatchedはファイルの種類や大きさがDBと一致しない状態である。
	AvailabilityMismatched Availability = "MISMATCHED"
)

// FilePlanは録画保存先からの相対パスだけで、部分ファイルと完成ファイルを識別する。
type FilePlan struct {
	PartialPath string
	FinalPath   string
}

// NewFilePlanは予定開始のUTC年月と録画処理IDだけから相対パスを作る。
func NewFilePlan(start time.Time, attemptID catalogmodel.ID) (FilePlan, error) {
	if start.IsZero() || start.Location() != time.UTC || start.UnixMilli() < 0 || start.Year() > 9999 ||
		attemptID == (catalogmodel.ID{}) {
		return FilePlan{}, errors.New("recording: invalid file plan source")
	}
	directory := fmt.Sprintf("%04d/%02d", start.Year(), int(start.Month()))
	name := attemptID.String() + ".ts"
	plan := FilePlan{PartialPath: directory + "/" + name + ".partial", FinalPath: directory + "/" + name}
	if err := plan.Validate(); err != nil {
		return FilePlan{}, errors.New("recording: invalid generated file plan")
	}
	return plan, nil
}

// Validateは録画保存先の外を指す参照と、同名の部分・完成ファイルを拒否する。
func (plan FilePlan) Validate() error {
	if !validRelativePath(plan.PartialPath) || !validRelativePath(plan.FinalPath) ||
		plan.PartialPath == plan.FinalPath || path.Dir(plan.PartialPath) != path.Dir(plan.FinalPath) {
		return errors.New("recording: invalid file plan")
	}
	return nil
}

// ClaimRequestは一つの予約へ録画処理と最初のファイル計画を同時に割り当てる。
type ClaimRequest struct {
	ReservationID   catalogmodel.ID
	AttemptID       catalogmodel.ID
	SegmentID       catalogmodel.ID
	OwnerID         catalogmodel.ID
	OwnerGeneration int64
	Now             time.Time
	Plan            FilePlan
}

// ValidateはDBへ録画所有権を保存できる値かを検証する。
func (request ClaimRequest) Validate() error {
	zero := catalogmodel.ID{}
	if request.ReservationID == zero || request.AttemptID == zero || request.SegmentID == zero || request.OwnerID == zero ||
		request.OwnerGeneration < 1 || request.Now.IsZero() || request.Now.Location() != time.UTC || request.Now.UnixMilli() < 0 ||
		request.Plan.Validate() != nil {
		return errors.New("recording: invalid claim request")
	}
	return nil
}

// AttemptはDBへ確定した一回の録画処理と最初のファイル計画である。
type Attempt struct {
	ID            catalogmodel.ID
	ReservationID catalogmodel.ID
	State         AttemptState
	PlannedStart  time.Time
	PlannedEnd    time.Time
	ByteCount     int64
	Plan          FilePlan
}

// HistoryItemは一回の終了済み録画を外部形式から独立して読み出すための保存済み事実である。
// FilePlanはadapter内の安全なfile解決にだけ使い、HTTPやCtrlCmdへ直接公開しない。
type HistoryItem struct {
	Number            int32
	State             AttemptState
	Reason            TerminalReason
	Title             string
	StationName       string
	NetworkID         uint16
	TransportStreamID uint16
	ServiceID         uint16
	EventID           uint16
	PlannedStart      time.Time
	PlannedEnd        time.Time
	ActualStart       *time.Time
	ActualEnd         *time.Time
	ByteCount         int64
	Plan              FilePlan
	SegmentState      SegmentState
	Availability      Availability
	FileSynced        bool
	FinalPublished    bool
	DirectorySynced   bool
}

// ValidateはDBから読んだ履歴が終了状態と時刻、file計画の不変条件を満たすか確認する。
func (item HistoryItem) Validate() error {
	if item.Number < 1 || !terminalAttemptState(item.State) || !item.Reason.Valid() ||
		!validText(item.Title, 0, 4096) || !validText(item.StationName, 0, 4096) || item.ByteCount < 0 ||
		item.PlannedStart.IsZero() || item.PlannedStart.Location() != time.UTC ||
		item.PlannedEnd.Location() != time.UTC || !item.PlannedEnd.After(item.PlannedStart) ||
		item.PlannedEnd.Sub(item.PlannedStart) > 24*time.Hour || item.Plan.Validate() != nil {
		return errors.New("recording: invalid history item")
	}
	if (item.ActualStart == nil) != (item.ActualEnd == nil) {
		return errors.New("recording: incomplete history time")
	}
	if item.ActualStart != nil && (item.ActualStart.Location() != time.UTC || item.ActualEnd.Location() != time.UTC ||
		item.ActualEnd.Before(*item.ActualStart) || item.ActualEnd.Sub(*item.ActualStart) > 24*time.Hour) {
		return errors.New("recording: invalid actual history time")
	}
	switch item.SegmentState {
	case SegmentPlanned, SegmentWriting, SegmentPartial, SegmentFinalized:
	default:
		return errors.New("recording: invalid history segment state")
	}
	switch item.Availability {
	case AvailabilityPlanned, AvailabilityPartial, AvailabilityFinal, AvailabilityMissing, AvailabilityMismatched:
	default:
		return errors.New("recording: invalid history availability")
	}
	return nil
}

// Playableは完成済み録画としてCtrlCmdとHTTPへ公開できる場合だけtrueを返す。
func (item HistoryItem) Playable() bool {
	return item.Validate() == nil && item.State == AttemptSucceeded && item.Reason.successful() &&
		item.ActualStart != nil && item.ActualEnd.Sub(*item.ActualStart) >= time.Second && item.ByteCount >= 188 && item.SegmentState == SegmentFinalized &&
		item.Availability == AvailabilityFinal && item.FileSynced && item.FinalPublished && item.DirectorySynced
}

func terminalAttemptState(state AttemptState) bool {
	switch state {
	case AttemptSucceeded, AttemptPartial, AttemptFailed, AttemptCancelled, AttemptMissed:
		return true
	default:
		return false
	}
}

// RecoveryItemは再起動時にDBと録画ファイルを照合するための、保存済みの事実である。
type RecoveryItem struct {
	Attempt
	FinalizationToken catalogmodel.ID
	FileSynced        bool
	FinalPublished    bool
	DirectorySynced   bool
	Availability      Availability
	Recovered         bool
}

// FileFactはDBに記録した一つの相対パスで観測したファイル状態である。
type FileFact struct {
	Exists  bool
	Regular bool
	Size    int64
}

// FileObservationは部分名と完成名の現在状態、および同じファイルを指すかを表す。
type FileObservation struct {
	Partial  FileFact
	Final    FileFact
	SameFile bool
	Unsafe   bool
}

// FinalizeRequestは録画データの同期完了をDBへ確定するための値をまとめる。
type FinalizeRequest struct {
	AttemptID catalogmodel.ID
	Token     catalogmodel.ID
	ByteCount int64
	Now       time.Time
}

// Validateは完成処理を始められる値かを検証する。
func (request FinalizeRequest) Validate() error {
	zero := catalogmodel.ID{}
	if request.AttemptID == zero || request.Token == zero || request.ByteCount < 188 ||
		request.Now.IsZero() || request.Now.Location() != time.UTC || request.Now.UnixMilli() < 0 {
		return errors.New("recording: invalid finalize request")
	}
	return nil
}

// FinishRequestは録画試行と予約を同時に終了状態へ進めるための値である。
type FinishRequest struct {
	AttemptID    catalogmodel.ID
	State        AttemptState
	Reason       TerminalReason
	ByteCount    int64
	Availability Availability
	Recovered    bool
	Now          time.Time
}

// Validateは終了状態とファイル状態の組合せを検証する。
func (request FinishRequest) Validate() error {
	zero := catalogmodel.ID{}
	if request.AttemptID == zero || !request.Reason.Valid() || request.ByteCount < 0 ||
		request.Now.IsZero() || request.Now.Location() != time.UTC || request.Now.UnixMilli() < 0 {
		return errors.New("recording: invalid finish request")
	}
	switch request.State {
	case AttemptSucceeded:
		if !request.Reason.successful() || request.ByteCount < 188 || request.Availability != AvailabilityFinal {
			return errors.New("recording: invalid successful finish")
		}
	case AttemptPartial:
		if request.Reason.successful() || request.ByteCount < 188 || request.Availability != AvailabilityPartial {
			return errors.New("recording: invalid partial finish")
		}
	case AttemptFailed, AttemptCancelled:
		if request.Reason.successful() || (request.Availability != AvailabilityPartial &&
			request.Availability != AvailabilityMissing && request.Availability != AvailabilityMismatched) {
			return errors.New("recording: invalid unsuccessful finish")
		}
	case AttemptMissed:
		if request.Reason.successful() || request.ByteCount != 0 || request.Availability != AvailabilityMissing {
			return errors.New("recording: invalid empty finish")
		}
	default:
		return errors.New("recording: finish state is not terminal")
	}
	return nil
}

func validRelativePath(value string) bool {
	return value != "" && len(value) <= 512 && !strings.HasPrefix(value, "/") && !strings.Contains(value, "\\") &&
		path.Clean(value) == value && value != "." && value != ".." && !strings.HasPrefix(value, "../")
}

func validText(value string, minimum, maximum int) bool {
	return utf8.ValidString(value) && len(value) >= minimum && len(value) <= maximum
}
