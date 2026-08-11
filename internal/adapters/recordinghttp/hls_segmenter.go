package recordinghttp

import (
	"bytes"
	"errors"
	"math"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/mpegts"
)

const (
	hlsClockHz        = uint64(27_000_000)
	hlsMinimumTicks   = hlsClockHz
	hlsMaximumTicks   = 2 * hlsClockHz
	hlsMaximumSegment = 32 * 1024 * 1024
)

var (
	errTSSyncUnavailable     = hlsSegmentReason("ts-sync-unavailable")
	errPSIInvalid            = hlsSegmentReason("psi-invalid")
	errSingleProgramRequired = hlsSegmentReason("single-program-required")
	errVideoUnsupported      = hlsSegmentReason("video-unsupported")
	errRandomAccess          = hlsSegmentReason("random-access-unavailable")
	errTimestampInvalid      = hlsSegmentReason("timestamp-invalid")
	errSessionEnded          = hlsSegmentReason("session-ended")
)

// hlsSegmentはcacheへ渡す一件の完成済みMPEG-TS segmentを表す。
// Dataは入力packetの内容と順序を保ち、次の境界PCRから求めたDurationを持つ。
type hlsSegment struct {
	Sequence      uint64
	Data          []byte
	Duration      time.Duration
	Discontinuity bool
}

// hlsSegmenterは任意の入力chunkを検証済みTS segmentへ分ける。
// 現在の境界と次の境界がそろうまで一件分だけを保持し、末尾は公開しない。
type hlsSegmenter struct {
	emit       func(hlsSegment) error
	packetizer mpegts.Packetizer
	data       []byte
	sequence   uint64
	active     *hlsBoundary
	candidate  *hlsCandidate

	patCollector mpegts.PSICollector
	patTracker   mpegts.VersionTracker
	pmtCollector mpegts.PSICollector
	pmtTracker   mpegts.VersionTracker
	pmtPID       uint16
	pmtKnown     bool
	failed       error
	ended        bool
	finishErr    error
	lastPCR      uint64
	lastPCRPID   uint16
	lastPCRKnown bool
}

type hlsBoundary struct {
	clock         uint64
	patVersion    byte
	pmtVersion    byte
	programNumber uint16
	pmtPID        uint16
	pcrPID        uint16
	videoPID      uint16
	videoType     byte
	discontinuity bool
}

type hlsCandidate struct {
	start         int
	pat           *mpegts.PAT
	pmt           *mpegts.PMT
	videoPID      uint16
	videoType     byte
	clocks        []hlsObservedClock
	clock         uint64
	lastClock     uint64
	clockKnown    bool
	randomAccess  bool
	pesStart      bool
	discontinuity bool
}

type hlsObservedClock struct {
	pid     uint16
	clock   uint64
	last    uint64
	invalid bool
}

type hlsSegmentReason string

// Errorは内部情報を含まない固定した失敗理由を返す。
func (reason hlsSegmentReason) Error() string { return string(reason) }

// newHLSSegmenterは完成segmentを一件ずつ受け取るcallbackを固定する。
func newHLSSegmenter(emit func(hlsSegment) error) (*hlsSegmenter, error) {
	if emit == nil {
		return nil, errSessionEnded
	}
	return &hlsSegmenter{emit: emit}, nil
}

// Writeはproviderのread境界に依存せず、受理したbyte数を返す。
func (segmenter *hlsSegmenter) Write(data []byte) (int, error) {
	if segmenter == nil || segmenter.ended || segmenter.failed != nil {
		if segmenter != nil && segmenter.ended {
			return 0, errSessionEnded
		}
		if segmenter != nil && segmenter.failed != nil {
			return 0, segmenter.failed
		}
		return 0, errSessionEnded
	}
	err := segmenter.packetizer.Feed(data, segmenter.processPacket)
	if err != nil {
		segmenter.failed = normalizeHLSError(err)
		segmenter.releaseBuffers()
		return 0, segmenter.failed
	}
	return len(data), nil
}

// Finishはpacket未満の末尾を検査し、次の境界がない未完成segmentを破棄する。
func (segmenter *hlsSegmenter) Finish() error {
	if segmenter == nil {
		return errSessionEnded
	}
	if segmenter.ended {
		return segmenter.finishErr
	}
	segmenter.ended = true
	if segmenter.failed != nil {
		segmenter.finishErr = segmenter.failed
		segmenter.releaseBuffers()
		return segmenter.finishErr
	}
	err := segmenter.packetizer.Finish()
	if err != nil {
		segmenter.failed = normalizeHLSError(err)
	} else if segmenter.patCollector.Incomplete() || segmenter.pmtCollector.Incomplete() {
		segmenter.failed = errPSIInvalid
	} else if segmenter.active == nil {
		segmenter.failed = segmenter.initialBoundaryFailure()
	} else if segmenter.candidateRequiresBoundary() {
		segmenter.failed = errRandomAccess
	}
	segmenter.finishErr = segmenter.failed
	segmenter.releaseBuffers()
	return segmenter.finishErr
}

func (segmenter *hlsSegmenter) releaseBuffers() {
	segmenter.emit = nil
	segmenter.packetizer = mpegts.Packetizer{}
	segmenter.data = nil
	segmenter.active = nil
	segmenter.candidate = nil
	segmenter.patCollector = mpegts.PSICollector{}
	segmenter.patTracker = mpegts.VersionTracker{}
	segmenter.pmtCollector = mpegts.PSICollector{}
	segmenter.pmtTracker = mpegts.VersionTracker{}
	segmenter.pmtPID = 0
	segmenter.pmtKnown = false
	segmenter.lastPCR = 0
	segmenter.lastPCRPID = 0
	segmenter.lastPCRKnown = false
}

func (segmenter *hlsSegmenter) initialBoundaryFailure() error {
	if segmenter.candidate == nil || segmenter.candidate.pat == nil || segmenter.candidate.pmt == nil {
		return errPSIInvalid
	}
	if !segmenter.candidate.clockKnown {
		return errTimestampInvalid
	}
	return errRandomAccess
}

func (segmenter *hlsSegmenter) processPacket(packet []byte) error {
	start := len(segmenter.data)
	segmenter.data = append(segmenter.data, packet...)
	if len(segmenter.data) > hlsMaximumSegment {
		return errRandomAccess
	}
	parsed, err := mpegts.ParsePacket(packet)
	if err != nil {
		return errTSSyncUnavailable
	}
	startsCandidate := parsed.PID == 0 && parsed.PayloadUnitStart
	if parsed.Discontinuity && !startsCandidate && (segmenter.active != nil || segmenter.candidate != nil) {
		return errTimestampInvalid
	}
	if err := segmenter.trackProgramClock(parsed); err != nil {
		return err
	}

	if startsCandidate {
		if segmenter.candidateRequiresBoundary() {
			return errRandomAccess
		}
		segmenter.candidate = &hlsCandidate{start: start}
	}
	if segmenter.candidate != nil {
		if parsed.Discontinuity {
			segmenter.candidate.discontinuity = true
		}
		if parsed.HasProgramClock {
			if err := segmenter.candidate.observeClock(parsed.PID, parsed.ProgramClock27MHz); err != nil {
				return err
			}
		}
	}
	if parsed.PID == 0 {
		if err := segmenter.processPAT(packet); err != nil {
			return err
		}
	}
	if segmenter.pmtKnown && parsed.PID == segmenter.pmtPID {
		if err := segmenter.processPMT(packet); err != nil {
			return err
		}
	}
	if segmenter.candidate != nil && segmenter.candidate.pmt != nil && parsed.PID == segmenter.candidate.videoPID {
		if parsed.RandomAccess {
			segmenter.candidate.randomAccess = true
		}
		if parsed.PayloadUnitStart && segmenter.candidate.randomAccess && bytes.HasPrefix(parsed.Payload, []byte{0, 0, 1}) {
			segmenter.candidate.pesStart = true
		}
	}
	if segmenter.candidate != nil && segmenter.candidate.pesStart {
		if err := segmenter.completeCandidate(); err != nil {
			return err
		}
	}
	return segmenter.checkBounds(parsed)
}

func (segmenter *hlsSegmenter) processPAT(packet []byte) error {
	sections, err := segmenter.patCollector.Feed(packet)
	if err != nil {
		return errPSIInvalid
	}
	for _, section := range sections {
		pat, parseErr := mpegts.ParsePAT(section)
		if parseErr != nil {
			if errors.Is(parseErr, mpegts.ErrSingleProgram) {
				return errSingleProgramRequired
			}
			return errPSIInvalid
		}
		if _, trackErr := segmenter.patTracker.Accept(pat.Version, section); trackErr != nil {
			return errPSIInvalid
		}
		if !segmenter.pmtKnown || segmenter.pmtPID != pat.PMTPID {
			segmenter.pmtPID, segmenter.pmtKnown = pat.PMTPID, true
			segmenter.pmtCollector = mpegts.PSICollector{}
			segmenter.pmtTracker = mpegts.VersionTracker{}
		}
		if segmenter.candidate != nil {
			copyPAT := pat
			segmenter.candidate.pat = &copyPAT
		}
	}
	return nil
}

func (segmenter *hlsSegmenter) processPMT(packet []byte) error {
	sections, err := segmenter.pmtCollector.Feed(packet)
	if err != nil {
		return errPSIInvalid
	}
	for _, section := range sections {
		pmt, parseErr := mpegts.ParsePMT(section)
		if parseErr != nil {
			if errors.Is(parseErr, mpegts.ErrProgramClock) {
				return errTimestampInvalid
			}
			return errPSIInvalid
		}
		changed, trackErr := segmenter.pmtTracker.Accept(pmt.Version, section)
		if trackErr != nil {
			return errPSIInvalid
		}
		if segmenter.candidate == nil || segmenter.candidate.pat == nil {
			if changed && segmenter.active != nil {
				return errPSIInvalid
			}
			continue
		}
		if segmenter.candidate.pmt != nil {
			if changed {
				return errPSIInvalid
			}
			continue
		}
		if pmt.ProgramNumber != segmenter.candidate.pat.ProgramNumber {
			return errPSIInvalid
		}
		videoPID, videoType, selectErr := selectHLSVideo(pmt.Streams)
		if selectErr != nil {
			return selectErr
		}
		copyPMT := pmt
		segmenter.candidate.pmt = &copyPMT
		segmenter.candidate.videoPID = videoPID
		segmenter.candidate.videoType = videoType
		if err := segmenter.candidate.selectClock(); err != nil {
			return err
		}
	}
	return nil
}

func selectHLSVideo(streams []mpegts.ElementaryStream) (uint16, byte, error) {
	var pid uint16
	var streamType byte
	for _, stream := range streams {
		if unsupportedHLSVideo(stream.Type) {
			return 0, 0, errVideoUnsupported
		}
		if stream.Type != 0x02 && stream.Type != 0x1b {
			continue
		}
		if pid != 0 {
			return 0, 0, errSingleProgramRequired
		}
		pid, streamType = stream.PID, stream.Type
	}
	if pid == 0 {
		return 0, 0, errVideoUnsupported
	}
	return pid, streamType, nil
}

func unsupportedHLSVideo(streamType byte) bool {
	switch streamType {
	case 0x01, 0x10, 0x24, 0x33:
		return true
	default:
		return false
	}
}

func (candidate *hlsCandidate) observeClock(pid uint16, clock uint64) error {
	for index := range candidate.clocks {
		observed := &candidate.clocks[index]
		if observed.pid != pid {
			continue
		}
		delta := mpegts.ProgramClockDelta(observed.last, clock)
		observed.last = clock
		if delta == 0 || delta > hlsMaximumTicks {
			observed.invalid = true
			if candidate.pmt != nil && candidate.pmt.PCRPID == pid {
				return errTimestampInvalid
			}
		}
		if candidate.clockKnown && candidate.pmt.PCRPID == pid {
			candidate.lastClock = clock
		}
		return nil
	}
	if len(candidate.clocks) >= mpegts.MaxElementaryStreams {
		return errTimestampInvalid
	}
	candidate.clocks = append(candidate.clocks, hlsObservedClock{pid: pid, clock: clock, last: clock})
	return candidate.selectClock()
}

func (candidate *hlsCandidate) selectClock() error {
	if candidate.pmt == nil || candidate.clockKnown {
		return nil
	}
	for _, observed := range candidate.clocks {
		if observed.pid == candidate.pmt.PCRPID {
			if observed.invalid {
				return errTimestampInvalid
			}
			candidate.clock, candidate.lastClock, candidate.clockKnown = observed.clock, observed.last, true
			return nil
		}
	}
	return nil
}

func (segmenter *hlsSegmenter) completeCandidate() error {
	candidate := segmenter.candidate
	if candidate == nil || candidate.pat == nil || candidate.pmt == nil || !candidate.clockKnown {
		return nil
	}
	next := &hlsBoundary{
		clock: candidate.clock, patVersion: candidate.pat.Version, pmtVersion: candidate.pmt.Version,
		programNumber: candidate.pat.ProgramNumber, pmtPID: candidate.pat.PMTPID, pcrPID: candidate.pmt.PCRPID,
		videoPID: candidate.videoPID, videoType: candidate.videoType,
	}
	if segmenter.active == nil {
		segmenter.data = append([]byte(nil), segmenter.data[candidate.start:]...)
		segmenter.active = next
		segmenter.resetProgramClock(next.pcrPID, candidate.lastClock)
		segmenter.candidate = nil
		return nil
	}
	delta := mpegts.ProgramClockDelta(segmenter.active.clock, next.clock)
	if delta == 0 || delta > hlsMaximumTicks {
		return errTimestampInvalid
	}
	if delta < hlsMinimumTicks {
		if candidate.discontinuity || hlsBoundaryChanged(segmenter.active, next) {
			return errTimestampInvalid
		}
		segmenter.candidate = nil
		return nil
	}
	if candidate.start > hlsMaximumSegment {
		return errRandomAccess
	}
	data := segmenter.data[:candidate.start]
	if err := validateHLSSegment(data); err != nil {
		return err
	}
	if segmenter.sequence == math.MaxUint64 {
		return errSessionEnded
	}
	next.discontinuity = candidate.discontinuity || hlsBoundaryChanged(segmenter.active, next)
	segment := hlsSegment{
		Sequence: segmenter.sequence, Data: append([]byte(nil), data...),
		Duration:      time.Duration(delta) * time.Second / time.Duration(hlsClockHz),
		Discontinuity: segmenter.active.discontinuity,
	}
	if err := segmenter.emit(segment); err != nil {
		return err
	}
	segmenter.sequence++
	segmenter.data = append([]byte(nil), segmenter.data[candidate.start:]...)
	segmenter.active = next
	segmenter.resetProgramClock(next.pcrPID, candidate.lastClock)
	segmenter.candidate = nil
	return nil
}

func (segmenter *hlsSegmenter) trackProgramClock(parsed mpegts.Packet) error {
	if segmenter.active == nil || !parsed.HasProgramClock || parsed.PID != segmenter.active.pcrPID {
		return nil
	}
	if parsed.Discontinuity || !segmenter.lastPCRKnown || segmenter.lastPCRPID != parsed.PID {
		segmenter.lastPCR = parsed.ProgramClock27MHz
		segmenter.lastPCRPID = parsed.PID
		segmenter.lastPCRKnown = true
		return nil
	}
	delta := mpegts.ProgramClockDelta(segmenter.lastPCR, parsed.ProgramClock27MHz)
	if delta == 0 || delta > hlsMaximumTicks {
		return errTimestampInvalid
	}
	segmenter.lastPCR = parsed.ProgramClock27MHz
	return nil
}

func (segmenter *hlsSegmenter) resetProgramClock(pid uint16, clock uint64) {
	segmenter.lastPCR = clock
	segmenter.lastPCRPID = pid
	segmenter.lastPCRKnown = true
}

func (segmenter *hlsSegmenter) checkBounds(parsed mpegts.Packet) error {
	if len(segmenter.data) > hlsMaximumSegment {
		return errRandomAccess
	}
	if segmenter.candidate != nil && segmenter.candidate.clockKnown &&
		mpegts.ProgramClockDelta(segmenter.candidate.clock, segmenter.candidate.lastClock) > hlsMaximumTicks {
		return errRandomAccess
	}
	if segmenter.active == nil {
		return nil
	}
	if segmenter.candidate != nil && segmenter.candidate.clockKnown {
		delta := mpegts.ProgramClockDelta(segmenter.active.clock, segmenter.candidate.clock)
		if delta == 0 || delta > hlsMaximumTicks {
			return errTimestampInvalid
		}
	}
	if parsed.HasProgramClock && parsed.PID == segmenter.active.pcrPID &&
		mpegts.ProgramClockDelta(segmenter.active.clock, parsed.ProgramClock27MHz) > hlsMaximumTicks {
		return errRandomAccess
	}
	return nil
}

func (segmenter *hlsSegmenter) candidateRequiresBoundary() bool {
	if segmenter.active == nil || segmenter.candidate == nil {
		return false
	}
	candidate := segmenter.candidate
	if candidate.discontinuity {
		return true
	}
	if candidate.pat != nil && (candidate.pat.Version != segmenter.active.patVersion ||
		candidate.pat.ProgramNumber != segmenter.active.programNumber || candidate.pat.PMTPID != segmenter.active.pmtPID) {
		return true
	}
	if candidate.pmt != nil && (candidate.pmt.Version != segmenter.active.pmtVersion ||
		candidate.pmt.PCRPID != segmenter.active.pcrPID || candidate.videoPID != segmenter.active.videoPID ||
		candidate.videoType != segmenter.active.videoType) {
		return true
	}
	return false
}

func hlsBoundaryChanged(current, next *hlsBoundary) bool {
	return current.patVersion != next.patVersion || current.pmtVersion != next.pmtVersion ||
		current.programNumber != next.programNumber || current.pmtPID != next.pmtPID || current.pcrPID != next.pcrPID ||
		current.videoPID != next.videoPID || current.videoType != next.videoType
}

func validateHLSSegment(data []byte) error {
	if len(data) == 0 || len(data)%mpegts.PacketBytes != 0 || len(data) > hlsMaximumSegment {
		return errRandomAccess
	}
	var patCollector, pmtCollector mpegts.PSICollector
	var patTracker, pmtTracker mpegts.VersionTracker
	var pat *mpegts.PAT
	var pmt *mpegts.PMT
	var pmtPID uint16
	randomAccessSeen := false
	randomAccess := false
	for offset := 0; offset < len(data); offset += mpegts.PacketBytes {
		packet := data[offset : offset+mpegts.PacketBytes]
		parsed, err := mpegts.ParsePacket(packet)
		if err != nil {
			return errTSSyncUnavailable
		}
		if parsed.Discontinuity && offset != 0 {
			return errTimestampInvalid
		}
		if parsed.PID == 0 {
			sections, feedErr := patCollector.Feed(packet)
			if feedErr != nil {
				return errPSIInvalid
			}
			for _, section := range sections {
				value, parseErr := mpegts.ParsePAT(section)
				if parseErr != nil {
					return errPSIInvalid
				}
				changed, trackErr := patTracker.Accept(value.Version, section)
				if trackErr != nil || pat != nil && changed {
					return errPSIInvalid
				}
				pat, pmtPID = &value, value.PMTPID
			}
		}
		if pmtPID != 0 && parsed.PID == pmtPID {
			sections, feedErr := pmtCollector.Feed(packet)
			if feedErr != nil {
				return errPSIInvalid
			}
			for _, section := range sections {
				value, parseErr := mpegts.ParsePMT(section)
				if parseErr != nil || pat == nil || value.ProgramNumber != pat.ProgramNumber {
					return errPSIInvalid
				}
				changed, trackErr := pmtTracker.Accept(value.Version, section)
				if trackErr != nil || pmt != nil && changed {
					return errPSIInvalid
				}
				pmt = &value
			}
		}
		if pmt != nil {
			videoPID, _, selectErr := selectHLSVideo(pmt.Streams)
			if selectErr != nil {
				return selectErr
			}
			if parsed.PID == videoPID {
				if parsed.RandomAccess {
					randomAccessSeen = true
				}
				if parsed.PayloadUnitStart && randomAccessSeen && bytes.HasPrefix(parsed.Payload, []byte{0, 0, 1}) {
					randomAccess = true
				}
			}
		}
	}
	if patCollector.Incomplete() || pmtCollector.Incomplete() {
		return errPSIInvalid
	}
	if pat == nil || pmt == nil || !randomAccess {
		return errRandomAccess
	}
	return nil
}

func normalizeHLSError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, mpegts.ErrSync) || errors.Is(err, mpegts.ErrTruncated) || errors.Is(err, mpegts.ErrPacket) {
		return errTSSyncUnavailable
	}
	if errors.Is(err, mpegts.ErrPSI) {
		return errPSIInvalid
	}
	return err
}
