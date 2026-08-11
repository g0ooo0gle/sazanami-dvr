//go:build unix

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	ctrlcmdruntime "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/runtime"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/recordingfs"
	sqliteadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/sqlite"
	autoreservationapp "github.com/g0ooo0gle/sazanami-dvr/internal/app/autoreservation"
	recordingapp "github.com/g0ooo0gle/sazanami-dvr/internal/app/recording"
	coreautoreservation "github.com/g0ooo0gle/sazanami-dvr/internal/core/autoreservation"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	providerstream "github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/stream"
	corerecording "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

const defaultSyntheticCycleCount = 10

type syntheticClock struct{ now time.Time }

func (clock *syntheticClock) Now() time.Time { return clock.now }

type syntheticIDs struct{ next uint64 }

func (source *syntheticIDs) New() (catalogmodel.ID, error) {
	source.next++
	var id catalogmodel.ID
	id[0] = 0x7f
	binary.BigEndian.PutUint64(id[8:], source.next)
	id[6] = 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return id, nil
}

func (source *syntheticIDs) Must() catalogmodel.ID {
	id, _ := source.New()
	return id
}

type syntheticResources struct {
	streamOpened, streamClosed, streamCancelled, activeStreams int
	fileOpened, fileClosed, activeFiles                        int
}

type syntheticStream struct {
	clock           *syntheticClock
	resources       *syntheticResources
	end             time.Time
	disconnectFirst bool
	onFirstRead     func() error
	stall           bool
	opened          int
	hooked          bool
}

func (stream *syntheticStream) OpenStream(context.Context, providerstream.Request) (providerstream.Lease, error) {
	connection := stream.opened
	stream.opened++
	stream.resources.streamOpened++
	stream.resources.activeStreams++
	return &syntheticLease{stream: stream, connection: connection}, nil
}

type syntheticLease struct {
	stream     *syntheticStream
	connection int
	closed     bool
	cancelled  bool
}

func (lease *syntheticLease) Read(ctx context.Context, destination []byte) (int, providerstream.Terminal, error) {
	if lease.stream.stall {
		<-ctx.Done()
		return 0, providerstream.Terminal{Done: true, Reason: providerstream.TerminalTimeout},
			provider.NewFailure(provider.ReasonTimeout, "synthetic-cycle")
	}
	copy(destination, bytes.Repeat([]byte{0x47}, 188))
	if !lease.stream.hooked {
		lease.stream.hooked = true
		if lease.stream.onFirstRead != nil {
			if err := lease.stream.onFirstRead(); err != nil {
				return 0, providerstream.Terminal{}, err
			}
		}
	}
	if lease.stream.disconnectFirst && lease.connection == 0 {
		return 188, providerstream.Terminal{Done: true, Reason: providerstream.TerminalEarlyEOF},
			provider.NewFailure(provider.ReasonEarlyEOF, "synthetic-cycle")
	}
	lease.stream.clock.now = lease.stream.end
	return 188, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
}

func (lease *syntheticLease) Cancel() error {
	if !lease.cancelled {
		lease.cancelled = true
		lease.stream.resources.streamCancelled++
	}
	return nil
}

func (lease *syntheticLease) Close() error {
	if !lease.closed {
		lease.closed = true
		lease.stream.resources.streamClosed++
		lease.stream.resources.activeStreams--
	}
	return nil
}

type syntheticPartial struct {
	recordingapp.PartialFile
	resources *syntheticResources
	noSpace   bool
	closed    bool
}

func (file *syntheticPartial) Write(data []byte) (int, error) {
	written, err := file.PartialFile.Write(data)
	if err == nil && file.noSpace {
		return written, syscall.ENOSPC
	}
	return written, err
}

func (file *syntheticPartial) Close() error {
	if file.closed {
		return nil
	}
	file.closed = true
	file.resources.fileClosed++
	file.resources.activeFiles--
	return file.PartialFile.Close()
}

type syntheticScenario uint8

const (
	syntheticNormal syntheticScenario = iota
	syntheticReconnect
	syntheticFollow
	syntheticNoSpace
	syntheticRestart
	syntheticStall
)

func syntheticScenarioAt(index int) syntheticScenario {
	switch {
	case index%100 == 0:
		return syntheticRestart
	case index%10 == 1:
		return syntheticNoSpace
	case index%10 == 2:
		return syntheticFollow
	case index%10 == 3:
		return syntheticReconnect
	case index%10 == 4:
		return syntheticStall
	default:
		return syntheticNormal
	}
}

// TestV09SyntheticRecordingCyclesは実時間を待たずに、応答停止を含む予約から録画後の復旧までを繰り返す。
func TestV09SyntheticRecordingCycles(t *testing.T) {
	ctx := context.Background()
	cycleCount := syntheticCycleCount(t)
	dataRoot := migratedRoot(t)
	recordingRootPath := filepath.Join(t.TempDir(), "recordings")
	store, err := sqliteadapter.OpenStore(ctx, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	recordingRoot, err := recordingfs.OpenRoot(recordingRootPath)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	defer func() {
		_ = recordingRoot.Close()
		_ = store.Close()
	}()

	ids := &syntheticIDs{}
	backendID := ids.Must()
	ownerID := ids.Must()
	base := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	if err := store.EnsureBackend(ctx, catalogmodel.Backend{
		ID: backendID, Kind: "MIRAKURUN", IdentityHash: sha256.Sum256([]byte("synthetic-v09-cycles")),
		ObservedAtMS: base.UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	channelMap := filepath.Join(dataRoot, "channels.json")
	document := fmt.Sprintf(`{"format":"sazanami-channel-map-v1","backend_id":%q,"services":[{"provider_locator":"1003","network_id":1,"service_id":3,"transport_stream_id":2,"provider_name":"","network_name":"合成ネットワーク","transport_stream_name":"合成TS","remote_control_key_id":1,"partial_reception":false,"epg_capture":true,"search":true}]}`, backendID.String())
	if err := os.WriteFile(channelMap, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAutomaticRule(ctx, coreautoreservation.Rule{
		ID: ids.Must(), Version: 1, Search: coreautoreservation.SearchCondition{Enabled: true},
		Recording: coreautoreservation.RecordingSettings{
			Mode: 1, Priority: 3, Follow: true, ServiceMode: 0x31,
		},
		CreatedAtUTCMS: base.UnixMilli(), UpdatedAtUTCMS: base.UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}

	clock := &syntheticClock{}
	resources := &syntheticResources{}
	catalogTime := base.UnixMilli()
	lastReservationNumber := int32(0)
	counts := make(map[syntheticScenario]int)
	shortenedEnds := make(map[int32]time.Time)
	baselineGoroutines := runtime.NumGoroutine()
	baselineFDs, fdCountAvailable := openFileDescriptorCount()
	for cycle := 0; cycle < cycleCount; cycle++ {
		scenario := syntheticScenarioAt(cycle)
		counts[scenario]++
		programStart := base.Add(time.Duration(cycle)*10*time.Minute + 5*time.Minute)
		_, err := seedSyntheticCycleCatalog(ctx, store, ids, backendID, cycle,
			programStart, 2*time.Minute, &catalogTime)
		if err != nil {
			t.Fatalf("cycle=%d catalog=%v", cycle+1, err)
		}
		snapshot, err := ctrlcmdruntime.BuildSnapshot(ctx, dataRoot, channelMap, store)
		if err != nil {
			t.Fatalf("cycle=%d snapshot=%v", cycle+1, err)
		}
		clock.now = programStart.Add(-5 * time.Minute)
		evaluator := autoreservationapp.Evaluator{
			Store: store, Catalog: snapshot, Clock: clock, NewID: ids.New,
			IsDuplicate: func(err error) bool { return errors.Is(err, sqliteadapter.ErrAutomaticReservationDuplicate) },
		}
		first, err := evaluator.Run(ctx)
		if err != nil || first.Created != 1 || first.Duplicates != 0 {
			t.Fatalf("cycle=%d first=%+v err=%v", cycle+1, first, err)
		}
		second, err := evaluator.Run(ctx)
		if err != nil || second.Created != 0 || second.Duplicates != 1 {
			t.Fatalf("cycle=%d second=%+v err=%v", cycle+1, second, err)
		}
		reservations, err := store.ActiveReservations(ctx, 1, lastReservationNumber)
		if err != nil || len(reservations) != 1 {
			t.Fatalf("cycle=%d reservations=%+v err=%v", cycle+1, reservations, err)
		}
		reservation := reservations[0]
		lastReservationNumber = reservation.Number
		clock.now = reservation.PlannedStart()
		stream := &syntheticStream{
			clock: clock, resources: resources, end: reservation.PlannedEnd(), stall: scenario == syntheticStall,
		}
		streamOpenedBefore, streamClosedBefore := resources.streamOpened, resources.streamClosed
		streamCancelledBefore := resources.streamCancelled
		fileOpenedBefore, fileClosedBefore := resources.fileOpened, resources.fileClosed
		noSpace := scenario == syntheticNoSpace
		if scenario == syntheticReconnect || scenario == syntheticFollow {
			stream.disconnectFirst = true
		}
		if scenario == syntheticFollow {
			targetStart := reservation.Program.Start.Add(30 * time.Second)
			targetRevisionID, seedErr := seedSyntheticCycleCatalog(ctx, store, ids, backendID, cycle,
				targetStart, time.Minute, &catalogTime)
			if seedErr != nil {
				t.Fatalf("cycle=%d follow catalog=%v", cycle+1, seedErr)
			}
			stream.end = targetStart.Add(time.Minute + corerecording.DefaultEndMargin)
			stream.onFirstRead = func() error {
				applied, followErr := store.ApplyReservationFollow(ctx, corerecording.ReservationFollowRequest{
					ReservationID: reservation.ID, ExpectedVersion: reservation.Version,
					ExpectedRevisionID: reservation.Program.ProgramRevisionID, TargetRevisionID: targetRevisionID,
					Now: clock.now.Add(time.Second),
				})
				if followErr != nil {
					return followErr
				}
				if !applied {
					return errors.New("follow was not applied")
				}
				active, readErr := store.ActiveReservations(ctx, 1, reservation.Number-1)
				if readErr != nil || len(active) != 1 || !active[0].Program.Start.Equal(targetStart) ||
					active[0].Program.Duration != time.Minute {
					return errors.New("followed reservation was not readable")
				}
				return nil
			}
			shortenedEnds[reservation.Number] = stream.end
		}
		files := syntheticFileOperations(recordingRoot, resources, noSpace)
		executor := recordingapp.Executor{
			Store: store, Stream: stream, Files: files, Clock: clock, NewID: ids.New,
			OwnerID: ownerID, Generation: 1,
			WithDeadline: func(parent context.Context, _ time.Time) (context.Context, context.CancelFunc) {
				if scenario == syntheticStall {
					return context.WithTimeout(parent, time.Millisecond)
				}
				return context.WithCancel(parent)
			},
			Wait: func(context.Context, time.Duration) error { return nil },
		}
		if scenario == syntheticRestart {
			attempt, claimErr := executor.Claim(ctx, reservation)
			if claimErr != nil {
				t.Fatalf("cycle=%d claim=%v", cycle+1, claimErr)
			}
			partial, createErr := trackedSyntheticPartial(recordingRoot, resources, attempt.Plan, false)
			if createErr != nil {
				t.Fatalf("cycle=%d partial=%v", cycle+1, createErr)
			}
			if written, writeErr := partial.Write(bytes.Repeat([]byte{0x47}, 188)); writeErr != nil || written != 188 {
				t.Fatalf("cycle=%d written=%d err=%v", cycle+1, written, writeErr)
			}
			if syncErr := partial.Sync(); syncErr != nil {
				t.Fatalf("cycle=%d sync=%v", cycle+1, syncErr)
			}
			if closeErr := partial.Close(); closeErr != nil {
				t.Fatalf("cycle=%d close=%v", cycle+1, closeErr)
			}
			if err := recordingRoot.Close(); err != nil {
				t.Fatalf("cycle=%d close recording root=%v", cycle+1, err)
			}
			if err := store.Close(); err != nil {
				t.Fatalf("cycle=%d close store=%v", cycle+1, err)
			}
			store, err = sqliteadapter.OpenStore(ctx, dataRoot)
			if err != nil {
				t.Fatalf("cycle=%d reopen store=%v", cycle+1, err)
			}
			recordingRoot, err = recordingfs.OpenRoot(recordingRootPath)
			if err != nil {
				t.Fatalf("cycle=%d reopen recording root=%v", cycle+1, err)
			}
			clock.now = reservation.Program.Start
			recovery := recordingapp.Recovery{
				Store: store, Clock: clock,
				Files: recordingapp.RecoveryFiles{
					FileOperations: syntheticFileOperations(recordingRoot, resources, false),
					Inspect:        recordingRoot.Inspect,
				},
			}
			if err := recovery.Run(ctx); err != nil {
				t.Fatalf("cycle=%d recovery=%v", cycle+1, err)
			}
		} else {
			result, executeErr := executor.Execute(ctx, reservation)
			if executeErr != nil {
				t.Fatalf("cycle=%d execute=%v", cycle+1, executeErr)
			}
			wantState, wantReason := corerecording.AttemptSucceeded, corerecording.ReasonCompleted
			if scenario == syntheticReconnect || scenario == syntheticFollow {
				wantReason = corerecording.ReasonCompletedAfterReconnect
			}
			if scenario == syntheticNoSpace {
				wantState, wantReason = corerecording.AttemptPartial, corerecording.ReasonFileWriteFailed
			}
			if scenario == syntheticStall {
				wantState, wantReason = corerecording.AttemptFailed, corerecording.ReasonStreamTimeout
			}
			if result.State != wantState || result.Reason != wantReason {
				t.Fatalf("cycle=%d result=%+v want=%s/%s", cycle+1, result, wantState, wantReason)
			}
		}
		item, err := store.RecordingHistoryItem(ctx, reservation.Number)
		if err != nil || item == nil {
			t.Fatalf("cycle=%d history=%+v err=%v", cycle+1, item, err)
		}
		if scenario == syntheticRestart && (item.State != corerecording.AttemptPartial ||
			item.Reason != corerecording.ReasonProcessInterrupted || item.ByteCount != 188) {
			t.Fatalf("cycle=%d recovered=%+v", cycle+1, item)
		}
		if scenario == syntheticStall && (item.State != corerecording.AttemptFailed ||
			item.Reason != corerecording.ReasonStreamTimeout || item.ByteCount != 0) {
			t.Fatalf("cycle=%d stalled=%+v", cycle+1, item)
		}
		if end, ok := shortenedEnds[item.Number]; ok && !item.PlannedEnd.Equal(end) {
			t.Fatalf("cycle=%d shortened end=%s want=%s", cycle+1, item.PlannedEnd, end)
		}
		if resources.activeStreams != 0 || resources.activeFiles != 0 {
			t.Fatalf("cycle=%d active streams=%d files=%d", cycle+1, resources.activeStreams, resources.activeFiles)
		}
		if scenario == syntheticStall && (stream.opened != 1 ||
			resources.streamOpened-streamOpenedBefore != 1 || resources.streamClosed-streamClosedBefore != 1 ||
			resources.streamCancelled-streamCancelledBefore != 1 || resources.fileOpened-fileOpenedBefore != 1 ||
			resources.fileClosed-fileClosedBefore != 1) {
			t.Fatalf("cycle=%d stalled opens=%d resources=%+v", cycle+1, stream.opened, resources)
		}
	}

	if active, err := store.ActiveReservations(ctx, corerecording.MaxPage, 0); err != nil || len(active) != 0 {
		t.Fatalf("active=%d err=%v", len(active), err)
	}
	historyCounts, err := inspectSyntheticHistory(ctx, store, recordingRoot, shortenedEnds)
	if err != nil {
		t.Fatal(err)
	}
	for scenario := syntheticNormal; scenario <= syntheticStall; scenario++ {
		if counts[scenario] == 0 {
			t.Fatalf("scenario=%d was not exercised", scenario)
		}
	}
	if historyCounts.total != cycleCount || historyCounts.final != counts[syntheticNormal]+counts[syntheticReconnect]+counts[syntheticFollow] ||
		historyCounts.partial != counts[syntheticNoSpace]+counts[syntheticRestart] || historyCounts.failed != counts[syntheticStall] ||
		historyCounts.shortened != counts[syntheticFollow] {
		t.Fatalf("history=%+v scenarios=%+v", historyCounts, counts)
	}
	if resources.streamOpened != resources.streamClosed || resources.streamOpened != resources.streamCancelled ||
		resources.fileOpened != resources.fileClosed || resources.activeStreams != 0 || resources.activeFiles != 0 {
		t.Fatalf("resources=%+v", resources)
	}
	fdDelta := 0
	if fdCountAvailable {
		afterFDs, available := openFileDescriptorCount()
		if !available || afterFDs > baselineFDs {
			t.Fatalf("fd before=%d after=%d available=%v", baselineFDs, afterFDs, available)
		}
		fdDelta = afterFDs - baselineFDs
	}
	if after := runtime.NumGoroutine(); after > baselineGoroutines+4 {
		t.Fatalf("goroutines before=%d after=%d", baselineGoroutines, after)
	}
	if err := recordingRoot.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("synthetic_cycles=%d final=%d partial=%d failed=%d stream_opened=%d file_opened=%d fd_delta=%d fd_counted=%v",
		cycleCount, historyCounts.final, historyCounts.partial, historyCounts.failed, resources.streamOpened,
		resources.fileOpened, fdDelta, fdCountAvailable)
}

func syntheticCycleCount(t *testing.T) int {
	t.Helper()
	value, configured := os.LookupEnv("SAZANAMI_SYNTHETIC_CYCLES")
	if !configured {
		return defaultSyntheticCycleCount
	}
	switch value {
	case "10":
		return 10
	case "100":
		return 100
	case "1000":
		return 1_000
	default:
		t.Fatalf("SAZANAMI_SYNTHETIC_CYCLESは10、100、1000のいずれかで指定してください: %q", value)
		return 0
	}
}

func seedSyntheticCycleCatalog(ctx context.Context, store *sqliteadapter.Store, ids *syntheticIDs,
	backendID catalogmodel.ID, cycle int, start time.Time, duration time.Duration, catalogTime *int64,
) (catalogmodel.ID, error) {
	syncID := ids.Must()
	*catalogTime++
	if err := store.BeginSync(ctx, catalogmodel.Sync{
		ID: syncID, BackendID: backendID, StartedAtMS: *catalogTime,
		CorrelationID: fmt.Sprintf("synthetic-cycle-%04d", cycle+1),
	}); err != nil {
		return catalogmodel.ID{}, err
	}
	networkID, transportID, serviceID := int64(1), int64(2), int64(3)
	tuningTarget := "1003"
	serviceType := "1"
	if err := store.StoreServices(ctx, syncID, []catalogmodel.ServiceObservation{{
		ProviderLocator: tuningTarget, NetworkID: &networkID, TransportID: &transportID, ServiceID: &serviceID,
		BroadcastKind: &serviceType, DisplayName: "合成サービス", TuningTarget: &tuningTarget,
		Validation: catalogmodel.ValidationValid,
	}}); err != nil {
		return catalogmodel.ID{}, err
	}
	startMS, durationMS, eventID := start.UnixMilli(), duration.Milliseconds(), int64(cycle+1)
	title := fmt.Sprintf("合成番組%04d", cycle+1)
	eventLocator := fmt.Sprintf("%04d", cycle+1)
	if err := store.StorePrograms(ctx, syncID, false, []catalogmodel.ProgramObservation{{
		ServiceLocator: tuningTarget, EventLocator: eventLocator, RawEventID: &eventID,
		Material: catalogmodel.RevisionMaterial{StartUTCMS: &startMS, DurationMS: &durationMS,
			Title: &title, Validation: catalogmodel.ValidationValid},
	}}); err != nil {
		return catalogmodel.ID{}, err
	}
	*catalogTime++
	if err := store.CompleteSync(ctx, syncID, *catalogTime, 1, 1); err != nil {
		return catalogmodel.ID{}, err
	}
	programs, err := store.ProgramsByServiceForGeneration(ctx, backendID, syncID, 1, catalogmodel.ProgramCursor{})
	if err != nil {
		return catalogmodel.ID{}, fmt.Errorf("read seeded program: %w", err)
	}
	if len(programs) != 1 {
		return catalogmodel.ID{}, fmt.Errorf("read seeded program: count=%d", len(programs))
	}
	return programs[0].RevisionID, nil
}

func syntheticFileOperations(root *recordingfs.Root, resources *syntheticResources,
	noSpace bool,
) recordingapp.FileOperations {
	return recordingapp.FileOperations{
		CreatePartial: func(plan corerecording.FilePlan) (recordingapp.PartialFile, error) {
			return trackedSyntheticPartial(root, resources, plan, noSpace)
		},
		LinkFinal: root.LinkFinal, SyncDirectory: root.SyncDirectory, RemovePartial: root.RemovePartial,
	}
}

func trackedSyntheticPartial(root *recordingfs.Root, resources *syntheticResources,
	plan corerecording.FilePlan, noSpace bool,
) (*syntheticPartial, error) {
	partial, err := root.CreatePartial(plan)
	if err != nil {
		return nil, err
	}
	resources.fileOpened++
	resources.activeFiles++
	return &syntheticPartial{PartialFile: partial, resources: resources, noSpace: noSpace}, nil
}

type syntheticHistoryCounts struct{ total, final, partial, failed, shortened int }

func inspectSyntheticHistory(ctx context.Context, store *sqliteadapter.Store, root *recordingfs.Root,
	shortenedEnds map[int32]time.Time,
) (syntheticHistoryCounts, error) {
	var counts syntheticHistoryCounts
	var before int32
	for {
		page, err := store.RecordingHistory(ctx, corerecording.MaxHistoryPage, before)
		if err != nil {
			return counts, err
		}
		for _, item := range page {
			counts.total++
			observation, err := root.Inspect(item.Plan)
			if err != nil {
				return counts, err
			}
			switch item.Reason {
			case corerecording.ReasonCompleted, corerecording.ReasonCompletedAfterReconnect:
				counts.final++
				if !item.Playable() || observation.Partial.Exists || !observation.Final.Regular ||
					observation.Final.Size != item.ByteCount {
					return counts, fmt.Errorf("invalid final history number=%d", item.Number)
				}
			case corerecording.ReasonFileWriteFailed, corerecording.ReasonProcessInterrupted:
				counts.partial++
				if item.State != corerecording.AttemptPartial || item.ByteCount != 188 ||
					!observation.Partial.Regular || observation.Partial.Size != 188 || observation.Final.Exists {
					return counts, fmt.Errorf("invalid partial history number=%d", item.Number)
				}
			case corerecording.ReasonStreamTimeout:
				counts.failed++
				if item.State != corerecording.AttemptFailed || item.ByteCount != 0 ||
					!observation.Partial.Regular || observation.Partial.Size != 0 || observation.Final.Exists {
					return counts, fmt.Errorf("invalid stalled history number=%d", item.Number)
				}
			default:
				return counts, fmt.Errorf("unexpected terminal reason number=%d reason=%s", item.Number, item.Reason)
			}
			if end, ok := shortenedEnds[item.Number]; ok {
				counts.shortened++
				if !item.PlannedEnd.Equal(end) {
					return counts, fmt.Errorf("shortened end mismatch number=%d", item.Number)
				}
			}
		}
		if len(page) < corerecording.MaxHistoryPage {
			return counts, nil
		}
		before = page[len(page)-1].Number
	}
}
