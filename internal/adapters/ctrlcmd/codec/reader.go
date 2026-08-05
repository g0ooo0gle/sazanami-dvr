package codec

import (
	"encoding/binary"
	"time"
	"unicode/utf16"
)

var japanStandardTime = time.FixedZone("Asia/Tokyo", 9*60*60)

type logicalBudget struct{ remaining int }

// Readerは1つの検証対象sliceを順方向に読み、nestingとlogical item数を共有上限で管理する。
type Reader struct {
	data   []byte
	pos    int
	depth  int
	limits Limits
	budget *logicalBudget
}

// NewReaderは指定上限を検証してReaderを生成する。data自体はcopyしない。
func NewReader(data []byte, limits Limits) (*Reader, error) {
	limits, err := limits.normalized()
	if err != nil {
		return nil, err
	}
	return &Reader{data: data, limits: limits, budget: &logicalBudget{remaining: limits.LogicalItems}}, nil
}

// Remainingは未読byte数を返す。
func (r *Reader) Remaining() int { return len(r.data) - r.pos }

// Positionはslice先頭からの現在位置を返す。
func (r *Reader) Position() int { return r.pos }

func (r *Reader) consumeItem() error {
	if r.budget.remaining <= 0 {
		return failure(OverLimit, "logical-item-budget", r.pos, 0)
	}
	r.budget.remaining--
	return nil
}

func (r *Reader) take(width int) ([]byte, error) {
	if width < 0 || width > r.Remaining() {
		return nil, failure(Truncated, "field-width", r.pos, int64(width))
	}
	value := r.data[r.pos : r.pos+width]
	r.pos += width
	return value, nil
}

// I32はlittle-endianのsigned 32-bit整数を1つ読む。
func (r *Reader) I32() (int32, error) {
	if err := r.consumeItem(); err != nil {
		return 0, err
	}
	value, err := r.take(4)
	if err != nil {
		return 0, err
	}
	return int32(binary.LittleEndian.Uint32(value)), nil
}

// I64はlittle-endianのsigned 64-bit整数を1つ読む。
func (r *Reader) I64() (int64, error) {
	if err := r.consumeItem(); err != nil {
		return 0, err
	}
	value, err := r.take(8)
	if err != nil {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint64(value)), nil
}

// U8は符号なし8-bit整数を1つ読む。
func (r *Reader) U8() (uint8, error) {
	if err := r.consumeItem(); err != nil {
		return 0, err
	}
	value, err := r.take(1)
	if err != nil {
		return 0, err
	}
	return value[0], nil
}

// U16はlittle-endianのunsigned 16-bit整数を1つ読む。
func (r *Reader) U16() (uint16, error) {
	if err := r.consumeItem(); err != nil {
		return 0, err
	}
	value, err := r.take(2)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(value), nil
}

// U32はlittle-endianのunsigned 32-bit整数を1つ読む。
func (r *Reader) U32() (uint32, error) {
	if err := r.consumeItem(); err != nil {
		return 0, err
	}
	value, err := r.take(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(value), nil
}

// SystemTimeはCtrlCmdの16-byte日本標準時をUTCへ変換して読む。
func (r *Reader) SystemTime() (time.Time, error) {
	start := r.pos
	fields := [8]uint16{}
	for index := range fields {
		value, err := r.U16()
		if err != nil {
			return time.Time{}, err
		}
		fields[index] = value
	}
	if fields[0] < 1 || fields[1] < 1 || fields[1] > 12 || fields[2] > 6 || fields[3] < 1 ||
		fields[4] > 23 || fields[5] > 59 || fields[6] > 59 || fields[7] > 999 {
		return time.Time{}, failure(Malformed, "system-time-field", start, 16)
	}
	local := time.Date(int(fields[0]), time.Month(fields[1]), int(fields[3]), int(fields[4]), int(fields[5]),
		int(fields[6]), int(fields[7])*int(time.Millisecond), japanStandardTime)
	if local.Year() != int(fields[0]) || local.Month() != time.Month(fields[1]) || local.Day() != int(fields[3]) ||
		uint16(local.Weekday()) != fields[2] {
		return time.Time{}, failure(Malformed, "system-time-calendar", start, 16)
	}
	return local.UTC(), nil
}

// StringはextentとNUL終端を含むCtrlCmd UTF-16LE文字列を厳格に読む。
func (r *Reader) String() (string, error) {
	start := r.pos
	extent, err := r.I32()
	if err != nil {
		return "", err
	}
	if extent < 6 || extent%2 != 0 {
		return "", failure(Malformed, "string-extent", start, int64(extent))
	}
	if int64(extent) > int64(r.limits.StringExtent) {
		return "", failure(OverLimit, "string-extent", start, int64(extent))
	}
	payloadBytes := int(extent) - 4
	data, err := r.take(payloadBytes)
	if err != nil {
		return "", err
	}
	units := make([]uint16, len(data)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(data[i*2 : i*2+2])
	}
	if units[len(units)-1] != 0 {
		return "", failure(Malformed, "missing-string-terminator", start, int64(extent))
	}
	units = units[:len(units)-1]
	for i := 0; i < len(units); i++ {
		u := units[i]
		switch {
		case u == 0:
			return "", failure(Malformed, "embedded-nul", start, int64(extent))
		case u >= 0xD800 && u <= 0xDBFF:
			if i+1 >= len(units) || units[i+1] < 0xDC00 || units[i+1] > 0xDFFF {
				return "", failure(Malformed, "unpaired-high-surrogate", start, int64(extent))
			}
			i++
		case u >= 0xDC00 && u <= 0xDFFF:
			return "", failure(Malformed, "unpaired-low-surrogate", start, int64(extent))
		}
	}
	return string(utf16.Decode(units)), nil
}

// Structureはextentで区切られた範囲だけを子Readerへ渡し、読み残しも不正として扱う。
func (r *Reader) Structure(fn func(*Reader) error) error {
	if fn == nil {
		return failure(Internal, "nil-structure-reader", r.pos, 0)
	}
	if r.depth >= r.limits.Depth {
		return failure(OverLimit, "structure-depth", r.pos, int64(r.depth+1))
	}
	start := r.pos
	extent, err := r.I32()
	if err != nil {
		return err
	}
	if extent < 4 {
		return failure(Malformed, "structure-extent", start, int64(extent))
	}
	if int64(extent) > int64(r.limits.StructureExtent) {
		return failure(OverLimit, "structure-extent", start, int64(extent))
	}
	remaining := int(extent) - 4
	if remaining > r.Remaining() {
		return failure(Truncated, "structure-body", r.pos, int64(remaining))
	}
	child := &Reader{data: r.data[start : start+int(extent)], pos: 4, depth: r.depth + 1, limits: r.limits, budget: r.budget}
	if err := fn(child); err != nil {
		return err
	}
	if err := child.Exact(); err != nil {
		return err
	}
	r.pos = start + int(extent)
	return nil
}

// Vectorは要素数と最小要素幅の両方で入力を検証する。
// 実現可能性の判定は乗算を避け、極端な値でも整数overflowを起こさない。
func (r *Reader) Vector(minimumElementBytes, effectiveMax int, fn func(*Reader, int) error) error {
	if fn == nil {
		return failure(Internal, "nil-vector-reader", r.pos, 0)
	}
	if r.depth >= r.limits.Depth {
		return failure(OverLimit, "vector-depth", r.pos, int64(r.depth+1))
	}
	start := r.pos
	extent, err := r.I32()
	if err != nil {
		return err
	}
	count, err := r.I32()
	if err != nil {
		return err
	}
	if extent < 8 || count < 0 {
		return failure(Malformed, "vector-header", start, int64(extent))
	}
	if int64(extent) > int64(r.limits.StructureExtent) {
		return failure(OverLimit, "vector-extent", start, int64(extent))
	}
	maximum := r.limits.VectorElements
	if effectiveMax > 0 && effectiveMax < maximum {
		maximum = effectiveMax
	}
	if int64(count) > int64(maximum) {
		return failure(OverLimit, "vector-count", start+4, int64(count))
	}
	remaining := int(extent) - 8
	if remaining < 0 || remaining > r.Remaining() {
		return failure(Truncated, "vector-body", r.pos, int64(remaining))
	}
	if minimumElementBytes < 1 || (count > 0 && int64(minimumElementBytes) > int64(remaining)/int64(count)) {
		return failure(Malformed, "vector-count-impossible", start+4, int64(count))
	}
	child := &Reader{data: r.data[start : start+int(extent)], pos: 8, depth: r.depth + 1, limits: r.limits, budget: r.budget}
	for i := 0; i < int(count); i++ {
		if err := fn(child, i); err != nil {
			return err
		}
	}
	if err := child.Exact(); err != nil {
		return err
	}
	r.pos = start + int(extent)
	return nil
}

// Exactは検証対象を過不足なく読み終えたことを確認する。
func (r *Reader) Exact() error {
	if r.pos != len(r.data) {
		return failure(Malformed, "trailing-bytes", r.pos, int64(r.Remaining()))
	}
	return nil
}
