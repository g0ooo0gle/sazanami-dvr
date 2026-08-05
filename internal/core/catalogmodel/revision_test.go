package catalogmodel

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

func pointer[T any](value T) *T { return &value }

func TestRevisionGoldenVectors(t *testing.T) {
	tests := []struct {
		name     string
		material RevisionMaterial
		encoded  string
		hash     string
	}{
		{
			name:     "empty",
			material: RevisionMaterial{Validation: ValidationProvisional},
			encoded:  "535a43415452455631000000000002",
			hash:     "34e6d5711bf579d05b684ecb2e4d1b7f1b517e0455604d821a015ba8f8e1e3bd",
		},
		{
			name: "timed",
			material: RevisionMaterial{
				StartUTCMS: pointer[int64](1785628800000), DurationMS: pointer[int64](1800000),
				Title: pointer("Test"), Description: pointer(""), FreeAccess: FreeYes, Validation: ValidationValid,
			},
			encoded: "535a43415452455631010000019fbfc534000100000000001b774001000000045465737401000000000201",
			hash:    "7655ebc055b5aeb9a1da8dbe5e18f4778cdc9774e65aed48f46677ecceb18479",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := EncodeRevisionV1(test.material)
			if err != nil {
				t.Fatal(err)
			}
			if got := hex.EncodeToString(encoded); got != test.encoded {
				t.Fatalf("encoded=%s, want=%s", got, test.encoded)
			}
			hash, err := HashRevisionV1(test.material)
			if err != nil {
				t.Fatal(err)
			}
			if got := hex.EncodeToString(hash[:]); got != test.hash {
				t.Fatalf("hash=%s, want=%s", got, test.hash)
			}
		})
	}
}

func TestRevisionValidationAndPresence(t *testing.T) {
	knownEmpty, err := EncodeRevisionV1(RevisionMaterial{Title: pointer(""), Validation: ValidationValid})
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := EncodeRevisionV1(RevisionMaterial{Validation: ValidationValid})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(knownEmpty, unknown) {
		t.Fatal("known-emptyとunknownが同じencodingになりました")
	}
	for name, material := range map[string]RevisionMaterial{
		"zero duration":      {DurationMS: pointer[int64](0), Validation: ValidationValid},
		"invalid free":       {FreeAccess: FreeAccess(3), Validation: ValidationValid},
		"invalid validation": {Validation: Validation(0)},
		"invalid utf8":       {Title: pointer(string([]byte{0xff})), Validation: ValidationValid},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := EncodeRevisionV1(material); err == nil {
				t.Fatal("不正値が受理されました")
			}
		})
	}
}

func TestNewIDFrom(t *testing.T) {
	id, err := NewIDFrom(bytes.NewReader(make([]byte, 16)))
	if err != nil {
		t.Fatal(err)
	}
	if id[6]>>4 != 4 || id[8]>>6 != 2 {
		t.Fatalf("version/variantが不正です: %x", id)
	}
	if got := id.String(); got != "00000000-0000-4000-8000-000000000000" {
		t.Fatalf("String()=%s", got)
	}
	parsed, err := ParseID(id.String())
	if err != nil || parsed != id {
		t.Fatalf("parsed=%v err=%v", parsed, err)
	}
	for _, invalid := range []string{"", "00000000-0000-4000-8000-00000000000A", "00000000-0000-1000-8000-000000000000"} {
		if _, err := ParseID(invalid); err == nil {
			t.Fatalf("invalid ID %qが受理されました", invalid)
		}
	}
	if _, err := NewIDFrom(bytes.NewReader(make([]byte, 15))); err == nil {
		t.Fatal("短い乱数源が受理されました")
	}
	if _, err := NewIDFrom(errorReader{}); err == nil {
		t.Fatal("乱数源の失敗が無視されました")
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("injected") }

func FuzzRevisionEncodingIsDeterministic(f *testing.F) {
	f.Add(int64(1785628800000), int64(1800000), "Test", "", byte(3))
	f.Add(int64(0), int64(1), "緊急変更", "合成データ", byte(0))
	f.Fuzz(func(t *testing.T, start, duration int64, title, description string, flags byte) {
		material := RevisionMaterial{Validation: Validation(flags%3 + 1), FreeAccess: FreeAccess(flags % 3)}
		if flags&0x04 != 0 {
			material.StartUTCMS = &start
		}
		if flags&0x08 != 0 {
			material.DurationMS = &duration
		}
		if flags&0x10 != 0 {
			material.Title = &title
		}
		if flags&0x20 != 0 {
			material.Description = &description
		}
		first, firstErr := EncodeRevisionV1(material)
		second, secondErr := EncodeRevisionV1(material)
		if (firstErr == nil) != (secondErr == nil) || !bytes.Equal(first, second) {
			t.Fatalf("同じ入力で結果が変わりました: first=%v second=%v", firstErr, secondErr)
		}
		if firstErr != nil {
			return
		}
		digest, err := HashRevisionV1(material)
		if err != nil || digest != sha256.Sum256(first) {
			t.Fatalf("encodingとhashが一致しません: %v", err)
		}
	})
}

func FuzzParseIDRoundTrip(f *testing.F) {
	f.Add("00000000-0000-4000-8000-000000000000")
	f.Add("INVALID")
	f.Fuzz(func(t *testing.T, value string) {
		id, err := ParseID(value)
		if err == nil && id.String() != value {
			t.Fatalf("round trip=%q want=%q", id.String(), value)
		}
	})
}
