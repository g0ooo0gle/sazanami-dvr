package recording

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

type fixedCatalog struct {
	program core.ProgramSnapshot
	err     error
}

func (catalog fixedCatalog) FindProgram(context.Context, core.ReservationRequest) (core.ProgramSnapshot, error) {
	return catalog.program, catalog.err
}

type memoryReservations struct {
	created []core.Reservation
	err     error
}

func (store *memoryReservations) CreateReservation(_ context.Context, reservation core.Reservation) (core.Reservation, error) {
	if store.err != nil {
		return core.Reservation{}, store.err
	}
	reservation.Number = 1
	store.created = append(store.created, reservation)
	return reservation, nil
}

func (store *memoryReservations) ActiveReservations(context.Context, int, int32) ([]core.Reservation, error) {
	return append([]core.Reservation(nil), store.created...), nil
}

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

func TestReservationServiceAddsAfterCatalogMatch(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	request := core.ReservationRequest{
		NetworkID: 1, TransportStreamID: 2, ServiceID: 3, EventID: 4,
		Start: now.Add(time.Hour), Duration: 30 * time.Minute, Priority: 4, RequestedFollow: true,
	}
	program := appProgram(t, request)
	store := &memoryReservations{}
	notified := 0
	service := ReservationService{
		Catalog: fixedCatalog{program: program}, Store: store, Clock: fixedClock{now: now},
		NewID:   func() (catalogmodel.ID, error) { return appID(t, 9), nil },
		OnAdded: func() { notified++ },
	}
	created, err := service.Add(context.Background(), request)
	if err != nil || created.Number != 1 || len(store.created) != 1 || !created.RequestedFollow ||
		created.EffectiveFollow || notified != 1 || created.Program.Title != "server title" {
		t.Fatalf("created=%+v stored=%d notified=%d err=%v", created, len(store.created), notified, err)
	}
}

func TestReservationServiceDoesNotNotifyOnFailure(t *testing.T) {
	now := time.Date(2026, 8, 5, 2, 0, 0, 0, time.UTC)
	request := core.ReservationRequest{Start: now.Add(-time.Hour), Duration: 30 * time.Minute, Priority: 3}
	program := appProgram(t, request)
	store := &memoryReservations{}
	notified := 0
	service := ReservationService{
		Catalog: fixedCatalog{program: program}, Store: store, Clock: fixedClock{now: now},
		NewID: func() (catalogmodel.ID, error) { return appID(t, 9), nil }, OnAdded: func() { notified++ },
	}
	if _, err := service.Add(context.Background(), request); err == nil {
		t.Fatal("終了済み番組が予約されました")
	}
	if len(store.created) != 0 || notified != 0 {
		t.Fatalf("stored=%d notified=%d", len(store.created), notified)
	}
	store.err = errors.New("save failed")
	request.Start = now.Add(time.Hour)
	service.Catalog = fixedCatalog{program: appProgram(t, request)}
	if _, err := service.Add(context.Background(), request); err == nil || notified != 0 {
		t.Fatalf("save err=%v notified=%d", err, notified)
	}
}

func appProgram(t *testing.T, request core.ReservationRequest) core.ProgramSnapshot {
	t.Helper()
	return core.ProgramSnapshot{
		ProgramInstanceID: appID(t, 1), ProgramRevisionID: appID(t, 2), BackendID: appID(t, 3),
		ProviderServiceLocator: "1003", TuningTarget: "1003", NetworkID: request.NetworkID,
		TransportStreamID: request.TransportStreamID, ServiceID: request.ServiceID, EventID: request.EventID,
		Title: "server title", StationName: "server station", Start: request.Start, Duration: request.Duration,
	}
}

func appID(t *testing.T, marker byte) catalogmodel.ID {
	t.Helper()
	id, err := catalogmodel.NewIDFrom(bytes.NewReader(bytes.Repeat([]byte{marker}, 16)))
	if err != nil {
		t.Fatal(err)
	}
	return id
}
