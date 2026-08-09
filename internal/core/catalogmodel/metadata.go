package catalogmodel

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"unicode/utf8"
)

const (
	metadataPrefix          = "SZMETA1"
	MaxMetadataBytes        = 256 * 1024
	MaxExtendedItems        = 64
	MaxExtendedHeadingBytes = 4 * 1024
	MaxExtendedBodyBytes    = 64 * 1024
	MaxGenres               = 64
	MaxAudios               = 16
	MaxAudioLanguages       = 2
)

// ExtendedItemは番組詳細の見出しと本文をproviderに依存しない形で保持する。
type ExtendedItem struct {
	Heading string
	Body    string
}

// GenreはARIBの大分類、中分類、利用者定義分類を数値のまま保持する。
type Genre struct {
	Level1 uint8
	Level2 uint8
	User1  uint8
	User2  uint8
}

// Videoは映像componentの検索とCtrlCmd変換に必要な値だけを保持する。
type Video struct {
	StreamContent uint8
	ComponentType uint8
}

// Audioは音声componentの検索とCtrlCmd変換に必要な値だけを保持する。
type Audio struct {
	ComponentType uint8
	ComponentTag  uint8
	Main          bool
	SamplingRate  uint32
	Languages     []string
}

// ProgramMetadataは番組の任意詳細情報である。EmptyがtrueならSQLiteへ保存しない。
type ProgramMetadata struct {
	Extended []ExtendedItem
	Genres   []Genre
	Video    *Video
	Audios   []Audio
}

// Emptyは詳細情報が一つもないかを返す。
func (metadata ProgramMetadata) Empty() bool {
	return len(metadata.Extended) == 0 && len(metadata.Genres) == 0 && metadata.Video == nil && len(metadata.Audios) == 0
}

// Cloneはsliceとpointerを共有しない複製を返す。
func (metadata ProgramMetadata) Clone() ProgramMetadata {
	cloned := metadata
	cloned.Extended = slices.Clone(metadata.Extended)
	cloned.Genres = slices.Clone(metadata.Genres)
	if metadata.Video != nil {
		video := *metadata.Video
		cloned.Video = &video
	}
	cloned.Audios = make([]Audio, len(metadata.Audios))
	for index := range metadata.Audios {
		cloned.Audios[index] = metadata.Audios[index]
		cloned.Audios[index].Languages = slices.Clone(metadata.Audios[index].Languages)
	}
	return cloned
}

// NormalizeMetadataは上限と値を検証し、決定的な順序へ整列して完全な重複を除く。
func NormalizeMetadata(metadata ProgramMetadata) (ProgramMetadata, error) {
	if len(metadata.Extended) > MaxExtendedItems {
		return ProgramMetadata{}, errors.New("catalogmodel: too many extended items")
	}
	if len(metadata.Genres) > MaxGenres {
		return ProgramMetadata{}, errors.New("catalogmodel: too many genres")
	}
	if len(metadata.Audios) > MaxAudios {
		return ProgramMetadata{}, errors.New("catalogmodel: too many audios")
	}

	normalized := metadata.Clone()
	for _, item := range normalized.Extended {
		if err := validateMetadataText(item.Heading, MaxExtendedHeadingBytes, "extended heading"); err != nil {
			return ProgramMetadata{}, err
		}
		if err := validateMetadataText(item.Body, MaxExtendedBodyBytes, "extended body"); err != nil {
			return ProgramMetadata{}, err
		}
	}
	sort.Slice(normalized.Extended, func(left, right int) bool {
		if normalized.Extended[left].Heading != normalized.Extended[right].Heading {
			return normalized.Extended[left].Heading < normalized.Extended[right].Heading
		}
		return normalized.Extended[left].Body < normalized.Extended[right].Body
	})
	normalized.Extended = slices.Compact(normalized.Extended)

	sort.Slice(normalized.Genres, func(left, right int) bool {
		return genreLess(normalized.Genres[left], normalized.Genres[right])
	})
	normalized.Genres = slices.Compact(normalized.Genres)

	for index := range normalized.Audios {
		audio := &normalized.Audios[index]
		if !validSamplingRate(audio.SamplingRate) {
			return ProgramMetadata{}, errors.New("catalogmodel: invalid audio sampling rate")
		}
		if len(audio.Languages) > MaxAudioLanguages {
			return ProgramMetadata{}, errors.New("catalogmodel: too many audio languages")
		}
		for _, language := range audio.Languages {
			if !validLanguage(language) {
				return ProgramMetadata{}, errors.New("catalogmodel: invalid audio language")
			}
		}
		sort.Strings(audio.Languages)
		audio.Languages = slices.Compact(audio.Languages)
	}
	sort.Slice(normalized.Audios, func(left, right int) bool {
		return audioLess(normalized.Audios[left], normalized.Audios[right])
	})
	normalized.Audios = slices.CompactFunc(normalized.Audios, func(left, right Audio) bool {
		return left.ComponentType == right.ComponentType && left.ComponentTag == right.ComponentTag &&
			left.Main == right.Main && left.SamplingRate == right.SamplingRate && slices.Equal(left.Languages, right.Languages)
	})
	return normalized, nil
}

// EncodeMetadataV1は正規化済みの番組詳細を上限付きcanonical binaryへ変換する。
func EncodeMetadataV1(metadata ProgramMetadata) ([]byte, error) {
	normalized, err := NormalizeMetadata(metadata)
	if err != nil {
		return nil, err
	}
	if normalized.Empty() {
		return nil, nil
	}
	var output bytes.Buffer
	output.WriteString(metadataPrefix)
	writeUint16(&output, uint16(len(normalized.Extended)))
	for _, item := range normalized.Extended {
		writeMetadataText(&output, item.Heading)
		writeMetadataText(&output, item.Body)
	}
	writeUint16(&output, uint16(len(normalized.Genres)))
	for _, genre := range normalized.Genres {
		output.Write([]byte{genre.Level1, genre.Level2, genre.User1, genre.User2})
	}
	if normalized.Video == nil {
		output.WriteByte(0)
	} else {
		output.Write([]byte{1, normalized.Video.StreamContent, normalized.Video.ComponentType})
	}
	writeUint16(&output, uint16(len(normalized.Audios)))
	for _, audio := range normalized.Audios {
		main := byte(0)
		if audio.Main {
			main = 1
		}
		output.Write([]byte{audio.ComponentType, audio.ComponentTag, main})
		writeUint32(&output, audio.SamplingRate)
		output.WriteByte(byte(len(audio.Languages)))
		for _, language := range audio.Languages {
			output.WriteString(language)
		}
	}
	if output.Len() > MaxMetadataBytes {
		return nil, errors.New("catalogmodel: metadata exceeds byte limit")
	}
	return output.Bytes(), nil
}

// DecodeMetadataV1はcanonical binaryを検証し、番組詳細へ戻す。非canonicalな順序や末尾も拒否する。
func DecodeMetadataV1(encoded []byte) (ProgramMetadata, error) {
	if len(encoded) == 0 {
		return ProgramMetadata{}, nil
	}
	if len(encoded) > MaxMetadataBytes {
		return ProgramMetadata{}, errors.New("catalogmodel: metadata exceeds byte limit")
	}
	reader := bytes.NewReader(encoded)
	prefix := make([]byte, len(metadataPrefix))
	if _, err := io.ReadFull(reader, prefix); err != nil || string(prefix) != metadataPrefix {
		return ProgramMetadata{}, errors.New("catalogmodel: invalid metadata prefix")
	}
	var metadata ProgramMetadata
	extendedCount, err := readBoundedCount(reader, MaxExtendedItems, "extended items")
	if err != nil {
		return ProgramMetadata{}, err
	}
	metadata.Extended = make([]ExtendedItem, extendedCount)
	for index := range metadata.Extended {
		metadata.Extended[index].Heading, err = readMetadataText(reader, MaxExtendedHeadingBytes, "extended heading")
		if err != nil {
			return ProgramMetadata{}, err
		}
		metadata.Extended[index].Body, err = readMetadataText(reader, MaxExtendedBodyBytes, "extended body")
		if err != nil {
			return ProgramMetadata{}, err
		}
	}
	genreCount, err := readBoundedCount(reader, MaxGenres, "genres")
	if err != nil {
		return ProgramMetadata{}, err
	}
	metadata.Genres = make([]Genre, genreCount)
	for index := range metadata.Genres {
		var raw [4]byte
		if _, err := io.ReadFull(reader, raw[:]); err != nil {
			return ProgramMetadata{}, errors.New("catalogmodel: truncated genre")
		}
		metadata.Genres[index] = Genre{Level1: raw[0], Level2: raw[1], User1: raw[2], User2: raw[3]}
	}
	present, err := reader.ReadByte()
	if err != nil || present > 1 {
		return ProgramMetadata{}, errors.New("catalogmodel: invalid video presence")
	}
	if present == 1 {
		var raw [2]byte
		if _, err := io.ReadFull(reader, raw[:]); err != nil {
			return ProgramMetadata{}, errors.New("catalogmodel: truncated video")
		}
		metadata.Video = &Video{StreamContent: raw[0], ComponentType: raw[1]}
	}
	audioCount, err := readBoundedCount(reader, MaxAudios, "audios")
	if err != nil {
		return ProgramMetadata{}, err
	}
	metadata.Audios = make([]Audio, audioCount)
	for index := range metadata.Audios {
		var raw [7]byte
		if _, err := io.ReadFull(reader, raw[:]); err != nil {
			return ProgramMetadata{}, errors.New("catalogmodel: truncated audio")
		}
		if raw[2] > 1 || !validSamplingRate(binary.BigEndian.Uint32(raw[3:7])) {
			return ProgramMetadata{}, errors.New("catalogmodel: invalid audio")
		}
		languageCount, languageErr := reader.ReadByte()
		if languageErr != nil || languageCount > MaxAudioLanguages {
			return ProgramMetadata{}, errors.New("catalogmodel: invalid audio language count")
		}
		audio := Audio{ComponentType: raw[0], ComponentTag: raw[1], Main: raw[2] == 1, SamplingRate: binary.BigEndian.Uint32(raw[3:7])}
		audio.Languages = make([]string, int(languageCount))
		for languageIndex := range audio.Languages {
			var language [3]byte
			if _, err := io.ReadFull(reader, language[:]); err != nil || !validLanguage(string(language[:])) {
				return ProgramMetadata{}, errors.New("catalogmodel: invalid audio language")
			}
			audio.Languages[languageIndex] = string(language[:])
		}
		metadata.Audios[index] = audio
	}
	if reader.Len() != 0 {
		return ProgramMetadata{}, errors.New("catalogmodel: trailing metadata bytes")
	}
	canonical, err := EncodeMetadataV1(metadata)
	if err != nil || !bytes.Equal(canonical, encoded) {
		return ProgramMetadata{}, errors.New("catalogmodel: non-canonical metadata")
	}
	return metadata, nil
}

func validateMetadataText(value string, maximum int, field string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("catalogmodel: %s is not valid UTF-8", field)
	}
	if len(value) > maximum {
		return fmt.Errorf("catalogmodel: %s exceeds byte limit", field)
	}
	return nil
}

func writeMetadataText(output *bytes.Buffer, value string) {
	writeUint32(output, uint32(len(value)))
	output.WriteString(value)
}

func readMetadataText(reader *bytes.Reader, maximum int, field string) (string, error) {
	var sizeRaw [4]byte
	if _, err := io.ReadFull(reader, sizeRaw[:]); err != nil {
		return "", fmt.Errorf("catalogmodel: truncated %s", field)
	}
	size := binary.BigEndian.Uint32(sizeRaw[:])
	if size > uint32(maximum) || uint64(size) > uint64(reader.Len()) {
		return "", fmt.Errorf("catalogmodel: invalid %s size", field)
	}
	raw := make([]byte, int(size))
	if _, err := io.ReadFull(reader, raw); err != nil || !utf8.Valid(raw) {
		return "", fmt.Errorf("catalogmodel: invalid %s", field)
	}
	return string(raw), nil
}

func readBoundedCount(reader *bytes.Reader, maximum int, field string) (int, error) {
	var raw [2]byte
	if _, err := io.ReadFull(reader, raw[:]); err != nil {
		return 0, fmt.Errorf("catalogmodel: truncated %s count", field)
	}
	count := int(binary.BigEndian.Uint16(raw[:]))
	if count > maximum {
		return 0, fmt.Errorf("catalogmodel: too many %s", field)
	}
	return count, nil
}

func writeUint16(output *bytes.Buffer, value uint16) {
	var raw [2]byte
	binary.BigEndian.PutUint16(raw[:], value)
	output.Write(raw[:])
}

func writeUint32(output *bytes.Buffer, value uint32) {
	var raw [4]byte
	binary.BigEndian.PutUint32(raw[:], value)
	output.Write(raw[:])
}

func genreLess(left, right Genre) bool {
	if left.Level1 != right.Level1 {
		return left.Level1 < right.Level1
	}
	if left.Level2 != right.Level2 {
		return left.Level2 < right.Level2
	}
	if left.User1 != right.User1 {
		return left.User1 < right.User1
	}
	return left.User2 < right.User2
}

func audioLess(left, right Audio) bool {
	if left.ComponentType != right.ComponentType {
		return left.ComponentType < right.ComponentType
	}
	if left.ComponentTag != right.ComponentTag {
		return left.ComponentTag < right.ComponentTag
	}
	if left.Main != right.Main {
		return !left.Main
	}
	if left.SamplingRate != right.SamplingRate {
		return left.SamplingRate < right.SamplingRate
	}
	return slices.Compare(left.Languages, right.Languages) < 0
}

func validSamplingRate(rate uint32) bool {
	switch rate {
	case 16_000, 22_050, 24_000, 32_000, 44_100, 48_000:
		return true
	default:
		return false
	}
}

func validLanguage(language string) bool {
	switch language {
	case "jpn", "eng", "deu", "fra", "ita", "rus", "zho", "kor", "spa", "etc":
		return true
	default:
		return false
	}
}
