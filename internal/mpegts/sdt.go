package mpegts

// MaxSDTSectionBytes は初期設定で受け付けるSDT section一件の上限である。
const MaxSDTSectionBytes = 1 << 10

// SDT はSDT actualの世代情報と、そのsectionに記載されたservice IDを表す。
// 記述子の内容は初期設定の所属確認に使わないため保持しない。
type SDT struct {
	TransportStreamID uint16
	OriginalNetworkID uint16
	Version           byte
	CurrentNext       bool
	SectionNumber     byte
	LastSectionNumber byte
	ServiceIDs        []uint16
}

// ParseSDT はSDT actual sectionの構造、CRC、可変長descriptor境界を検証する。
// table_idはここで判定し、0x42だけを受け付ける。
func ParseSDT(section []byte) (SDT, error) {
	if len(section) < 15 || len(section) > MaxSDTSectionBytes || section[0] != 0x42 ||
		section[1]&0x80 == 0 || 3+((int(section[1]&0x0f)<<8)|int(section[2])) != len(section) ||
		section[6] > section[7] || !ValidCRC(section) {
		return SDT{}, ErrPSI
	}
	result := SDT{
		TransportStreamID: uint16(section[3])<<8 | uint16(section[4]),
		Version:           (section[5] >> 1) & 0x1f,
		CurrentNext:       section[5]&1 != 0,
		SectionNumber:     section[6],
		LastSectionNumber: section[7],
		OriginalNetworkID: uint16(section[8])<<8 | uint16(section[9]),
		ServiceIDs:        make([]uint16, 0, 16),
	}
	seen := make(map[uint16]struct{}, 16)
	for offset, end := 11, len(section)-4; offset < end; {
		if end-offset < 5 {
			return SDT{}, ErrPSI
		}
		serviceID := uint16(section[offset])<<8 | uint16(section[offset+1])
		if _, duplicate := seen[serviceID]; duplicate {
			return SDT{}, ErrPSI
		}
		seen[serviceID] = struct{}{}
		descriptorLength := int(section[offset+3]&0x0f)<<8 | int(section[offset+4])
		descriptorEnd := offset + 5 + descriptorLength
		if descriptorEnd > end {
			return SDT{}, ErrPSI
		}
		for descriptorOffset := offset + 5; descriptorOffset < descriptorEnd; {
			if descriptorEnd-descriptorOffset < 2 {
				return SDT{}, ErrPSI
			}
			next := descriptorOffset + 2 + int(section[descriptorOffset+1])
			if next > descriptorEnd {
				return SDT{}, ErrPSI
			}
			descriptorOffset = next
		}
		result.ServiceIDs = append(result.ServiceIDs, serviceID)
		offset = descriptorEnd
	}
	return result, nil
}
