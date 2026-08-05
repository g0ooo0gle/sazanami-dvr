package catalogsync_test

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/app/catalogsync"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

func TestRecoveryClosesInterruptedSyncAndIsIdempotent(t *testing.T) {
	_, store := migratedStore(t)
	backendID := recoveryID(t, 1)
	backend := catalogmodel.Backend{
		ID: backendID, Kind: "FAKE", IdentityHash: sha256.Sum256([]byte("fake:recovery")), ObservedAtMS: 100,
	}
	if err := store.EnsureBackend(context.Background(), backend); err != nil {
		t.Fatal(err)
	}
	completedID := recoveryID(t, 2)
	if err := store.BeginSync(context.Background(), catalogmodel.Sync{
		ID: completedID, BackendID: backendID, StartedAtMS: 101, CorrelationID: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteSync(context.Background(), completedID, 102, 0, 0); err != nil {
		t.Fatal(err)
	}
	runningID := recoveryID(t, 3)
	if err := store.BeginSync(context.Background(), catalogmodel.Sync{
		ID: runningID, BackendID: backendID, StartedAtMS: 103, CorrelationID: "interrupted",
	}); err != nil {
		t.Fatal(err)
	}

	clock := fixedRecoveryClock{now: time.UnixMilli(104)}
	service := catalogsync.RecoveryService{Repository: store, Clock: clock}
	count, err := service.Reconcile(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	count, err = service.Reconcile(context.Background())
	if err != nil || count != 0 {
		t.Fatalf("second count=%d err=%v", count, err)
	}
}

func TestRecoveryRefusesClockBeforeRunningSync(t *testing.T) {
	_, store := migratedStore(t)
	backendID := recoveryID(t, 4)
	if err := store.EnsureBackend(context.Background(), catalogmodel.Backend{
		ID: backendID, Kind: "FAKE", IdentityHash: sha256.Sum256([]byte("fake:future")), ObservedAtMS: 200,
	}); err != nil {
		t.Fatal(err)
	}
	runningID := recoveryID(t, 5)
	if err := store.BeginSync(context.Background(), catalogmodel.Sync{
		ID: runningID, BackendID: backendID, StartedAtMS: 201, CorrelationID: "future",
	}); err != nil {
		t.Fatal(err)
	}
	service := catalogsync.RecoveryService{Repository: store, Clock: fixedRecoveryClock{now: time.UnixMilli(200)}}
	if _, err := service.Reconcile(context.Background()); err == nil {
		t.Fatal("逆行したclockでreconciliationが成功しました")
	}
	service.Clock = fixedRecoveryClock{now: time.UnixMilli(202)}
	if count, err := service.Reconcile(context.Background()); err != nil || count != 1 {
		t.Fatalf("clock修正後 count=%d err=%v", count, err)
	}
}

type fixedRecoveryClock struct{ now time.Time }

func (clock fixedRecoveryClock) Now() time.Time { return clock.now }

func recoveryID(t *testing.T, marker byte) catalogmodel.ID {
	t.Helper()
	return testIDFromMarker(t, marker)
}

type markerReader byte

func (reader markerReader) Read(destination []byte) (int, error) {
	for index := range destination {
		destination[index] = byte(reader)
	}
	return len(destination), nil
}

func testIDFromMarker(t *testing.T, marker byte) catalogmodel.ID {
	t.Helper()
	id, err := catalogmodel.NewIDFrom(markerReader(marker))
	if err != nil {
		t.Fatal(err)
	}
	return id
}
