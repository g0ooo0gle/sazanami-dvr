package programguide

import (
	"math"
	"strings"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

type eventMetadata struct {
	extended     string
	extendedSize int64
	genres       []catalogmodel.Genre
	video        *catalogmodel.Video
	audios       []catalogmodel.Audio
	contentSize  int64
	videoSize    int64
	audioSize    int64
}

// prepareEventMetadataは一件の番組詳細をKonomiTV固定版が読む構造へ決定的に変換し、出力前に大きさを確定する。
func prepareEventMetadata(metadata catalogmodel.ProgramMetadata, limits codec.Limits) (eventMetadata, error) {
	normalized, err := catalogmodel.NormalizeMetadata(metadata)
	if err != nil {
		return eventMetadata{}, failure(codec.Malformed, "invalid-program-metadata", 0)
	}
	if _, err := catalogmodel.EncodeMetadataV1(normalized); err != nil {
		return eventMetadata{}, failure(codec.OverLimit, "program-metadata-over-limit", 0)
	}
	prepared := eventMetadata{
		extendedSize: 4,
		contentSize:  4,
		videoSize:    4,
		audioSize:    4,
		genres:       normalized.Genres,
		video:        normalized.Video,
		audios:       mainAudioFirst(normalized.Audios),
	}
	if len(normalized.Extended) > 0 {
		prepared.extended = extendedText(normalized.Extended)
		textSize, sizeErr := codec.StringSize(prepared.extended, limits)
		if sizeErr != nil {
			return eventMetadata{}, sizeErr
		}
		prepared.extendedSize = 4 + textSize
	}
	if len(prepared.genres) > 0 {
		prepared.contentSize = 12 + int64(len(prepared.genres))*8
	}
	if prepared.video != nil {
		emptySize, sizeErr := codec.StringSize("", limits)
		if sizeErr != nil {
			return eventMetadata{}, sizeErr
		}
		prepared.videoSize = 7 + emptySize
	}
	if len(prepared.audios) > 0 {
		emptySize, sizeErr := codec.StringSize("", limits)
		if sizeErr != nil {
			return eventMetadata{}, sizeErr
		}
		prepared.audioSize = 12 + int64(len(prepared.audios))*(13+emptySize)
	}
	for _, size := range [...]int64{prepared.extendedSize, prepared.contentSize, prepared.videoSize, prepared.audioSize} {
		if size < 4 || size > math.MaxInt32 {
			return eventMetadata{}, failure(codec.OverLimit, "event-metadata-structure", size)
		}
	}
	return prepared, nil
}

func extendedText(items []catalogmodel.ExtendedItem) string {
	var output strings.Builder
	for index, item := range items {
		if index > 0 {
			output.WriteString("\r\n")
		}
		output.WriteString("- ")
		heading := strings.NewReplacer("\r", " ", "\n", " ").Replace(item.Heading)
		output.WriteString(heading)
		output.WriteString("\r\n")
		output.WriteString(strings.ReplaceAll(item.Body, "\r", ""))
	}
	return output.String()
}

func mainAudioFirst(audios []catalogmodel.Audio) []catalogmodel.Audio {
	ordered := make([]catalogmodel.Audio, 0, len(audios))
	for _, main := range [...]bool{true, false} {
		for _, audio := range audios {
			if audio.Main == main {
				ordered = append(ordered, audio)
			}
		}
	}
	return ordered
}

func (metadata eventMetadata) writeExtended(writer *codec.Writer) error {
	if metadata.extendedSize == 4 {
		return writer.I32(4)
	}
	if err := writer.I32(int32(metadata.extendedSize)); err != nil {
		return err
	}
	return writer.String(metadata.extended)
}

func (metadata eventMetadata) writeContent(writer *codec.Writer) error {
	if metadata.contentSize == 4 {
		return writer.I32(4)
	}
	if err := writer.I32(int32(metadata.contentSize)); err != nil {
		return err
	}
	if err := writer.I32(int32(8 + len(metadata.genres)*8)); err != nil {
		return err
	}
	if err := writer.I32(int32(len(metadata.genres))); err != nil {
		return err
	}
	for _, genre := range metadata.genres {
		if err := writer.I32(8); err != nil {
			return err
		}
		for _, value := range [...]uint8{genre.Level1, genre.Level2, genre.User1, genre.User2} {
			if err := writer.U8(value); err != nil {
				return err
			}
		}
	}
	return nil
}

func (metadata eventMetadata) writeVideo(writer *codec.Writer) error {
	if metadata.video == nil {
		return writer.I32(4)
	}
	if err := writer.I32(int32(metadata.videoSize)); err != nil {
		return err
	}
	for _, value := range [...]uint8{metadata.video.StreamContent, metadata.video.ComponentType, 0} {
		if err := writer.U8(value); err != nil {
			return err
		}
	}
	return writer.String("")
}

func (metadata eventMetadata) writeAudio(writer *codec.Writer) error {
	if metadata.audioSize == 4 {
		return writer.I32(4)
	}
	if err := writer.I32(int32(metadata.audioSize)); err != nil {
		return err
	}
	vectorSize := 8 + len(metadata.audios)*19
	if err := writer.I32(int32(vectorSize)); err != nil {
		return err
	}
	if err := writer.I32(int32(len(metadata.audios))); err != nil {
		return err
	}
	for _, audio := range metadata.audios {
		if err := writer.I32(19); err != nil {
			return err
		}
		main := uint8(0)
		if audio.Main {
			main = 1
		}
		multilingual := uint8(0)
		if len(audio.Languages) == 2 {
			multilingual = 1
		}
		values := [...]uint8{2, audio.ComponentType, audio.ComponentTag, 0, 0, multilingual, main, 0, edcbSamplingRate(audio.SamplingRate)}
		for _, value := range values {
			if err := writer.U8(value); err != nil {
				return err
			}
		}
		if err := writer.String(""); err != nil {
			return err
		}
	}
	return nil
}

func edcbSamplingRate(rate uint32) uint8 {
	switch rate {
	case 16_000:
		return 0x01
	case 22_050:
		return 0x02
	case 24_000:
		return 0x03
	case 32_000:
		return 0x05
	case 44_100:
		return 0x06
	case 48_000:
		return 0x07
	default:
		return 0
	}
}
