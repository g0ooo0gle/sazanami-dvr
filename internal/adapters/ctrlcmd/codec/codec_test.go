package codec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"math/rand"
	"strings"
	"testing"
)

func requireCategory(t *testing.T, err error, category Category) *Error {
	t.Helper()
	var codecError *Error
	if !errors.As(err, &codecError) {
		t.Fatalf("codec Errorではありません: %v", err)
	}
	if codecError.Category != category {
		t.Fatalf("category=%s, want=%s (%v)", codecError.Category, category, err)
	}
	return codecError
}

func frame(code int32, body []byte) []byte {
	value := make([]byte, HeaderSize+len(body))
	binary.LittleEndian.PutUint32(value[0:4], uint32(code))
	binary.LittleEndian.PutUint32(value[4:8], uint32(len(body)))
	copy(value[8:], body)
	return value
}

func TestParseRequestFrame(t *testing.T) {
	t.Run("exact", func(t *testing.T) {
		got, err := ParseRequestFrame(frame(2200, []byte{1, 2}), Limits{})
		if err != nil || got.Code != 2200 || !bytes.Equal(got.Body, []byte{1, 2}) {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	})
	t.Run("short header", func(t *testing.T) {
		_, err := ParseRequestFrame(make([]byte, 7), Limits{})
		requireCategory(t, err, Truncated)
	})
	t.Run("negative length", func(t *testing.T) {
		value := make([]byte, 8)
		binary.LittleEndian.PutUint32(value[4:], ^uint32(0))
		_, err := ParseRequestFrame(value, Limits{})
		requireCategory(t, err, Malformed)
	})
	t.Run("over limit", func(t *testing.T) {
		value := make([]byte, 8)
		binary.LittleEndian.PutUint32(value[4:], MaxRequestBody+1)
		_, err := ParseRequestFrame(value, Limits{})
		requireCategory(t, err, OverLimit)
	})
	t.Run("truncated body", func(t *testing.T) {
		value := frame(1, []byte{1})
		binary.LittleEndian.PutUint32(value[4:], 2)
		_, err := ParseRequestFrame(value, Limits{})
		requireCategory(t, err, Truncated)
	})
	t.Run("trailing body", func(t *testing.T) {
		value := frame(1, []byte{1, 2})
		binary.LittleEndian.PutUint32(value[4:], 1)
		_, err := ParseRequestFrame(value, Limits{})
		requireCategory(t, err, Malformed)
	})
}

func TestReaderPrimitivesAndExactConsumption(t *testing.T) {
	data := []byte{0x34, 0x12, 0x78, 0x56, 0x34, 0x12}
	r, err := NewReader(data, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	u16, err := r.U16()
	if err != nil || u16 != 0x1234 {
		t.Fatalf("u16=%x err=%v", u16, err)
	}
	u32, err := r.U32()
	if err != nil || u32 != 0x12345678 {
		t.Fatalf("u32=%x err=%v", u32, err)
	}
	if err := r.Exact(); err != nil {
		t.Fatal(err)
	}
	_, err = r.U16()
	requireCategory(t, err, Truncated)
}

func encodeString(t *testing.T, value string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w, err := NewWriter(&buffer, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.String(value); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestStrictUTF16String(t *testing.T) {
	for _, value := range []string{"", "日本語", "波🌊"} {
		t.Run(value, func(t *testing.T) {
			r, _ := NewReader(encodeString(t, value), Limits{})
			got, err := r.String()
			if err != nil || got != value {
				t.Fatalf("got=%q err=%v", got, err)
			}
		})
	}
	cases := []struct {
		name     string
		data     []byte
		category Category
	}{
		{"extent too small", []byte{4, 0, 0, 0}, Malformed},
		{"odd extent", []byte{7, 0, 0, 0, 0, 0, 0}, Malformed},
		{"missing terminator", []byte{6, 0, 0, 0, 'A', 0}, Malformed},
		{"embedded nul", []byte{8, 0, 0, 0, 0, 0, 0, 0}, Malformed},
		{"high surrogate", []byte{8, 0, 0, 0, 0x00, 0xD8, 0, 0}, Malformed},
		{"low surrogate", []byte{8, 0, 0, 0, 0x00, 0xDC, 0, 0}, Malformed},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			r, _ := NewReader(test.data, Limits{})
			_, err := r.String()
			requireCategory(t, err, test.category)
		})
	}
	t.Run("effective cap", func(t *testing.T) {
		r, _ := NewReader(encodeString(t, "ab"), Limits{StringExtent: 6})
		_, err := r.String()
		requireCategory(t, err, OverLimit)
	})
	t.Run("writer rejects nul", func(t *testing.T) {
		var destination bytes.Buffer
		w, _ := NewWriter(&destination, Limits{})
		err := w.String("a\x00b")
		requireCategory(t, err, Malformed)
	})
	t.Run("writer cap", func(t *testing.T) {
		var destination bytes.Buffer
		w, _ := NewWriter(&destination, Limits{StringExtent: 8})
		err := w.String("abc")
		requireCategory(t, err, OverLimit)
		if destination.Len() != 0 {
			t.Fatalf("上限超過時に%d byte書き込まれました", destination.Len())
		}
	})
	t.Run("writer invalid utf8", func(t *testing.T) {
		var destination bytes.Buffer
		w, _ := NewWriter(&destination, Limits{})
		err := w.String(string([]byte{0xff}))
		requireCategory(t, err, Malformed)
		if destination.Len() != 0 {
			t.Fatalf("不正UTF-8時に%d byte書き込まれました", destination.Len())
		}
	})
}

func TestWriterU8BytesAndStringSize(t *testing.T) {
	t.Run("exact", func(t *testing.T) {
		var destination bytes.Buffer
		writer, err := NewWriter(&destination, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if err := writer.U8(0x12); err != nil {
			t.Fatal(err)
		}
		if err := writer.Bytes([]byte{0x34, 0x56}); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(destination.Bytes(), []byte{0x12, 0x34, 0x56}) || writer.BytesWritten() != 3 {
			t.Fatalf("bytes=%x written=%d", destination.Bytes(), writer.BytesWritten())
		}
	})

	for _, test := range []struct {
		name  string
		value string
		want  int64
	}{
		{"empty", "", 6},
		{"basic multilingual plane", "日本", 10},
		{"surrogate pair", "波🌊", 12},
	} {
		t.Run("string size "+test.name, func(t *testing.T) {
			got, err := StringSize(test.value, Limits{})
			if err != nil || got != test.want {
				t.Fatalf("size=%d want=%d err=%v", got, test.want, err)
			}
			if encoded := encodeString(t, test.value); int64(len(encoded)) != got {
				t.Fatalf("encoded=%d size=%d", len(encoded), got)
			}
		})
	}

	t.Run("invalid utf8", func(t *testing.T) {
		_, err := StringSize(string([]byte{0xff}), Limits{})
		requireCategory(t, err, Malformed)
	})
	t.Run("nul", func(t *testing.T) {
		_, err := StringSize("a\x00b", Limits{})
		requireCategory(t, err, Malformed)
	})
	t.Run("limit", func(t *testing.T) {
		if size, err := StringSize("a", Limits{StringExtent: 8}); err != nil || size != 8 {
			t.Fatalf("size=%d err=%v", size, err)
		}
		_, err := StringSize("ab", Limits{StringExtent: 8})
		requireCategory(t, err, OverLimit)
	})
	t.Run("short write", func(t *testing.T) {
		destination := &shortWriter{limit: 1}
		writer, err := NewWriter(destination, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if err := writer.Bytes([]byte{1, 2}); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestStructureAndVectorBounds(t *testing.T) {
	t.Run("structure exact", func(t *testing.T) {
		data := []byte{6, 0, 0, 0, 7, 0}
		r, _ := NewReader(data, Limits{})
		if err := r.Structure(func(child *Reader) error {
			value, err := child.U16()
			if value != 7 {
				t.Fatalf("value=%d", value)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("structure trailing", func(t *testing.T) {
		r, _ := NewReader([]byte{7, 0, 0, 0, 7, 0, 1}, Limits{})
		err := r.Structure(func(child *Reader) error { _, err := child.U16(); return err })
		requireCategory(t, err, Malformed)
	})
	t.Run("structure cap", func(t *testing.T) {
		r, _ := NewReader([]byte{8, 0, 0, 0, 0, 0, 0, 0}, Limits{StructureExtent: 7})
		err := r.Structure(func(*Reader) error { return nil })
		requireCategory(t, err, OverLimit)
	})
	t.Run("vector exact", func(t *testing.T) {
		data := []byte{12, 0, 0, 0, 2, 0, 0, 0, 1, 0, 2, 0}
		r, _ := NewReader(data, Limits{})
		var got []uint16
		err := r.Vector(2, 2, func(child *Reader, _ int) error {
			value, err := child.U16()
			got = append(got, value)
			return err
		})
		if err != nil || len(got) != 2 || got[0] != 1 || got[1] != 2 {
			t.Fatalf("got=%v err=%v", got, err)
		}
	})
	t.Run("negative count", func(t *testing.T) {
		data := []byte{8, 0, 0, 0, 0xff, 0xff, 0xff, 0xff}
		r, _ := NewReader(data, Limits{})
		err := r.Vector(1, 0, func(*Reader, int) error { return nil })
		requireCategory(t, err, Malformed)
	})
	t.Run("impossible count", func(t *testing.T) {
		data := []byte{9, 0, 0, 0, 2, 0, 0, 0, 1}
		r, _ := NewReader(data, Limits{})
		err := r.Vector(1, 2, func(*Reader, int) error { return nil })
		requireCategory(t, err, Malformed)
	})
	t.Run("minimum width overflow", func(t *testing.T) {
		data := []byte{8, 0, 0, 0, 2, 0, 0, 0}
		r, _ := NewReader(data, Limits{})
		err := r.Vector(math.MaxInt, 2, func(*Reader, int) error { return nil })
		requireCategory(t, err, Malformed)
	})
	t.Run("nil callbacks", func(t *testing.T) {
		structure, _ := NewReader([]byte{4, 0, 0, 0}, Limits{})
		requireCategory(t, structure.Structure(nil), Internal)
		vector, _ := NewReader([]byte{8, 0, 0, 0, 0, 0, 0, 0}, Limits{})
		requireCategory(t, vector.Vector(1, 1, nil), Internal)
	})
	t.Run("count cap", func(t *testing.T) {
		data := []byte{10, 0, 0, 0, 2, 0, 0, 0, 1, 2}
		r, _ := NewReader(data, Limits{})
		err := r.Vector(1, 1, func(*Reader, int) error { return nil })
		requireCategory(t, err, OverLimit)
	})
	t.Run("extent cap", func(t *testing.T) {
		data := []byte{9, 0, 0, 0, 1, 0, 0, 0, 1}
		r, _ := NewReader(data, Limits{StructureExtent: 8})
		err := r.Vector(1, 1, func(*Reader, int) error { return nil })
		requireCategory(t, err, OverLimit)
	})
}

type shortWriter struct {
	limit int
	data  bytes.Buffer
}

type invalidCountWriter struct{}

func (invalidCountWriter) Write(value []byte) (int, error) { return len(value) + 1, nil }

func (w *shortWriter) Write(value []byte) (int, error) {
	if w.limit == 0 {
		return 0, io.ErrClosedPipe
	}
	if len(value) > w.limit {
		value = value[:w.limit]
	}
	w.limit -= len(value)
	return w.data.Write(value)
}

func TestWriteFrameBoundsAndPartialWrite(t *testing.T) {
	t.Run("exact", func(t *testing.T) {
		var destination bytes.Buffer
		err := WriteFrame(&destination, 1, 2, Limits{}, func(w *Writer) error { return w.U16(5) })
		if err != nil || !bytes.Equal(destination.Bytes(), []byte{1, 0, 0, 0, 2, 0, 0, 0, 5, 0}) {
			t.Fatalf("bytes=%x err=%v", destination.Bytes(), err)
		}
	})
	t.Run("underrun", func(t *testing.T) {
		var destination bytes.Buffer
		err := WriteFrame(&destination, 1, 2, Limits{}, func(*Writer) error { return nil })
		requireCategory(t, err, Internal)
	})
	t.Run("overrun", func(t *testing.T) {
		var destination bytes.Buffer
		err := WriteFrame(&destination, 1, 1, Limits{}, func(w *Writer) error { return w.U16(5) })
		requireCategory(t, err, Internal)
	})
	t.Run("destination failure", func(t *testing.T) {
		destination := &shortWriter{limit: 4}
		err := WriteFrame(destination, 1, 2, Limits{}, func(w *Writer) error { return w.U16(5) })
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("invalid write count", func(t *testing.T) {
		err := WriteFrame(invalidCountWriter{}, 1, 0, Limits{}, func(*Writer) error { return nil })
		requireCategory(t, err, Internal)
	})
	t.Run("nil body is rejected before output", func(t *testing.T) {
		var destination bytes.Buffer
		err := WriteFrame(&destination, 1, 0, Limits{}, nil)
		requireCategory(t, err, Internal)
		if destination.Len() != 0 {
			t.Fatalf("nil body時に%d byte書き込まれました", destination.Len())
		}
	})
}

func TestDeterministicMutationDoesNotPanic(t *testing.T) {
	random := rand.New(rand.NewSource(0x53415a414e414d49))
	seed := frame(2200, []byte{5, 0, 0, 0, 0, 0})
	for i := 0; i < 8192; i++ {
		candidate := append([]byte(nil), seed...)
		switch random.Intn(4) {
		case 0:
			candidate = candidate[:random.Intn(len(candidate)+1)]
		case 1:
			candidate[random.Intn(len(candidate))] ^= byte(1 + random.Intn(255))
		case 2:
			candidate = append(candidate, byte(random.Intn(256)))
		case 3:
			binary.LittleEndian.PutUint32(candidate[4:8], random.Uint32())
		}
		parsed, err := ParseRequestFrame(candidate, Limits{})
		if err == nil {
			reader, readerErr := NewReader(parsed.Body, Limits{LogicalItems: 16})
			if readerErr == nil && len(parsed.Body) >= 6 {
				_, _ = reader.U16()
				_, _ = reader.U32()
				_ = reader.Exact()
			}
		}
	}
}

func TestEffectiveLimitsRejectInvalidValues(t *testing.T) {
	for name, limits := range map[string]Limits{
		"negative":         {RequestBody: -1},
		"request hard cap": {RequestBody: MaxRequestBody + 1},
		"depth hard cap":   {Depth: MaxDepth + 1},
		"items hard cap":   {LogicalItems: MaxLogicalItems + 1},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewReader(nil, limits)
			requireCategory(t, err, OverLimit)
		})
	}
	t.Run("logical budget", func(t *testing.T) {
		r, _ := NewReader(make([]byte, 6), Limits{LogicalItems: 1})
		_, _ = r.U16()
		_, err := r.U32()
		requireCategory(t, err, OverLimit)
	})
	t.Run("huge writer string", func(t *testing.T) {
		var destination bytes.Buffer
		w, _ := NewWriter(&destination, Limits{})
		err := w.String(strings.Repeat("a", MaxStringExtent))
		requireCategory(t, err, OverLimit)
	})
}
