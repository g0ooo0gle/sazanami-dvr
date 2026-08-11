package mirakurun

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/catalog"
)

const (
	maxUnknownTokens = 4_096
	maxJSONDepth     = 32
)

func decodeVersion(decoder *json.Decoder) (VersionObservation, error) {
	var result VersionObservation
	seen, err := beginObject(decoder)
	if err != nil {
		return result, err
	}
	hasCurrent := false
	for decoder.More() {
		key, err := readObjectKey(decoder, seen)
		if err != nil {
			return result, err
		}
		switch key {
		case "current":
			result.Current, err = readString(decoder, 128)
			hasCurrent = err == nil
		case "latest":
			result.Latest, err = readString(decoder, 128)
		default:
			count := 0
			err = skipValue(decoder, 0, &count)
		}
		if err != nil {
			return VersionObservation{}, err
		}
	}
	if err := endObject(decoder); err != nil {
		return VersionObservation{}, err
	}
	if !hasCurrent || result.Current == "" {
		return VersionObservation{}, provider.NewFailure(provider.ReasonMalformed, "version-current-required")
	}
	if strings.IndexFunc(result.Current, unicode.IsControl) >= 0 || strings.IndexFunc(result.Latest, unicode.IsControl) >= 0 {
		return VersionObservation{}, provider.NewFailure(provider.ReasonMalformed, "version-text-invalid")
	}
	return result, nil
}

func decodeTuner(decoder *json.Decoder) error {
	seen, err := beginObject(decoder)
	if err != nil {
		return err
	}
	tokens := 0
	for decoder.More() {
		if _, err := readObjectKey(decoder, seen); err != nil {
			return err
		}
		if err := skipValue(decoder, 0, &tokens); err != nil {
			return err
		}
	}
	return endObject(decoder)
}

func decodeService(decoder *json.Decoder, provenance provider.Provenance) (catalog.ServiceObservation, error) {
	var result catalog.ServiceObservation
	seen, err := beginObject(decoder)
	if err != nil {
		return result, err
	}
	var id uint64
	var serviceType uint64
	var hasID, hasNetwork, hasService, hasName, hasType bool
	for decoder.More() {
		key, err := readObjectKey(decoder, seen)
		if err != nil {
			return result, err
		}
		switch key {
		case "id":
			id, err = readUint(decoder, math.MaxInt64)
			hasID = err == nil
		case "networkId":
			value, valueErr := readUint(decoder, math.MaxUint16)
			err = valueErr
			result.NetworkID = uint16(value)
			hasNetwork = err == nil
		case "serviceId":
			value, valueErr := readUint(decoder, math.MaxUint16)
			err = valueErr
			result.ServiceID = uint16(value)
			hasService = err == nil
		case "name":
			result.DisplayName, err = readString(decoder, 4_096)
			hasName = err == nil
		case "type":
			serviceType, err = readUint(decoder, math.MaxUint16)
			hasType = err == nil
		default:
			count := 0
			err = skipValue(decoder, 0, &count)
		}
		if err != nil {
			return catalog.ServiceObservation{}, err
		}
	}
	if err := endObject(decoder); err != nil {
		return catalog.ServiceObservation{}, err
	}
	if !hasID || !hasNetwork || !hasService || !hasName || !hasType {
		return catalog.ServiceObservation{}, provider.NewFailure(provider.ReasonMalformed, "service-required-field-missing")
	}
	derived := serviceProviderID(result.NetworkID, result.ServiceID)
	if id != derived {
		return catalog.ServiceObservation{}, provider.NewFailure(provider.ReasonMalformed, "service-id-contract-mismatch")
	}
	result.Provenance = provenance
	result.Locator = numberText(id)
	result.Broadcast = numberText(serviceType)
	result.Validation = provider.ValidationUnknown
	return result, nil
}

func decodeProgram(decoder *json.Decoder, provenance provider.Provenance) (catalog.ProgramObservation, error) {
	var result catalog.ProgramObservation
	seen, err := beginObject(decoder)
	if err != nil {
		return result, err
	}
	var id uint64
	var networkID, serviceID, eventID uint16
	var startMS, durationMS *int64
	var free bool
	var hasID, hasNetwork, hasService, hasEvent, hasStart, hasDuration, hasFree bool
	for decoder.More() {
		key, err := readObjectKey(decoder, seen)
		if err != nil {
			return result, err
		}
		switch key {
		case "id":
			id, err = readUint(decoder, math.MaxInt64)
			hasID = err == nil
		case "networkId":
			value, valueErr := readUint(decoder, math.MaxUint16)
			err, networkID = valueErr, uint16(value)
			hasNetwork = err == nil
		case "serviceId":
			value, valueErr := readUint(decoder, math.MaxUint16)
			err, serviceID = valueErr, uint16(value)
			hasService = err == nil
		case "eventId":
			value, valueErr := readUint(decoder, math.MaxUint16)
			err, eventID = valueErr, uint16(value)
			hasEvent = err == nil
		case "startAt":
			startMS, err = readNullableInt(decoder)
			hasStart = err == nil
		case "duration":
			durationMS, err = readNullableInt(decoder)
			hasDuration = err == nil
		case "isFree":
			free, err = readBool(decoder)
			hasFree = err == nil
		case "name":
			result.Title, err = readString(decoder, 4_096)
		case "description":
			result.Description, err = readString(decoder, 65_536)
		case "extended":
			result.Extended, err = decodeExtended(decoder)
		case "genres":
			result.Genres, err = decodeGenres(decoder)
		case "video":
			result.Video, err = decodeVideo(decoder)
		case "audios":
			result.Audios, err = decodeAudios(decoder)
		default:
			count := 0
			err = skipValue(decoder, 0, &count)
		}
		if err != nil {
			return catalog.ProgramObservation{}, err
		}
	}
	if err := endObject(decoder); err != nil {
		return catalog.ProgramObservation{}, err
	}
	if !hasID || !hasNetwork || !hasService || !hasEvent || !hasStart || !hasDuration || !hasFree {
		return catalog.ProgramObservation{}, provider.NewFailure(provider.ReasonMalformed, "program-required-field-missing")
	}
	if id != programProviderID(networkID, serviceID, eventID) {
		return catalog.ProgramObservation{}, provider.NewFailure(provider.ReasonMalformed, "program-id-contract-mismatch")
	}
	if durationMS != nil && (*durationMS <= 0 || *durationMS > math.MaxInt64/int64(time.Millisecond)) {
		return catalog.ProgramObservation{}, provider.NewFailure(provider.ReasonOverLimit, "program-duration-out-of-range")
	}
	result.Provenance = provenance
	result.ServiceLocator = numberText(serviceProviderID(networkID, serviceID))
	result.EventLocator = numberText(id)
	result.EventID = &eventID
	result.FreeAccess = &free
	result.Validation = provider.ValidationValid
	if startMS != nil {
		value := time.UnixMilli(*startMS).UTC()
		result.Start = &value
	} else {
		result.Validation = provider.ValidationUnknown
	}
	if durationMS != nil {
		value := time.Duration(*durationMS) * time.Millisecond
		result.Duration = &value
	} else {
		result.Validation = provider.ValidationUnknown
	}
	return result, nil
}

func decodeExtended(decoder *json.Decoder) ([]catalog.ProgramExtended, error) {
	seen, err := beginObject(decoder)
	if err != nil {
		return nil, err
	}
	items := make([]catalog.ProgramExtended, 0, 8)
	for decoder.More() {
		heading, keyErr := readObjectKey(decoder, seen)
		if keyErr != nil {
			return nil, keyErr
		}
		if len(items) >= 64 {
			return nil, provider.NewFailure(provider.ReasonOverLimit, "program-extended-count-over-limit")
		}
		if len(heading) > 4_096 {
			return nil, provider.NewFailure(provider.ReasonOverLimit, "program-extended-heading-over-limit")
		}
		body, bodyErr := readString(decoder, 65_536)
		if bodyErr != nil {
			return nil, bodyErr
		}
		items = append(items, catalog.ProgramExtended{Heading: heading, Body: body})
	}
	if err := endObject(decoder); err != nil {
		return nil, err
	}
	return items, nil
}

func decodeGenres(decoder *json.Decoder) ([]catalog.ProgramGenre, error) {
	if err := beginArray(decoder); err != nil {
		return nil, err
	}
	items := make([]catalog.ProgramGenre, 0, 8)
	for decoder.More() {
		if len(items) >= 64 {
			return nil, provider.NewFailure(provider.ReasonOverLimit, "program-genre-count-over-limit")
		}
		item, err := decodeGenre(decoder)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := endArray(decoder); err != nil {
		return nil, err
	}
	return items, nil
}

func decodeGenre(decoder *json.Decoder) (catalog.ProgramGenre, error) {
	seen, err := beginObject(decoder)
	if err != nil {
		return catalog.ProgramGenre{}, err
	}
	var item catalog.ProgramGenre
	var hasLevel1, hasLevel2, hasUser1, hasUser2 bool
	for decoder.More() {
		key, keyErr := readObjectKey(decoder, seen)
		if keyErr != nil {
			return catalog.ProgramGenre{}, keyErr
		}
		switch key {
		case "lv1":
			value, valueErr := readUint(decoder, math.MaxUint8)
			err, item.Level1, hasLevel1 = valueErr, uint8(value), valueErr == nil
		case "lv2":
			value, valueErr := readUint(decoder, math.MaxUint8)
			err, item.Level2, hasLevel2 = valueErr, uint8(value), valueErr == nil
		case "un1":
			value, valueErr := readUint(decoder, math.MaxUint8)
			err, item.User1, hasUser1 = valueErr, uint8(value), valueErr == nil
		case "un2":
			value, valueErr := readUint(decoder, math.MaxUint8)
			err, item.User2, hasUser2 = valueErr, uint8(value), valueErr == nil
		default:
			count := 0
			err = skipValue(decoder, 0, &count)
		}
		if err != nil {
			return catalog.ProgramGenre{}, err
		}
	}
	if err := endObject(decoder); err != nil {
		return catalog.ProgramGenre{}, err
	}
	if !hasLevel1 || !hasLevel2 || !hasUser1 || !hasUser2 {
		return catalog.ProgramGenre{}, provider.NewFailure(provider.ReasonMalformed, "program-genre-required-field-missing")
	}
	return item, nil
}

func decodeVideo(decoder *json.Decoder) (*catalog.ProgramVideo, error) {
	seen, err := beginObject(decoder)
	if err != nil {
		return nil, err
	}
	var item catalog.ProgramVideo
	var hasType, hasResolution, hasStream, hasComponent bool
	for decoder.More() {
		key, keyErr := readObjectKey(decoder, seen)
		if keyErr != nil {
			return nil, keyErr
		}
		switch key {
		case "type":
			_, err = readString(decoder, 32)
			hasType = err == nil
		case "resolution":
			_, err = readString(decoder, 32)
			hasResolution = err == nil
		case "streamContent":
			value, valueErr := readUint(decoder, math.MaxUint8)
			err, item.StreamContent, hasStream = valueErr, uint8(value), valueErr == nil
		case "componentType":
			value, valueErr := readUint(decoder, math.MaxUint8)
			err, item.ComponentType, hasComponent = valueErr, uint8(value), valueErr == nil
		default:
			count := 0
			err = skipValue(decoder, 0, &count)
		}
		if err != nil {
			return nil, err
		}
	}
	if err := endObject(decoder); err != nil {
		return nil, err
	}
	if !hasType || !hasResolution || !hasStream || !hasComponent {
		return nil, provider.NewFailure(provider.ReasonMalformed, "program-video-required-field-missing")
	}
	return &item, nil
}

func decodeAudios(decoder *json.Decoder) ([]catalog.ProgramAudio, error) {
	if err := beginArray(decoder); err != nil {
		return nil, err
	}
	items := make([]catalog.ProgramAudio, 0, 2)
	for decoder.More() {
		if len(items) >= 16 {
			return nil, provider.NewFailure(provider.ReasonOverLimit, "program-audio-count-over-limit")
		}
		item, err := decodeAudio(decoder)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := endArray(decoder); err != nil {
		return nil, err
	}
	return items, nil
}

func decodeAudio(decoder *json.Decoder) (catalog.ProgramAudio, error) {
	seen, err := beginObject(decoder)
	if err != nil {
		return catalog.ProgramAudio{}, err
	}
	var item catalog.ProgramAudio
	var hasType, hasTag, hasMain, hasRate, hasLanguages bool
	for decoder.More() {
		key, keyErr := readObjectKey(decoder, seen)
		if keyErr != nil {
			return catalog.ProgramAudio{}, keyErr
		}
		switch key {
		case "componentType":
			value, valueErr := readUint(decoder, math.MaxUint8)
			err, item.ComponentType, hasType = valueErr, uint8(value), valueErr == nil
		case "componentTag":
			value, valueErr := readUint(decoder, math.MaxUint8)
			err, item.ComponentTag, hasTag = valueErr, uint8(value), valueErr == nil
		case "isMain":
			item.Main, err = readBool(decoder)
			hasMain = err == nil
		case "samplingRate":
			value, valueErr := readUint(decoder, math.MaxUint32)
			err, item.SamplingRate, hasRate = valueErr, uint32(value), valueErr == nil
			if err == nil && !validAudioSamplingRate(item.SamplingRate) {
				err = provider.NewFailure(provider.ReasonMalformed, "program-audio-sampling-rate-invalid")
			}
		case "langs":
			item.Languages, err = decodeLanguages(decoder)
			hasLanguages = err == nil
		default:
			count := 0
			err = skipValue(decoder, 0, &count)
		}
		if err != nil {
			return catalog.ProgramAudio{}, err
		}
	}
	if err := endObject(decoder); err != nil {
		return catalog.ProgramAudio{}, err
	}
	if !hasType || !hasTag || !hasMain || !hasRate || !hasLanguages {
		return catalog.ProgramAudio{}, provider.NewFailure(provider.ReasonMalformed, "program-audio-required-field-missing")
	}
	return item, nil
}

func decodeLanguages(decoder *json.Decoder) ([]string, error) {
	if err := beginArray(decoder); err != nil {
		return nil, err
	}
	languages := make([]string, 0, 2)
	for decoder.More() {
		if len(languages) >= 2 {
			return nil, provider.NewFailure(provider.ReasonOverLimit, "program-audio-language-count-over-limit")
		}
		language, err := readString(decoder, 3)
		if err != nil {
			return nil, err
		}
		if !validAudioLanguage(language) {
			return nil, provider.NewFailure(provider.ReasonMalformed, "program-audio-language-invalid")
		}
		languages = append(languages, language)
	}
	if err := endArray(decoder); err != nil {
		return nil, err
	}
	return languages, nil
}

func beginArray(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '[' {
		return provider.NewFailure(provider.ReasonMalformed, "json-array-required")
	}
	return nil
}

func endArray(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != ']' {
		return provider.NewFailure(provider.ReasonMalformed, "json-array-end-required")
	}
	return nil
}

func validAudioSamplingRate(rate uint32) bool {
	switch rate {
	case 16_000, 22_050, 24_000, 32_000, 44_100, 48_000:
		return true
	default:
		return false
	}
}

func validAudioLanguage(language string) bool {
	switch language {
	case "jpn", "eng", "deu", "fra", "ita", "rus", "zho", "kor", "spa", "etc":
		return true
	default:
		return false
	}
}

func beginObject(decoder *json.Decoder) (map[string]struct{}, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, provider.NewFailure(provider.ReasonMalformed, "json-object-required")
	}
	return make(map[string]struct{}, 16), nil
}

func endObject(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '}' {
		return provider.NewFailure(provider.ReasonMalformed, "json-object-end-required")
	}
	return nil
}

func readObjectKey(decoder *json.Decoder, seen map[string]struct{}) (string, error) {
	token, err := decoder.Token()
	if err != nil {
		return "", err
	}
	key, ok := token.(string)
	if !ok || invalidJSONString(key) {
		return "", provider.NewFailure(provider.ReasonMalformed, "invalid-json-key")
	}
	if _, duplicate := seen[key]; duplicate {
		return "", provider.NewFailure(provider.ReasonMalformed, "duplicate-json-key")
	}
	seen[key] = struct{}{}
	return key, nil
}

func readString(decoder *json.Decoder, limit int) (string, error) {
	token, err := decoder.Token()
	if err != nil {
		return "", err
	}
	value, ok := token.(string)
	if !ok || invalidJSONString(value) {
		return "", provider.NewFailure(provider.ReasonMalformed, "json-string-required")
	}
	if len(value) > limit {
		return "", provider.NewFailure(provider.ReasonOverLimit, "json-string-over-limit")
	}
	return value, nil
}

func readBool(decoder *json.Decoder) (bool, error) {
	token, err := decoder.Token()
	if err != nil {
		return false, err
	}
	value, ok := token.(bool)
	if !ok {
		return false, provider.NewFailure(provider.ReasonMalformed, "json-boolean-required")
	}
	return value, nil
}

func readUint(decoder *json.Decoder, maximum uint64) (uint64, error) {
	token, err := decoder.Token()
	if err != nil {
		return 0, err
	}
	number, ok := token.(json.Number)
	if !ok {
		return 0, provider.NewFailure(provider.ReasonMalformed, "json-integer-required")
	}
	value, err := strconv.ParseUint(string(number), 10, 64)
	if err != nil {
		return 0, provider.NewFailure(provider.ReasonMalformed, "json-integer-invalid")
	}
	if value > maximum {
		return 0, provider.NewFailure(provider.ReasonOverLimit, "json-integer-overflow")
	}
	return value, nil
}

func readNullableInt(decoder *json.Decoder) (*int64, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if token == nil {
		return nil, nil
	}
	number, ok := token.(json.Number)
	if !ok {
		return nil, provider.NewFailure(provider.ReasonMalformed, "json-nullable-integer-required")
	}
	value, err := strconv.ParseInt(string(number), 10, 64)
	if err != nil {
		return nil, provider.NewFailure(provider.ReasonOverLimit, "json-integer-overflow")
	}
	return &value, nil
}

func skipValue(decoder *json.Decoder, depth int, count *int) error {
	if depth > maxJSONDepth || *count >= maxUnknownTokens {
		return provider.NewFailure(provider.ReasonOverLimit, "unknown-json-structure-over-limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	*count = *count + 1
	if text, ok := token.(string); ok && invalidJSONString(text) {
		return provider.NewFailure(provider.ReasonMalformed, "invalid-json-string")
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '[':
		for decoder.More() {
			if err := skipValue(decoder, depth+1, count); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return provider.NewFailure(provider.ReasonMalformed, "unknown-json-array-invalid")
		}
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			if _, err := readObjectKey(decoder, seen); err != nil {
				return err
			}
			if err := skipValue(decoder, depth+1, count); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return provider.NewFailure(provider.ReasonMalformed, "unknown-json-object-invalid")
		}
	default:
		return provider.NewFailure(provider.ReasonMalformed, "unknown-json-delimiter-invalid")
	}
	return nil
}

func invalidJSONString(value string) bool {
	return !utf8.ValidString(value) || strings.ContainsRune(value, utf8.RuneError)
}

func serviceProviderID(networkID, serviceID uint16) uint64 {
	return uint64(networkID)*100_000 + uint64(serviceID)
}

func programProviderID(networkID, serviceID, eventID uint16) uint64 {
	return uint64(networkID)*10_000_000_000 + uint64(serviceID)*100_000 + uint64(eventID)
}
