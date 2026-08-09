package programsearch

import (
	"regexp"
	"strings"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/autoreservation"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

var japanStandardTime = time.FixedZone("Asia/Tokyo", 9*60*60)

type preparedCondition struct {
	search           core.SearchCondition
	keyword, exclude *regexp.Regexp
}

func prepare(search core.SearchCondition) (preparedCondition, error) {
	prepared := preparedCondition{search: search}
	if !search.Regex {
		return prepared, nil
	}
	var err error
	prepared.keyword, err = compile(search.Keyword, search.CaseSensitive)
	if err == nil {
		prepared.exclude, err = compile(search.Exclude, search.CaseSensitive)
	}
	if err != nil {
		return preparedCondition{}, failure(codec.Malformed, "program-search-regexp-invalid", 0)
	}
	return prepared, nil
}

func compile(value string, caseSensitive bool) (*regexp.Regexp, error) {
	if value == "" {
		return nil, nil
	}
	if !caseSensitive {
		value = "(?i:" + value + ")"
	}
	return regexp.Compile(value)
}

func (prepared preparedCondition) matches(program catalogmodel.CurrentProgram) bool {
	return prepared.search.Enabled && prepared.matchesText(program.Material) && prepared.matchesDate(program.Material) &&
		prepared.matchesDuration(program.Material) && prepared.matchesFreeAccess(program.Material) &&
		prepared.matchesContents(program.Material.Metadata) && prepared.matchesComponents(program.Material.Metadata)
}

func (prepared preparedCondition) matchesText(material catalogmodel.RevisionMaterial) bool {
	var targetBuilder strings.Builder
	if material.Title != nil {
		targetBuilder.WriteString(*material.Title)
	}
	if !prepared.search.TitleOnly && material.Description != nil {
		targetBuilder.WriteByte('\n')
		targetBuilder.WriteString(*material.Description)
	}
	if !prepared.search.TitleOnly {
		for _, item := range material.Metadata.Extended {
			targetBuilder.WriteByte('\n')
			targetBuilder.WriteString(item.Heading)
			targetBuilder.WriteByte('\n')
			targetBuilder.WriteString(item.Body)
		}
	}
	target := targetBuilder.String()
	if prepared.search.Regex {
		return (prepared.keyword == nil || prepared.keyword.MatchString(target)) &&
			(prepared.exclude == nil || !prepared.exclude.MatchString(target))
	}
	if prepared.search.Fuzzy {
		normalizedTarget := normalizeFuzzyText(target, prepared.search.CaseSensitive)
		for _, word := range strings.Fields(normalizeFuzzyText(prepared.search.Keyword, prepared.search.CaseSensitive)) {
			if !fuzzyContains(normalizedTarget, word) {
				return false
			}
		}
		exclude := prepared.search.Exclude
		if !prepared.search.CaseSensitive {
			target, exclude = strings.ToLower(target), strings.ToLower(exclude)
		}
		for _, word := range strings.Fields(exclude) {
			if strings.Contains(target, word) {
				return false
			}
		}
		return true
	}
	keyword, exclude := prepared.search.Keyword, prepared.search.Exclude
	if !prepared.search.CaseSensitive {
		target, keyword, exclude = strings.ToLower(target), strings.ToLower(keyword), strings.ToLower(exclude)
	}
	for _, word := range strings.Fields(keyword) {
		if !strings.Contains(target, word) {
			return false
		}
	}
	for _, word := range strings.Fields(exclude) {
		if strings.Contains(target, word) {
			return false
		}
	}
	return true
}

func (prepared preparedCondition) matchesContents(metadata catalogmodel.ProgramMetadata) bool {
	if len(prepared.search.Contents) == 0 {
		return true
	}
	matched := false
	for _, condition := range prepared.search.Contents {
		level1, level2 := uint8(condition.Content), uint8(condition.Content>>8)
		user1, user2 := uint8(condition.User), uint8(condition.User>>8)
		if level1 == 0xff && level2 == 0xff {
			matched = len(metadata.Genres) == 0
		} else {
			for _, genre := range metadata.Genres {
				if genre.Level1 != level1 || level2 != 0xff && genre.Level2 != level2 {
					continue
				}
				if level1 == 0x0e && (genre.User1 != user1 || genre.User2 != user2) {
					continue
				}
				matched = true
				break
			}
		}
		if matched {
			break
		}
	}
	return matched != prepared.search.ExcludeContents
}

func (prepared preparedCondition) matchesComponents(metadata catalogmodel.ProgramMetadata) bool {
	if len(prepared.search.Video) > 0 {
		if metadata.Video == nil || !containsComponent(prepared.search.Video, metadata.Video.StreamContent, metadata.Video.ComponentType) {
			return false
		}
	}
	if len(prepared.search.Audio) > 0 {
		matched := false
		for _, audio := range metadata.Audios {
			if containsComponent(prepared.search.Audio, 2, audio.ComponentType) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func containsComponent(conditions []uint16, streamContent, componentType uint8) bool {
	packed := uint16(streamContent)<<8 | uint16(componentType)
	for _, condition := range conditions {
		if condition == packed {
			return true
		}
	}
	return false
}

func (prepared preparedCondition) matchesDate(material catalogmodel.RevisionMaterial) bool {
	if len(prepared.search.Dates) == 0 {
		return true
	}
	if material.StartUTCMS == nil || *material.StartUTCMS < 0 {
		return false
	}
	local := time.UnixMilli(*material.StartUTCMS).In(japanStandardTime)
	minute := int(local.Weekday())*24*60 + local.Hour()*60 + local.Minute()
	matched := false
	for _, value := range prepared.search.Dates {
		from := int(value.StartDay)*24*60 + int(value.StartHour)*60 + int(value.StartMinute)
		to := int(value.EndDay)*24*60 + int(value.EndHour)*60 + int(value.EndMinute)
		if from == to || from < to && minute >= from && minute < to || from > to && (minute >= from || minute < to) {
			matched = true
			break
		}
	}
	return matched != prepared.search.ExcludeDates
}

func (prepared preparedCondition) matchesDuration(material catalogmodel.RevisionMaterial) bool {
	if prepared.search.MinimumMinutes == 0 && prepared.search.MaximumMinutes == 0 {
		return true
	}
	if material.DurationMS == nil || *material.DurationMS < 0 {
		return false
	}
	minutes := uint64(*material.DurationMS / int64(time.Minute/time.Millisecond))
	return (prepared.search.MinimumMinutes == 0 || minutes >= uint64(prepared.search.MinimumMinutes)) &&
		(prepared.search.MaximumMinutes == 0 || minutes <= uint64(prepared.search.MaximumMinutes))
}

func (prepared preparedCondition) matchesFreeAccess(material catalogmodel.RevisionMaterial) bool {
	switch prepared.search.FreeAccess {
	case 0:
		return true
	case 1:
		return material.FreeAccess == catalogmodel.FreeYes
	case 2:
		return material.FreeAccess == catalogmodel.FreeNo
	default:
		return false
	}
}
