package recording

import (
	"bytes"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
	"github.com/g0ooo0gle/sazanami-dvr/internal/mpegts"
	"slices"
)

const maxQualityPIDs = 64

func addQuality(q *core.QualitySummary, counter *int64, count int64) {
	if count <= 0 {
		return
	}
	q.Status = core.QualityDegraded
	if count >= 2147483647-*counter {
		*counter = 2147483647
		q.CountersSaturated = true
		return
	}
	*counter += count
}

// parseRecordingPacketは観測用scratchだけのTEIを外し、原packetは変更しない。
func parseRecordingPacket(data []byte) (mpegts.Packet, bool, error) {
	tei := len(data) == 188 && data[1]&0x80 != 0
	if !tei {
		p, err := mpegts.ParsePacket(data)
		return p, false, err
	}
	var scratch [188]byte
	copy(scratch[:], data)
	scratch[1] &= 0x7f
	p, err := mpegts.ParsePacket(scratch[:])
	return p, true, err
}

type ccObservation struct {
	packet [188]byte
	cc     byte
	known  bool
}
type tsQualityTracker struct {
	pids       map[uint16]ccObservation
	configured bool
}

func (tracker *tsQualityTracker) prioritize(pmtPID uint16, q *core.QualitySummary) {
	if tracker.pids == nil {
		tracker.pids = make(map[uint16]ccObservation, maxQualityPIDs)
	}
	for _, pid := range []uint16{0, pmtPID} {
		if _, ok := tracker.pids[pid]; ok {
			continue
		}
		if len(tracker.pids) == maxQualityPIDs {
			var victim uint16
			for other := range tracker.pids {
				if other != 0 && other != pmtPID && other > victim {
					victim = other
				}
			}
			delete(tracker.pids, victim)
			q.ObservationLimited = true
		}
		tracker.pids[pid] = ccObservation{}
	}
}

func (tracker *tsQualityTracker) reset() {
	for pid := range tracker.pids {
		tracker.pids[pid] = ccObservation{}
	}
}

// configureはPAT・PMTを優先し、残りのPMT PIDを番号順に選ぶ。
func (tracker *tsQualityTracker) configure(pmtPID uint16, pmt mpegts.PMT, q *core.QualitySummary) {
	ids := []uint16{pmt.PCRPID}
	for _, s := range pmt.Streams {
		ids = append(ids, s.PID)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	selected := make(map[uint16]ccObservation, maxQualityPIDs)
	selected[0] = tracker.pids[0]
	selected[pmtPID] = tracker.pids[pmtPID]
	for _, pid := range ids {
		if pid == 0 || pid == pmtPID {
			continue
		}
		if len(selected) == maxQualityPIDs {
			q.ObservationLimited = true
			continue
		}
		selected[pid] = tracker.pids[pid]
	}
	tracker.pids = selected
	tracker.configured = true
}

func (tracker *tsQualityTracker) observe(packet []byte, p mpegts.Packet, q *core.QualitySummary) {
	if p.PID == 0x1fff {
		return
	}
	if tracker.pids == nil {
		tracker.pids = make(map[uint16]ccObservation, maxQualityPIDs)
		tracker.pids[0] = ccObservation{}
	}
	previous, ok := tracker.pids[p.PID]
	if !ok && (len(tracker.pids) == maxQualityPIDs || tracker.configured) {
		q.ObservationLimited = true
		return
	}
	if p.Discontinuity {
		previous.known = false
	}
	if p.HasPayload {
		if previous.known && !p.Discontinuity {
			if p.ContinuityCounter == previous.cc && bytes.Equal(packet, previous.packet[:]) {
				addQuality(q, &q.CCDuplicateEvents, 1)
			} else if p.ContinuityCounter != (previous.cc+1)&0x0f {
				addQuality(q, &q.CCGapEvents, 1)
			}
		}
		previous.cc = p.ContinuityCounter
		previous.known = true
	}
	copy(previous.packet[:], packet)
	tracker.pids[p.PID] = previous
}
