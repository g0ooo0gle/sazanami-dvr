package sqlite

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

func TestOperationsUIQueriesUseCompletedBoundedCatalog(t *testing.T) {
	_, store := openMigratedStore(t)
	backendID := testID(t, 80)
	if err := store.EnsureBackend(context.Background(), catalogmodel.Backend{
		ID: backendID, Kind: "FAKE", IdentityHash: sha256.Sum256([]byte("fake:ops-ui")), ObservedAtMS: 100,
	}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC).UnixMilli()
	completeCatalogGeneration(t, store, backendID, testID(t, 81), start, 257)

	backends, err := store.CurrentBackends(context.Background(), 16, catalogmodel.ID{})
	if err != nil || len(backends) != 1 || backends[0].ID != backendID || backends[0].Kind != "FAKE" {
		t.Fatalf("backends=%+v err=%v", backends, err)
	}
	services, err := store.CurrentServices(context.Background(), backendID, 256, catalogmodel.ID{})
	if err != nil || len(services) != 1 || services[0].DisplayName != "合成サービス" {
		t.Fatalf("services=%+v err=%v", services, err)
	}
	programs, truncated, err := store.CurrentProgramsInWindow(context.Background(), backendID,
		start-time.Hour.Milliseconds(), start+24*time.Hour.Milliseconds(), 256)
	if err != nil || len(programs) != 256 || !truncated {
		t.Fatalf("programs=%d truncated=%v err=%v", len(programs), truncated, err)
	}
	empty, truncated, err := store.CurrentProgramsInWindow(context.Background(), backendID,
		start-48*time.Hour.Milliseconds(), start-24*time.Hour.Milliseconds(), 256)
	if err != nil || len(empty) != 0 || truncated {
		t.Fatalf("empty=%d truncated=%v err=%v", len(empty), truncated, err)
	}
	if _, _, err := store.CurrentProgramsInWindow(context.Background(), backendID, start, start, 1); err == nil {
		t.Fatal("空のtime windowを受理しました")
	}
}

func completeCatalogGeneration(t *testing.T, store *Store, backendID, syncID catalogmodel.ID, start int64, count int) {
	t.Helper()
	if err := store.BeginSync(context.Background(), catalogmodel.Sync{
		ID: syncID, BackendID: backendID, StartedAtMS: start - 1, CorrelationID: "ops-ui-complete",
		VerifiedFakeLineage: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreServices(context.Background(), syncID, []catalogmodel.ServiceObservation{{
		ProviderLocator: "service:1", DisplayName: "合成サービス", Validation: catalogmodel.ValidationValid,
	}}); err != nil {
		t.Fatal(err)
	}
	duration := int64((30 * time.Minute) / time.Millisecond)
	for batchStart := 0; batchStart < count; batchStart += catalogmodel.MaxWriteBatch {
		batchEnd := min(batchStart+catalogmodel.MaxWriteBatch, count)
		observations := make([]catalogmodel.ProgramObservation, 0, batchEnd-batchStart)
		for index := batchStart; index < batchEnd; index++ {
			title := fmt.Sprintf("合成番組%03d", index)
			programStart := start + int64(index)*time.Minute.Milliseconds()
			observations = append(observations, catalogmodel.ProgramObservation{
				ServiceLocator: "service:1", EventLocator: fmt.Sprintf("event:%03d", index),
				Material: catalogmodel.RevisionMaterial{StartUTCMS: &programStart, DurationMS: &duration,
					Title: &title, Validation: catalogmodel.ValidationValid},
			})
		}
		if err := store.StorePrograms(context.Background(), syncID, true, observations); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CompleteSync(context.Background(), syncID, start, 1, count); err != nil {
		t.Fatal(err)
	}
}
