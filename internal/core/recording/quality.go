package recording

import "errors"

// IsCommunicationPartialReasonは安全な確定を許可する通信終了理由だけを返す。
func IsCommunicationPartialReason(reason TerminalReason) bool {
	return reason == ReasonStreamEndedEarly || reason == ReasonStreamTimeout || reason == ReasonStreamReconnectExhausted
}

// QualityStatusは保存処理の状態とは独立した、有界な品質観測の結果である。
type QualityStatus uint8

const (
	QualityUnknown QualityStatus = iota
	QualityNoIssuesObserved
	QualityDegraded
)

// Stringは品質状態を保存・応答用の文字列へ変換する。
func (status QualityStatus) String() string {
	switch status {
	case QualityUnknown:
		return "UNKNOWN"
	case QualityNoIssuesObserved:
		return "NO_ISSUES_OBSERVED"
	case QualityDegraded:
		return "DEGRADED"
	default:
		return ""
	}
}

// ParseQualityStatusは保存された品質状態を読み取り、未知の値を拒否する。
func ParseQualityStatus(value string) (QualityStatus, error) {
	switch value {
	case "UNKNOWN":
		return QualityUnknown, nil
	case "NO_ISSUES_OBSERVED":
		return QualityNoIssuesObserved, nil
	case "DEGRADED":
		return QualityDegraded, nil
	default:
		return QualityUnknown, errors.New("recording: invalid quality status")
	}
}

// QualitySummaryは固定件数だけを保持する。旧行のzero valueは未測定を表す。
type QualitySummary struct {
	Status                                                                       QualityStatus
	SelectionUnverified, ObservationLimited, CountersSaturated                   bool
	CCGapEvents, CCDuplicateEvents, TEIPackets, MalformedPacketEvents            int64
	PSIContinuityEvents, PSICRCEvents, PSIStructureEvents, PSILimitEvents        int64
	SyncLossEvents, SyncRecoveredEvents, SyncDiscardedBytes                      int64
	TrailingIncompleteBytes, UnfinishedPSIEvents, FallbackEvents, ReconnectCount int64
}

// Validateは品質状態と固定カウンターの値域を検証する。
func (quality QualitySummary) Validate() error {
	if quality.Status > QualityDegraded || quality.FallbackEvents < 0 || quality.FallbackEvents > 1 || quality.ReconnectCount < 0 || quality.ReconnectCount > 3 {
		return errors.New("recording: invalid quality summary")
	}
	for _, count := range [...]int64{quality.CCGapEvents, quality.CCDuplicateEvents, quality.TEIPackets, quality.MalformedPacketEvents,
		quality.PSIContinuityEvents, quality.PSICRCEvents, quality.PSIStructureEvents, quality.PSILimitEvents,
		quality.SyncLossEvents, quality.SyncRecoveredEvents, quality.SyncDiscardedBytes, quality.TrailingIncompleteBytes, quality.UnfinishedPSIEvents} {
		if count < 0 || count > 2147483647 {
			return errors.New("recording: invalid quality counter")
		}
	}
	return nil
}
