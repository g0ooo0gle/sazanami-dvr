package autoreservation

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/autoreservation"
)

const (
	disabledPrefix = "^!{999}"
	casePrefix     = "C!{999}"
)

func decodeOneRule(body []byte, limits codec.Limits, requireNumber bool) (core.Rule, error) {
	reader, err := codec.NewReader(body, limits)
	if err != nil {
		return core.Rule{}, err
	}
	version, err := reader.U16()
	if err != nil || version != Version {
		return core.Rule{}, failure(codec.Malformed, "automatic-rule-version", int64(version))
	}
	var rule core.Rule
	count := 0
	err = reader.Vector(1, 1, func(item *codec.Reader, _ int) error {
		count++
		return decodeAutoAdd(item, &rule)
	})
	if err != nil || count != 1 || reader.Exact() != nil || (!requireNumber && rule.Number != 0) ||
		(requireNumber && core.ValidateChange(rule.Number, rule.Search, rule.Recording) != nil) {
		return core.Rule{}, failure(codec.Malformed, "automatic-rule-value", int64(count))
	}
	if !requireNumber {
		test := rule
		test.ID[0], test.Version = 1, 1
		if test.ValidateNew() != nil {
			return core.Rule{}, failure(codec.Malformed, "automatic-rule-value", 0)
		}
	}
	return rule, nil
}

func decodeAutoAdd(reader *codec.Reader, rule *core.Rule) error {
	return reader.Structure(func(item *codec.Reader) error {
		number, err := item.I32()
		if err != nil {
			return err
		}
		rule.Number = number
		if err := decodeSearch(item, &rule.Search); err != nil {
			return err
		}
		if err := decodeRecording(item, &rule.Recording); err != nil {
			return err
		}
		count, err := item.I32()
		if err != nil || count < 0 {
			return failure(codec.Malformed, "automatic-reservation-count", int64(count))
		}
		rule.ReservationCount = count
		return nil
	})
}

func decodeSearch(reader *codec.Reader, search *core.SearchCondition) error {
	return reader.Structure(func(item *codec.Reader) error {
		wireKeyword, err := item.String()
		if err != nil {
			return err
		}
		if err := decodeKeyword(wireKeyword, search); err != nil {
			return err
		}
		if search.Exclude, err = item.String(); err != nil {
			return err
		}
		regex, err := item.I32()
		if err != nil {
			return err
		}
		title, err := item.I32()
		if err != nil || !bool32(regex) || !bool32(title) {
			return failure(codec.Malformed, "automatic-search-flag", int64(regex))
		}
		search.Regex, search.TitleOnly = regex == 1, title == 1
		if err := item.Vector(8, 256, func(value *codec.Reader, _ int) error {
			var content core.ContentRange
			if err := value.Structure(func(fields *codec.Reader) error {
				var readErr error
				content.Content, readErr = fields.U16()
				if readErr == nil {
					content.User, readErr = fields.U16()
				}
				return readErr
			}); err != nil {
				return err
			}
			search.Contents = append(search.Contents, content)
			return nil
		}); err != nil {
			return err
		}
		if err := item.Vector(14, 64, func(value *codec.Reader, _ int) error {
			var date core.DateRange
			if err := value.Structure(func(fields *codec.Reader) error { return readDate(fields, &date) }); err != nil {
				return err
			}
			search.Dates = append(search.Dates, date)
			return nil
		}); err != nil {
			return err
		}
		if err := item.Vector(8, 4096, func(value *codec.Reader, _ int) error {
			packed, err := value.I64()
			if err != nil || packed < 0 || uint64(packed)>>48 != 0 {
				return failure(codec.Malformed, "automatic-service", packed)
			}
			search.Services = append(search.Services, core.ServiceRange{NetworkID: uint16(packed >> 32), TransportStreamID: uint16(packed >> 16), ServiceID: uint16(packed)})
			return nil
		}); err != nil {
			return err
		}
		if err := readU16Vector(item, 256, &search.Video); err != nil {
			return err
		}
		if err := readU16Vector(item, 256, &search.Audio); err != nil {
			return err
		}
		flags := make([]uint8, 5)
		for index := range flags {
			flags[index], err = item.U8()
			if err != nil || flags[index] > 1 && index < 4 {
				return failure(codec.Malformed, "automatic-search-byte", int64(flags[index]))
			}
		}
		search.Fuzzy, search.ExcludeContents, search.ExcludeDates = flags[0] == 1, flags[1] == 1, flags[2] == 1
		search.FreeAccess = flags[3]
		if search.FreeAccess > 2 || flags[4] > 1 {
			return failure(codec.Malformed, "automatic-search-byte", int64(search.FreeAccess))
		}
		search.CheckRecordedTitle = flags[4] == 1
		days, err := item.U16()
		if err != nil {
			return err
		}
		search.CheckRecordedAllServices = days >= 40_000
		if search.CheckRecordedAllServices {
			days -= 40_000
		}
		search.CheckRecordedDays = days
		return nil
	})
}

func decodeRecording(reader *codec.Reader, settings *core.RecordingSettings) error {
	return reader.Structure(func(item *codec.Reader) error {
		var err error
		if settings.Mode, err = item.U8(); err != nil {
			return err
		}
		if settings.Priority, err = item.U8(); err != nil {
			return err
		}
		follow, err := item.U8()
		if err != nil || follow > 1 {
			return failure(codec.Malformed, "automatic-recording-follow", int64(follow))
		}
		settings.Follow = follow == 1
		if settings.ServiceMode, err = item.U32(); err != nil {
			return err
		}
		exact, err := item.U8()
		if err != nil || exact > 1 {
			return failure(codec.Malformed, "automatic-recording-exact", int64(exact))
		}
		settings.Exact = exact == 1
		if settings.Batch, err = item.String(); err != nil {
			return err
		}
		if err := readFolders(item, &settings.Folders); err != nil {
			return err
		}
		if settings.Suspend, err = item.U8(); err != nil {
			return err
		}
		reboot, err := item.U8()
		if err != nil || reboot > 1 {
			return failure(codec.Malformed, "automatic-recording-reboot", int64(reboot))
		}
		settings.Reboot = reboot == 1
		useMargins, err := item.U8()
		if err != nil || useMargins > 1 {
			return failure(codec.Malformed, "automatic-recording-margin", int64(useMargins))
		}
		start, err := item.I32()
		if err != nil {
			return err
		}
		end, err := item.I32()
		if err != nil {
			return err
		}
		if useMargins == 1 {
			settings.StartMargin, settings.EndMargin = &start, &end
		}
		continued, err := item.U8()
		if err != nil || continued > 1 {
			return failure(codec.Malformed, "automatic-recording-continue", int64(continued))
		}
		settings.Continue = continued == 1
		if settings.PartialMode, err = item.U8(); err != nil {
			return err
		}
		if settings.TunerID, err = item.U32(); err != nil {
			return err
		}
		return readFolders(item, &settings.PartialFolders)
	})
}

func readDate(reader *codec.Reader, date *core.DateRange) error {
	var err error
	if date.StartDay, err = reader.U8(); err != nil {
		return err
	}
	if date.StartHour, err = reader.U16(); err != nil {
		return err
	}
	if date.StartMinute, err = reader.U16(); err != nil {
		return err
	}
	if date.EndDay, err = reader.U8(); err != nil {
		return err
	}
	if date.EndHour, err = reader.U16(); err != nil {
		return err
	}
	date.EndMinute, err = reader.U16()
	return err
}

func readU16Vector(reader *codec.Reader, maximum int, target *[]uint16) error {
	return reader.Vector(2, maximum, func(item *codec.Reader, _ int) error {
		value, err := item.U16()
		if err == nil {
			*target = append(*target, value)
		}
		return err
	})
}

func readFolders(reader *codec.Reader, target *[]core.Folder) error {
	return reader.Vector(28, 16, func(item *codec.Reader, _ int) error {
		var folder core.Folder
		return item.Structure(func(fields *codec.Reader) error {
			var err error
			if folder.Path, err = fields.String(); err != nil {
				return err
			}
			if folder.Writer, err = fields.String(); err != nil {
				return err
			}
			if folder.Name, err = fields.String(); err != nil {
				return err
			}
			reserved, err := fields.String()
			if err != nil || reserved != "" {
				return failure(codec.Malformed, "automatic-folder-reserved", int64(len(reserved)))
			}
			*target = append(*target, folder)
			return nil
		})
	})
}

func bool32(value int32) bool { return value == 0 || value == 1 }

func decodeKeyword(value string, search *core.SearchCondition) error {
	search.Enabled = !strings.HasPrefix(value, disabledPrefix)
	value = strings.TrimPrefix(value, disabledPrefix)
	search.CaseSensitive = strings.HasPrefix(value, casePrefix)
	value = strings.TrimPrefix(value, casePrefix)
	if len(value) >= 13 && strings.HasPrefix(value, "D!{1") && value[12] == '}' {
		number, err := strconv.ParseUint(value[4:12], 10, 32)
		if err != nil {
			return failure(codec.Malformed, "automatic-duration-prefix", 0)
		}
		search.MinimumMinutes, search.MaximumMinutes = uint16(number/10_000), uint16(number%10_000)
		value = value[13:]
	}
	search.Keyword = value
	return nil
}

func wireKeyword(search core.SearchCondition) string {
	var builder strings.Builder
	if !search.Enabled {
		builder.WriteString(disabledPrefix)
	}
	if search.CaseSensitive {
		builder.WriteString(casePrefix)
	}
	if search.MinimumMinutes > 0 || search.MaximumMinutes > 0 {
		fmt.Fprintf(&builder, "D!{1%08d}", uint32(search.MinimumMinutes)*10_000+uint32(search.MaximumMinutes))
	}
	builder.WriteString(search.Keyword)
	return builder.String()
}
