// Package programsearchは完成済み番組表へCtrlCmd 1025の検索条件を適用する。
package programsearch

import (
	"strconv"
	"strings"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/autoreservation"
)

const (
	disabledPrefix = "^!{999}"
	casePrefix     = "C!{999}"
)

func decodeRequest(body []byte, limits codec.Limits) (core.SearchCondition, error) {
	reader, err := codec.NewReader(body, limits)
	if err != nil {
		return core.SearchCondition{}, err
	}
	var search core.SearchCondition
	count := 0
	err = reader.Vector(4, 1, func(item *codec.Reader, _ int) error {
		count++
		return decodeCondition(item, &search)
	})
	if err != nil {
		return core.SearchCondition{}, err
	}
	if count != 1 {
		return core.SearchCondition{}, failure(codec.Malformed, "program-search-request-shape", int64(count))
	}
	if err := reader.Exact(); err != nil {
		return core.SearchCondition{}, err
	}
	settings := core.RecordingSettings{Mode: 1, Priority: 1}
	if core.ValidateChange(1, search, settings) != nil {
		return core.SearchCondition{}, failure(codec.Malformed, "program-search-condition-invalid", 0)
	}
	return search, nil
}

func decodeCondition(reader *codec.Reader, search *core.SearchCondition) error {
	return reader.Structure(func(item *codec.Reader) error {
		wireKeyword, err := item.String()
		if err != nil {
			return err
		}
		if len(wireKeyword) > 4_096 {
			return failure(codec.OverLimit, "program-search-keyword", int64(len(wireKeyword)))
		}
		if err := decodeKeyword(wireKeyword, search); err != nil {
			return err
		}
		wireExclude, err := item.String()
		if err != nil {
			return err
		}
		if len(wireExclude) > 4_096 {
			return failure(codec.OverLimit, "program-search-exclude", int64(len(wireExclude)))
		}
		search.Exclude, err = excludeWithoutNote(wireExclude)
		if err != nil {
			return err
		}
		regex, err := item.I32()
		if err != nil {
			return err
		}
		titleOnly, err := item.I32()
		if err != nil {
			return err
		}
		if !bool32(regex) || !bool32(titleOnly) {
			return failure(codec.Malformed, "program-search-flag", int64(regex))
		}
		search.Regex, search.TitleOnly = regex == 1, titleOnly == 1
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
			if err := value.Structure(func(fields *codec.Reader) error {
				var readErr error
				if date.StartDay, readErr = fields.U8(); readErr == nil {
					date.StartHour, readErr = fields.U16()
				}
				if readErr == nil {
					date.StartMinute, readErr = fields.U16()
				}
				if readErr == nil {
					date.EndDay, readErr = fields.U8()
				}
				if readErr == nil {
					date.EndHour, readErr = fields.U16()
				}
				if readErr == nil {
					date.EndMinute, readErr = fields.U16()
				}
				return readErr
			}); err != nil {
				return err
			}
			search.Dates = append(search.Dates, date)
			return nil
		}); err != nil {
			return err
		}
		if err := item.Vector(8, 4_096, func(value *codec.Reader, _ int) error {
			packed, readErr := value.I64()
			if readErr != nil {
				return readErr
			}
			if packed < 0 || uint64(packed)>>48 != 0 {
				return failure(codec.Malformed, "program-search-service", packed)
			}
			search.Services = append(search.Services, core.ServiceRange{
				NetworkID: uint16(packed >> 32), TransportStreamID: uint16(packed >> 16), ServiceID: uint16(packed),
			})
			return nil
		}); err != nil {
			return err
		}
		if err := readU16Vector(item, &search.Video); err != nil {
			return err
		}
		if err := readU16Vector(item, &search.Audio); err != nil {
			return err
		}
		flags := [4]uint8{}
		for index := range flags {
			flags[index], err = item.U8()
			if err != nil {
				return err
			}
			if flags[index] > 1 && index < 3 {
				return failure(codec.Malformed, "program-search-byte", int64(flags[index]))
			}
		}
		if flags[3] > 2 {
			return failure(codec.Malformed, "program-search-free-access", int64(flags[3]))
		}
		search.Fuzzy = flags[0] == 1
		search.ExcludeContents = flags[1] == 1
		search.ExcludeDates = flags[2] == 1
		search.FreeAccess = flags[3]
		return nil
	})
}

func decodeKeyword(value string, search *core.SearchCondition) error {
	search.Enabled = !strings.HasPrefix(value, disabledPrefix)
	value = strings.TrimPrefix(value, disabledPrefix)
	search.CaseSensitive = strings.HasPrefix(value, casePrefix)
	value = strings.TrimPrefix(value, casePrefix)
	if strings.HasPrefix(value, "D!{1") {
		if len(value) < 13 || value[12] != '}' {
			return failure(codec.Malformed, "program-search-duration-prefix", 0)
		}
		number, err := strconv.ParseUint(value[4:12], 10, 32)
		if err != nil {
			return failure(codec.Malformed, "program-search-duration-prefix", 0)
		}
		search.MinimumMinutes = uint16(number / 10_000)
		search.MaximumMinutes = uint16(number % 10_000)
		value = value[13:]
	}
	search.Keyword = value
	return nil
}

func excludeWithoutNote(value string) (string, error) {
	if !strings.HasPrefix(value, ":note:") {
		return value, nil
	}
	for index := len(":note:"); index < len(value); index++ {
		switch value[index] {
		case ' ':
			return value[index+1:], nil
		case '\\':
			index++
			if index >= len(value) || value[index] != '\\' && value[index] != 's' && value[index] != 'm' {
				return "", failure(codec.Malformed, "program-search-note-invalid", 0)
			}
		}
	}
	return "", nil
}

func readU16Vector(reader *codec.Reader, target *[]uint16) error {
	return reader.Vector(2, 256, func(item *codec.Reader, _ int) error {
		value, err := item.U16()
		if err == nil {
			*target = append(*target, value)
		}
		return err
	})
}

func bool32(value int32) bool { return value == 0 || value == 1 }
