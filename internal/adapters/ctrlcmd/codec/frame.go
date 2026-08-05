package codec

import "encoding/binary"

// Frameは検証済みのCtrlCmd外側frameを表す。
type Frame struct {
	Code int32
	Body []byte
}

// ParseRequestFrameはheaderと宣言body長を照合し、余剰byteも不足byteも拒否する。
func ParseRequestFrame(input []byte, limits Limits) (Frame, error) {
	limits, err := limits.normalized()
	if err != nil {
		return Frame{}, err
	}
	if len(input) < HeaderSize {
		return Frame{}, failure(Truncated, "outer-header", len(input), HeaderSize)
	}
	code := int32(binary.LittleEndian.Uint32(input[0:4]))
	declared := int32(binary.LittleEndian.Uint32(input[4:8]))
	if declared < 0 {
		return Frame{}, failure(Malformed, "negative-body-length", 4, int64(declared))
	}
	if int64(declared) > int64(limits.RequestBody) {
		return Frame{}, failure(OverLimit, "request-body", 4, int64(declared))
	}
	if int64(len(input)-HeaderSize) != int64(declared) {
		category := Malformed
		if len(input)-HeaderSize < int(declared) {
			category = Truncated
		}
		return Frame{}, failure(category, "body-length-mismatch", HeaderSize, int64(declared))
	}
	return Frame{Code: code, Body: input[HeaderSize:]}, nil
}
