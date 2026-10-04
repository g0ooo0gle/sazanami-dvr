package recording

import core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"

const recordingSyncBytes = 64 * 1024
const recoveryPacketBytes = 5 * 188

// tsFramerは固定領域でpacketを組み立てる。同期喪失後だけ五候補を要求する。
type tsFramer struct {
	buffer     [recordingSyncBytes]byte
	used       int
	start      int
	lost       bool
	generation uint64
}

// Writeは完全なpacketだけを渡し、同期喪失時は固定領域で再同期を試みる。
func (framer *tsFramer) Write(data []byte, q *core.QualitySummary, emit func([]byte) (int64, error)) (int64, error) {
	var written int64
	for len(data) > 0 {
		if framer.start > 0 {
			copy(framer.buffer[:], framer.buffer[framer.start:framer.start+framer.used])
			framer.start = 0
		}
		count := min(len(data), len(framer.buffer)-framer.used)
		copy(framer.buffer[framer.used:], data[:count])
		framer.used += count
		data = data[count:]
		for {
			if !framer.lost {
				if framer.used < 188 {
					break
				}
				if framer.buffer[framer.start] != 0x47 {
					framer.lost = true
					addQuality(q, &q.SyncLossEvents, 1)
					continue
				}
				n, err := emit(framer.buffer[framer.start : framer.start+188])
				written += n
				if err != nil {
					framer.used, framer.start = 0, 0
					return written, err
				}
				framer.discard(188)
				continue
			}
			if framer.used < recoveryPacketBytes {
				break
			}
			position := -1
			for offset := 0; offset <= framer.used-recoveryPacketBytes; offset++ {
				if framer.buffer[framer.start+offset] != 0x47 {
					continue
				}
				valid := true
				for i := 0; i < 5; i++ {
					_, _, err := parseRecordingPacket(framer.buffer[framer.start+offset+i*188 : framer.start+offset+(i+1)*188])
					if err != nil {
						valid = false
						break
					}
				}
				if valid {
					position = offset
					break
				}
			}
			if position < 0 {
				drop := framer.used - (recoveryPacketBytes - 1)
				addQuality(q, &q.SyncDiscardedBytes, int64(drop))
				framer.discard(drop)
				break
			}
			addQuality(q, &q.SyncDiscardedBytes, int64(position))
			framer.discard(position)
			framer.lost = false
			framer.generation++
			addQuality(q, &q.SyncRecoveredEvents, 1)
		}
	}
	return written, nil
}

func (framer *tsFramer) discard(count int) {
	framer.start += count
	framer.used -= count
}

// Finishは未保存の末尾を品質情報へ記録し、保持領域を空にする。
func (framer *tsFramer) Finish(q *core.QualitySummary) {
	if framer.lost {
		addQuality(q, &q.SyncDiscardedBytes, int64(framer.used))
	} else {
		addQuality(q, &q.TrailingIncompleteBytes, int64(framer.used))
	}
	framer.used, framer.start = 0, 0
}
