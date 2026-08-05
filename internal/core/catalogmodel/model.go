// Package catalogmodelはproviderやSQL表現に依存しない番組identityとrevisionの中核型を提供する。
package catalogmodel

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// IDはSQLiteでは16-byte BLOBとして保存するopaqueな内部entity IDである。
type ID [16]byte

// NewIDはRFC 9562 UUID version 4互換のIDを暗号学的乱数から生成する。
func NewID() (ID, error) {
	return NewIDFrom(rand.Reader)
}

// NewIDFromはテスト可能な乱数源からIDを生成し、短いreadや失敗時はfallbackせずエラーを返す。
func NewIDFrom(source io.Reader) (ID, error) {
	var id ID
	if source == nil {
		return ID{}, errors.New("catalogmodel: nil random source")
	}
	if _, err := io.ReadFull(source, id[:]); err != nil {
		return ID{}, fmt.Errorf("catalogmodel: generate id: %w", err)
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return id, nil
}

// ParseIDはlowercase canonical UUIDv4だけをIDへ変換する。
func ParseID(value string) (ID, error) {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || value != strings.ToLower(value) {
		return ID{}, errors.New("catalogmodel: invalid canonical id")
	}
	compact := value[0:8] + value[9:13] + value[14:18] + value[19:23] + value[24:36]
	decoded, err := hex.DecodeString(compact)
	if err != nil || len(decoded) != 16 {
		return ID{}, errors.New("catalogmodel: invalid canonical id")
	}
	var id ID
	copy(id[:], decoded)
	if id[6]>>4 != 4 || id[8]>>6 != 2 {
		return ID{}, errors.New("catalogmodel: id is not UUIDv4")
	}
	return id, nil
}

// StringはIDをlowercase canonical UUID表現へ変換する。
func (id ID) String() string {
	var text [36]byte
	hex.Encode(text[0:8], id[0:4])
	text[8] = '-'
	hex.Encode(text[9:13], id[4:6])
	text[13] = '-'
	hex.Encode(text[14:18], id[6:8])
	text[18] = '-'
	hex.Encode(text[19:23], id[8:10])
	text[23] = '-'
	hex.Encode(text[24:36], id[10:16])
	return string(text[:])
}

// BytesはDBへ渡せるdefensive copyを返す。
func (id ID) Bytes() []byte {
	value := make([]byte, len(id))
	copy(value, id[:])
	return value
}

// IdentityStateは内部identityの確度を表し、自動追従の可否と混同しない。
type IdentityState string

const (
	// IdentityVerifiedはAcceptedなlineage factsでidentityを確定できる状態である。
	IdentityVerified IdentityState = "VERIFIED"
	// IdentityProvisionalはprovider factsが不足し、cross-backend mergeへ使えない状態である。
	IdentityProvisional IdentityState = "PROVISIONAL"
	// IdentityAmbiguousは複数候補を安全に区別できない状態である。
	IdentityAmbiguous IdentityState = "AMBIGUOUS"
)

// Classificationは1つの番組観測を既存履歴へ関連付けた結果を表す。
type Classification string

const (
	// SameContentは同じinstanceと同じcanonical revisionへの再観測を表す。
	SameContent Classification = "SAME_CONTENT"
	// VerifiedSuccessorは明示的に証明された同一番組の新revisionを表す。
	VerifiedSuccessor Classification = "VERIFIED_SUCCESSOR"
	// Ambiguousは既存番組へ安全に関連付けられない観測を表す。
	Ambiguous Classification = "AMBIGUOUS"
	// NewInstanceは新しい番組instanceとして保存する観測を表す。
	NewInstance Classification = "NEW_INSTANCE"
	// Invalidはrevisionへ変換できない観測を表す。
	Invalid Classification = "INVALID"
)

// FreeAccessは無料放送flagのunknownを保持できる3値型である。
type FreeAccess uint8

const (
	// FreeUnknownはproviderが無料状態を提供しなかったことを表す。
	FreeUnknown FreeAccess = iota
	// FreeNoは無料放送ではないことを表す。
	FreeNo
	// FreeYesは無料放送であることを表す。
	FreeYes
)

// Validationはrevision materialの検証状態を表す。
type Validation uint8

const (
	// ValidationValidはAcceptedなfield規則をすべて満たした状態である。
	ValidationValid Validation = iota + 1
	// ValidationProvisionalは保存できるが、確定identityへ使えない状態である。
	ValidationProvisional
	// ValidationInvalidはrevisionとして利用できない状態である。
	ValidationInvalid
)

// RevisionMaterialはcanonical encoding v1の意味fieldだけを保持する。
// nil pointerはunknown、空文字へのpointerはknown-emptyとして区別する。
type RevisionMaterial struct {
	StartUTCMS  *int64
	DurationMS  *int64
	Title       *string
	Description *string
	FreeAccess  FreeAccess
	Validation  Validation
}

// ProgramRevisionはimmutableな番組revisionを表す。
type ProgramRevision struct {
	ID         ID
	InstanceID ID
	Number     int64
	Hash       [32]byte
	Material   RevisionMaterial
	CreatedMS  int64
}
