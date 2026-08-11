package recordinghttp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHLSSessionSelectionExpiresWithoutOpeningProvider(t *testing.T) {
	clock := newManualHLSSessionClock()
	live := newFakeHLSSessionLive()
	controller := testHLSSessionController(t, context.Background(), live, clock)
	request := testHLSTvCastRequest(0, 1)
	if rejection := controller.selectLive(context.Background(), request); rejection != nil {
		t.Fatalf("select rejection=%+v", rejection)
	}
	if selected, opened, closed := live.counts(); selected != 1 || opened != 0 || closed != 0 {
		t.Fatalf("selected=%d opened=%d closed=%d", selected, opened, closed)
	}
	clock.advance(hlsSelectionLifetime - time.Nanosecond)
	if _, _, closed := live.counts(); closed != 0 {
		t.Fatalf("closed before boundary=%d", closed)
	}
	clock.advance(time.Nanosecond)
	if _, opened, closed := live.counts(); opened != 0 || closed != 1 {
		t.Fatalf("opened=%d closed at boundary=%d", opened, closed)
	}
	rejection := controller.startLive(testHLSViewRequest(0, 1, "expired", hlsViewStart))
	assertHLSSessionRejection(t, rejection, 409, "selection-required")
}

func TestHLSSessionSelectionExpiryFinishesBeforeNextSelection(t *testing.T) {
	clock := newManualHLSSessionClock()
	live := newFakeHLSSessionLive()
	closeStarted := make(chan struct{})
	closeRelease := make(chan struct{})
	live.closeStarted = closeStarted
	live.closeRelease = closeRelease
	controller := testHLSSessionController(t, context.Background(), live, clock)
	if rejection := controller.selectLive(context.Background(), testHLSTvCastRequest(0, 1)); rejection != nil {
		t.Fatal(rejection)
	}
	advanced := make(chan struct{})
	go func() {
		clock.advance(hlsSelectionLifetime)
		close(advanced)
	}()
	waitHLSSignal(t, closeStarted)
	selected := make(chan *hlsHTTPRejection, 1)
	go func() {
		selected <- controller.selectLive(context.Background(), testHLSTvCastRequest(0, 2))
	}()
	select {
	case rejection := <-selected:
		t.Fatalf("selection completed before previous Close: %+v", rejection)
	case <-time.After(20 * time.Millisecond):
	}
	if count, _, _ := live.counts(); count != 1 {
		t.Fatalf("selected during previous Close=%d", count)
	}
	close(closeRelease)
	waitHLSSignal(t, advanced)
	if rejection := waitHLSRejection(t, selected); rejection != nil {
		t.Fatal(rejection)
	}
	if count, _, closed := live.counts(); count != 2 || closed != 1 {
		t.Fatalf("selected=%d closed=%d", count, closed)
	}
}

func TestHLSSessionStartIsIdempotentAndReplacementIsSlotLocal(t *testing.T) {
	clock := newManualHLSSessionClock()
	live := newFakeHLSSessionLive()
	mainStream := newFakeHLSSessionStream(testHLSSessionTS(t, 4), nil, true)
	dualStream := newFakeHLSSessionStream(testHLSSessionTS(t, 4), nil, true)
	live.queue(mainStream, dualStream)
	controller := testHLSSessionController(t, context.Background(), live, clock)

	requestContext, cancelRequest := context.WithCancel(context.Background())
	if rejection := controller.selectLive(requestContext, testHLSTvCastRequest(0, 1)); rejection != nil {
		t.Fatal(rejection)
	}
	cancelRequest()
	start := testHLSViewRequest(0, 1, "main-key", hlsViewStart)
	if rejection := controller.startLive(start); rejection != nil {
		t.Fatal(rejection)
	}
	if rejection := controller.startLive(start); rejection != nil {
		t.Fatalf("idempotent rejection=%+v", rejection)
	}
	waitHLSSignal(t, mainStream.started)
	if _, opened, _ := live.counts(); opened != 1 {
		t.Fatalf("opened=%d", opened)
	}
	select {
	case <-mainStream.done:
		t.Fatal("request cancellation stopped worker")
	default:
	}

	if rejection := controller.selectLive(context.Background(), testHLSTvCastRequest(1, 2)); rejection != nil {
		t.Fatal(rejection)
	}
	if rejection := controller.startLive(testHLSViewRequest(1, 2, "dual-key", hlsViewStart)); rejection != nil {
		t.Fatal(rejection)
	}
	waitHLSSignal(t, dualStream.started)
	if rejection := controller.selectLive(context.Background(), testHLSTvCastRequest(0, 3)); rejection != nil {
		t.Fatal(rejection)
	}
	waitHLSSignal(t, mainStream.done)
	if mainStream.closeCount() != 1 {
		t.Fatalf("main close count=%d", mainStream.closeCount())
	}
	select {
	case <-dualStream.done:
		t.Fatal("main replacement stopped dual slot")
	default:
	}
	if selected, opened, _ := live.counts(); selected != 3 || opened != 2 {
		t.Fatalf("selected=%d opened=%d", selected, opened)
	}
}

func TestHLSSessionConcurrentDuplicateStartOpensProviderOnce(t *testing.T) {
	clock := newManualHLSSessionClock()
	live := newFakeHLSSessionLive()
	stream := newFakeHLSSessionStream(testHLSSessionTS(t, 4), nil, true)
	live.queue(stream)
	controller := testHLSSessionController(t, context.Background(), live, clock)
	if rejection := controller.selectLive(context.Background(), testHLSTvCastRequest(0, 1)); rejection != nil {
		t.Fatal(rejection)
	}
	request := testHLSViewRequest(0, 1, "duplicate-key", hlsViewStart)
	results := make(chan *hlsHTTPRejection, 16)
	var workers sync.WaitGroup
	for range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			results <- controller.startLive(request)
		}()
	}
	workers.Wait()
	close(results)
	for rejection := range results {
		if rejection != nil {
			t.Fatalf("rejection=%+v", rejection)
		}
	}
	if _, opened, _ := live.counts(); opened != 1 {
		t.Fatalf("opened=%d", opened)
	}
}

func TestHLSSessionPlaylistWaitsForNotificationAndRetainsEndlist(t *testing.T) {
	clock := newManualHLSSessionClock()
	live := newFakeHLSSessionLive()
	release := make(chan struct{})
	stream := newFakeHLSSessionStream(testHLSSessionTS(t, 4), release, true)
	live.queue(stream)
	controller := testHLSSessionController(t, context.Background(), live, clock)
	selectAndStartHLSSession(t, controller, 0, 1, "playlist-key")
	waitHLSSignal(t, stream.started)

	request := testHLSViewRequest(0, 1, "playlist-key", hlsViewPlaylist)
	type playlistResult struct {
		data      []byte
		rejection *hlsHTTPRejection
	}
	result := make(chan playlistResult, 1)
	go func() {
		data, rejection := controller.playlistBytes(context.Background(), request)
		result <- playlistResult{data: data, rejection: rejection}
	}()
	waitForHLSTimerCount(t, clock, 3)
	clock.advance(hlsPlaylistWait - time.Nanosecond)
	select {
	case got := <-result:
		t.Fatalf("playlist returned before notification: %+v", got.rejection)
	default:
	}
	close(release)
	got := waitHLSPlaylistResult(t, result)
	if got.rejection != nil || bytes.HasPrefix(got.data, []byte{0xef, 0xbb, 0xbf}) ||
		strings.Count(string(got.data), "#EXTINF:") != 3 || strings.Contains(string(got.data), "#EXT-X-ENDLIST") {
		t.Fatalf("active playlist=%q rejection=%+v", got.data, got.rejection)
	}
	stream.endNaturally()
	session, _ := controller.lookupSession("playlist-key")
	waitHLSSignal(t, session.done)
	data, rejection := controller.playlistBytes(context.Background(), request)
	if rejection != nil || !strings.HasSuffix(string(data), "#EXT-X-ENDLIST\n") {
		t.Fatalf("terminal playlist=%q rejection=%+v", data, rejection)
	}

	clock.advance(2 * time.Second)
	if _, rejection := controller.playlistBytes(context.Background(), request); rejection != nil {
		t.Fatalf("retention extension rejection=%+v", rejection)
	}
	clock.advance(3*time.Second - time.Nanosecond)
	file, rejection := controller.openSegment(hlsSegmentRequest{key: "playlist-key", sequence: 0})
	if rejection != nil {
		t.Fatalf("segment before retention boundary=%+v", rejection)
	}
	_ = file.Close()
	clock.advance(time.Nanosecond)
	_, rejection = controller.openSegment(hlsSegmentRequest{key: "playlist-key", sequence: 0})
	assertHLSSessionRejection(t, rejection, 410, "session-ended")
	_, rejection = controller.playlistBytes(context.Background(), request)
	assertHLSSessionRejection(t, rejection, 410, "session-ended")
}

func TestHLSSessionPlaylistTimeoutDoesNotStopWorker(t *testing.T) {
	clock := newManualHLSSessionClock()
	live := newFakeHLSSessionLive()
	release := make(chan struct{})
	stream := newFakeHLSSessionStream(testHLSSessionTS(t, 4), release, true)
	live.queue(stream)
	controller := testHLSSessionController(t, context.Background(), live, clock)
	selectAndStartHLSSession(t, controller, 0, 1, "waiting-key")
	waitHLSSignal(t, stream.started)

	result := make(chan *hlsHTTPRejection, 1)
	go func() {
		_, rejection := controller.playlistBytes(context.Background(), testHLSViewRequest(0, 1, "waiting-key", hlsViewPlaylist))
		result <- rejection
	}()
	waitForHLSTimerCount(t, clock, 3)
	clock.advance(hlsPlaylistWait)
	assertHLSSessionRejection(t, waitHLSRejection(t, result), 503, "playlist-unavailable")
	select {
	case <-stream.done:
		t.Fatal("playlist timeout stopped worker")
	default:
	}
}

func TestHLSSessionIdleUsesLastPlaylistOrSegmentAccess(t *testing.T) {
	clock := newManualHLSSessionClock()
	live := newFakeHLSSessionLive()
	stream := newFakeHLSSessionStream(testHLSSessionTS(t, 4), nil, true)
	live.queue(stream)
	controller := testHLSSessionController(t, context.Background(), live, clock)
	selectAndStartHLSSession(t, controller, 0, 1, "idle-key")
	session, _ := controller.lookupSession("idle-key")
	waitHLSSignal(t, session.ready)

	clock.advance(hlsIdleLifetime - time.Nanosecond)
	if _, rejection := controller.playlistBytes(context.Background(), testHLSViewRequest(0, 1, "idle-key", hlsViewPlaylist)); rejection != nil {
		t.Fatal(rejection)
	}
	clock.advance(hlsIdleLifetime - time.Nanosecond)
	select {
	case <-stream.done:
		t.Fatal("worker stopped before refreshed idle boundary")
	default:
	}
	clock.advance(time.Nanosecond)
	waitHLSSignal(t, stream.done)
	if stream.closeCount() != 1 {
		t.Fatalf("close count=%d", stream.closeCount())
	}
}

func TestHLSSessionStopsAtServiceAndMaximumLifetimeBoundaries(t *testing.T) {
	t.Run("service context", func(t *testing.T) {
		clock := newManualHLSSessionClock()
		live := newFakeHLSSessionLive()
		stream := newFakeHLSSessionStream(nil, nil, true)
		live.queue(stream)
		serviceContext, cancel := context.WithCancel(context.Background())
		controller := testHLSSessionController(t, serviceContext, live, clock)
		selectAndStartHLSSession(t, controller, 0, 1, "service-key")
		waitHLSSignal(t, stream.started)
		session := controller.slots[0].session
		cancel()
		waitHLSSignal(t, session.done)
		if stream.closeCount() != 1 {
			t.Fatalf("close count=%d", stream.closeCount())
		}
		session.mu.Lock()
		reason := session.reason
		session.mu.Unlock()
		if reason != "session-ended" {
			t.Fatalf("reason=%s", reason)
		}
	})
	t.Run("maximum lifetime", func(t *testing.T) {
		clock := newManualHLSSessionClock()
		live := newFakeHLSSessionLive()
		stream := newFakeHLSSessionStream(nil, nil, true)
		live.queue(stream)
		controller := testHLSSessionController(t, context.Background(), live, clock)
		controller.timing.idle = 2 * hlsWorkerLifetime
		selectAndStartHLSSession(t, controller, 0, 1, "lifetime-key")
		session := controller.slots[0].session
		waitHLSSignal(t, stream.started)
		clock.advance(hlsWorkerLifetime - time.Nanosecond)
		select {
		case <-stream.done:
			t.Fatal("worker stopped before lifetime boundary")
		default:
		}
		clock.advance(time.Nanosecond)
		waitHLSSignal(t, session.done)
		if stream.closeCount() != 1 {
			t.Fatalf("close count=%d", stream.closeCount())
		}
	})
}

func TestHLSSessionKeyLimitPreservesExistingRetention(t *testing.T) {
	clock := newManualHLSSessionClock()
	live := newFakeHLSSessionLive()
	stream := newFakeHLSSessionStream(testHLSSessionTS(t, 4), nil, true)
	live.queue(stream)
	controller := testHLSSessionController(t, context.Background(), live, clock)
	controller.mu.Lock()
	for index := 0; index < hlsSessionKeyLimit-1; index++ {
		controller.keys[fmt.Sprintf("used-%d", index)] = struct{}{}
	}
	controller.mu.Unlock()
	selectAndStartHLSSession(t, controller, 0, 1, "last-key")
	session, _ := controller.lookupSession("last-key")
	waitHLSSignal(t, session.ready)
	if rejection := controller.startLive(testHLSViewRequest(0, 1, "last-key", hlsViewStart)); rejection != nil {
		t.Fatalf("existing retry rejection=%+v", rejection)
	}
	if rejection := controller.selectLive(context.Background(), testHLSTvCastRequest(0, 2)); rejection != nil {
		t.Fatal(rejection)
	}
	waitHLSSignal(t, session.done)
	rejection := controller.startLive(testHLSViewRequest(0, 2, "one-over", hlsViewStart))
	assertHLSSessionRejection(t, rejection, 503, "session-key-limit")
	if _, opened, _ := live.counts(); opened != 1 {
		t.Fatalf("provider opened at key limit: %d", opened)
	}
	data, rejection := controller.playlistBytes(context.Background(), testHLSViewRequest(0, 1, "last-key", hlsViewPlaylist))
	if rejection != nil || !strings.Contains(string(data), "#EXT-X-ENDLIST") {
		t.Fatalf("retained playlist=%q rejection=%+v", data, rejection)
	}
}

func TestHLSSessionReplacementKeepsEveryRetainedGeneration(t *testing.T) {
	clock := newManualHLSSessionClock()
	live := newFakeHLSSessionLive()
	first := newFakeHLSSessionStream(testHLSSessionTS(t, 4), nil, false)
	second := newFakeHLSSessionStream(testHLSSessionTS(t, 4), nil, true)
	live.queue(first, second)
	controller := testHLSSessionController(t, context.Background(), live, clock)
	selectAndStartHLSSession(t, controller, 0, 1, "first-key")
	firstSession, _ := controller.lookupSession("first-key")
	waitHLSSignal(t, firstSession.done)
	selectAndStartHLSSession(t, controller, 0, 2, "second-key")
	secondSession, _ := controller.lookupSession("second-key")
	waitHLSSignal(t, secondSession.ready)

	controller.cache.mu.Lock()
	generations := len(controller.cache.generations)
	controller.cache.mu.Unlock()
	if generations != 2 {
		t.Fatalf("cache generations=%d", generations)
	}
	for _, key := range []string{"first-key", "second-key"} {
		file, rejection := controller.openSegment(hlsSegmentRequest{key: key, sequence: 0})
		if rejection != nil {
			t.Fatalf("key=%s rejection=%+v", key, rejection)
		}
		_ = file.Close()
	}
}

func TestHLSSessionRetentionKeepsAlreadyOpenSegmentReadable(t *testing.T) {
	clock := newManualHLSSessionClock()
	live := newFakeHLSSessionLive()
	stream := newFakeHLSSessionStream(testHLSSessionTS(t, 4), nil, false)
	live.queue(stream)
	controller := testHLSSessionController(t, context.Background(), live, clock)
	selectAndStartHLSSession(t, controller, 0, 1, "reader-key")
	session, _ := controller.lookupSession("reader-key")
	waitHLSSignal(t, session.done)
	file, rejection := controller.openSegment(hlsSegmentRequest{key: "reader-key", sequence: 0})
	if rejection != nil {
		t.Fatal(rejection)
	}
	clock.advance(3 * time.Second)
	_, rejection = controller.openSegment(hlsSegmentRequest{key: "reader-key", sequence: 0})
	assertHLSSessionRejection(t, rejection, 410, "session-ended")
	data, err := io.ReadAll(file)
	if err != nil || len(data) == 0 || data[0] != 0x47 {
		t.Fatalf("read bytes=%d err=%v", len(data), err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestHLSSessionOpenFailureClosesPartialResources(t *testing.T) {
	clock := newManualHLSSessionClock()
	live := newFakeHLSSessionLive()
	live.openErr = errors.New("provider unavailable")
	controller := testHLSSessionController(t, context.Background(), live, clock)
	if rejection := controller.selectLive(context.Background(), testHLSTvCastRequest(0, 1)); rejection != nil {
		t.Fatal(rejection)
	}
	rejection := controller.startLive(testHLSViewRequest(0, 1, "failed-key", hlsViewStart))
	assertHLSSessionRejection(t, rejection, 503, "live-provider-unavailable")
	controller.cache.mu.Lock()
	generations := len(controller.cache.generations)
	controller.cache.mu.Unlock()
	if generations != 0 {
		t.Fatalf("cache generations=%d", generations)
	}
	if session, used := controller.lookupSession("failed-key"); session != nil || !used {
		t.Fatalf("session=%v used=%v", session, used)
	}
}

func TestHLSSessionServiceCancellationDuringOpenIsSessionEnded(t *testing.T) {
	clock := newManualHLSSessionClock()
	live := newFakeHLSSessionLive()
	stream := newFakeHLSSessionStream(nil, nil, true)
	live.queue(stream)
	serviceContext, cancel := context.WithCancel(context.Background())
	live.openHook = cancel
	controller := testHLSSessionController(t, serviceContext, live, clock)
	if rejection := controller.selectLive(context.Background(), testHLSTvCastRequest(0, 1)); rejection != nil {
		t.Fatal(rejection)
	}
	rejection := controller.startLive(testHLSViewRequest(0, 1, "cancel-key", hlsViewStart))
	assertHLSSessionRejection(t, rejection, 503, "session-ended")
	if stream.closeCount() != 1 {
		t.Fatalf("stream close count=%d", stream.closeCount())
	}
	controller.cache.mu.Lock()
	generations := len(controller.cache.generations)
	controller.cache.mu.Unlock()
	if generations != 0 {
		t.Fatalf("cache generations=%d", generations)
	}
}

func TestHLSSessionKeepsSegmenterReasonWhenLiveRelayNormalizesWriteError(t *testing.T) {
	tests := []struct {
		name    string
		payload func(*testing.T) []byte
		limit   int64
		reason  string
	}{
		{
			name: "TS sync", reason: "ts-sync-unavailable",
			payload: func(*testing.T) []byte { return make([]byte, 64*1024+1) },
		},
		{
			name: "cache limit", reason: "hls-cache-limit", limit: 100,
			payload: func(t *testing.T) []byte { return testHLSSessionTS(t, 2) },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clock := newManualHLSSessionClock()
			live := newFakeHLSSessionLive()
			stream := newFakeHLSSessionStream(test.payload(t), nil, false)
			stream.normalizeWriteError = true
			live.queue(stream)
			controller := testHLSSessionController(t, context.Background(), live, clock)
			if test.limit > 0 {
				controller.cache.mu.Lock()
				controller.cache.limits.segmentBytes = test.limit
				controller.cache.mu.Unlock()
			}
			selectAndStartHLSSession(t, controller, 0, 1, "reason-key")
			session := controller.slots[0].session
			waitHLSSignal(t, session.done)
			session.mu.Lock()
			reason := session.reason
			session.mu.Unlock()
			if reason != test.reason {
				t.Fatalf("reason=%s want=%s", reason, test.reason)
			}
		})
	}
}

func TestHLSSessionCloseReleasesTwoWorkersAndTimers(t *testing.T) {
	clock := newManualHLSSessionClock()
	live := newFakeHLSSessionLive()
	first := newFakeHLSSessionStream(testHLSSessionTS(t, 4), nil, true)
	second := newFakeHLSSessionStream(testHLSSessionTS(t, 4), nil, true)
	live.queue(first, second)
	controller := testHLSSessionControllerNoCleanup(t, context.Background(), live, clock)
	selectAndStartHLSSession(t, controller, 0, 1, "close-main")
	selectAndStartHLSSession(t, controller, 1, 2, "close-dual")
	waitHLSSignal(t, first.started)
	waitHLSSignal(t, second.started)
	if err := controller.close(); err != nil {
		t.Fatal(err)
	}
	waitHLSSignal(t, first.done)
	waitHLSSignal(t, second.done)
	if first.closeCount() != 1 || second.closeCount() != 1 {
		t.Fatalf("close counts=%d,%d", first.closeCount(), second.closeCount())
	}
	if pending := clock.pending(); pending != 0 {
		t.Fatalf("pending timers=%d", pending)
	}
	if controller.cache.root != nil {
		t.Fatal("cache root remains open")
	}
}

func TestHLSSessionCloseWaitsForRunningTimerCallback(t *testing.T) {
	clock := newManualHLSSessionClock()
	live := newFakeHLSSessionLive()
	controller := testHLSSessionControllerNoCleanup(t, context.Background(), live, clock)
	started := make(chan struct{})
	release := make(chan struct{})
	if _, ok := controller.newTimer(time.Second, func() {
		close(started)
		<-release
	}); !ok {
		t.Fatal("timer was not registered")
	}
	advanced := make(chan struct{})
	go func() {
		clock.advance(time.Second)
		close(advanced)
	}()
	waitHLSSignal(t, started)
	closed := make(chan error, 1)
	go func() { closed <- controller.close() }()
	select {
	case err := <-closed:
		t.Fatalf("close returned before callback: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	waitHLSSignal(t, advanced)
	if err := waitHLSPlaylistResult(t, closed); err != nil {
		t.Fatal(err)
	}
	called := false
	if _, ok := controller.newTimer(time.Second, func() { called = true }); ok {
		t.Fatal("timer registered after close")
	}
	clock.advance(time.Second)
	if called {
		t.Fatal("callback ran after close")
	}
}

func TestHLSSessionConcurrentStartAndCloseNeverDoubleOpens(t *testing.T) {
	for iteration := 0; iteration < 8; iteration++ {
		clock := newManualHLSSessionClock()
		live := newFakeHLSSessionLive()
		stream := newFakeHLSSessionStream(nil, nil, true)
		live.queue(stream)
		controller := testHLSSessionControllerNoCleanup(t, context.Background(), live, clock)
		if rejection := controller.selectLive(context.Background(), testHLSTvCastRequest(0, 1)); rejection != nil {
			t.Fatal(rejection)
		}
		barrier := make(chan struct{})
		started := make(chan *hlsHTTPRejection, 1)
		closed := make(chan error, 1)
		go func() {
			<-barrier
			started <- controller.startLive(testHLSViewRequest(0, 1, "race-key", hlsViewStart))
		}()
		go func() {
			<-barrier
			closed <- controller.close()
		}()
		close(barrier)
		_ = waitHLSRejection(t, started)
		if err := waitHLSPlaylistResult(t, closed); err != nil {
			t.Fatal(err)
		}
		_, opened, _ := live.counts()
		if opened > 1 || stream.closeCount() != opened {
			t.Fatalf("iteration=%d opened=%d stream closes=%d", iteration, opened, stream.closeCount())
		}
		if pending := clock.pending(); pending != 0 {
			t.Fatalf("iteration=%d pending timers=%d", iteration, pending)
		}
	}
}

func TestHLSSessionConcurrentSelectionAndCloseReleasesSelectionOnce(t *testing.T) {
	for iteration := 0; iteration < 8; iteration++ {
		clock := newManualHLSSessionClock()
		live := newFakeHLSSessionLive()
		controller := testHLSSessionControllerNoCleanup(t, context.Background(), live, clock)
		barrier := make(chan struct{})
		selected := make(chan *hlsHTTPRejection, 1)
		closed := make(chan error, 1)
		go func() {
			<-barrier
			selected <- controller.selectLive(context.Background(), testHLSTvCastRequest(0, 1))
		}()
		go func() {
			<-barrier
			closed <- controller.close()
		}()
		close(barrier)
		_ = waitHLSRejection(t, selected)
		if err := waitHLSPlaylistResult(t, closed); err != nil {
			t.Fatal(err)
		}
		selections, _, releases := live.counts()
		if selections > 1 || releases != selections {
			t.Fatalf("iteration=%d selections=%d releases=%d", iteration, selections, releases)
		}
		if pending := clock.pending(); pending != 0 {
			t.Fatalf("iteration=%d pending timers=%d", iteration, pending)
		}
	}
}

func TestHLSSessionConcurrentIdleAndCloseReleasesWorkerOnce(t *testing.T) {
	for iteration := 0; iteration < 8; iteration++ {
		clock := newManualHLSSessionClock()
		live := newFakeHLSSessionLive()
		stream := newFakeHLSSessionStream(nil, nil, true)
		live.queue(stream)
		controller := testHLSSessionControllerNoCleanup(t, context.Background(), live, clock)
		selectAndStartHLSSession(t, controller, 0, 1, "idle-close-key")
		waitHLSSignal(t, stream.started)
		barrier := make(chan struct{})
		advanced := make(chan struct{})
		closed := make(chan error, 1)
		go func() {
			<-barrier
			clock.advance(hlsIdleLifetime)
			close(advanced)
		}()
		go func() {
			<-barrier
			closed <- controller.close()
		}()
		close(barrier)
		waitHLSSignal(t, advanced)
		if err := waitHLSPlaylistResult(t, closed); err != nil {
			t.Fatal(err)
		}
		if stream.closeCount() != 1 {
			t.Fatalf("iteration=%d stream closes=%d", iteration, stream.closeCount())
		}
		if pending := clock.pending(); pending != 0 {
			t.Fatalf("iteration=%d pending timers=%d", iteration, pending)
		}
	}
}

func testHLSSessionController(t *testing.T, serviceContext context.Context, live *fakeHLSSessionLive,
	clock *manualHLSSessionClock) *hlsSessionController {
	t.Helper()
	controller := testHLSSessionControllerNoCleanup(t, serviceContext, live, clock)
	t.Cleanup(func() {
		if err := controller.close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return controller
}

func testHLSSessionControllerNoCleanup(t *testing.T, serviceContext context.Context, live *fakeHLSSessionLive,
	clock *manualHLSSessionClock) *hlsSessionController {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	cache, err := openHLSCache(root, hlsCacheLimits{
		segmentBytes: 1024 * 1024, sessionBytes: 8 * 1024 * 1024,
		totalBytes: 16 * 1024 * 1024, cleanupEntries: 128,
	})
	if err != nil {
		t.Fatal(err)
	}
	timing := defaultHLSSessionTiming()
	timing.clock = hlsSessionClock{now: clock.Now, afterFunc: clock.AfterFunc}
	controller, err := newHLSSessionControllerWithTiming(serviceContext, live, cache, timing)
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

func selectAndStartHLSSession(t *testing.T, controller *hlsSessionController, slot uint8, service uint16, key string) {
	t.Helper()
	if rejection := controller.selectLive(context.Background(), testHLSTvCastRequest(slot, service)); rejection != nil {
		t.Fatalf("select rejection=%+v", rejection)
	}
	if rejection := controller.startLive(testHLSViewRequest(slot, service, key, hlsViewStart)); rejection != nil {
		t.Fatalf("start rejection=%+v", rejection)
	}
}

func testHLSTvCastRequest(slot uint8, service uint16) hlsTvCastRequest {
	return hlsTvCastRequest{slot: slot, service: hlsServiceID{onid: service, tsid: service + 10, sid: service + 20}}
}

func testHLSViewRequest(slot uint8, service uint16, key string, operation hlsViewOperation) hlsViewRequest {
	return hlsViewRequest{slot: slot, key: key, operation: operation,
		service: hlsServiceID{onid: service, tsid: service + 10, sid: service + 20}}
}

func testHLSSessionTS(t *testing.T, boundaries int) []byte {
	t.Helper()
	var data []byte
	for index := 0; index < boundaries; index++ {
		data = append(data, testHLSBoundary(t, byte(index), 0, uint64(index)*hlsClockHz, 0x1b, true, []uint16{1})...)
	}
	return data
}

func assertHLSSessionRejection(t *testing.T, rejection *hlsHTTPRejection, status int, reason string) {
	t.Helper()
	if rejection == nil || rejection.status != status || rejection.reason != reason {
		t.Fatalf("rejection=%+v want status=%d reason=%s", rejection, status, reason)
	}
}

func waitHLSSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for HLS signal")
	}
}

func waitHLSPlaylistResult[T any](t *testing.T, result <-chan T) T {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for HLS result")
		var zero T
		return zero
	}
}

func waitHLSRejection(t *testing.T, result <-chan *hlsHTTPRejection) *hlsHTTPRejection {
	t.Helper()
	return waitHLSPlaylistResult(t, result)
}

func waitForHLSTimerCount(t *testing.T, clock *manualHLSSessionClock, minimum int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for clock.pending() < minimum && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if pending := clock.pending(); pending < minimum {
		t.Fatalf("pending timers=%d want at least %d", pending, minimum)
	}
}

type fakeHLSSessionLive struct {
	mu           sync.Mutex
	next         int32
	selected     int
	opened       int
	closed       int
	openErr      error
	openHook     func()
	closeStarted chan struct{}
	closeRelease <-chan struct{}
	closeOnce    sync.Once
	streamQueue  []*fakeHLSSessionStream
}

func newFakeHLSSessionLive() *fakeHLSSessionLive {
	return &fakeHLSSessionLive{next: 1}
}

func (live *fakeHLSSessionLive) queue(streams ...*fakeHLSSessionStream) {
	live.mu.Lock()
	live.streamQueue = append(live.streamQueue, streams...)
	live.mu.Unlock()
}

func (live *fakeHLSSessionLive) Select(ctx context.Context, service LiveService, networkTVID int32) (int32, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if service.NetworkID == 0 || service.TransportStreamID == 0 || service.ServiceID == 0 || networkTVID < 1 || networkTVID > 2 {
		return 0, errors.New("invalid selection")
	}
	live.mu.Lock()
	defer live.mu.Unlock()
	processID := live.next
	live.next++
	live.selected++
	return processID, nil
}

func (live *fakeHLSSessionLive) Open(ctx context.Context, processID int32) (LiveStream, error) {
	live.mu.Lock()
	defer live.mu.Unlock()
	live.opened++
	if live.openHook != nil {
		live.openHook()
	}
	if live.openErr != nil {
		return nil, live.openErr
	}
	if processID <= 0 || len(live.streamQueue) == 0 {
		return nil, errors.New("stream unavailable")
	}
	stream := live.streamQueue[0]
	live.streamQueue = live.streamQueue[1:]
	stream.context = ctx
	return stream, nil
}

func (live *fakeHLSSessionLive) Close(networkTVID int32) {
	if networkTVID < 1 || networkTVID > 2 {
		return
	}
	live.mu.Lock()
	live.closed++
	started := live.closeStarted
	release := live.closeRelease
	live.mu.Unlock()
	if started != nil {
		live.closeOnce.Do(func() { close(started) })
	}
	if release != nil {
		<-release
	}
}

func (live *fakeHLSSessionLive) counts() (selected, opened, closed int) {
	live.mu.Lock()
	defer live.mu.Unlock()
	return live.selected, live.opened, live.closed
}

type fakeHLSSessionStream struct {
	context             context.Context
	payload             []byte
	release             <-chan struct{}
	hold                bool
	normalizeWriteError bool
	started             chan struct{}
	done                chan struct{}
	closed              chan struct{}
	natural             chan struct{}

	startOnce   sync.Once
	doneOnce    sync.Once
	closeOnce   sync.Once
	naturalOnce sync.Once
	mu          sync.Mutex
	closes      int
}

func newFakeHLSSessionStream(payload []byte, release <-chan struct{}, hold bool) *fakeHLSSessionStream {
	return &fakeHLSSessionStream{
		payload: append([]byte(nil), payload...), release: release, hold: hold,
		started: make(chan struct{}), done: make(chan struct{}), closed: make(chan struct{}), natural: make(chan struct{}),
	}
}

func (stream *fakeHLSSessionStream) Copy(destination io.Writer) error {
	stream.startOnce.Do(func() { close(stream.started) })
	defer stream.doneOnce.Do(func() { close(stream.done) })
	if stream.release != nil {
		select {
		case <-stream.release:
		case <-stream.closed:
			return errors.New("closed")
		case <-stream.context.Done():
			return stream.context.Err()
		}
	}
	if len(stream.payload) > 0 {
		written, err := destination.Write(stream.payload)
		if err != nil {
			if stream.normalizeWriteError {
				return errors.New("live-client-write-failed")
			}
			return err
		}
		if written != len(stream.payload) {
			return io.ErrShortWrite
		}
	}
	if !stream.hold {
		return nil
	}
	select {
	case <-stream.natural:
		return nil
	case <-stream.closed:
		return errors.New("closed")
	case <-stream.context.Done():
		return stream.context.Err()
	}
}

func (stream *fakeHLSSessionStream) Close() error {
	stream.closeOnce.Do(func() {
		stream.mu.Lock()
		stream.closes++
		stream.mu.Unlock()
		close(stream.closed)
	})
	return nil
}

func (stream *fakeHLSSessionStream) endNaturally() {
	stream.naturalOnce.Do(func() { close(stream.natural) })
}

func (stream *fakeHLSSessionStream) closeCount() int {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.closes
}

type manualHLSSessionClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*manualHLSSessionTimer
}

type manualHLSSessionTimer struct {
	clock    *manualHLSSessionClock
	deadline time.Time
	callback func()
	stopped  bool
	fired    bool
}

func newManualHLSSessionClock() *manualHLSSessionClock {
	return &manualHLSSessionClock{now: time.Unix(1_700_000_000, 0)}
}

func (clock *manualHLSSessionClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *manualHLSSessionClock) AfterFunc(delay time.Duration, callback func()) hlsTimer {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	timer := &manualHLSSessionTimer{clock: clock, deadline: clock.now.Add(delay), callback: callback}
	clock.timers = append(clock.timers, timer)
	return timer
}

func (clock *manualHLSSessionClock) advance(duration time.Duration) {
	clock.mu.Lock()
	target := clock.now.Add(duration)
	clock.mu.Unlock()
	for {
		clock.mu.Lock()
		var next *manualHLSSessionTimer
		for _, timer := range clock.timers {
			if timer.stopped || timer.fired || timer.deadline.After(target) {
				continue
			}
			if next == nil || timer.deadline.Before(next.deadline) {
				next = timer
			}
		}
		if next == nil {
			clock.now = target
			clock.mu.Unlock()
			return
		}
		clock.now = next.deadline
		next.fired = true
		callback := next.callback
		clock.mu.Unlock()
		callback()
	}
}

func (clock *manualHLSSessionClock) pending() int {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	count := 0
	for _, timer := range clock.timers {
		if !timer.stopped && !timer.fired {
			count++
		}
	}
	return count
}

func (timer *manualHLSSessionTimer) Stop() bool {
	timer.clock.mu.Lock()
	defer timer.clock.mu.Unlock()
	if timer.stopped || timer.fired {
		return false
	}
	timer.stopped = true
	return true
}
