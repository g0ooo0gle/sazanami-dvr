package programguide

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/channel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

func TestEventMetadataMatchesKonomiTVReaderLayout(t *testing.T) {
	start, duration, eventID := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC).UnixMilli(), int64(30*time.Minute/time.Millisecond), int64(4)
	title, description := "番組", "概要"
	video := catalogmodel.Video{StreamContent: 1, ComponentType: 0xb3}
	program := catalogmodel.CurrentProgram{RawEventID: &eventID, Material: catalogmodel.RevisionMaterial{
		StartUTCMS: &start, DurationMS: &duration, Title: &title, Description: &description,
		FreeAccess: catalogmodel.FreeYes, Validation: catalogmodel.ValidationValid,
		Metadata: catalogmodel.ProgramMetadata{
			Extended: []catalogmodel.ExtendedItem{{Heading: "Z", Body: "End"}, {Heading: "A\r\nB", Body: "Line1\r\nLine2"}},
			Genres:   []catalogmodel.Genre{{Level1: 1, Level2: 2, User1: 3, User2: 4}},
			Video:    &video,
			Audios: []catalogmodel.Audio{
				{ComponentType: 1, ComponentTag: 2, SamplingRate: 44_100, Languages: []string{"jpn"}},
				{ComponentType: 3, ComponentTag: 1, Main: true, SamplingRate: 48_000, Languages: []string{"jpn", "eng"}},
			},
		},
	}}
	service := channel.Service{NetworkID: 1, TransportStreamID: 2, ServiceID: 3}
	limits := codec.DefaultLimits()
	wantSize, eligible, err := EventStructureSize(program, limits)
	if err != nil || !eligible {
		t.Fatalf("size=%d eligible=%v err=%v", wantSize, eligible, err)
	}
	var encoded bytes.Buffer
	writer, err := codec.NewWriter(&encoded, limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteEvent(writer, service, program, limits); err != nil {
		t.Fatal(err)
	}
	if int64(encoded.Len()) != wantSize {
		t.Fatalf("written=%d want=%d", encoded.Len(), wantSize)
	}
	reader, err := codec.NewReader(encoded.Bytes(), limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Structure(func(event *codec.Reader) error {
		for range 4 {
			if _, err := event.U16(); err != nil {
				return err
			}
		}
		if _, err := event.U8(); err != nil {
			return err
		}
		if _, err := event.SystemTime(); err != nil {
			return err
		}
		if _, err := event.U8(); err != nil {
			return err
		}
		if _, err := event.I32(); err != nil {
			return err
		}
		if err := event.Structure(func(short *codec.Reader) error {
			for _, want := range []string{title, description} {
				got, err := short.String()
				if err != nil || got != want {
					return fmt.Errorf("short=%q want=%q err=%w", got, want, err)
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if err := event.Structure(func(ext *codec.Reader) error {
			got, err := ext.String()
			want := "- A  B\r\nLine1\nLine2\r\n- Z\r\nEnd"
			if err != nil || got != want {
				return fmt.Errorf("extended=%q want=%q err=%w", got, want, err)
			}
			return nil
		}); err != nil {
			return err
		}
		if err := event.Structure(func(content *codec.Reader) error {
			return content.Vector(8, 64, func(item *codec.Reader, _ int) error {
				return item.Structure(func(genre *codec.Reader) error {
					for index, want := range []uint8{1, 2, 3, 4} {
						got, err := genre.U8()
						if err != nil || got != want {
							return fmt.Errorf("genre[%d]=%d want=%d err=%w", index, got, want, err)
						}
					}
					return nil
				})
			})
		}); err != nil {
			return err
		}
		if err := event.Structure(func(component *codec.Reader) error {
			for index, want := range []uint8{1, 0xb3, 0} {
				got, err := component.U8()
				if err != nil || got != want {
					return fmt.Errorf("video[%d]=%d want=%d err=%w", index, got, want, err)
				}
			}
			text, err := component.String()
			if err != nil || text != "" {
				return fmt.Errorf("video text=%q err=%w", text, err)
			}
			return nil
		}); err != nil {
			return err
		}
		if err := event.Structure(func(audio *codec.Reader) error {
			return audio.Vector(19, 16, func(item *codec.Reader, index int) error {
				return item.Structure(func(component *codec.Reader) error {
					values := make([]uint8, 9)
					for valueIndex := range values {
						value, err := component.U8()
						if err != nil {
							return err
						}
						values[valueIndex] = value
					}
					if _, err := component.String(); err != nil {
						return err
					}
					if index == 0 && (values[1] != 3 || values[5] != 1 || values[6] != 1 || values[8] != 7) {
						return fmt.Errorf("主音声=%v", values)
					}
					if index == 1 && (values[1] != 1 || values[6] != 0 || values[8] != 6) {
						return fmt.Errorf("副音声=%v", values)
					}
					return nil
				})
			})
		}); err != nil {
			return err
		}
		for range 2 {
			if err := event.Structure(func(*codec.Reader) error { return nil }); err != nil {
				return err
			}
		}
		free, err := event.U8()
		if err != nil || free != 0 {
			return fmt.Errorf("free=%d err=%w", free, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := reader.Exact(); err != nil {
		t.Fatal(err)
	}
}

func TestEDCBSamplingRateMapping(t *testing.T) {
	for rate, want := range map[uint32]uint8{16_000: 1, 22_050: 2, 24_000: 3, 32_000: 5, 44_100: 6, 48_000: 7, 47_999: 0} {
		if got := edcbSamplingRate(rate); got != want {
			t.Fatalf("rate=%d got=%d want=%d", rate, got, want)
		}
	}
}
