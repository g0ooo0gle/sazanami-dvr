// Package autoreservationは通信形式やSQLに依存しない自動予約条件を提供する。
package autoreservation

import (
	"errors"
	"unicode/utf8"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

const (
	// MaxRulesは一つのDBへ保存できる自動予約条件数である。
	MaxRules = 128
	// MaxProgramsPerRunは一回の評価で調べる番組数である。
	MaxProgramsPerRun = 32_768
	// MaxComparisonsPerRunは一回の評価で許す条件と番組の組合せ数である。
	MaxComparisonsPerRun = 4_194_304
	// MaxReservationsPerRunは一回の評価で追加できる予約数である。
	MaxReservationsPerRun = 256
	// MaxPageは永続化された条件を一度に読む上限である。
	MaxPage = 128

	maxTextBytes  = 4_096
	maxServices   = 4_096
	maxContents   = 256
	maxDates      = 64
	maxComponents = 256
	maxFolders    = 16
)

// ContentRangeはARIBの大分類・中分類と利用者定義分類を保持する。
type ContentRange struct {
	Content uint16
	User    uint16
}

// DateRangeはAsia/Tokyoの曜日と時刻で表した検索区間である。
type DateRange struct {
	StartDay, EndDay       uint8
	StartHour, EndHour     uint16
	StartMinute, EndMinute uint16
}

// ServiceRangeは検索対象の放送サービスを三つの放送IDで表す。
type ServiceRange struct {
	NetworkID, TransportStreamID, ServiceID uint16
}

// SearchConditionは自動予約の番組検索条件を通信形式から独立して保持する。
type SearchCondition struct {
	Enabled, CaseSensitive, Regex, TitleOnly, Fuzzy   bool
	Keyword, Exclude                                  string
	Contents                                          []ContentRange
	Dates                                             []DateRange
	Services                                          []ServiceRange
	Video, Audio                                      []uint16
	ExcludeContents, ExcludeDates                     bool
	FreeAccess                                        uint8
	CheckRecordedTitle, CheckRecordedAllServices      bool
	CheckRecordedDays, MinimumMinutes, MaximumMinutes uint16
}

// Folderは録画先とファイル名指定を保持する。空の一覧は既定保存先を表す。
type Folder struct {
	Path, Writer, Name string
}

// RecordingSettingsは自動予約から作る録画の設定を通信形式から独立して保持する。
type RecordingSettings struct {
	Mode, Priority, Suspend, PartialMode uint8
	Follow, Exact, Reboot, Continue      bool
	ServiceMode, TunerID                 uint32
	Batch                                string
	Folders, PartialFolders              []Folder
	StartMargin, EndMargin               *int32
}

// Ruleは永続化された一つの自動予約条件である。
type Rule struct {
	ID               catalogmodel.ID
	Number           int32
	Version          int64
	Search           SearchCondition
	Recording        RecordingSettings
	ReservationCount int32
	CreatedAtUTCMS   int64
	UpdatedAtUTCMS   int64
}

// ValidateNewは番号割当て前の規則が保存可能かを検証する。
func (rule Rule) ValidateNew() error {
	if rule.ID == (catalogmodel.ID{}) || rule.Number != 0 || rule.Version != 1 || rule.ReservationCount != 0 ||
		rule.CreatedAtUTCMS < 0 || rule.UpdatedAtUTCMS != rule.CreatedAtUTCMS {
		return errors.New("autoreservation: invalid new rule")
	}
	return validateValues(rule.Search, rule.Recording)
}

// ValidateStoredはDBやCtrlCmdへ返す規則の番号、版、時刻、条件を検証する。
func (rule Rule) ValidateStored() error {
	if rule.ID == (catalogmodel.ID{}) || rule.Number < 1 || rule.Version < 1 || rule.ReservationCount < 0 ||
		rule.CreatedAtUTCMS < 0 || rule.UpdatedAtUTCMS < rule.CreatedAtUTCMS {
		return errors.New("autoreservation: invalid stored rule")
	}
	return validateValues(rule.Search, rule.Recording)
}

// ValidateChangeは既存番号へ保存できる新しい条件かを検証する。
func ValidateChange(number int32, search SearchCondition, settings RecordingSettings) error {
	if number < 1 {
		return errors.New("autoreservation: invalid rule number")
	}
	return validateValues(search, settings)
}

func validateValues(search SearchCondition, settings RecordingSettings) error {
	if !validText(search.Keyword) || !validText(search.Exclude) || len(search.Services) > maxServices ||
		len(search.Contents) > maxContents || len(search.Dates) > maxDates || len(search.Video) > maxComponents ||
		len(search.Audio) > maxComponents || search.FreeAccess > 2 ||
		search.CheckRecordedDays > 9_999 ||
		(search.MinimumMinutes > 0 && search.MaximumMinutes > 0 && search.MinimumMinutes > search.MaximumMinutes) {
		return errors.New("autoreservation: invalid search condition")
	}
	for _, date := range search.Dates {
		if date.StartDay > 6 || date.EndDay > 6 || date.StartHour > 23 || date.EndHour > 23 ||
			date.StartMinute > 59 || date.EndMinute > 59 {
			return errors.New("autoreservation: invalid date range")
		}
	}
	if settings.Mode > 9 || settings.Priority < 1 || settings.Priority > 5 || settings.Suspend > 4 ||
		settings.PartialMode > 1 || !validText(settings.Batch) || len(settings.Folders) > maxFolders ||
		len(settings.PartialFolders) > maxFolders || (settings.StartMargin == nil) != (settings.EndMargin == nil) {
		return errors.New("autoreservation: invalid recording settings")
	}
	if settings.StartMargin != nil && (*settings.StartMargin < -3600 || *settings.StartMargin > 3600 ||
		*settings.EndMargin < -3600 || *settings.EndMargin > 3600) {
		return errors.New("autoreservation: invalid recording margins")
	}
	for _, folder := range append(append([]Folder(nil), settings.Folders...), settings.PartialFolders...) {
		if !validText(folder.Path) || !validText(folder.Writer) || !validText(folder.Name) {
			return errors.New("autoreservation: invalid recording folder")
		}
	}
	return nil
}

func validText(value string) bool {
	return utf8.ValidString(value) && len([]byte(value)) <= maxTextBytes
}
