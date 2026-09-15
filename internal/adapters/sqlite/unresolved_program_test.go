package sqlite

import (
	"context"
	"crypto/sha256"
	"testing"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

// 過去または別backendのserviceへ誤って紐付ける変更を検出する。
func TestUnresolvedProgramUsesCurrentGenerationAndPreservesReservations(t *testing.T) {
	for _, scenario := range []string{"unknown", "previous-generation", "other-backend"} {
		t.Run(scenario, func(t *testing.T) {
			_, store := openMigratedStore(t)
			ctx := context.Background()
			reservation := reservationForTest(t, store)
			if _, err := store.CreateReservation(ctx, reservation); err != nil {
				t.Fatal(err)
			}
			backend := reservation.Program.BackendID
			previous, err := store.CurrentPrograms(ctx, backend, 10, catalogmodel.ID{})
			if err != nil || len(previous) != 1 {
				t.Fatalf("previous=%v err=%v", previous, err)
			}
			locator := "missing"
			if scenario != "unknown" {
				locator = reservation.Program.ProviderServiceLocator
			}
			if scenario == "other-backend" {
				backend = testID(t, 201)
				if err := store.EnsureBackend(ctx, catalogmodel.Backend{ID: backend, Kind: "MIRAKURUN", IdentityHash: sha256.Sum256([]byte("other")), ObservedAtMS: 1}); err != nil {
					t.Fatal(err)
				}
			}
			syncID := testID(t, 202)
			started := reservation.Program.Start.UnixMilli() + 1
			if err := store.BeginSync(ctx, catalogmodel.Sync{ID: syncID, BackendID: backend, StartedAtMS: started, CorrelationID: "unresolved"}); err != nil {
				t.Fatal(err)
			}
			if err := store.StoreServices(ctx, syncID, []catalogmodel.ServiceObservation{{ProviderLocator: "present", DisplayName: "present", Validation: catalogmodel.ValidationProvisional}}); err != nil {
				t.Fatal(err)
			}
			known := catalogmodel.ProgramObservation{ServiceLocator: "present", EventLocator: "known", Material: previous[0].Material}
			unknown := known
			unknown.ServiceLocator, unknown.EventLocator = locator, "unresolved"
			if err := store.StorePrograms(ctx, syncID, false, []catalogmodel.ProgramObservation{known, unknown}); err != nil {
				t.Fatal(err)
			}
			if err := store.CompleteSync(ctx, syncID, started+1, 1, 2); err != nil {
				t.Fatal(err)
			}
			var classification, reason string
			var unbound bool
			if err := store.reader.QueryRow(`SELECT classification, validation_reason,
				program_instance_id IS NULL AND program_revision_id IS NULL AND content_hash IS NULL
				FROM program_observations WHERE sync_id=? AND provider_event_locator='unresolved'`, syncID.Bytes()).Scan(&classification, &reason, &unbound); err != nil {
				t.Fatal(err)
			}
			if classification != "INVALID" || reason != "service-not-in-current-catalog" || !unbound {
				t.Fatalf("classification=%s reason=%s unbound=%v", classification, reason, unbound)
			}
			current, err := store.CurrentPrograms(ctx, backend, 10, catalogmodel.ID{})
			if err != nil || len(current) != 1 || current[0].ServiceLocator != "present" {
				t.Fatalf("current=%v err=%v", current, err)
			}
			selected, err := store.CurrentProgramsForService(ctx, backend, locator, 10, "")
			if err != nil || len(selected) != 0 {
				t.Fatalf("unresolved selection=%v err=%v", selected, err)
			}
			reservations, err := store.ActiveReservations(ctx, 10, 0)
			if err != nil || len(reservations) != 1 || reservations[0].Program.ProgramRevisionID != reservation.Program.ProgramRevisionID {
				t.Fatalf("reservation changed: %v %v", reservations, err)
			}
		})
	}
}

func TestUnresolvedProgramDoesNotHideDuplicateCancellationOrDatabaseFailure(t *testing.T) {
	_, store := openMigratedStore(t)
	ctx := context.Background()
	reservation := reservationForTest(t, store)
	syncID := testID(t, 203)
	if err := store.BeginSync(ctx, catalogmodel.Sync{ID: syncID, BackendID: reservation.Program.BackendID, StartedAtMS: reservation.Program.Start.UnixMilli() + 1, CorrelationID: "failures"}); err != nil {
		t.Fatal(err)
	}
	observation := catalogmodel.ProgramObservation{ServiceLocator: "missing", EventLocator: "event"}
	if err := store.StorePrograms(ctx, syncID, false, []catalogmodel.ProgramObservation{observation, observation}); err == nil {
		t.Fatal("duplicate accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.StorePrograms(canceled, syncID, false, []catalogmodel.ProgramObservation{observation}); err == nil {
		t.Fatal("cancellation ignored")
	}
	if _, err := store.writer.Exec(`CREATE TRIGGER fail_unresolved BEFORE INSERT ON program_observations BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.StorePrograms(ctx, syncID, false, []catalogmodel.ProgramObservation{observation}); err == nil {
		t.Fatal("database failure ignored")
	}
	var rows int
	if err := store.reader.QueryRow(`SELECT count(*) FROM program_observations WHERE sync_id=?`, syncID.Bytes()).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rows=%d err=%v", rows, err)
	}
}
