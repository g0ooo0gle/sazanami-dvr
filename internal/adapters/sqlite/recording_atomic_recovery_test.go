//go:build unix

package sqlite

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/recordingfs"
	apprecording "github.com/g0ooo0gle/sazanami-dvr/internal/app/recording"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

func TestAtomicPublicationBeforeDatabaseRecordSurvivesRestart(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		for _, window := range []string{"main", "one-seg"} {
			name := "normal/" + window
			if stopped {
				name = "user-stop/" + window
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				dataRoot, store := openMigratedStore(t)
				reservation := reservationForTest(t, store)
				reservation.OneSegOutput = &core.OneSegOutput{ProviderServiceLocator: "1004"}
				created, err := store.CreateReservation(ctx, reservation)
				if err != nil {
					t.Fatal(err)
				}
				onePlan, err := core.NewOneSegFilePlan(created, testID(t, 212))
				if err != nil {
					t.Fatal(err)
				}
				now := reservation.CreatedAt.Add(time.Minute)
				claim := core.ClaimRequest{
					ReservationID: created.ID, ReservationVersion: created.Version,
					AttemptID: testID(t, 212), SegmentID: testID(t, 213), OneSegSegmentID: testID(t, 214),
					OwnerID: testID(t, 215), OwnerGeneration: 1, Now: now,
					Plan:       core.FilePlan{PartialPath: "2026/08/main.ts.partial", FinalPath: "2026/08/main.ts"},
					OneSegPlan: &onePlan,
				}
				if _, err := store.ClaimRecording(ctx, claim); err != nil {
					t.Fatal(err)
				}
				if err := store.StartAttempt(ctx, claim.AttemptID, now.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				if _, err := store.RecordingStarted(ctx, claim.AttemptID, now.Add(2*time.Second)); err != nil {
					t.Fatal(err)
				}
				if err := store.OneSegRecordingStarted(ctx, claim.AttemptID, now.Add(3*time.Second)); err != nil {
					t.Fatal(err)
				}
				rootPath := filepath.Join(t.TempDir(), "recordings")
				root, err := recordingfs.OpenRoot(rootPath)
				if err != nil {
					t.Fatal(err)
				}
				defer root.Close()
				plans := []core.FilePlan{claim.Plan, onePlan}
				contents := [][]byte{bytes.Repeat([]byte{0x47}, 376), bytes.Repeat([]byte{0x48}, 188)}
				for index, plan := range plans {
					file, err := root.CreatePartial(plan)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := file.Write(contents[index]); err != nil {
						t.Fatal(err)
					}
					if err := file.Sync(); err != nil {
						t.Fatal(err)
					}
					if err := file.Close(); err != nil {
						t.Fatal(err)
					}
				}
				if stopped {
					if _, err := store.StopReservation(ctx, created.Number, now.Add(4*time.Second)); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := store.BeginFinalization(ctx, core.FinalizeRequest{
					AttemptID: claim.AttemptID, Token: testID(t, 216), ByteCount: 376,
					State: core.AttemptSucceeded, Reason: core.ReasonCompleted, Now: now.Add(5 * time.Second),
					OneSeg: &core.OneSegResult{ByteCount: 188, Availability: core.AvailabilityPartial,
						Reason: core.ReasonCompleted, FileSynced: true, Publish: true},
				}); err != nil {
					t.Fatal(err)
				}
				if err := root.LinkFinal(claim.Plan); err != nil {
					t.Fatal(err)
				}
				if window == "one-seg" {
					if err := store.MarkFinalPublished(ctx, claim.AttemptID, now.Add(6*time.Second)); err != nil {
						t.Fatal(err)
					}
					if err := root.SyncDirectory(claim.Plan); err != nil {
						t.Fatal(err)
					}
					if err := store.MarkDirectorySynced(ctx, claim.AttemptID, now.Add(7*time.Second)); err != nil {
						t.Fatal(err)
					}
					if err := root.LinkFinal(onePlan); err != nil {
						t.Fatal(err)
					}
				}
				// 対象のrenameだけが完了し、公開フラグを書かずに再起動する。
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				if err := root.Close(); err != nil {
					t.Fatal(err)
				}
				store, err = OpenStore(ctx, dataRoot)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				root, err = recordingfs.OpenRoot(rootPath)
				if err != nil {
					t.Fatal(err)
				}
				defer root.Close()
				recovery := apprecording.Recovery{Store: store, Clock: &e2eClock{now: now.Add(10 * time.Second)},
					Files: apprecording.RecoveryFiles{Inspect: root.Inspect, FileOperations: apprecording.FileOperations{
						CreatePartial: func(plan core.FilePlan) (apprecording.PartialFile, error) { return root.CreatePartial(plan) },
						LinkFinal:     root.LinkFinal, SyncDirectory: root.SyncDirectory, RemovePartial: root.RemovePartial,
					}}}
				for range 2 {
					if err := recovery.Run(ctx); err != nil {
						t.Fatal(err)
					}
				}
				wantState, wantReason := core.AttemptSucceeded, core.ReasonCompleted
				if stopped {
					wantState, wantReason = core.AttemptPartial, core.ReasonUserRequestedStop
				}
				history, err := store.RecordingHistoryItem(ctx, created.Number)
				if err != nil || history == nil || !history.Playable() || history.State != wantState || history.Reason != wantReason {
					t.Fatalf("history=%+v err=%v", history, err)
				}
				items, err := store.RecoveryAttempts(ctx, core.MaxRecoveryPage, catalogmodel.ID{})
				if err != nil || len(items) != 1 || !items[0].Recovered || items[0].OneSeg == nil ||
					items[0].OneSeg.State != core.SegmentFinalized || items[0].OneSeg.Availability != core.AvailabilityFinal {
					t.Fatalf("recovery=%+v err=%v", items, err)
				}
				for index, plan := range plans {
					file, err := root.OpenFinal(plan, int64(len(contents[index])))
					if err != nil {
						t.Fatal(err)
					}
					got, readErr := io.ReadAll(file)
					closeErr := file.Close()
					if readErr != nil || closeErr != nil || !bytes.Equal(got, contents[index]) {
						t.Fatalf("segment=%d bytes=%d read=%v close=%v", index, len(got), readErr, closeErr)
					}
				}
			})
		}
	}
}
