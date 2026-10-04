package recording

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	stream "github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/stream"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

func TestQualityFinishPreservesTermination(t *testing.T) {
	for _, test := range []struct {
		name      string
		terminal  stream.TerminalReason
		want      core.TerminalReason
		published bool
	}{
		{"EOF", stream.TerminalEarlyEOF, core.ReasonStreamEndedEarly, true},
		{"timeout", stream.TerminalTimeout, core.ReasonStreamTimeout, true},
		{"peer", stream.TerminalPeer, core.ReasonStreamUnavailable, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
			clock := &mutableClock{now: start}
			store := &attemptMemory{start: start, end: start.Add(59 * time.Second)}
			lease := &fakeLease{read: func(destination []byte) (int, stream.Terminal, error) {
				copy(destination, makePayloadPacket(0x101))
				destination[188] = 0x47
				clock.now = start.Add(time.Second)
				return 189, stream.Terminal{Done: true, Reason: test.terminal}, nil
			}}
			executor := executorForTest(t, store, &fakeProvider{lease: lease}, clock, false)
			post := 0
			executor.PostRecording = func(context.Context, PostRecordingRequest) string { post++; return "" }
			reservation := reservationForExecutor(t, start, 59*time.Second)
			reservation.PostRecording = core.PostRecordingSettings{Mode: core.PostRecordingStandby, Script: "/allowed/test.sh"}
			result, err := executor.Execute(context.Background(), reservation)
			if err != nil || result.Reason != test.want || store.finish.ByteCount != 188 ||
				(store.finish.Availability == core.AvailabilityFinal) != test.published ||
				store.finish.Quality.Status != core.QualityDegraded || store.finish.Quality.TrailingIncompleteBytes != 1 ||
				post != 0 || result.PostRecording.ChangesPower() {
				t.Fatalf("result=%+v finish=%+v post=%d err=%v", result, store.finish, post, err)
			}
		})
	}
}

func TestQualityParentCancelAtEndDoesNotPublish(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
		clock := &mutableClock{now: start}
		store := &attemptMemory{start: start, end: start.Add(time.Minute)}
		ctx, cancel := context.WithCancel(context.Background())
		var deadlineCancel context.CancelFunc
		if deadline {
			ctx, deadlineCancel = context.WithDeadline(context.Background(), time.Now().Add(100*time.Millisecond))
			defer deadlineCancel()
		}
		lease := &fakeLease{read: func(destination []byte) (int, stream.Terminal, error) {
			copy(destination, makePayloadPacket(0x101))
			clock.now = store.end
			if deadline {
				<-ctx.Done()
			} else {
				cancel()
			}
			return 188, stream.Terminal{Done: true, Reason: stream.TerminalCancelled}, context.Canceled
		}}
		executor := executorForTest(t, store, &fakeProvider{lease: lease}, clock, false)
		result, err := executor.Execute(ctx, reservationForExecutor(t, start, time.Minute))
		cancel()
		if err != nil || result.Reason != core.ReasonProcessShutdown || store.finalizeCall != 0 ||
			store.finish.Availability == core.AvailabilityFinal || countString(store.operations, "link") != 0 {
			t.Fatalf("deadline=%t result=%+v finish=%+v finalize=%d err=%v", deadline, result, store.finish, store.finalizeCall, err)
		}
	}
}

func TestQualityExplicitStopCASOrder(t *testing.T) {
	for _, stopFirst := range []bool{false, true} {
		start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
		clock := &mutableClock{now: start}
		store := &attemptMemory{start: start, end: start.Add(time.Minute)}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		lease := &fakeLease{read: func(destination []byte) (int, stream.Terminal, error) {
			copy(destination, makePayloadPacket(0x101))
			clock.now = store.end
			if stopFirst {
				store.stop.Store(true)
				cancel()
			}
			return 188, stream.Terminal{Reason: stream.TerminalActive}, nil
		}}
		store.finalizeFunc = func(request core.FinalizeRequest) (core.FinalizeRequest, error) {
			if !stopFirst {
				store.stop.Store(true)
				cancel()
			}
			return request, nil
		}
		executor := executorForTest(t, store, &fakeProvider{lease: lease}, clock, false)
		result, err := executor.Execute(ctx, reservationForExecutor(t, start, time.Minute))
		want := core.ReasonCompleted
		if stopFirst {
			want = core.ReasonUserRequestedStop
		}
		if err != nil || result.Reason != want || store.finish.Availability != core.AvailabilityFinal {
			t.Fatalf("stopFirst=%t result=%+v finish=%+v err=%v", stopFirst, result, store.finish, err)
		}
	}
}

func TestQualityReconnectAndFallbackState(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(2 * time.Minute)}
	failure := provider.NewFailure(provider.ReasonTimeout, "private endpoint")
	provider := &fakeProvider{errors: []error{failure, failure, failure, failure}}
	executor := executorForTest(t, store, provider, clock, false)
	result, err := executor.Execute(context.Background(), reservationForExecutor(t, start, 2*time.Minute))
	if err != nil || result.Reason != core.ReasonStreamReconnectExhausted || provider.opens != 4 ||
		store.finish.Quality.ReconnectCount != 3 || store.finish.Quality.Status != core.QualityDegraded {
		t.Fatalf("result=%+v quality=%+v opens=%d err=%v", result, store.finish.Quality, provider.opens, err)
	}
}

func TestQualityCancelBeforeFinalizationDoesNotBecomeUserStop(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	store := &attemptMemory{start: start, end: start.Add(time.Minute)}
	executor := executorForTest(t, store, &fakeProvider{}, &mutableClock{now: start}, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := executor.publishFinal(ctx, core.Attempt{ID: appID(t, 20)}, 188,
		core.AttemptSucceeded, core.ReasonCompleted, core.QualitySummary{})
	if err != nil || result.Reason != core.ReasonProcessShutdown || store.finalizeCall != 0 ||
		!errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("result=%+v finalize=%d err=%v", result, store.finalizeCall, err)
	}
}

func TestQualityCancellationDuringFinalizationWaitResolvesDatabaseStop(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "process-cancel", true: "database-stop"}[stop], func(t *testing.T) {
			start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
			store := &attemptMemory{start: start, end: start.Add(time.Minute)}
			executor := executorForTest(t, store, &fakeProvider{}, &mutableClock{now: start}, false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store.finalizeFunc = func(request core.FinalizeRequest) (core.FinalizeRequest, error) {
				if store.finalizeCall == 1 {
					store.stop.Store(stop)
					cancel()
					return core.FinalizeRequest{}, core.ErrFinalizationUnavailable
				}
				return request, nil
			}
			quality := core.QualitySummary{Status: core.QualityDegraded, TEIPackets: 1}
			result, err := executor.publishFinal(ctx, core.Attempt{ID: appID(t, 20)}, 188,
				core.AttemptSucceeded, core.ReasonCompleted, quality)
			wantState, wantReason, wantCalls, wantLinks := core.AttemptCancelled, core.ReasonProcessShutdown, 1, 0
			if stop {
				wantState, wantReason, wantCalls, wantLinks = core.AttemptPartial, core.ReasonUserRequestedStop, 2, 1
			}
			if err != nil || result.State != wantState || result.Reason != wantReason ||
				store.finish.Quality != quality || store.finalizeCall != wantCalls || countString(store.operations, "link") != wantLinks {
				t.Fatalf("result=%+v finish=%+v calls=%d links=%d err=%v", result, store.finish,
					store.finalizeCall, countString(store.operations, "link"), err)
			}
		})
	}
}

func TestQualityObservationRateBound(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(120 * time.Second)}
	reads := 0
	lease := &fakeLease{read: func(destination []byte) (int, stream.Terminal, error) {
		reads++
		packet := makePayloadPacket(0x101)
		packet[1] |= 0x80
		copy(destination, packet)
		clock.now = start.Add(time.Duration(reads) * time.Second)
		return 188, stream.Terminal{Reason: stream.TerminalActive}, nil
	}}
	executor := executorForTest(t, store, &fakeProvider{lease: lease}, clock, false)
	counts := map[string]int{}
	var lastWarning time.Time
	executor.ObserveQuality = func(event QualityObservation) {
		counts[event.Phase]++
		if event.Ordinal != 0 {
			t.Fatalf("ordinal=%d", event.Ordinal)
		}
		if event.Phase == "continue" || event.Phase == "fallback" || event.Phase == "reconnect" {
			if !lastWarning.IsZero() && clock.now.Sub(lastWarning) < 30*time.Second {
				t.Fatal("warning burst")
			}
			lastWarning = clock.now
		}
	}
	if _, err := executor.Execute(context.Background(), reservationForExecutor(t, start, 120*time.Second)); err != nil {
		t.Fatal(err)
	}
	if counts["start"] != 1 || counts["final"] != 1 || counts["connection-end"] != 1 ||
		counts["continue"] < 1 || counts["continue"] > 4 || reads != 120 {
		t.Fatalf("counts=%v reads=%d", counts, reads)
	}
}

func TestQualityOneSegIsolation(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(time.Minute)}
	executor := executorForTest(t, store, &fakeProvider{}, clock, false)
	attempt := core.Attempt{ID: appID(t, 20), PlannedStart: start}
	inputs := [][]byte{bytes.Repeat(makePayloadPacket(0x101), maxPSIBuffer/188+2),
		testTransportStream(t, []testStream{{pid: 0x101, typeValue: 0x1b}})}
	var qualities [2]core.QualitySummary
	for ordinal, input := range inputs {
		clock.now = start
		offset := 0
		lease := &fakeLease{read: func(destination []byte) (int, stream.Terminal, error) {
			n := copy(destination, input[offset:])
			offset += n
			if offset == len(input) {
				clock.now = store.end
			}
			return n, stream.Terminal{Reason: stream.TerminalActive}, nil
		}}
		output := &tsBufferFile{}
		result := executor.copy(context.Background(), context.Background(), lease, output, attempt, core.ComponentNeither,
			streamCopyResult{LastProgress: start, PlannedEnd: store.end, MaximumEnd: store.end}, ordinal == 1)
		qualities[ordinal] = result.Quality
		if !result.ReachedEnd || result.ByteCount != int64(output.Len()) || result.ByteCount%188 != 0 {
			t.Fatalf("ordinal=%d result=%+v", ordinal, result)
		}
	}
	if !qualities[0].SelectionUnverified || qualities[0].FallbackEvents != 1 || qualities[1].SelectionUnverified ||
		qualities[1].FallbackEvents != 0 || qualities[1].Status != core.QualityNoIssuesObserved {
		t.Fatalf("main=%+v auxiliary=%+v", qualities[0], qualities[1])
	}
}

func TestQualityOneSegCommunicationPublication(t *testing.T) {
	for _, reason := range []core.TerminalReason{core.ReasonStreamEndedEarly, core.ReasonStreamTimeout, core.ReasonStreamReconnectExhausted} {
		done := make(chan core.OneSegResult, 1)
		coordinator := &oneSegCoordinator{done: done, started: true, cancel: func() {
			done <- core.OneSegResult{ByteCount: 188, FileSynced: true, Reason: core.ReasonStreamCancelled,
				Availability: core.AvailabilityPartial, Quality: core.QualitySummary{Status: core.QualityNoIssuesObserved}}
		}}
		result := coordinator.join(reason, true)
		if !result.Publish || result.Reason != reason || result.Quality.Status != core.QualityDegraded {
			t.Fatalf("reason=%s result=%+v", reason, result)
		}
		store := &attemptMemory{}
		wrapper := &oneSegAttemptStore{AttemptStore: store, coordinator: coordinator}
		if err := wrapper.FinishAttempt(context.Background(), core.FinishRequest{State: core.AttemptPartial,
			Reason: reason, Availability: core.AvailabilityFinal}); err != nil {
			t.Fatal(err)
		}
		if store.finish.OneSeg != nil || !coordinator.result.Publish {
			t.Fatalf("communication finish cancelled auxiliary: %+v", store.finish)
		}
	}
}

func TestQualityOneSegFailedReconnectCounts(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	store := &attemptMemory{start: start, end: start.Add(2 * time.Minute)}
	failure := provider.NewFailure(provider.ReasonTimeout, "test")
	provider := &fakeProvider{errors: []error{failure, failure, failure, failure}}
	executor := executorForTest(t, store, provider, &mutableClock{now: start}, false)
	plan := core.FilePlan{PartialPath: "aux.part", FinalPath: "aux.ts"}
	reservation := reservationForExecutor(t, start, 2*time.Minute)
	reservation.OneSegOutput = &core.OneSegOutput{ProviderServiceLocator: "1004"}
	result := executor.runOneSeg(context.Background(), context.Background(), reservation,
		core.Attempt{ID: appID(t, 20), PlannedStart: start, OneSegPlan: &plan}, store.end, store.end)
	if result.Reason != core.ReasonStreamReconnectExhausted || result.Quality.ReconnectCount != 3 || result.Publish {
		t.Fatalf("result=%+v", result)
	}
}

func TestQualityReconnectPreservesFallback(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(2 * time.Minute)}
	first := bytes.Repeat(makePayloadPacket(0x101), maxPSIBuffer/188+2)
	second := testTransportStream(t, []testStream{{pid: 0x101, typeValue: 0x1b}, {pid: 0x110, typeValue: 0x0d}})
	offset := 0
	leases := []stream.Lease{&fakeLease{read: func(destination []byte) (int, stream.Terminal, error) {
		n := copy(destination, first[offset:])
		offset += n
		clock.now = clock.now.Add(time.Second)
		return n, stream.Terminal{Done: offset == len(first), Reason: stream.TerminalEarlyEOF}, nil
	}}, &fakeLease{read: func(destination []byte) (int, stream.Terminal, error) {
		clock.now = store.end
		return copy(destination, second), stream.Terminal{Reason: stream.TerminalActive}, nil
	}}}
	executor := executorForTest(t, store, &fakeProvider{leases: leases}, clock, false)
	output := &tsBufferFile{}
	executor.Files.CreatePartial = func(core.FilePlan) (PartialFile, error) { return output, nil }
	reservation := reservationForExecutor(t, start, 2*time.Minute)
	reservation.Components = core.ComponentNeither
	result, err := executor.Execute(context.Background(), reservation)
	if err != nil || result.Reason != core.ReasonCompletedAfterReconnect ||
		!bytes.Equal(output.Bytes(), append(first, second...)) || store.finish.Quality.FallbackEvents != 1 ||
		store.finish.Quality.ReconnectCount != 1 || !store.finish.Quality.SelectionUnverified {
		t.Fatalf("result=%+v quality=%+v bytes=%d err=%v", result, store.finish.Quality, output.Len(), err)
	}
}

func TestQualityStopDuringRetryDelayDoesNotReopen(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(2 * time.Minute)}
	lease := &fakeLease{read: func(destination []byte) (int, stream.Terminal, error) {
		clock.now = start.Add(time.Second)
		return copy(destination, makePayloadPacket(0x101)), stream.Terminal{Done: true, Reason: stream.TerminalEarlyEOF}, nil
	}}
	provider := &fakeProvider{lease: lease}
	executor := executorForTest(t, store, provider, clock, false)
	executor.Wait = func(context.Context, time.Duration) error { store.stop.Store(true); return nil }
	result, err := executor.Execute(context.Background(), reservationForExecutor(t, start, 2*time.Minute))
	if err != nil || provider.opens != 1 || result.Reason != core.ReasonUserRequestedStop {
		t.Fatalf("opens=%d result=%+v err=%v", provider.opens, result, err)
	}
}

func TestQualityCancellationDuringTokenGenerationDoesNotPublish(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	store := &attemptMemory{start: start, end: start.Add(time.Minute)}
	executor := executorForTest(t, store, &fakeProvider{}, &mutableClock{now: start}, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	executor.NewID = func() (catalogmodel.ID, error) { cancel(); return appID(t, 22), nil }
	result, err := executor.publishFinal(ctx, core.Attempt{ID: appID(t, 20)}, 188, core.AttemptSucceeded, core.ReasonCompleted, core.QualitySummary{})
	if err != nil || result.Reason != core.ReasonProcessShutdown || store.finalizeCall != 0 {
		t.Fatalf("result=%+v calls=%d err=%v", result, store.finalizeCall, err)
	}
}

func TestQualityOneSegJoinCancellationBeforePlanDoesNotPublish(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan core.OneSegResult, 1)
	store := &attemptMemory{}
	coordinator := &oneSegCoordinator{started: true, done: done, cancel: func() {
		cancel()
		done <- core.OneSegResult{ByteCount: 188, FileSynced: true, Reason: core.ReasonStreamCancelled, Availability: core.AvailabilityPartial}
	}}
	wrapper := &oneSegAttemptStore{AttemptStore: store, coordinator: coordinator}
	_, err := wrapper.BeginFinalization(ctx, core.FinalizeRequest{State: core.AttemptSucceeded, Reason: core.ReasonCompleted})
	if !errors.Is(err, core.ErrFinalizationUnavailable) || store.finalizeCall != 0 {
		t.Fatalf("calls=%d err=%v", store.finalizeCall, err)
	}
}

func TestQualityEndDuringRetryDelayFinishesWithoutNewRead(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(2 * time.Minute)}
	reads := 0
	lease := &fakeLease{read: func(destination []byte) (int, stream.Terminal, error) {
		reads++
		clock.now = start.Add(time.Second)
		return copy(destination, makePayloadPacket(0x101)), stream.Terminal{Done: true, Reason: stream.TerminalEarlyEOF}, nil
	}}
	provider := &fakeProvider{lease: lease}
	executor := executorForTest(t, store, provider, clock, false)
	executor.Wait = func(context.Context, time.Duration) error { clock.now = store.end; return context.DeadlineExceeded }
	result, err := executor.Execute(context.Background(), reservationForExecutor(t, start, 2*time.Minute))
	if err != nil || reads != 1 || provider.opens != 1 || result.Reason != core.ReasonCompleted || store.finish.Availability != core.AvailabilityFinal {
		t.Fatalf("result=%+v reads=%d opens=%d err=%v", result, reads, provider.opens, err)
	}
}

func TestQualityOneSegOwnedDeadlineAtEndCompletes(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(2 * time.Minute)}
	lease := &fakeLease{read: func(destination []byte) (int, stream.Terminal, error) {
		return copy(destination, makePayloadPacket(0x101)), stream.Terminal{Done: true, Reason: stream.TerminalEarlyEOF}, nil
	}}
	provider := &fakeProvider{lease: lease}
	executor := executorForTest(t, store, provider, clock, false)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(100*time.Millisecond))
	defer cancel()
	executor.Wait = func(context.Context, time.Duration) error { <-ctx.Done(); clock.now = store.end; return nil }
	plan := core.FilePlan{PartialPath: "aux.part", FinalPath: "aux.ts"}
	reservation := reservationForExecutor(t, start, 2*time.Minute)
	reservation.OneSegOutput = &core.OneSegOutput{ProviderServiceLocator: "1004"}
	result := executor.runOneSeg(context.Background(), ctx, reservation,
		core.Attempt{ID: appID(t, 20), PlannedStart: start, OneSegPlan: &plan}, store.end, store.end)
	if provider.opens != 1 || result.Reason != core.ReasonCompleted || !result.Publish {
		t.Fatalf("opens=%d result=%+v", provider.opens, result)
	}
}

func TestQualityCommunicationFailureDoesNotPublish(t *testing.T) {
	for _, boundary := range []string{"sync", "close", "progress", "finalize", "link", "directory", "published", "directory-recorded", "collision"} {
		t.Run(boundary, func(t *testing.T) {
			start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
			clock := &mutableClock{now: start}
			store := &attemptMemory{start: start, end: start.Add(59 * time.Second)}
			lease := &fakeLease{read: func(destination []byte) (int, stream.Terminal, error) {
				clock.now = start.Add(time.Second)
				return copy(destination, makePayloadPacket(0x101)), stream.Terminal{Done: true, Reason: stream.TerminalEarlyEOF}, nil
			}}
			executor := executorForTest(t, store, &fakeProvider{lease: lease}, clock, false)
			failure := errors.New("private failure")
			file := &fakePartial{operations: &store.operations}
			executor.Files.CreatePartial = func(core.FilePlan) (PartialFile, error) { return file, nil }
			switch boundary {
			case "sync":
				file.syncErr = failure
			case "close":
				file.closeErr = failure
			case "progress":
				store.progressErr = failure
			case "finalize":
				store.finalizeErr = failure
			case "link":
				executor.Files.LinkFinal = func(core.FilePlan) error { return failure }
			case "directory":
				executor.Files.SyncDirectory = func(core.FilePlan) error { return failure }
			case "published":
				store.publishErr = failure
			case "directory-recorded":
				store.directoryErr = failure
			case "collision":
				executor.Files.LinkFinal = func(core.FilePlan) error { return core.ErrFinalExists }
			}
			result, err := executor.Execute(context.Background(), reservationForExecutor(t, start, 59*time.Second))
			if store.finish.Availability == core.AvailabilityFinal || result.State == core.AttemptSucceeded || result.PostRecording.ChangesPower() {
				t.Fatalf("boundary=%s result=%+v finish=%+v err=%v", boundary, result, store.finish, err)
			}
			if boundary == "sync" || boundary == "close" {
				if err != nil || result.Reason != core.ReasonFileSyncFailed {
					t.Fatalf("reason=%s err=%v", result.Reason, err)
				}
			} else if err == nil {
				t.Fatal("failure was hidden")
			}
		})
	}
}

func TestQualityReconnectWindowRecheckedAfterDelay(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(61500 * time.Millisecond)}
	lease := &fakeLease{read: func(destination []byte) (int, stream.Terminal, error) {
		clock.now = start.Add(time.Second)
		return copy(destination, makePayloadPacket(0x101)), stream.Terminal{Done: true, Reason: stream.TerminalEarlyEOF}, nil
	}}
	provider := &fakeProvider{lease: lease}
	executor := executorForTest(t, store, provider, clock, false)
	executor.Wait = func(_ context.Context, delay time.Duration) error { clock.now = clock.now.Add(delay); return nil }
	result, err := executor.Execute(context.Background(), reservationForExecutor(t, start, 61500*time.Millisecond))
	if err != nil || provider.opens != 1 || store.finish.Quality.ReconnectCount != 0 || result.Reason != core.ReasonStreamEndedEarly {
		t.Fatalf("opens=%d result=%+v quality=%+v err=%v", provider.opens, result, store.finish.Quality, err)
	}
}

type qualityStopHook struct {
	AttemptStore
	afterRead func()
}

func (store qualityStopHook) AttemptStopRequested(ctx context.Context, id catalogmodel.ID) (bool, error) {
	stop, err := store.AttemptStore.AttemptStopRequested(ctx, id)
	store.afterRead()
	return stop, err
}

func TestQualityCancellationBeforeFinishDoesNotFlushSelectionBuffer(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(time.Second)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lease := &fakeLease{read: func(destination []byte) (int, stream.Terminal, error) {
		clock.now = store.end
		return copy(destination, makePayloadPacket(0x101)), stream.Terminal{Reason: stream.TerminalActive}, nil
	}}
	executor := executorForTest(t, store, &fakeProvider{lease: lease}, clock, false)
	executor.Store = qualityStopHook{AttemptStore: store, afterRead: func() {
		if clock.now.Equal(store.end) {
			cancel()
		}
	}}
	reservation := reservationForExecutor(t, start, time.Second)
	reservation.Components = core.ComponentNeither
	result, err := executor.Execute(ctx, reservation)
	if err != nil || result.Reason != core.ReasonProcessShutdown || store.finish.ByteCount != 0 || countString(store.operations, "write") != 0 {
		t.Fatalf("result=%+v finish=%+v operations=%v err=%v", result, store.finish, store.operations, err)
	}
}

func TestQualityOneSegOwnedDeadlineBeforeEndRemainsTimeout(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(59 * time.Second)}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(100*time.Millisecond))
	defer cancel()
	lease := &fakeLease{read: func(destination []byte) (int, stream.Terminal, error) {
		<-ctx.Done()
		clock.now = start.Add(time.Second)
		return copy(destination, makePayloadPacket(0x101)), stream.Terminal{Done: true, Reason: stream.TerminalTimeout}, ctx.Err()
	}}
	executor := executorForTest(t, store, &fakeProvider{lease: lease}, clock, false)
	plan := core.FilePlan{PartialPath: "aux.part", FinalPath: "aux.ts"}
	reservation := reservationForExecutor(t, start, 59*time.Second)
	reservation.OneSegOutput = &core.OneSegOutput{ProviderServiceLocator: "1004"}
	result := executor.runOneSeg(context.Background(), ctx, reservation,
		core.Attempt{ID: appID(t, 20), PlannedStart: start, OneSegPlan: &plan}, store.end, store.end)
	if result.Reason != core.ReasonStreamTimeout || !result.Publish {
		t.Fatalf("result=%+v", result)
	}
}
