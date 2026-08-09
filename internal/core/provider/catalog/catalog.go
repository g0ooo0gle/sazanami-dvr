// Package catalogは上限付きpull型catalog Portを定義する。
package catalog

import (
	"context"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
)

// ServiceRequestはservice cursor 1件の相関IDとpage上限を指定する。
type ServiceRequest struct {
	CorrelationID string
	Limit         int
}

// ProgramRequestはprogram cursor 1件の相関IDとpage上限を指定する。
type ProgramRequest struct {
	CorrelationID string
	Limit         int
}

// ServiceObservationはbackendから観測したservice 1件を表す。
type ServiceObservation struct {
	Provenance   provider.Provenance
	Locator      string
	Broadcast    string
	NetworkID    uint16
	ServiceID    uint16
	DisplayName  string
	TuningTarget provider.TuningTarget
	Validation   provider.ValidationState
}

// ProgramObservationはbackendから観測した番組1件を表す。
// StartとDurationをpointerにして、未知値とzero値を区別する。
type ProgramObservation struct {
	Provenance          provider.Provenance
	ServiceLocator      string
	EventLocator        string
	EventID             *uint16
	Start               *time.Time
	Duration            *time.Duration
	Title               string
	Description         string
	FreeAccess          *bool
	RevisionFingerprint string
	Validation          provider.ValidationState
	Extended            []ProgramExtended
	Genres              []ProgramGenre
	Video               *ProgramVideo
	Audios              []ProgramAudio
}

// ProgramExtendedはproviderから観測した番組詳細の見出しと本文を表す。
type ProgramExtended struct {
	Heading string
	Body    string
}

// ProgramGenreはproviderから観測した番組ジャンルの分類値を表す。
type ProgramGenre struct {
	Level1 uint8
	Level2 uint8
	User1  uint8
	User2  uint8
}

// ProgramVideoはproviderから観測した映像componentを表す。
type ProgramVideo struct {
	StreamContent uint8
	ComponentType uint8
}

// ProgramAudioはproviderから観測した音声componentを表す。
type ProgramAudio struct {
	ComponentType uint8
	ComponentTag  uint8
	Main          bool
	SamplingRate  uint32
	Languages     []string
}

// ServicePageはserviceのbounded pageと終端有無を返す。
type ServicePage struct {
	Items []ServiceObservation
	End   bool
}

// ProgramPageは番組のbounded pageと終端有無を返す。
type ProgramPage struct {
	Items []ProgramObservation
	End   bool
}

// CloneServicePageは呼び出し元とslice backing arrayを共有しないcopyを返す。
func CloneServicePage(page ServicePage) ServicePage {
	items := make([]ServiceObservation, len(page.Items))
	copy(items, page.Items)
	return ServicePage{Items: items, End: page.End}
}

// CloneProgramPageはsliceとoptional値をdeep copyして返す。
func CloneProgramPage(page ProgramPage) ProgramPage {
	items := make([]ProgramObservation, len(page.Items))
	copy(items, page.Items)
	for i := range items {
		if page.Items[i].Start != nil {
			value := *page.Items[i].Start
			items[i].Start = &value
		}
		if page.Items[i].Duration != nil {
			value := *page.Items[i].Duration
			items[i].Duration = &value
		}
		if page.Items[i].EventID != nil {
			value := *page.Items[i].EventID
			items[i].EventID = &value
		}
		if page.Items[i].FreeAccess != nil {
			value := *page.Items[i].FreeAccess
			items[i].FreeAccess = &value
		}
		items[i].Extended = append([]ProgramExtended(nil), page.Items[i].Extended...)
		items[i].Genres = append([]ProgramGenre(nil), page.Items[i].Genres...)
		if page.Items[i].Video != nil {
			value := *page.Items[i].Video
			items[i].Video = &value
		}
		items[i].Audios = make([]ProgramAudio, len(page.Items[i].Audios))
		for audioIndex := range page.Items[i].Audios {
			items[i].Audios[audioIndex] = page.Items[i].Audios[audioIndex]
			items[i].Audios[audioIndex].Languages = append([]string(nil), page.Items[i].Audios[audioIndex].Languages...)
		}
	}
	return ProgramPage{Items: items, End: page.End}
}

// ServiceCursorはserviceをpage単位でpullし、resourceを明示的に閉じる。
type ServiceCursor interface {
	Next(context.Context) (ServicePage, error)
	Close() error
}

// ProgramCursorは番組をpage単位でpullし、resourceを明示的に閉じる。
type ProgramCursor interface {
	Next(context.Context) (ProgramPage, error)
	Close() error
}

// CatalogProviderはservice取得と番組取得のcursorを開くPortである。
type CatalogProvider interface {
	OpenServices(context.Context, ServiceRequest) (ServiceCursor, error)
	OpenPrograms(context.Context, ProgramRequest) (ProgramCursor, error)
}
