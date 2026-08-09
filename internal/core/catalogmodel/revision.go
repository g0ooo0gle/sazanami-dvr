package catalogmodel

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"unicode/utf8"
)

const (
	revisionPrefix      = "SZCATREV1"
	revisionV2Prefix    = "SZCATREV2"
	maxTitleBytes       = 4 * 1024
	maxDescriptionBytes = 64 * 1024
)

// EncodeRevisionV1は仕様で固定したfield順とlength prefixでrevision materialを符号化する。
func EncodeRevisionV1(material RevisionMaterial) ([]byte, error) {
	if material.DurationMS != nil && *material.DurationMS <= 0 {
		return nil, errors.New("catalogmodel: duration must be positive")
	}
	if material.FreeAccess > FreeYes {
		return nil, errors.New("catalogmodel: invalid free-access value")
	}
	if material.Validation < ValidationValid || material.Validation > ValidationInvalid {
		return nil, errors.New("catalogmodel: invalid validation value")
	}

	var output bytes.Buffer
	output.Grow(len(revisionPrefix) + 32)
	output.WriteString(revisionPrefix)
	writeOptionalInt64(&output, material.StartUTCMS)
	writeOptionalInt64(&output, material.DurationMS)
	if err := writeOptionalText(&output, material.Title, maxTitleBytes, "title"); err != nil {
		return nil, err
	}
	if err := writeOptionalText(&output, material.Description, maxDescriptionBytes, "description"); err != nil {
		return nil, err
	}
	output.WriteByte(byte(material.FreeAccess))
	output.WriteByte(byte(material.Validation))
	return output.Bytes(), nil
}

// HashRevisionV1はcanonical encoding v1のSHA-256を返す。
func HashRevisionV1(material RevisionMaterial) ([32]byte, error) {
	encoded, err := EncodeRevisionV1(material)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

// EncodeRevisionV2は基本項目とcanonical metadataを一つのrevision materialとして符号化する。
func EncodeRevisionV2(material RevisionMaterial) ([]byte, error) {
	base, err := EncodeRevisionV1(material)
	if err != nil {
		return nil, err
	}
	metadata, err := EncodeMetadataV1(material.Metadata)
	if err != nil {
		return nil, err
	}
	if len(metadata) == 0 {
		return nil, errors.New("catalogmodel: revision v2 requires metadata")
	}
	var output bytes.Buffer
	output.Grow(len(revisionV2Prefix) + 8 + len(base) + len(metadata))
	output.WriteString(revisionV2Prefix)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(base)))
	output.Write(size[:])
	output.Write(base)
	binary.BigEndian.PutUint32(size[:], uint32(len(metadata)))
	output.Write(size[:])
	output.Write(metadata)
	return output.Bytes(), nil
}

// HashRevisionはmetadataが空なら既存v1、それ以外はv2のSHA-256を返す。
func HashRevision(material RevisionMaterial) ([32]byte, error) {
	if material.Metadata.Empty() {
		return HashRevisionV1(material)
	}
	encoded, err := EncodeRevisionV2(material)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

func writeOptionalInt64(output *bytes.Buffer, value *int64) {
	if value == nil {
		output.WriteByte(0)
		return
	}
	output.WriteByte(1)
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], uint64(*value))
	output.Write(raw[:])
}

func writeOptionalText(output *bytes.Buffer, value *string, maximum int, field string) error {
	if value == nil {
		output.WriteByte(0)
		return nil
	}
	if !utf8.ValidString(*value) {
		return fmt.Errorf("catalogmodel: %s is not valid UTF-8", field)
	}
	if len(*value) > maximum {
		return fmt.Errorf("catalogmodel: %s exceeds %d bytes", field, maximum)
	}
	output.WriteByte(1)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(*value)))
	output.Write(size[:])
	output.WriteString(*value)
	return nil
}
