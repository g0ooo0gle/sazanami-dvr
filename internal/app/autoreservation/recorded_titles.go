package autoreservation

import (
	"context"
	"errors"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/autoreservation"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

type recordedServiceTitle struct {
	title                                   string
	networkID, transportStreamID, serviceID uint16
}

// recordedTitleIndexは番組名ごとに最新の録画開始予定だけを保持し、番組ごとのDB検索を避ける。
type recordedTitleIndex struct {
	allServices map[string]int64
	sameService map[recordedServiceTitle]int64
}

// loadRecordedTitleIndexは必要な規則がある場合だけ、録画履歴を一回の上限付き走査で索引化する。
// 読取りに失敗した場合は、呼出し元が予約を変更する前に評価全体を止める。
func loadRecordedTitleIndex(ctx context.Context, store EvaluationStore,
	rules []preparedRule,
) (recordedTitleIndex, error) {
	index := recordedTitleIndex{}
	needed := false
	for _, candidate := range rules {
		if !candidate.skip && !candidate.unavailable && candidate.rule.Search.CheckRecordedTitle &&
			candidate.rule.Search.CheckRecordedDays > 0 {
			needed = true
			break
		}
	}
	if !needed {
		return index, nil
	}
	index.allServices = make(map[string]int64)
	index.sameService = make(map[recordedServiceTitle]int64)
	var before, previous int32
	count := 0
	for {
		if ctx.Err() != nil {
			return recordedTitleIndex{}, errors.New("autoreservation: context ended")
		}
		page, err := store.RecordingHistory(ctx, recording.MaxHistoryPage, before)
		if err != nil || len(page) > recording.MaxHistoryPage {
			return recordedTitleIndex{}, errors.New("autoreservation: read recording history failed")
		}
		for _, item := range page {
			if item.Validate() != nil || previous != 0 && item.Number >= previous {
				return recordedTitleIndex{}, errors.New("autoreservation: invalid recording history order")
			}
			count++
			if count > recording.MaxHistoryItems {
				return recordedTitleIndex{}, errors.New("autoreservation: recording history limit exceeded")
			}
			previous, before = item.Number, item.Number
			if item.Title == "" {
				continue
			}
			start := item.PlannedStart.UnixMilli()
			if current, ok := index.allServices[item.Title]; !ok || start > current {
				index.allServices[item.Title] = start
			}
			key := recordedServiceTitle{
				title: item.Title, networkID: item.NetworkID,
				transportStreamID: item.TransportStreamID, serviceID: item.ServiceID,
			}
			if current, ok := index.sameService[key]; !ok || start > current {
				index.sameService[key] = start
			}
		}
		if len(page) < recording.MaxHistoryPage {
			return index, nil
		}
	}
}

// matchesは固定した録画履歴索引から、番組名、期間、放送範囲を照合する。
func (index recordedTitleIndex) matches(search autoreservation.SearchCondition, request recording.ReservationRequest,
	program catalogmodel.CurrentProgram,
) bool {
	if !search.CheckRecordedTitle || search.CheckRecordedDays == 0 || program.Material.Title == nil ||
		*program.Material.Title == "" {
		return false
	}
	lower := request.Start.Add(-time.Duration(search.CheckRecordedDays) * 24 * time.Hour).UnixMilli()
	title := *program.Material.Title
	if search.CheckRecordedAllServices {
		start, ok := index.allServices[title]
		return ok && start > lower
	}
	start, ok := index.sameService[recordedServiceTitle{
		title: title, networkID: request.NetworkID,
		transportStreamID: request.TransportStreamID, serviceID: request.ServiceID,
	}]
	return ok && start > lower
}
