package codec

import (
	"encoding/binary"
	"io"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// WriterはCtrlCmd primitiveをlittle-endianで逐次出力し、実書込byte数を追跡する。
type Writer struct {
	w       io.Writer
	written int64
	limits  Limits
}

// NewWriterは出力先と上限を検証してWriterを生成する。
func NewWriter(destination io.Writer, limits Limits) (*Writer, error) {
	if destination == nil {
		return nil, failure(Internal, "nil-writer", 0, 0)
	}
	limits, err := limits.normalized()
	if err != nil {
		return nil, err
	}
	return &Writer{w: destination, limits: limits}, nil
}

// BytesWrittenは出力先が受理したbyte数を返す。
func (w *Writer) BytesWritten() int64 { return w.written }

func (w *Writer) write(data []byte) error {
	for len(data) > 0 {
		n, err := w.w.Write(data)
		if n < 0 || n > len(data) {
			return failure(Internal, "invalid-write-count", int(w.written), int64(n))
		}
		if n > 0 {
			w.written += int64(n)
			data = data[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return failure(PeerDisconnect, "zero-progress-write", int(w.written), int64(len(data)))
		}
	}
	return nil
}

// I32はsigned 32-bit整数をlittle-endianで書く。
func (w *Writer) I32(value int32) error {
	var data [4]byte
	binary.LittleEndian.PutUint32(data[:], uint32(value))
	return w.write(data[:])
}

// I64はsigned 64-bit整数をlittle-endianで書く。
func (w *Writer) I64(value int64) error {
	var data [8]byte
	binary.LittleEndian.PutUint64(data[:], uint64(value))
	return w.write(data[:])
}

// U8は、符号なし8-bit整数を1つ書く。
func (w *Writer) U8(value uint8) error {
	return w.write([]byte{value})
}

// U16はunsigned 16-bit整数をlittle-endianで書く。
func (w *Writer) U16(value uint16) error {
	var data [2]byte
	binary.LittleEndian.PutUint16(data[:], value)
	return w.write(data[:])
}

// U32はunsigned 32-bit整数をlittle-endianで書く。
func (w *Writer) U32(value uint32) error {
	var data [4]byte
	binary.LittleEndian.PutUint32(data[:], value)
	return w.write(data[:])
}

// SystemTimeは時刻をCtrlCmdの16-byte日本標準時で書く。
func (w *Writer) SystemTime(value time.Time) error {
	if value.IsZero() {
		return failure(Malformed, "zero-system-time", int(w.written), 0)
	}
	local := value.UTC().In(japanStandardTime)
	if local.Year() < 1 || local.Year() > 65_535 {
		return failure(OverLimit, "system-time-year", int(w.written), int64(local.Year()))
	}
	fields := [...]uint16{
		uint16(local.Year()), uint16(local.Month()), uint16(local.Weekday()), uint16(local.Day()),
		uint16(local.Hour()), uint16(local.Minute()), uint16(local.Second()), uint16(local.Nanosecond() / int(time.Millisecond)),
	}
	for _, field := range fields {
		if err := w.U16(field); err != nil {
			return err
		}
	}
	return nil
}

// Bytesは、呼び出し側で検証済みのbyte列をそのまま書く。
// WriteFrame内では、宣言済みのbody sizeを越える書き込みをboundedWriterが拒否する。
func (w *Writer) Bytes(value []byte) error {
	return w.write(value)
}

// StringSizeは、CtrlCmd UTF-16LE文字列を出力したときのextentを返す。
// Stringと同じ規則で妥当性と上限を先に検証するため、応答サイズの事前計算に利用できる。
func StringSize(value string, limits Limits) (int64, error) {
	limits, err := limits.normalized()
	if err != nil {
		return 0, err
	}
	unitCount := 0
	maxUnits := (limits.StringExtent - 6) / 2
	for offset := 0; offset < len(value); {
		r, width := utf8.DecodeRuneInString(value[offset:])
		if r == utf8.RuneError && width == 1 {
			return 0, failure(Malformed, "invalid-utf8-input", offset, int64(len(value)))
		}
		if r == 0 || (r >= 0xD800 && r <= 0xDFFF) {
			return 0, failure(Malformed, "invalid-string-codepoint", offset, int64(r))
		}
		if r <= 0xFFFF {
			unitCount++
		} else {
			unitCount += 2
		}
		if unitCount > maxUnits {
			return 0, failure(OverLimit, "string-extent", offset, int64(4+(unitCount+1)*2))
		}
		offset += width
	}
	extent := int64(4 + (unitCount+1)*2)
	if extent > int64(limits.StringExtent) || extent > 1<<31-1 {
		return 0, failure(OverLimit, "string-extent", len(value), extent)
	}
	return extent, nil
}

// StringはCtrlCmdのUTF-16LE文字列を出力する。
// 上限と文字の妥当性を先に確定し、不正な値で出力先へ途中まで書かない。
func (w *Writer) String(value string) error {
	extent, err := StringSize(value, w.limits)
	if err != nil {
		return err
	}
	if err := w.I32(int32(extent)); err != nil {
		return err
	}
	var encoded [2]byte
	for _, r := range value {
		if r <= 0xFFFF {
			binary.LittleEndian.PutUint16(encoded[:], uint16(r))
			if err := w.write(encoded[:]); err != nil {
				return err
			}
			continue
		}
		high, low := utf16.EncodeRune(r)
		binary.LittleEndian.PutUint16(encoded[:], uint16(high))
		if err := w.write(encoded[:]); err != nil {
			return err
		}
		binary.LittleEndian.PutUint16(encoded[:], uint16(low))
		if err := w.write(encoded[:]); err != nil {
			return err
		}
	}
	return w.U16(0)
}

type boundedWriter struct {
	w         io.Writer
	remaining int64
}

// Writeは宣言済みbody sizeを越える書込みを拒否し、残りbyte数を更新する。
func (w *boundedWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		return 0, failure(Internal, "writer-size-overrun", 0, int64(len(data)))
	}
	n, err := w.w.Write(data)
	if n < 0 || n > len(data) {
		return 0, failure(Internal, "invalid-write-count", 0, int64(n))
	}
	w.remaining -= int64(n)
	return n, err
}

// WriteFrameは固定済みbody sizeを越えないWriterをcallbackへ渡し、過不足を検出する。
// bodyの妥当性はheader出力前に確認し、呼び出し側のprogramming errorで部分frameを作らない。
func WriteFrame(destination io.Writer, code int32, bodySize int64, limits Limits, body func(*Writer) error) error {
	limits, err := limits.normalized()
	if err != nil {
		return err
	}
	if bodySize < 0 || bodySize > int64(limits.ResponseBody) || bodySize > 1<<31-1 {
		return failure(OverLimit, "response-body", 0, bodySize)
	}
	if body == nil {
		return failure(Internal, "nil-frame-body", 0, bodySize)
	}
	header, err := NewWriter(destination, limits)
	if err != nil {
		return err
	}
	if err := header.I32(code); err != nil {
		return err
	}
	if err := header.I32(int32(bodySize)); err != nil {
		return err
	}
	limited := &boundedWriter{w: destination, remaining: bodySize}
	bodyWriter, err := NewWriter(limited, limits)
	if err != nil {
		return err
	}
	if err := body(bodyWriter); err != nil {
		return err
	}
	if limited.remaining != 0 {
		return failure(Internal, "writer-size-underrun", int(bodyWriter.BytesWritten()), limited.remaining)
	}
	return nil
}
