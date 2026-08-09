package programsearch

import (
	"strings"
	"unicode"
)

var halfwidthKatakana = []rune("。「」、・ヲァィゥェォャュョッーアイウエオカキクケコサシスセソタチツテトナニヌネノハヒフヘホマミムメモヤユヨラリルレロワン")

// normalizeFuzzyTextはKonomiTVのあいまい検索で同一視する表記を標準ライブラリだけで揃える。
func normalizeFuzzyText(value string, caseSensitive bool) string {
	result := make([]rune, 0, len(value))
	for _, original := range value {
		character := original
		switch {
		case character >= 0xff01 && character <= 0xff5e:
			character -= 0xfee0
		case character == 0x3000:
			character = ' '
		case character >= 0x3041 && character <= 0x3096:
			character += 0x60
		case character >= 0xff61 && character <= 0xff9d:
			character = halfwidthKatakana[character-0xff61]
		case character == 0xff9e || character == 0x3099 || character == 0x309b:
			if composeKana(&result, true) {
				continue
			}
			character = 0x3099
		case character == 0xff9f || character == 0x309a || character == 0x309c:
			if composeKana(&result, false) {
				continue
			}
			character = 0x309a
		case unicode.IsSpace(character):
			character = ' '
		}
		if !caseSensitive && character >= 'A' && character <= 'Z' {
			character += 'a' - 'A'
		}
		result = append(result, character)
	}
	return string(result)
}

func composeKana(value *[]rune, voiced bool) bool {
	if len(*value) == 0 {
		return false
	}
	last := (*value)[len(*value)-1]
	var composed rune
	if voiced {
		switch last {
		case 'ウ':
			composed = 'ヴ'
		case 'ワ':
			composed = 'ヷ'
		case 'ヰ':
			composed = 'ヸ'
		case 'ヱ':
			composed = 'ヹ'
		case 'ヲ':
			composed = 'ヺ'
		case 'カ', 'キ', 'ク', 'ケ', 'コ', 'サ', 'シ', 'ス', 'セ', 'ソ', 'タ', 'チ', 'ツ', 'テ', 'ト', 'ハ', 'ヒ', 'フ', 'ヘ', 'ホ':
			composed = last + 1
		}
	} else {
		switch last {
		case 'ハ', 'ヒ', 'フ', 'ヘ', 'ホ':
			composed = last + 2
		}
	}
	if composed == 0 {
		return false
	}
	(*value)[len(*value)-1] = composed
	return true
}

// fuzzyContainsは検索語の25%以下の編集距離で一致する部分文字列を2行の配列だけで探す。
func fuzzyContains(target, keyword string) bool {
	pattern := []rune(keyword)
	if len(pattern) == 0 {
		return true
	}
	if strings.Contains(target, keyword) {
		return true
	}
	threshold := len(pattern) / 4
	if threshold == 0 {
		return false
	}
	previous := make([]int, len(pattern)+1)
	current := make([]int, len(pattern)+1)
	for index := range previous {
		previous[index] = index
	}
	for _, character := range target {
		current[0] = 0
		for index, expected := range pattern {
			cost := 0
			if character != expected {
				cost = 1
			}
			current[index+1] = min(previous[index+1]+1, current[index]+1, previous[index]+cost)
		}
		if current[len(pattern)] <= threshold {
			return true
		}
		previous, current = current, previous
	}
	return false
}
