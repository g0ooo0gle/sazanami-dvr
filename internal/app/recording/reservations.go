// Package recordingは番組予約から録画完了までの手順を組み立てる。
package recording

import (
	"context"
	"errors"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

// Catalogは完成済み番組表から予約条件に一致する一件を返す。
type Catalog interface {
	FindProgram(context.Context, recording.ReservationRequest) (recording.ProgramSnapshot, error)
}

// ReservationStoreは予約の確定保存と上限付き読出しを行う。
type ReservationStore interface {
	CreateReservation(context.Context, recording.Reservation) (recording.Reservation, error)
	ActiveReservations(context.Context, int, int32) ([]recording.Reservation, error)
}

// Clockは予約追加時刻をテストから指定できるようにする。
type Clock interface {
	Now() time.Time
}

// ReservationServiceは番組照合後に予約を一度だけ確定する。
type ReservationService struct {
	Catalog Catalog
	Store   ReservationStore
	Clock   Clock
	NewID   func() (catalogmodel.ID, error)
	OnAdded func()
}

// Addは終了前の番組を照合し、DBへの保存完了後に作成済み予約を返す。
func (service ReservationService) Add(ctx context.Context, request recording.ReservationRequest) (recording.Reservation, error) {
	if ctx == nil || service.Catalog == nil || service.Store == nil || service.Clock == nil || service.NewID == nil || request.Validate() != nil {
		return recording.Reservation{}, errors.New("recording: invalid reservation operation")
	}
	now := service.Clock.Now()
	if now.IsZero() {
		return recording.Reservation{}, errors.New("recording: invalid clock")
	}
	now = now.UTC()
	program, err := service.Catalog.FindProgram(ctx, request)
	if err != nil || !program.Start.Equal(request.Start) || program.Duration != request.Duration ||
		!program.Start.Add(program.Duration).After(now) {
		return recording.Reservation{}, errors.New("recording: program not reservable")
	}
	id, err := service.NewID()
	if err != nil {
		return recording.Reservation{}, errors.New("recording: id generation failed")
	}
	reservation, err := service.Store.CreateReservation(ctx, recording.Reservation{
		ID: id, Version: 1, State: recording.ReservationActive, Program: program,
		Priority: request.Priority, RequestedFollow: request.RequestedFollow,
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return recording.Reservation{}, err
	}
	if service.OnAdded != nil {
		service.OnAdded()
	}
	return reservation, nil
}

// Activeは未完了予約を予約番号順に、指定された上限まで返す。
func (service ReservationService) Active(ctx context.Context, limit int, after int32) ([]recording.Reservation, error) {
	if ctx == nil || service.Store == nil {
		return nil, errors.New("recording: invalid reservation operation")
	}
	return service.Store.ActiveReservations(ctx, limit, after)
}
