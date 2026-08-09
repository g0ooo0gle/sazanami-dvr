package catalogmodel

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

func TestMetadataCanonicalRoundTrip(t *testing.T) {
	video := Video{StreamContent: 1, ComponentType: 0xb3}
	input := ProgramMetadata{
		Extended: []ExtendedItem{{Heading: "後", Body: "二"}, {Heading: "先", Body: "一"}, {Heading: "先", Body: "一"}},
		Genres:   []Genre{{Level1: 5, Level2: 2}, {Level1: 1, Level2: 3}, {Level1: 1, Level2: 3}},
		Video:    &video,
		Audios: []Audio{
			{ComponentType: 3, ComponentTag: 2, Main: true, SamplingRate: 48_000, Languages: []string{"jpn", "eng"}},
			{ComponentType: 3, ComponentTag: 1, SamplingRate: 44_100, Languages: []string{"etc"}},
		},
	}
	encoded, err := EncodeMetadataV1(input)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeMetadataV1(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.Extended) != 2 || len(decoded.Genres) != 2 || len(decoded.Audios) != 2 {
		t.Fatalf("重複が残っています: %+v", decoded)
	}
	if decoded.Extended[0].Heading != "先" || !slices.Equal(decoded.Audios[1].Languages, []string{"eng", "jpn"}) {
		t.Fatalf("並び順がcanonicalではありません: %+v", decoded)
	}
	reencoded, err := EncodeMetadataV1(decoded)
	if err != nil || !bytes.Equal(reencoded, encoded) {
		t.Fatalf("round tripが一致しません: err=%v", err)
	}

	clone := decoded.Clone()
	clone.Audios[1].Languages[0] = "etc"
	clone.Video.ComponentType = 1
	if decoded.Audios[1].Languages[0] != "eng" || decoded.Video.ComponentType != 0xb3 {
		t.Fatal("Cloneが元のmetadataを共有しています")
	}
}

func TestMetadataEmptyAndRevisionVersion(t *testing.T) {
	material := RevisionMaterial{Validation: ValidationProvisional}
	v1, err := HashRevisionV1(material)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := HashRevision(material)
	if err != nil || selected != v1 {
		t.Fatalf("空metadataでv1 hashが維持されません: err=%v", err)
	}
	if encoded, err := EncodeMetadataV1(ProgramMetadata{}); err != nil || encoded != nil {
		t.Fatalf("空metadata=%x err=%v", encoded, err)
	}
	if decoded, err := DecodeMetadataV1(nil); err != nil || !decoded.Empty() {
		t.Fatalf("空decode=%+v err=%v", decoded, err)
	}

	material.Metadata.Genres = []Genre{{Level1: 1, Level2: 2}}
	v2, err := HashRevision(material)
	if err != nil {
		t.Fatal(err)
	}
	if v2 == v1 {
		t.Fatal("metadataを含むhashがv1から変わりません")
	}
	reordered := material
	reordered.Metadata.Genres = []Genre{{Level1: 1, Level2: 2}, {Level1: 1, Level2: 2}}
	duplicate, err := HashRevision(reordered)
	if err != nil || duplicate != v2 {
		t.Fatalf("重複によってhashが変わりました: err=%v", err)
	}
}

func TestMetadataValidationLimits(t *testing.T) {
	validAudio := Audio{SamplingRate: 48_000, Languages: []string{"jpn"}}
	tests := map[string]ProgramMetadata{
		"extended count": {Extended: make([]ExtendedItem, MaxExtendedItems+1)},
		"heading bytes":  {Extended: []ExtendedItem{{Heading: strings.Repeat("a", MaxExtendedHeadingBytes+1)}}},
		"body bytes":     {Extended: []ExtendedItem{{Body: strings.Repeat("a", MaxExtendedBodyBytes+1)}}},
		"invalid utf8":   {Extended: []ExtendedItem{{Body: string([]byte{0xff})}}},
		"genre count":    {Genres: make([]Genre, MaxGenres+1)},
		"audio count":    {Audios: make([]Audio, MaxAudios+1)},
		"language count": {Audios: []Audio{{SamplingRate: 48_000, Languages: []string{"jpn", "eng", "etc"}}}},
		"language value": {Audios: []Audio{{SamplingRate: 48_000, Languages: []string{"und"}}}},
		"sampling rate":  {Audios: []Audio{{SamplingRate: 47_999}}},
	}
	for name, metadata := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := EncodeMetadataV1(metadata); err == nil {
				t.Fatal("不正なmetadataが受理されました")
			}
		})
	}
	if _, err := EncodeMetadataV1(ProgramMetadata{Audios: []Audio{validAudio}}); err != nil {
		t.Fatalf("有効な音声が拒否されました: %v", err)
	}
}

func TestMetadataDecodeRejectsCorruption(t *testing.T) {
	encoded, err := EncodeMetadataV1(ProgramMetadata{Extended: []ExtendedItem{{Heading: "A", Body: "B"}}})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"prefix":    append([]byte("BADMETA"), encoded[len(metadataPrefix):]...),
		"truncated": encoded[:len(encoded)-1],
		"trailing":  append(slices.Clone(encoded), 0),
		"oversize":  make([]byte, MaxMetadataBytes+1),
		"noncanonical": func() []byte {
			value, encodeErr := EncodeMetadataV1(ProgramMetadata{Extended: []ExtendedItem{{Heading: "A", Body: "B"}, {Heading: "C", Body: "D"}}})
			if encodeErr != nil {
				t.Fatal(encodeErr)
			}
			// 二項目の見出しを入れ替え、長さは変えずに順序だけを壊す。
			value[13], value[23] = value[23], value[13]
			return value
		}(),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeMetadataV1(value); err == nil {
				t.Fatal("破損metadataが受理されました")
			}
		})
	}
}
