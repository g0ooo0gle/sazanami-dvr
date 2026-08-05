package sqlite

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const maxManifestBytes = 64 * 1024

func decodeStrictJSONObject(input io.Reader, destination any) error {
	data, err := io.ReadAll(io.LimitReader(input, maxManifestBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxManifestBytes {
		return errors.New("sqlite: invalid manifest size")
	}
	scanner := json.NewDecoder(bytes.NewReader(data))
	scanner.UseNumber()
	if err := scanUniqueJSONValue(scanner); err != nil {
		return err
	}
	if _, err := scanner.Token(); err != io.EOF {
		return errors.New("sqlite: trailing manifest data")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("sqlite: decode strict manifest")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("sqlite: trailing manifest data")
	}
	return nil
}

func scanUniqueJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return errors.New("sqlite: decode manifest token")
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return errors.New("sqlite: decode manifest key")
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("sqlite: invalid manifest key")
			}
			if _, duplicate := keys[key]; duplicate {
				return errors.New("sqlite: duplicate manifest key")
			}
			keys[key] = struct{}{}
			if err := scanUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("sqlite: invalid manifest object")
		}
	case '[':
		for decoder.More() {
			if err := scanUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("sqlite: invalid manifest array")
		}
	default:
		return errors.New("sqlite: invalid manifest delimiter")
	}
	return nil
}
