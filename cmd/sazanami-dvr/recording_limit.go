package main

import "errors"

// recordingStreamLimitは各録画に本編とワンセグの最大二接続を割り当てる。
// intの乗算があふれる値と、録画数として使えない0以下を起動前に拒否する。
func recordingStreamLimit(maximumRecordings int) (int, error) {
	maximumInt := int(^uint(0) >> 1)
	if maximumRecordings <= 0 || maximumRecordings > maximumInt/2 {
		return 0, errors.New("recording stream limit is out of range")
	}
	return maximumRecordings * 2, nil
}
