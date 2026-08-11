package recordinghttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

const (
	hlsSessionSlots      = 2
	hlsSessionKeyLimit   = 4096
	hlsSelectionLifetime = 30 * time.Second
	hlsWorkerLifetime    = 12 * time.Hour
	hlsPlaylistWait      = 2 * time.Second
	hlsIdleLifetime      = 30 * time.Second
)

// LiveServiceはHTTPで検証した放送IDをライブ管理へ渡すための値である。
// ProviderのlocatorやMirakurun固有の形式はこの境界へ持ち込まない。
type LiveService struct {
	NetworkID         uint16
	TransportStreamID uint16
	ServiceID         uint16
}

// LiveStreamは一つのProvider streamをHLS分割処理へ転送し、明示的に閉じる。
type LiveStream interface {
	Copy(io.Writer) error
	Close() error
}

// LiveOperationsは放送の選択とProvider streamの寿命だけをHTTP adapterへ公開する。
// 実装側は表示枠ごとの正のIDを使い、SelectではまだProviderを開かない。
type LiveOperations interface {
	Select(context.Context, LiveService, int32) (int32, error)
	Open(context.Context, int32) (LiveStream, error)
	Close(int32)
}

type hlsTimer interface {
	Stop() bool
}

type hlsManagedTimer struct {
	timer hlsTimer
	once  sync.Once
	wait  *sync.WaitGroup
}

type hlsStoppedTimer struct{}

// Stopは登録されていないtimerを停止済みとして扱う。
func (hlsStoppedTimer) Stop() bool { return false }

// Stopはcallbackが始まる前なら停止し、終了待ちを一度だけ完了させる。
func (timer *hlsManagedTimer) Stop() bool {
	if timer == nil || timer.timer == nil || !timer.timer.Stop() {
		return false
	}
	timer.complete()
	return true
}

func (timer *hlsManagedTimer) complete() {
	if timer != nil {
		timer.once.Do(timer.wait.Done)
	}
}

type hlsSessionClock struct {
	now       func() time.Time
	afterFunc func(time.Duration, func()) hlsTimer
}

type hlsSessionTiming struct {
	selection time.Duration
	worker    time.Duration
	wait      time.Duration
	idle      time.Duration
	clock     hlsSessionClock
}

func defaultHLSSessionTiming() hlsSessionTiming {
	return hlsSessionTiming{
		selection: hlsSelectionLifetime,
		worker:    hlsWorkerLifetime,
		wait:      hlsPlaylistWait,
		idle:      hlsIdleLifetime,
		clock: hlsSessionClock{
			now:       time.Now,
			afterFunc: func(delay time.Duration, callback func()) hlsTimer { return time.AfterFunc(delay, callback) },
		},
	}
}

func validHLSSessionTiming(timing hlsSessionTiming) bool {
	return timing.selection > 0 && timing.worker > 0 && timing.wait > 0 && timing.idle > 0 &&
		timing.clock.now != nil && timing.clock.afterFunc != nil
}

// hlsSessionControllerは二つの表示枠、使用済みkey、保持中cacheをprocess内で管理する。
// 表示枠ごとのmutexで選択と開始を直列化し、一方の停止が他方を待たせない。
type hlsSessionController struct {
	serviceContext context.Context
	live           LiveOperations
	cache          *hlsCache
	timing         hlsSessionTiming
	slots          [hlsSessionSlots]hlsSessionSlot

	mu             sync.Mutex
	timers         sync.WaitGroup
	closed         bool
	keys           map[string]struct{}
	sessions       map[string]*hlsSession
	nextGeneration uint64
}

type hlsSessionSlot struct {
	mu        sync.Mutex
	selection *hlsSelection
	session   *hlsSession
}

type hlsSelection struct {
	service   hlsServiceID
	processID int32
	timer     hlsTimer
}

type hlsSession struct {
	controller *hlsSessionController
	service    hlsServiceID
	slot       uint8
	key        string
	generation *hlsCacheGeneration
	playlist   *hlsMediaPlaylist
	context    context.Context
	cancel     context.CancelFunc
	stream     LiveStream
	done       chan struct{}
	ready      chan struct{}

	streamOnce sync.Once
	streamErr  error
	mu         sync.Mutex
	terminal   bool
	stopping   bool
	reason     string
	published  uint64
	readyDone  bool
	idleEpoch  uint64
	idleTimer  hlsTimer
	lifeTimer  hlsTimer
	segments   map[uint64]*hlsRetainedSegment
}

type hlsRetainedSegment struct {
	deadline time.Time
	version  uint64
	timer    hlsTimer
}

func newHLSSessionController(serviceContext context.Context, live LiveOperations, cache *hlsCache) (*hlsSessionController, error) {
	return newHLSSessionControllerWithTiming(serviceContext, live, cache, defaultHLSSessionTiming())
}

func newHLSSessionControllerWithTiming(serviceContext context.Context, live LiveOperations, cache *hlsCache,
	timing hlsSessionTiming) (*hlsSessionController, error) {
	if serviceContext == nil || live == nil || cache == nil || !validHLSSessionTiming(timing) {
		return nil, errors.New("recordinghttp: missing hls session dependency")
	}
	return &hlsSessionController{
		serviceContext: serviceContext,
		live:           live,
		cache:          cache,
		timing:         timing,
		keys:           make(map[string]struct{}, hlsSessionKeyLimit),
		sessions:       make(map[string]*hlsSession, hlsSessionSlots),
		nextGeneration: 1,
	}, nil
}

// selectLiveは古い表示枠を同期的に止めてから、新しい放送を30秒だけ保存する。
// LiveOperations.Selectは放送の照合だけを行い、Provider streamを開かない。
func (controller *hlsSessionController) selectLive(ctx context.Context, request hlsTvCastRequest) *hlsHTTPRejection {
	if controller == nil || ctx == nil || request.slot >= hlsSessionSlots || !validHLSServiceID(request.service) {
		return rejectHLS(http.StatusNotFound, "service-unavailable", "")
	}
	slot := &controller.slots[request.slot]
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if controller.isClosed() {
		return rejectHLS(http.StatusServiceUnavailable, "session-ended", "")
	}
	controller.clearSlotLocked(request.slot, slot, "session-ended")
	processID, err := controller.live.Select(ctx, projectHLSLiveService(request.service), hlsNetworkTVID(request.slot))
	if err != nil || processID <= 0 {
		controller.live.Close(hlsNetworkTVID(request.slot))
		return rejectHLS(http.StatusNotFound, "service-unavailable", "")
	}
	if controller.isClosed() || controller.serviceContext.Err() != nil || ctx.Err() != nil {
		controller.live.Close(hlsNetworkTVID(request.slot))
		return rejectHLS(http.StatusServiceUnavailable, "session-ended", "")
	}
	selection := &hlsSelection{service: request.service, processID: processID}
	var timerOK bool
	selection.timer, timerOK = controller.newTimer(controller.timing.selection, func() {
		controller.expireSelection(request.slot, selection)
	})
	if !timerOK {
		controller.live.Close(hlsNetworkTVID(request.slot))
		return rejectHLS(http.StatusServiceUnavailable, "session-ended", "")
	}
	slot.selection = selection
	if controller.isClosed() {
		slot.selection = nil
		selection.timer.Stop()
		controller.live.Close(hlsNetworkTVID(request.slot))
		return rejectHLS(http.StatusServiceUnavailable, "session-ended", "")
	}
	return nil
}

func (controller *hlsSessionController) expireSelection(slotNumber uint8, selection *hlsSelection) {
	if controller == nil || slotNumber >= hlsSessionSlots || selection == nil {
		return
	}
	slot := &controller.slots[slotNumber]
	slot.mu.Lock()
	if slot.selection != selection {
		slot.mu.Unlock()
		return
	}
	slot.selection = nil
	controller.live.Close(hlsNetworkTVID(slotNumber))
	slot.mu.Unlock()
}

// startLiveは対応するTvCast選択を一度だけ消費し、service contextからworkerを開始する。
// 同じ放送、表示枠、keyへの再送では同じworkerを返し、Providerを二重に開かない。
func (controller *hlsSessionController) startLive(request hlsViewRequest) *hlsHTTPRejection {
	if controller == nil || request.operation != hlsViewStart || request.slot >= hlsSessionSlots ||
		!validHLSServiceID(request.service) || !validHLSKey(request.key) {
		return rejectHLS(http.StatusBadRequest, "invalid-view-request", "")
	}
	slot := &controller.slots[request.slot]
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if controller.isClosed() || controller.serviceContext.Err() != nil {
		return rejectHLS(http.StatusServiceUnavailable, "session-ended", "")
	}
	if current := slot.session; current != nil {
		if current.matches(request.service, request.key) {
			if current.isTerminal() {
				return rejectHLS(http.StatusGone, "session-ended", "")
			}
			return nil
		}
		current.stop("session-ended")
		slot.session = nil
		controller.live.Close(hlsNetworkTVID(request.slot))
	}
	selection := slot.selection
	if selection == nil || selection.service != request.service {
		return rejectHLS(http.StatusConflict, "selection-required", "")
	}
	if selection.timer != nil {
		selection.timer.Stop()
	}
	slot.selection = nil

	generationID, rejection := controller.reserveKey(request.key)
	if rejection != nil {
		controller.live.Close(hlsNetworkTVID(request.slot))
		return rejection
	}
	generation, err := controller.cache.begin(request.slot, generationID)
	if err != nil {
		controller.live.Close(hlsNetworkTVID(request.slot))
		return rejectHLS(http.StatusServiceUnavailable, "session-ended", "")
	}
	playlist, err := newHLSMediaPlaylist(request.key)
	if err != nil {
		_ = generation.stop()
		controller.live.Close(hlsNetworkTVID(request.slot))
		return rejectHLS(http.StatusServiceUnavailable, "session-ended", "")
	}
	workerContext, cancel := context.WithCancel(controller.serviceContext)
	session := &hlsSession{
		controller: controller, service: request.service, slot: request.slot, key: request.key,
		generation: generation, playlist: playlist, context: workerContext, cancel: cancel,
		done: make(chan struct{}), ready: make(chan struct{}), segments: make(map[uint64]*hlsRetainedSegment),
	}
	stream, err := controller.live.Open(workerContext, selection.processID)
	startReason := ""
	if workerContext.Err() != nil {
		startReason = "session-ended"
	} else if err != nil || stream == nil {
		startReason = "live-provider-unavailable"
	}
	if startReason != "" {
		if stream != nil {
			_ = stream.Close()
		}
		cancel()
		_ = generation.stop()
		controller.live.Close(hlsNetworkTVID(request.slot))
		return rejectHLS(http.StatusServiceUnavailable, startReason, "")
	}
	session.stream = stream
	controller.mu.Lock()
	if controller.closed {
		controller.mu.Unlock()
		cancel()
		_ = stream.Close()
		_ = generation.stop()
		controller.live.Close(hlsNetworkTVID(request.slot))
		return rejectHLS(http.StatusServiceUnavailable, "session-ended", "")
	}
	controller.sessions[request.key] = session
	controller.mu.Unlock()
	slot.session = session
	if !session.startTimers() {
		slot.session = nil
		session.cancel()
		_ = session.closeStream()
		_ = generation.stop()
		controller.removeSession(session)
		controller.live.Close(hlsNetworkTVID(request.slot))
		return rejectHLS(http.StatusServiceUnavailable, "session-ended", "")
	}
	go session.run()
	return nil
}

func (controller *hlsSessionController) reserveKey(key string) (uint64, *hlsHTTPRejection) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.closed {
		return 0, rejectHLS(http.StatusServiceUnavailable, "session-ended", "")
	}
	if _, used := controller.keys[key]; used {
		return 0, rejectHLS(http.StatusGone, "session-ended", "")
	}
	if len(controller.keys) >= hlsSessionKeyLimit || controller.nextGeneration == 0 {
		return 0, rejectHLS(http.StatusServiceUnavailable, "session-key-limit", "")
	}
	generation := controller.nextGeneration
	controller.nextGeneration++
	controller.keys[key] = struct{}{}
	return generation, nil
}

func (session *hlsSession) startTimers() bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	var ok bool
	session.lifeTimer, ok = session.controller.newTimer(session.controller.timing.worker, func() {
		session.stop("session-ended")
	})
	if !ok || !session.touchLocked() {
		if session.lifeTimer != nil {
			session.lifeTimer.Stop()
			session.lifeTimer = nil
		}
		return false
	}
	return true
}

func (session *hlsSession) run() {
	segmenter, err := newHLSSegmenter(session.emit)
	if err == nil {
		err = session.stream.Copy(segmenter)
		finishErr := segmenter.Finish()
		if finishErr != nil {
			err = finishErr
		}
	}
	_ = session.closeStream()
	reason := hlsSessionReason(err)
	if session.context.Err() != nil {
		reason = "session-ended"
	}
	session.finish(reason)
	close(session.done)
}

func (session *hlsSession) emit(segment hlsSegment) error {
	if session.context.Err() != nil {
		return errSessionEnded
	}
	sequence, err := session.playlist.nextSequence()
	if err != nil || sequence != segment.Sequence {
		return errSessionEnded
	}
	partial, err := session.generation.create(sequence)
	if err != nil {
		return errSessionEnded
	}
	written, writeErr := partial.Write(segment.Data)
	if writeErr != nil || written != len(segment.Data) {
		_ = partial.discard()
		if errors.Is(writeErr, errHLSCacheLimit) {
			return hlsSegmentReason("hls-cache-limit")
		}
		return errSessionEnded
	}
	if _, err := partial.publish(); err != nil {
		_ = partial.discard()
		return errSessionEnded
	}
	session.mu.Lock()
	retired, err := session.playlist.append(sequence, segment.Duration, segment.Discontinuity)
	if err != nil {
		session.mu.Unlock()
		_ = session.generation.retire(sequence)
		return errSessionEnded
	}
	session.segments[sequence] = &hlsRetainedSegment{}
	session.published++
	if session.published >= 3 && !session.readyDone {
		close(session.ready)
		session.readyDone = true
	}
	for _, retention := range retired {
		session.scheduleRetentionLocked(retention.sequence, retention.retainFor)
	}
	session.mu.Unlock()
	return nil
}

func (session *hlsSession) finish(reason string) {
	_ = session.generation.stop()
	session.cancel()
	session.mu.Lock()
	session.terminal = true
	if session.reason == "" {
		session.reason = reason
	}
	if session.reason == "" {
		session.reason = "session-ended"
	}
	if session.idleTimer != nil {
		session.idleTimer.Stop()
		session.idleTimer = nil
	}
	if session.lifeTimer != nil {
		session.lifeTimer.Stop()
		session.lifeTimer = nil
	}
	retained := session.playlist.finish()
	for _, retention := range retained {
		session.scheduleRetentionLocked(retention.sequence, retention.retainFor)
	}
	empty := len(session.segments) == 0
	session.mu.Unlock()
	if empty {
		session.controller.removeSession(session)
	}
}

func (session *hlsSession) stop(reason string) {
	if session == nil {
		return
	}
	session.mu.Lock()
	if !session.terminal && !session.stopping {
		session.stopping = true
		session.reason = reason
		session.cancel()
	}
	done := session.done
	session.mu.Unlock()
	_ = session.closeStream()
	<-done
}

func (session *hlsSession) closeStream() error {
	if session == nil {
		return nil
	}
	session.streamOnce.Do(func() {
		if session.stream != nil {
			session.streamErr = session.stream.Close()
		}
	})
	return session.streamErr
}

func (session *hlsSession) matches(service hlsServiceID, key string) bool {
	return session != nil && session.service == service && session.key == key
}

func (session *hlsSession) isTerminal() bool {
	if session == nil {
		return true
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.terminal || session.stopping
}

// playlistBytesは完成segmentが3件そろうまで通知で待ち、pollingを行わない。
func (controller *hlsSessionController) playlistBytes(ctx context.Context, request hlsViewRequest) ([]byte, *hlsHTTPRejection) {
	if controller == nil || ctx == nil || request.operation != hlsViewPlaylist || request.slot >= hlsSessionSlots {
		return nil, rejectHLS(http.StatusBadRequest, "invalid-view-request", "")
	}
	session, used := controller.lookupSession(request.key)
	if session == nil {
		if used {
			return nil, rejectHLS(http.StatusGone, "session-ended", "")
		}
		return nil, rejectHLS(http.StatusNotFound, "not-found", "")
	}
	if session.slot != request.slot || session.service != request.service {
		return nil, rejectHLS(http.StatusNotFound, "not-found", "")
	}
	if !session.waitUntilReady(ctx) {
		return nil, rejectHLS(http.StatusServiceUnavailable, "playlist-unavailable", "")
	}
	data, ok := session.renderPlaylist()
	if !ok {
		return nil, rejectHLS(http.StatusGone, "session-ended", "")
	}
	return data, nil
}

func (session *hlsSession) waitUntilReady(ctx context.Context) bool {
	session.mu.Lock()
	_ = session.touchLocked()
	if session.published >= 3 {
		session.mu.Unlock()
		return true
	}
	if session.terminal || session.stopping {
		session.mu.Unlock()
		return false
	}
	ready := session.ready
	done := session.done
	session.mu.Unlock()
	timedOut := make(chan struct{})
	timer, ok := session.controller.newTimer(session.controller.timing.wait, func() { close(timedOut) })
	if !ok {
		return false
	}
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-done:
		return session.hasThreeSegments()
	case <-timedOut:
		return session.hasThreeSegments()
	case <-ready:
		return true
	}
}

func (session *hlsSession) hasThreeSegments() bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.published >= 3
}

func (session *hlsSession) renderPlaylist() ([]byte, bool) {
	session.mu.Lock()
	defer session.mu.Unlock()
	sequences, duration := session.currentPlaylistLocked()
	if len(sequences) < 3 {
		return nil, false
	}
	for _, sequence := range sequences {
		if session.segments[sequence] == nil {
			return nil, false
		}
	}
	data, err := session.playlist.render()
	if err != nil {
		return nil, false
	}
	if session.terminal {
		for _, sequence := range sequences {
			session.scheduleRetentionLocked(sequence, duration)
		}
	}
	return data, true
}

func (session *hlsSession) currentPlaylistLocked() ([]uint64, time.Duration) {
	session.playlist.mu.Lock()
	defer session.playlist.mu.Unlock()
	sequences := make([]uint64, len(session.playlist.segments))
	for index, segment := range session.playlist.segments {
		sequences[index] = segment.sequence
	}
	return sequences, playlistDuration(session.playlist.segments)
}

// openSegmentは保持中の完成fileだけを開き、retention後の連番を410として区別する。
func (controller *hlsSessionController) openSegment(request hlsSegmentRequest) (*hlsCacheReadFile, *hlsHTTPRejection) {
	if controller == nil || !validHLSKey(request.key) {
		return nil, rejectHLS(http.StatusNotFound, "not-found", "")
	}
	session, used := controller.lookupSession(request.key)
	if session == nil {
		if used {
			return nil, rejectHLS(http.StatusGone, "session-ended", "")
		}
		return nil, rejectHLS(http.StatusNotFound, "not-found", "")
	}
	session.mu.Lock()
	retained := session.segments[request.sequence] != nil
	wasPublished := request.sequence < session.published
	if !retained {
		session.mu.Unlock()
		if wasPublished {
			return nil, rejectHLS(http.StatusGone, "session-ended", "")
		}
		return nil, rejectHLS(http.StatusNotFound, "not-found", "")
	}
	file, err := session.generation.open(request.sequence)
	if err == nil {
		_ = session.touchLocked()
	}
	session.mu.Unlock()
	if err != nil {
		return nil, rejectHLS(http.StatusGone, "session-ended", "")
	}
	return file, nil
}

func (session *hlsSession) touchLocked() bool {
	if session.terminal || session.stopping {
		return false
	}
	session.idleEpoch++
	epoch := session.idleEpoch
	if session.idleTimer != nil {
		session.idleTimer.Stop()
	}
	var ok bool
	session.idleTimer, ok = session.controller.newTimer(session.controller.timing.idle, func() {
		session.stopIdle(epoch)
	})
	return ok
}

func (session *hlsSession) stopIdle(epoch uint64) {
	session.mu.Lock()
	if session.terminal || session.stopping || session.idleEpoch != epoch {
		session.mu.Unlock()
		return
	}
	session.stopping = true
	session.reason = "session-ended"
	session.cancel()
	done := session.done
	session.mu.Unlock()
	_ = session.closeStream()
	<-done
}

func (session *hlsSession) scheduleRetentionLocked(sequence uint64, retainFor time.Duration) {
	retained := session.segments[sequence]
	if retained == nil || retainFor <= 0 {
		return
	}
	now := session.controller.timing.clock.now()
	deadline := now.Add(retainFor)
	if !deadline.After(retained.deadline) {
		return
	}
	if retained.timer != nil {
		retained.timer.Stop()
	}
	retained.deadline = deadline
	retained.version++
	version := retained.version
	var ok bool
	retained.timer, ok = session.controller.newTimer(retainFor, func() {
		session.retireSegment(sequence, version)
	})
	if !ok {
		retained.timer = nil
	}
}

func (session *hlsSession) retireSegment(sequence, version uint64) {
	session.mu.Lock()
	retained := session.segments[sequence]
	if retained == nil || retained.version != version {
		session.mu.Unlock()
		return
	}
	retained.timer = nil
	if err := session.generation.retire(sequence); err != nil {
		session.mu.Unlock()
		return
	}
	delete(session.segments, sequence)
	empty := session.terminal && len(session.segments) == 0
	session.mu.Unlock()
	if empty {
		session.controller.removeSession(session)
	}
}

func (controller *hlsSessionController) lookupSession(key string) (*hlsSession, bool) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	_, used := controller.keys[key]
	return controller.sessions[key], used
}

func (controller *hlsSessionController) removeSession(session *hlsSession) {
	controller.mu.Lock()
	if controller.sessions[session.key] == session {
		delete(controller.sessions, session.key)
	}
	controller.mu.Unlock()
}

func (controller *hlsSessionController) clearSlotLocked(slotNumber uint8, slot *hlsSessionSlot, reason string) {
	hadUse := slot.selection != nil || slot.session != nil
	if slot.selection != nil && slot.selection.timer != nil {
		slot.selection.timer.Stop()
	}
	slot.selection = nil
	if slot.session != nil {
		slot.session.stop(reason)
		slot.session = nil
	}
	if hadUse {
		controller.live.Close(hlsNetworkTVID(slotNumber))
	}
}

// closeは新しい開始を止め、二つのworkerと全timerを閉じてからcache handleを解放する。
func (controller *hlsSessionController) close() error {
	if controller == nil {
		return nil
	}
	controller.mu.Lock()
	controller.closed = true
	sessions := make([]*hlsSession, 0, len(controller.sessions))
	for _, session := range controller.sessions {
		sessions = append(sessions, session)
	}
	controller.mu.Unlock()
	for slotNumber := range controller.slots {
		slot := &controller.slots[slotNumber]
		slot.mu.Lock()
		controller.clearSlotLocked(uint8(slotNumber), slot, "session-ended")
		slot.mu.Unlock()
	}
	for _, session := range sessions {
		session.shutdownRetention()
	}
	controller.timers.Wait()
	controller.mu.Lock()
	clear(controller.sessions)
	controller.mu.Unlock()
	return controller.cache.close()
}

func (session *hlsSession) shutdownRetention() {
	session.stop("session-ended")
	session.mu.Lock()
	if session.idleTimer != nil {
		session.idleTimer.Stop()
	}
	if session.lifeTimer != nil {
		session.lifeTimer.Stop()
	}
	for _, retained := range session.segments {
		if retained.timer != nil {
			retained.timer.Stop()
			retained.timer = nil
		}
	}
	session.mu.Unlock()
}

func (controller *hlsSessionController) isClosed() bool {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.closed
}

// newTimerはclose開始後の登録を拒否し、停止済みを含むcallbackの完了をcloseから待てるようにする。
func (controller *hlsSessionController) newTimer(delay time.Duration, callback func()) (hlsTimer, bool) {
	if controller == nil || delay <= 0 || callback == nil {
		return hlsStoppedTimer{}, false
	}
	controller.mu.Lock()
	if controller.closed {
		controller.mu.Unlock()
		return hlsStoppedTimer{}, false
	}
	controller.timers.Add(1)
	controller.mu.Unlock()
	managed := &hlsManagedTimer{wait: &controller.timers}
	managed.timer = controller.timing.clock.afterFunc(delay, func() {
		defer managed.complete()
		callback()
	})
	if managed.timer == nil {
		managed.complete()
		return hlsStoppedTimer{}, false
	}
	return managed, true
}

func validHLSServiceID(service hlsServiceID) bool {
	return service.onid != 0 && service.tsid != 0 && service.sid != 0
}

func projectHLSLiveService(service hlsServiceID) LiveService {
	return LiveService{NetworkID: service.onid, TransportStreamID: service.tsid, ServiceID: service.sid}
}

func hlsNetworkTVID(slot uint8) int32 {
	return int32(slot) + 1
}

func hlsSessionReason(err error) string {
	if err == nil {
		return "session-ended"
	}
	for _, reason := range []string{
		"ts-sync-unavailable", "psi-invalid", "single-program-required", "video-unsupported",
		"random-access-unavailable", "timestamp-invalid", "hls-cache-limit", "session-ended",
	} {
		if err.Error() == reason {
			return reason
		}
	}
	return "live-provider-unavailable"
}
