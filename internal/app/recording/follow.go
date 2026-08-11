package recording

import (
	"context"
	"errors"
	"fmt"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

// FollowStoreは完成済み番組表と未着手予約を短い操作で照合するPortである。
type FollowStore interface {
	ActiveReservations(context.Context, int, int32) ([]core.Reservation, error)
	CurrentFollowTarget(context.Context, catalogmodel.ID, catalogmodel.ID) (*core.FollowTarget, error)
	ApplyReservationFollow(context.Context, core.ReservationFollowRequest) (bool, error)
}

// FollowResultは番組表更新後の予約追従を、秘密情報を含まない件数で表す。
type FollowResult struct {
	Evaluated int
	Updated   int
	Unchanged int
	Blocked   int
}

// Stringは通常出力へ利用できる上限付き集計を返す。
func (result FollowResult) String() string {
	return fmt.Sprintf("reservation follow completed: evaluated=%d updated=%d unchanged=%d blocked=%d",
		result.Evaluated, result.Updated, result.Unchanged, result.Blocked)
}

// FollowServiceは追従対象予約を番号順に読み、完成済み番組表の時刻へ直列で更新する。
type FollowService struct {
	Store         FollowStore
	Clock         Clock
	ExtensionOnly bool
	OnUpdated     func()
}

// Runは最新の完成済み番組表を使い、最大16,384件の有効予約を追従させる。
func (service FollowService) Run(ctx context.Context) (FollowResult, error) {
	var result FollowResult
	if ctx == nil || service.Store == nil || service.Clock == nil {
		return result, errors.New("recording: invalid follow service")
	}
	now := service.Clock.Now().UTC()
	if now.IsZero() || now.UnixMilli() < 0 {
		return result, errors.New("recording: invalid follow clock")
	}
	after := int32(0)
	for {
		page, err := service.Store.ActiveReservations(ctx, core.MaxPage, after)
		if err != nil || len(page) > core.MaxPage {
			return result, errors.New("recording: read follow reservations")
		}
		for _, reservation := range page {
			if reservation.Number <= after || result.Evaluated >= core.MaxActiveReservations {
				return result, errors.New("recording: invalid follow reservation order")
			}
			after = reservation.Number
			result.Evaluated++
			if !reservation.RequestedFollow {
				result.Unchanged++
				continue
			}
			target, err := service.Store.CurrentFollowTarget(ctx, reservation.Program.BackendID,
				reservation.Program.ProgramInstanceID)
			if err != nil {
				return result, errors.New("recording: read follow target")
			}
			if target == nil || target.ProgramInstanceID != reservation.Program.ProgramInstanceID ||
				target.ProgramRevisionID == reservation.Program.ProgramRevisionID ||
				target.Start.Equal(reservation.Program.Start) && target.Duration == reservation.Program.Duration {
				result.Unchanged++
				continue
			}
			applied, err := service.Store.ApplyReservationFollow(ctx, core.ReservationFollowRequest{
				ReservationID: reservation.ID, ExpectedVersion: reservation.Version,
				ExpectedRevisionID: reservation.Program.ProgramRevisionID,
				TargetRevisionID:   target.ProgramRevisionID, Now: now,
				ExtensionOnly: service.ExtensionOnly,
			})
			if err != nil {
				return result, errors.New("recording: apply reservation follow")
			}
			if applied {
				result.Updated++
			} else {
				result.Blocked++
			}
		}
		if len(page) < core.MaxPage {
			if result.Updated > 0 && service.OnUpdated != nil {
				service.OnUpdated()
			}
			return result, nil
		}
	}
}
