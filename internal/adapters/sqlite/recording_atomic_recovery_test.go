//go:build unix

package sqlite

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/recordingfs"
	apprecording "github.com/g0ooo0gle/sazanami-dvr/internal/app/recording"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

func TestQualityFinalizeCrashReadback(t *testing.T) {
	for _, reason := range []core.TerminalReason{core.ReasonStreamEndedEarly, core.ReasonStreamTimeout, core.ReasonStreamReconnectExhausted} {
		for _, window := range []string{"pre-plan", "plan", "rename", "published", "directory", "terminal", "unplanned-terminal"} {
			t.Run(string(reason)+"/"+window, func(t *testing.T) {
				ctx := context.Background()
				dataRoot, store := openMigratedStore(t)
				claim, reservation, now := qualityRunningAttempt(t, store)
				q := core.QualitySummary{Status: core.QualityDegraded, ObservationLimited: true, TEIPackets: 3, ReconnectCount: 2}
				aux := core.QualitySummary{Status: core.QualityNoIssuesObserved}
				rootPath := filepath.Join(t.TempDir(), "recordings")
				root, err := recordingfs.OpenRoot(rootPath)
				if err != nil {
					t.Fatal(err)
				}
				for ordinal, plan := range []core.FilePlan{claim.Plan, *claim.OneSegPlan} {
					file, err := root.CreatePartial(plan)
					if err != nil {
						t.Fatal(err)
					}
					length := 376
					if ordinal == 1 {
						length = 188
					}
					if _, err := file.Write(bytes.Repeat([]byte{0x47}, length)); err != nil {
						t.Fatal(err)
					}
					if err := file.Sync(); err != nil {
						t.Fatal(err)
					}
					if err := file.Close(); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := store.UpdateRecordingProgress(ctx, claim.AttemptID, 376, now.Add(time.Second), q); err != nil {
					t.Fatal(err)
				}
				if _, err := store.UpdateOneSegProgress(ctx, claim.AttemptID, 188, now.Add(time.Second), aux); err != nil {
					t.Fatal(err)
				}
				planned := window != "pre-plan" && window != "unplanned-terminal"
				if planned {
					if _, err := store.BeginFinalization(ctx, core.FinalizeRequest{AttemptID: claim.AttemptID, Token: testID(t, 204),
						State: core.AttemptPartial, Reason: reason, ByteCount: 376, Now: now.Add(2 * time.Second), Quality: q,
						OneSeg: &core.OneSegResult{ByteCount: 188, Reason: core.ReasonCompleted, Availability: core.AvailabilityPartial,
							FileSynced: true, Publish: true, Quality: aux}}); err != nil {
						t.Fatal(err)
					}
				}
				if window == "rename" || window == "published" || window == "directory" || window == "terminal" {
					if err := root.LinkFinal(claim.Plan); err != nil {
						t.Fatal(err)
					}
				}
				if window == "published" || window == "directory" || window == "terminal" {
					if err := store.MarkFinalPublished(ctx, claim.AttemptID, now.Add(3*time.Second)); err != nil {
						t.Fatal(err)
					}
				}
				if window == "directory" || window == "terminal" {
					if err := root.SyncDirectory(claim.Plan); err != nil {
						t.Fatal(err)
					}
					if err := root.RemovePartial(claim.Plan); err != nil {
						t.Fatal(err)
					}
					if err := root.SyncDirectory(claim.Plan); err != nil {
						t.Fatal(err)
					}
					if err := store.MarkDirectorySynced(ctx, claim.AttemptID, now.Add(3*time.Second)); err != nil {
						t.Fatal(err)
					}
				}
				if window == "terminal" {
					if err := root.LinkFinal(*claim.OneSegPlan); err != nil {
						t.Fatal(err)
					}
					if err := store.MarkOneSegFinalPublished(ctx, claim.AttemptID, now.Add(3*time.Second)); err != nil {
						t.Fatal(err)
					}
					if err := root.SyncDirectory(*claim.OneSegPlan); err != nil {
						t.Fatal(err)
					}
					if err := root.RemovePartial(*claim.OneSegPlan); err != nil {
						t.Fatal(err)
					}
					if err := root.SyncDirectory(*claim.OneSegPlan); err != nil {
						t.Fatal(err)
					}
					if err := store.MarkOneSegDirectorySynced(ctx, claim.AttemptID, now.Add(3*time.Second)); err != nil {
						t.Fatal(err)
					}
					if err := store.FinishAttempt(ctx, core.FinishRequest{AttemptID: claim.AttemptID, State: core.AttemptPartial,
						Reason: reason, ByteCount: 376, Availability: core.AvailabilityFinal, Now: now.Add(3 * time.Second), Quality: q}); err != nil {
						t.Fatal(err)
					}
				}
				if window == "unplanned-terminal" {
					if err := store.FinishAttempt(ctx, core.FinishRequest{AttemptID: claim.AttemptID, State: core.AttemptPartial,
						Reason: reason, ByteCount: 376, Availability: core.AvailabilityPartial, Now: now.Add(3 * time.Second), Quality: q,
						OneSeg: &core.OneSegResult{ByteCount: 188, Reason: reason, Availability: core.AvailabilityPartial, Quality: aux}}); err != nil {
						t.Fatal(err)
					}
				}
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
				clock := &e2eClock{now: now.Add(4 * time.Second)}
				recovery := apprecording.Recovery{Store: store, Clock: clock, Files: apprecording.RecoveryFiles{
					FileOperations: apprecording.FileOperations{
						CreatePartial: func(plan core.FilePlan) (apprecording.PartialFile, error) { return root.CreatePartial(plan) },
						LinkFinal:     root.LinkFinal, SyncDirectory: root.SyncDirectory, RemovePartial: root.RemovePartial}, Inspect: root.Inspect}}
				for range 2 {
					if err := recovery.Run(ctx); err != nil {
						t.Fatal(err)
					}
				}
				history, err := store.RecordingHistoryItem(ctx, reservation.Number)
				if err != nil || history == nil || history.Quality != q || history.Playable() != planned {
					t.Fatalf("history=%+v err=%v", history, err)
				}
				if planned && (history.Reason != reason || history.PlannedReason != reason || history.FinalizationToken == (catalogmodel.ID{})) {
					t.Fatalf("plan lost: %+v", history)
				}
				observation, err := root.Inspect(claim.Plan)
				if err != nil || planned && (!observation.Final.Exists || observation.Partial.Exists) || !planned && (observation.Final.Exists || !observation.Partial.Exists) {
					t.Fatalf("files=%+v err=%v", observation, err)
				}
				reconciler := apprecording.CompletedReconciler{Store: store, Clock: clock, Inspect: root.Inspect}
				result, _, err := reconciler.Run(ctx)
				if err != nil || result.Changed != 0 {
					t.Fatalf("reconcile=%+v err=%v", result, err)
				}
				if planned {
					items, err := store.RecoveryAttempts(ctx, core.MaxRecoveryPage, catalogmodel.ID{})
					if err != nil || len(items) != 1 || items[0].OneSeg == nil || items[0].OneSeg.Quality != aux || items[0].OneSeg.Availability != core.AvailabilityFinal {
						t.Fatalf("aux=%+v err=%v", items, err)
					}
				}
			})
		}
	}
}

func TestAtomicPublicationBeforeDatabaseRecordSurvivesRestart(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		for _, window := range []string{"main", "one-seg", "unsupported-main", "unsupported-one-seg"} {
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
				if window != "unsupported-main" {
					if err := root.LinkFinal(claim.Plan); err != nil {
						t.Fatal(err)
					}
				}
				if strings.HasSuffix(window, "one-seg") {
					if err := store.MarkFinalPublished(ctx, claim.AttemptID, now.Add(6*time.Second)); err != nil {
						t.Fatal(err)
					}
					if err := root.SyncDirectory(claim.Plan); err != nil {
						t.Fatal(err)
					}
					if err := store.MarkDirectorySynced(ctx, claim.AttemptID, now.Add(7*time.Second)); err != nil {
						t.Fatal(err)
					}
					if window == "one-seg" {
						if err := root.LinkFinal(onePlan); err != nil {
							t.Fatal(err)
						}
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
				if strings.HasPrefix(window, "unsupported-") {
					recovery.Files.LinkFinal = func(core.FilePlan) error { return errors.ErrUnsupported }
				}
				for range 2 {
					if err := recovery.Run(ctx); err != nil {
						t.Fatal(err)
					}
				}
				wantState, wantReason := core.AttemptSucceeded, core.ReasonCompleted
				if stopped {
					wantState, wantReason = core.AttemptPartial, core.ReasonUserRequestedStop
				}
				if window == "unsupported-main" {
					wantState, wantReason = core.AttemptFailed, core.ReasonFinalPublicationFailed
				}
				history, err := store.RecordingHistoryItem(ctx, created.Number)
				if err != nil || history == nil || history.Playable() != (window != "unsupported-main") || history.State != wantState || history.Reason != wantReason {
					t.Fatalf("history=%+v err=%v", history, err)
				}
				items, err := store.RecoveryAttempts(ctx, core.MaxRecoveryPage, catalogmodel.ID{})
				oneState, oneAvailability := core.SegmentFinalized, core.AvailabilityFinal
				if window == "unsupported-one-seg" {
					oneState, oneAvailability = core.SegmentPartial, core.AvailabilityPartial
				}
				if err != nil || (window == "unsupported-main" && len(items) != 0) ||
					(window != "unsupported-main" && (len(items) != 1 || !items[0].Recovered || items[0].OneSeg == nil ||
						items[0].OneSeg.State != oneState || items[0].OneSeg.Availability != oneAvailability)) {
					t.Fatalf("recovery=%+v err=%v", items, err)
				}
				for index, plan := range plans {
					if window == "unsupported-main" || window == "unsupported-one-seg" && index == 1 {
						got, err := os.ReadFile(filepath.Join(rootPath, plan.PartialPath))
						if err != nil || !bytes.Equal(got, contents[index]) {
							t.Fatalf("partial lost: segment=%d bytes=%d err=%v", index, len(got), err)
						}
						continue
					}
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
