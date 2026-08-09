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
	if search.Fuzzy || search.ExcludeContents || len(search.Contents) != 0 || len(search.Video) != 0 || len(search.Audio) != 0 {
		return preparedCondition{}, failure(codec.Unsupported, "program-search-condition-unavailable", 0)
	}
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
		prepared.matchesDuration(program.Material) && prepared.matchesFreeAccess(program.Material)
}

func (prepared preparedCondition) matchesText(material catalogmodel.RevisionMaterial) bool {
	target := ""
	if material.Title != nil {
		target = *material.Title
	}
	if !prepared.search.TitleOnly && material.Description != nil {
		target += "\n" + *material.Description
	}
	if prepared.search.Regex {
		return (prepared.keyword == nil || prepared.keyword.MatchString(target)) &&
			(prepared.exclude == nil || !prepared.exclude.MatchString(target))
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
