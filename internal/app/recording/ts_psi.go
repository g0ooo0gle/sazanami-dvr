package recording

import (
	"bytes"
	"github.com/g0ooo0gle/sazanami-dvr/internal/mpegts"
)

type psiIssue uint8

const (
	psiOK psiIssue = iota
	psiContinuity
	psiCRC
	psiStructure
	psiLimit
)

// recordingPSIは共有collectorのstrict挙動を変えず、録画専用の1,024 bytes上限を守る。
type recordingPSI struct {
	section        [1024]byte
	used, want     int
	previous       [188]byte
	cc             byte
	known, waiting bool
}

func (psi *recordingPSI) incomplete() bool { return psi.used != 0 }
func (psi *recordingPSI) reset()           { *psi = recordingPSI{waiting: true} }

// Feedは上限内のPSI sectionを組み立て、重複と品質異常を区別して返す。
func (psi *recordingPSI) Feed(packet []byte, p mpegts.Packet) ([][]byte, bool, psiIssue) {
	if !p.HasPayload {
		if p.Discontinuity {
			psi.reset()
		}
		return nil, false, psiOK
	}
	if psi.known && !p.Discontinuity {
		if p.ContinuityCounter == psi.cc && bytes.Equal(packet, psi.previous[:]) {
			return nil, true, psiOK
		}
		if p.ContinuityCounter != (psi.cc+1)&15 {
			psi.reset()
			return nil, false, psiContinuity
		}
	}
	if p.Discontinuity {
		psi.reset()
	}
	if psi.waiting && !p.PayloadUnitStart {
		return nil, false, psiOK
	}
	psi.known = true
	psi.cc = p.ContinuityCounter
	copy(psi.previous[:], packet)
	data := p.Payload
	var sections [][]byte
	if p.PayloadUnitStart {
		pointer := int(data[0])
		if pointer > len(data)-1 {
			psi.reset()
			return nil, false, psiStructure
		}
		if psi.used != 0 {
			rest, section, issue := psi.append(data[1 : 1+pointer])
			if issue != psiOK {
				psi.reset()
				return nil, false, issue
			}
			if section == nil || !psiStuffing(rest) {
				psi.reset()
				return nil, false, psiStructure
			}
			sections = append(sections, section)
		}
		psi.used, psi.want, psi.waiting = 0, 0, false
		data = data[1+pointer:]
	} else if psi.used == 0 {
		return nil, false, psiOK
	}
	for len(data) > 0 {
		if psi.used == 0 && data[0] == 0xff {
			if !psiStuffing(data) {
				psi.reset()
				return nil, false, psiStructure
			}
			break
		}
		rest, section, issue := psi.append(data)
		if issue != psiOK {
			psi.reset()
			return nil, false, issue
		}
		if section == nil {
			break
		}
		sections = append(sections, section)
		data = rest
		if !p.PayloadUnitStart && !psiStuffing(data) {
			psi.reset()
			return nil, false, psiStructure
		}
	}
	return sections, false, psiOK
}

func (psi *recordingPSI) append(data []byte) ([]byte, []byte, psiIssue) {
	for len(data) > 0 {
		target := 3
		if psi.want > 0 {
			target = psi.want
		}
		count := min(len(data), target-psi.used)
		copy(psi.section[psi.used:], data[:count])
		psi.used += count
		data = data[count:]
		if psi.used == 3 && psi.want == 0 {
			psi.want = 3 + (int(psi.section[1]&15)<<8 | int(psi.section[2]))
			if psi.want > len(psi.section) {
				return nil, nil, psiLimit
			}
			if psi.want < 12 {
				return nil, nil, psiStructure
			}
		}
		if psi.want > 0 && psi.used == psi.want {
			if !mpegts.ValidCRC(psi.section[:psi.used]) {
				return nil, nil, psiCRC
			}
			section := append([]byte(nil), psi.section[:psi.used]...)
			psi.used, psi.want = 0, 0
			return data, section, psiOK
		}
	}
	return nil, nil, psiOK
}

func psiStuffing(data []byte) bool {
	for _, b := range data {
		if b != 0xff {
			return false
		}
	}
	return true
}

func pmtIssue(section []byte) psiIssue {
	if len(section) < 16 || section[0] != 2 {
		return psiStructure
	}
	offset := 12 + int(section[10]&15)<<8 + int(section[11])
	end := len(section) - 4
	count := 0
	if offset > end {
		return psiStructure
	}
	for offset < end {
		if end-offset < 5 {
			return psiStructure
		}
		count++
		if count > 64 {
			return psiLimit
		}
		offset += 5 + (int(section[offset+3]&15)<<8 | int(section[offset+4]))
		if offset > end {
			return psiStructure
		}
	}
	return psiOK
}
