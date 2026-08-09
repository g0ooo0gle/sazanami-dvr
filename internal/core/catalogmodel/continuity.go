package catalogmodel

const (
	mirakurunObservationHorizonMS = int64(36 * 60 * 60 * 1_000)
	mirakurunMaximumShiftMS       = int64(6 * 60 * 60 * 1_000)
	mirakurunMinimumDurationMS    = int64(60 * 1_000)
	mirakurunMaximumDurationMS    = int64(12 * 60 * 60 * 1_000)
)

// MirakurunSuccessorは同じMirakurun内の短期間の観測を、予約追従に使える後継と判定する。
// cross-backend identityの証明には使わず、locator一致は呼び出し側が確認する。
func MirakurunSuccessor(previous RevisionMaterial, previousEventID *int64, previousSeenMS int64,
	current RevisionMaterial, currentEventID *int64, currentSeenMS int64,
) bool {
	if previousEventID == nil || currentEventID == nil || *previousEventID != *currentEventID ||
		previous.StartUTCMS == nil || current.StartUTCMS == nil ||
		previous.DurationMS == nil || current.DurationMS == nil ||
		previous.Validation == ValidationInvalid || current.Validation == ValidationInvalid ||
		previousSeenMS < 0 || currentSeenMS < previousSeenMS ||
		currentSeenMS-previousSeenMS > mirakurunObservationHorizonMS {
		return false
	}
	oldStart, newStart := *previous.StartUTCMS, *current.StartUTCMS
	oldDuration, newDuration := *previous.DurationMS, *current.DurationMS
	if oldStart < 0 || newStart < 0 || !acceptedFollowDuration(oldDuration) || !acceptedFollowDuration(newDuration) {
		return false
	}
	return absoluteDifference(oldStart, newStart) <= mirakurunMaximumShiftMS &&
		absoluteDifference(oldDuration, newDuration) <= mirakurunMaximumShiftMS
}

func acceptedFollowDuration(value int64) bool {
	return value >= mirakurunMinimumDurationMS && value <= mirakurunMaximumDurationMS && value%1_000 == 0
}

func absoluteDifference(left, right int64) int64 {
	if left >= right {
		return left - right
	}
	return right - left
}
