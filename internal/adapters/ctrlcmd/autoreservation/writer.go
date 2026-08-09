package autoreservation

import (
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/autoreservation"
)

func autoAddSize(rule core.Rule, limits codec.Limits) (int64, error) {
	search, err := searchSize(rule.Search, limits)
	if err != nil {
		return 0, err
	}
	recording, err := recordingSize(rule.Recording, limits)
	if err != nil {
		return 0, err
	}
	return 4 + 4 + search + recording + 4, nil
}

func searchSize(search core.SearchCondition, limits codec.Limits) (int64, error) {
	keyword, err := codec.StringSize(wireKeyword(search), limits)
	if err != nil {
		return 0, err
	}
	exclude, err := codec.StringSize(search.Exclude, limits)
	if err != nil {
		return 0, err
	}
	return 4 + keyword + exclude + 8 + int64(8+len(search.Contents)*8) + int64(8+len(search.Dates)*14) +
		int64(8+len(search.Services)*8) + int64(8+len(search.Video)*2) + int64(8+len(search.Audio)*2) + 7, nil
}

func recordingSize(settings core.RecordingSettings, limits codec.Limits) (int64, error) {
	batch, err := codec.StringSize(settings.Batch, limits)
	if err != nil {
		return 0, err
	}
	folders, err := foldersSize(settings.Folders, limits)
	if err != nil {
		return 0, err
	}
	partial, err := foldersSize(settings.PartialFolders, limits)
	if err != nil {
		return 0, err
	}
	return 4 + 3 + 4 + 1 + batch + folders + 3 + 8 + 2 + 4 + partial, nil
}

func foldersSize(folders []core.Folder, limits codec.Limits) (int64, error) {
	size := int64(8)
	for _, folder := range folders {
		item := int64(4)
		for _, value := range []string{folder.Path, folder.Writer, folder.Name, ""} {
			field, err := codec.StringSize(value, limits)
			if err != nil {
				return 0, err
			}
			item += field
		}
		size += item
	}
	return size, nil
}

func writeAutoAdd(writer *codec.Writer, rule core.Rule, limits codec.Limits) error {
	size, err := autoAddSize(rule, limits)
	if err != nil {
		return err
	}
	if err := writer.I32(int32(size)); err != nil {
		return err
	}
	if err := writer.I32(rule.Number); err != nil {
		return err
	}
	if err := writeSearch(writer, rule.Search, limits); err != nil {
		return err
	}
	if err := writeRecording(writer, rule.Recording, limits); err != nil {
		return err
	}
	return writer.I32(rule.ReservationCount)
}

func writeSearch(writer *codec.Writer, search core.SearchCondition, limits codec.Limits) error {
	size, err := searchSize(search, limits)
	if err != nil {
		return err
	}
	if err := writer.I32(int32(size)); err != nil {
		return err
	}
	if err := writer.String(wireKeyword(search)); err != nil {
		return err
	}
	if err := writer.String(search.Exclude); err != nil {
		return err
	}
	if err := writer.I32(boolI32(search.Regex)); err != nil {
		return err
	}
	if err := writer.I32(boolI32(search.TitleOnly)); err != nil {
		return err
	}
	if err := writer.I32(int32(8 + len(search.Contents)*8)); err != nil {
		return err
	}
	if err := writer.I32(int32(len(search.Contents))); err != nil {
		return err
	}
	for _, content := range search.Contents {
		if err := writer.I32(8); err != nil {
			return err
		}
		if err := writer.U16(content.Content); err != nil {
			return err
		}
		if err := writer.U16(content.User); err != nil {
			return err
		}
	}
	if err := writer.I32(int32(8 + len(search.Dates)*14)); err != nil {
		return err
	}
	if err := writer.I32(int32(len(search.Dates))); err != nil {
		return err
	}
	for _, date := range search.Dates {
		if err := writer.I32(14); err != nil {
			return err
		}
		if err := writer.U8(date.StartDay); err != nil {
			return err
		}
		if err := writer.U16(date.StartHour); err != nil {
			return err
		}
		if err := writer.U16(date.StartMinute); err != nil {
			return err
		}
		if err := writer.U8(date.EndDay); err != nil {
			return err
		}
		if err := writer.U16(date.EndHour); err != nil {
			return err
		}
		if err := writer.U16(date.EndMinute); err != nil {
			return err
		}
	}
	if err := writeServiceVector(writer, search.Services); err != nil {
		return err
	}
	if err := writeU16Vector(writer, search.Video); err != nil {
		return err
	}
	if err := writeU16Vector(writer, search.Audio); err != nil {
		return err
	}
	for _, value := range []uint8{boolU8(search.Fuzzy), boolU8(search.ExcludeContents), boolU8(search.ExcludeDates), search.FreeAccess, boolU8(search.CheckRecordedTitle)} {
		if err := writer.U8(value); err != nil {
			return err
		}
	}
	days := search.CheckRecordedDays
	if search.CheckRecordedAllServices {
		days += 40_000
	}
	return writer.U16(days)
}

func writeRecording(writer *codec.Writer, settings core.RecordingSettings, limits codec.Limits) error {
	size, err := recordingSize(settings, limits)
	if err != nil {
		return err
	}
	if err := writer.I32(int32(size)); err != nil {
		return err
	}
	for _, value := range []uint8{settings.Mode, settings.Priority, boolU8(settings.Follow)} {
		if err := writer.U8(value); err != nil {
			return err
		}
	}
	if err := writer.U32(settings.ServiceMode); err != nil {
		return err
	}
	if err := writer.U8(boolU8(settings.Exact)); err != nil {
		return err
	}
	if err := writer.String(settings.Batch); err != nil {
		return err
	}
	if err := writeFolders(writer, settings.Folders, limits); err != nil {
		return err
	}
	if err := writer.U8(settings.Suspend); err != nil {
		return err
	}
	if err := writer.U8(boolU8(settings.Reboot)); err != nil {
		return err
	}
	if err := writer.U8(boolU8(settings.StartMargin != nil)); err != nil {
		return err
	}
	start, end := int32(0), int32(0)
	if settings.StartMargin != nil {
		start, end = *settings.StartMargin, *settings.EndMargin
	}
	if err := writer.I32(start); err != nil {
		return err
	}
	if err := writer.I32(end); err != nil {
		return err
	}
	if err := writer.U8(boolU8(settings.Continue)); err != nil {
		return err
	}
	if err := writer.U8(settings.PartialMode); err != nil {
		return err
	}
	if err := writer.U32(settings.TunerID); err != nil {
		return err
	}
	return writeFolders(writer, settings.PartialFolders, limits)
}

func writeFolders(writer *codec.Writer, folders []core.Folder, limits codec.Limits) error {
	size, err := foldersSize(folders, limits)
	if err != nil {
		return err
	}
	if err := writer.I32(int32(size)); err != nil {
		return err
	}
	if err := writer.I32(int32(len(folders))); err != nil {
		return err
	}
	for _, folder := range folders {
		item, err := foldersSize([]core.Folder{folder}, limits)
		if err != nil {
			return err
		}
		if err := writer.I32(int32(item - 8)); err != nil {
			return err
		}
		for _, value := range []string{folder.Path, folder.Writer, folder.Name, ""} {
			if err := writer.String(value); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeServiceVector(writer *codec.Writer, services []core.ServiceRange) error {
	if err := writer.I32(int32(8 + len(services)*8)); err != nil {
		return err
	}
	if err := writer.I32(int32(len(services))); err != nil {
		return err
	}
	for _, service := range services {
		packed := int64(service.NetworkID)<<32 | int64(service.TransportStreamID)<<16 | int64(service.ServiceID)
		if err := writer.I64(packed); err != nil {
			return err
		}
	}
	return nil
}

func writeU16Vector(writer *codec.Writer, values []uint16) error {
	if err := writer.I32(int32(8 + len(values)*2)); err != nil {
		return err
	}
	if err := writer.I32(int32(len(values))); err != nil {
		return err
	}
	for _, value := range values {
		if err := writer.U16(value); err != nil {
			return err
		}
	}
	return nil
}

func boolU8(value bool) uint8 {
	if value {
		return 1
	}
	return 0
}

func boolI32(value bool) int32 { return int32(boolU8(value)) }
