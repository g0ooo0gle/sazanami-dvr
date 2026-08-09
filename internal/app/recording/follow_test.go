package recording

import (
	"context"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

type memoryFollowStore struct {
	reservations []core.Reservation
	targets      map[catalogmodel.ID]*core.FollowTarget
	blocked      map[catalogmodel.ID]bool
	applied      []core.ReservationFollowRequest
}

func (store *memoryFollowStore) ActiveReservations(_ context.Context, limit int, after int32) ([]core.Reservation, error) {
	result := make([]core.Reservation, 0, limit)
	for _, reservation := range store.reservations {
		if reservation.Number > after && len(result) < limit {
			result = append(result, reservation)
		}
	}
	return result, nil
}

func (store *memoryFollowStore) CurrentFollowTarget(_ context.Context, _ catalogmodel.ID,
	instanceID catalogmodel.ID,
) (*core.FollowTarget, error) {
	return store.targets[instanceID], nil
}

func (store *memoryFollowStore) ApplyReservationFollow(_ context.Context, request core.ReservationFollowRequest) (bool, error) {
	store.applied = append(store.applied, request)
	return !store.blocked[request.ReservationID], nil
}

func TestFollowServiceUpdatesOnlyEligibleReservations(t *testing.T) {
	now := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	makeReservation := func(number int32, marker byte, requested bool) core.Reservation {
		return core.Reservation{
			ID: appID(t, marker), Number: number, Version: 1, State: core.ReservationActive,
			RequestedFollow: requested,
			Program: core.ProgramSnapshot{
				ProgramInstanceID: appID(t, marker+20), ProgramRevisionID: appID(t, marker+40),
				BackendID: appID(t, 90), Start: now.Add(time.Hour), Duration: 30 * time.Minute,
			},
		}
	}
	update := makeReservation(1, 1, true)
	unchanged := makeReservation(2, 2, true)
	off := makeReservation(3, 3, false)
	blocked := makeReservation(4, 4, true)
	store := &memoryFollowStore{
		reservations: []core.Reservation{update, unchanged, off, blocked},
		targets: map[catalogmodel.ID]*core.FollowTarget{
			update.Program.ProgramInstanceID: {
				ProgramInstanceID: update.Program.ProgramInstanceID, ProgramRevisionID: appID(t, 70),
				Start: update.Program.Start.Add(10 * time.Minute), Duration: 35 * time.Minute,
			},
			unchanged.Program.ProgramInstanceID: {
				ProgramInstanceID: unchanged.Program.ProgramInstanceID,
				ProgramRevisionID: appID(t, 72),
				Start:             unchanged.Program.Start, Duration: unchanged.Program.Duration,
			},
			blocked.Program.ProgramInstanceID: {
				ProgramInstanceID: blocked.Program.ProgramInstanceID, ProgramRevisionID: appID(t, 71),
				Start: blocked.Program.Start.Add(10 * time.Minute), Duration: 35 * time.Minute,
			},
		},
		blocked: map[catalogmodel.ID]bool{blocked.ID: true},
	}
	notified := 0
	result, err := (FollowService{Store: store, Clock: fixedClock{now: now}, OnUpdated: func() { notified++ }}).
		Run(context.Background())
	if err != nil || result != (FollowResult{Evaluated: 4, Updated: 1, Unchanged: 2, Blocked: 1}) ||
		len(store.applied) != 2 || notified != 1 {
		t.Fatalf("result=%+v applied=%d notified=%d err=%v", result, len(store.applied), notified, err)
	}
}

func TestFollowServiceReservationLimit(t *testing.T) {
	now := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	for _, count := range []int{core.MaxActiveReservations, core.MaxActiveReservations + 1} {
		reservations := make([]core.Reservation, count)
		for index := range reservations {
			reservations[index].Number = int32(index + 1)
		}
		_, err := (FollowService{Store: &memoryFollowStore{reservations: reservations}, Clock: fixedClock{now: now}}).
			Run(context.Background())
		if (err != nil) != (count > core.MaxActiveReservations) {
			t.Fatalf("count=%d err=%v", count, err)
		}
	}
}
