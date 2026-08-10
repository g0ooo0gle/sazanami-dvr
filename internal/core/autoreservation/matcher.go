package autoreservation

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

// ErrInvalidRegexpは検索条件の正規表現を準備できないことを表す。
var ErrInvalidRegexp = errors.New("autoreservation: invalid regexp")

var japanStandardTime = time.FixedZone("Asia/Tokyo", 9*60*60)

// ProgramMatcherは一つの検索条件を完成済み番組へ繰り返し適用する。
// 正規表現は生成時に一度だけcompileし、放送サービスの照合は呼出し側で行う。
type ProgramMatcher struct {
	search           SearchCondition
	keyword, exclude *regexp.Regexp
}

// PrepareProgramMatcherは番組検索と自動予約で共有できる判定器を返す。
func PrepareProgramMatcher(search SearchCondition) (ProgramMatcher, error) {
	matcher := ProgramMatcher{search: search}
	if !search.Regex {
		return matcher, nil
	}
	var err error
	matcher.keyword, err = compilePattern(search.Keyword, search.CaseSensitive)
	if err == nil {
		matcher.exclude, err = compilePattern(search.Exclude, search.CaseSensitive)
	}
	if err != nil {
		return ProgramMatcher{}, ErrInvalidRegexp
	}
	return matcher, nil
}

func compilePattern(value string, caseSensitive bool) (*regexp.Regexp, error) {
	if value == "" {
		return nil, nil
	}
	if !caseSensitive {
		value = "(?i:" + value + ")"
	}
	return regexp.Compile(value)
}

// Matchesは番組の文字列、metadata、時刻、長さ、無料状態をまとめて判定する。
func (matcher ProgramMatcher) Matches(program catalogmodel.CurrentProgram) bool {
	return matcher.search.Enabled && matcher.matchesText(program.Material) && matcher.matchesDate(program.Material) &&
		matcher.matchesDuration(program.Material) && matcher.matchesFreeAccess(program.Material) &&
		matcher.matchesContents(program.Material.Metadata) && matcher.matchesComponents(program.Material.Metadata)
}

func (matcher ProgramMatcher) matchesText(material catalogmodel.RevisionMaterial) bool {
	if matcher.search.Keyword == "" && matcher.search.Exclude == "" {
		return true
	}
	var targetBuilder strings.Builder
	if material.Title != nil {
		targetBuilder.WriteString(*material.Title)
	}
	if !matcher.search.TitleOnly && material.Description != nil {
		targetBuilder.WriteByte('\n')
		targetBuilder.WriteString(*material.Description)
	}
	if !matcher.search.TitleOnly {
		for _, item := range material.Metadata.Extended {
			targetBuilder.WriteByte('\n')
			targetBuilder.WriteString(item.Heading)
			targetBuilder.WriteByte('\n')
			targetBuilder.WriteString(item.Body)
		}
	}
	target := targetBuilder.String()
	if matcher.search.Regex {
		return (matcher.keyword == nil || matcher.keyword.MatchString(target)) &&
			(matcher.exclude == nil || !matcher.exclude.MatchString(target))
	}
	if matcher.search.Fuzzy {
		normalizedTarget := normalizeFuzzyText(target, matcher.search.CaseSensitive)
		for _, word := range strings.Fields(normalizeFuzzyText(matcher.search.Keyword, matcher.search.CaseSensitive)) {
			if !fuzzyContains(normalizedTarget, word) {
				return false
			}
		}
		exclude := matcher.search.Exclude
		if !matcher.search.CaseSensitive {
			target, exclude = strings.ToLower(target), strings.ToLower(exclude)
		}
		for _, word := range strings.Fields(exclude) {
			if strings.Contains(target, word) {
				return false
			}
		}
		return true
	}
	keyword, exclude := matcher.search.Keyword, matcher.search.Exclude
	if !matcher.search.CaseSensitive {
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

func (matcher ProgramMatcher) matchesContents(metadata catalogmodel.ProgramMetadata) bool {
	if len(matcher.search.Contents) == 0 {
		return true
	}
	matched := false
	for _, condition := range matcher.search.Contents {
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
	return matched != matcher.search.ExcludeContents
}

func (matcher ProgramMatcher) matchesComponents(metadata catalogmodel.ProgramMetadata) bool {
	if len(matcher.search.Video) > 0 {
		if metadata.Video == nil || !containsComponent(matcher.search.Video, metadata.Video.StreamContent, metadata.Video.ComponentType) {
			return false
		}
	}
	if len(matcher.search.Audio) > 0 {
		matched := false
		for _, audio := range metadata.Audios {
			if containsComponent(matcher.search.Audio, 2, audio.ComponentType) {
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

func (matcher ProgramMatcher) matchesDate(material catalogmodel.RevisionMaterial) bool {
	if len(matcher.search.Dates) == 0 {
		return true
	}
	if material.StartUTCMS == nil || *material.StartUTCMS < 0 {
		return false
	}
	local := time.UnixMilli(*material.StartUTCMS).In(japanStandardTime)
	minute := int(local.Weekday())*24*60 + local.Hour()*60 + local.Minute()
	matched := false
	for _, value := range matcher.search.Dates {
		from := int(value.StartDay)*24*60 + int(value.StartHour)*60 + int(value.StartMinute)
		to := int(value.EndDay)*24*60 + int(value.EndHour)*60 + int(value.EndMinute)
		if from == to || from < to && minute >= from && minute < to || from > to && (minute >= from || minute < to) {
			matched = true
			break
		}
	}
	return matched != matcher.search.ExcludeDates
}

func (matcher ProgramMatcher) matchesDuration(material catalogmodel.RevisionMaterial) bool {
	if matcher.search.MinimumMinutes == 0 && matcher.search.MaximumMinutes == 0 {
		return true
	}
	if material.DurationMS == nil || *material.DurationMS < 0 {
		return false
	}
	minutes := uint64(*material.DurationMS / int64(time.Minute/time.Millisecond))
	return (matcher.search.MinimumMinutes == 0 || minutes >= uint64(matcher.search.MinimumMinutes)) &&
		(matcher.search.MaximumMinutes == 0 || minutes <= uint64(matcher.search.MaximumMinutes))
}

func (matcher ProgramMatcher) matchesFreeAccess(material catalogmodel.RevisionMaterial) bool {
	switch matcher.search.FreeAccess {
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
