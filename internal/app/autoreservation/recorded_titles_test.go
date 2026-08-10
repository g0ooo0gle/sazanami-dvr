package autoreservation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/autoreservation"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

func TestRecordedTitleIndexMatchesExactTitlePeriodAndServiceScope(t *testing.T) {
	start := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	recordedStart := start.Add(-24 * time.Hour)
	index := recordedTitleIndex{
		allServices: map[string]int64{"番組": recordedStart.UnixMilli()},
		sameService: map[recordedServiceTitle]int64{
			{title: "番組", networkID: 1, transportStreamID: 2, serviceID: 3}: recordedStart.UnixMilli(),
		},
	}
	request := recording.ReservationRequest{
		NetworkID: 1, TransportStreamID: 2, ServiceID: 3, Start: start, Duration: time.Hour, Priority: 3,
	}
	program := currentProgram(1, start, "番組", "", catalogmodel.FreeYes)
	base := autoreservation.SearchCondition{CheckRecordedTitle: true, CheckRecordedDays: 2}
	if !index.matches(base, request, program) {
		t.Fatal("同じ番組名、期間、サービスが一致しません")
	}
	for name, change := range map[string]func(*recording.ReservationRequest){
		"network":   func(value *recording.ReservationRequest) { value.NetworkID++ },
		"transport": func(value *recording.ReservationRequest) { value.TransportStreamID++ },
		"service":   func(value *recording.ReservationRequest) { value.ServiceID++ },
	} {
		t.Run(name, func(t *testing.T) {
			changed := request
			change(&changed)
			if index.matches(base, changed, program) {
				t.Fatal("別サービスが一致しました")
			}
			all := base
			all.CheckRecordedAllServices = true
			if !index.matches(all, changed, program) {
				t.Fatal("全チャンネル指定で一致しません")
			}
		})
	}
	for _, title := range []string{"番組 ", "番組a", "ばんぐみ", "ＢＡＮＧＵＭＩ"} {
		if index.matches(base, request, currentProgram(1, start, title, "", catalogmodel.FreeYes)) {
			t.Fatalf("完全一致でない番組名が一致しました: %q", title)
		}
	}
	if index.matches(autoreservation.SearchCondition{CheckRecordedTitle: true}, request, program) {
		t.Fatal("0日の条件が一致しました")
	}
	if index.matches(base, request, currentProgram(1, start, "", "", catalogmodel.FreeYes)) {
		t.Fatal("空の番組名が一致しました")
	}

	lower := start.Add(-48 * time.Hour)
	key := recordedServiceTitle{title: "番組", networkID: 1, transportStreamID: 2, serviceID: 3}
	index.sameService[key] = lower.UnixMilli()
	if index.matches(base, request, program) {
		t.Fatal("期間の下限と同時刻が一致しました")
	}
	index.sameService[key] = lower.Add(time.Millisecond).UnixMilli()
	if !index.matches(base, request, program) {
		t.Fatal("期間内1ミリ秒が一致しません")
	}
	index.sameService[key] = lower.Add(-time.Millisecond).UnixMilli()
	if index.matches(base, request, program) {
		t.Fatal("期間外1ミリ秒が一致しました")
	}

	maximum := base
	maximum.CheckRecordedDays = 9_999
	index.sameService[key] = start.Add(-9_998 * 24 * time.Hour).UnixMilli()
	if !index.matches(maximum, request, program) {
		t.Fatal("最大日数の期間内が一致しません")
	}
}

func TestLoadRecordedTitleIndexIsPagedOnceAndAcceptsTerminalHistory(t *testing.T) {
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	history := make([]recording.HistoryItem, recording.MaxHistoryPage+1)
	for index := range history {
		number := int32(len(history) - index)
		history[index] = historyItemForEvaluation(number, "番組", now.Add(-time.Duration(index)*time.Hour), 1, 2, 3)
	}
	states := []struct {
		state  recording.AttemptState
		reason recording.TerminalReason
	}{
		{recording.AttemptSucceeded, recording.ReasonCompleted},
		{recording.AttemptPartial, recording.ReasonUserRequestedStop},
		{recording.AttemptFailed, recording.ReasonStreamUnavailable},
		{recording.AttemptCancelled, recording.ReasonStreamCancelled},
		{recording.AttemptMissed, recording.ReasonLateStartExpired},
	}
	for index, value := range states {
		history[index].State, history[index].Reason = value.state, value.reason
	}
	store := &evaluationStore{history: history}
	rules := []preparedRule{
		prepareRule(storedRule(1, autoreservation.SearchCondition{
			Enabled: true, CheckRecordedTitle: true, CheckRecordedDays: 1,
		}), nil),
		prepareRule(storedRule(2, autoreservation.SearchCondition{
			Enabled: true, CheckRecordedTitle: true, CheckRecordedDays: 2,
		}), nil),
	}
	index, err := loadRecordedTitleIndex(context.Background(), store, rules)
	if err != nil || store.historyCalls != 2 || len(index.allServices) != 1 || len(index.sameService) != 1 {
		t.Fatalf("history_calls=%d all=%d same=%d err=%v", store.historyCalls,
			len(index.allServices), len(index.sameService), err)
	}

	for name, search := range map[string]autoreservation.SearchCondition{
		"no condition": {Enabled: true},
		"zero days":    {Enabled: true, CheckRecordedTitle: true},
	} {
		t.Run(name, func(t *testing.T) {
			unused := &evaluationStore{history: history}
			if _, err := loadRecordedTitleIndex(context.Background(), unused,
				[]preparedRule{prepareRule(storedRule(1, search), nil)}); err != nil || unused.historyCalls != 0 {
				t.Fatalf("history_calls=%d err=%v", unused.historyCalls, err)
			}
		})
	}
}

func TestLoadRecordedTitleIndexRejectsInvalidOrUnboundedHistory(t *testing.T) {
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	rules := []preparedRule{prepareRule(storedRule(1, autoreservation.SearchCondition{
		Enabled: true, CheckRecordedTitle: true, CheckRecordedDays: 1,
	}), nil)}
	valid := func(number int32) recording.HistoryItem {
		return historyItemForEvaluation(number, "番組", now.Add(-time.Hour), 1, 2, 3)
	}
	tests := map[string]*evaluationStore{
		"read failure": {
			historyRead: func(context.Context, int, int32) ([]recording.HistoryItem, error) {
				return nil, errors.New("private database error")
			},
		},
		"page over": {
			historyRead: func(context.Context, int, int32) ([]recording.HistoryItem, error) {
				return make([]recording.HistoryItem, recording.MaxHistoryPage+1), nil
			},
		},
		"reversed":  {history: []recording.HistoryItem{valid(2), valid(3)}},
		"duplicate": {history: []recording.HistoryItem{valid(2), valid(2)}},
		"invalid": {history: []recording.HistoryItem{func() recording.HistoryItem {
			item := valid(1)
			item.Title = string([]byte{0xff})
			return item
		}()}},
	}
	for name, store := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := loadRecordedTitleIndex(context.Background(), store, rules); err == nil ||
				name == "read failure" && err.Error() == "private database error" {
				t.Fatalf("err=%v", err)
			}
		})
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := loadRecordedTitleIndex(cancelled, &evaluationStore{}, rules); err == nil {
		t.Fatal("取消し済みcontextで成功しました")
	}

	for _, count := range []int{recording.MaxHistoryItems, recording.MaxHistoryItems + 1} {
		t.Run(time.Duration(count).String(), func(t *testing.T) {
			history := make([]recording.HistoryItem, count)
			for index := range history {
				number := int32(count - index)
				history[index] = valid(number)
			}
			_, err := loadRecordedTitleIndex(context.Background(), &evaluationStore{history: history}, rules)
			if count == recording.MaxHistoryItems && err != nil {
				t.Fatalf("上限件数が失敗しました: %v", err)
			}
			if count > recording.MaxHistoryItems && err == nil {
				t.Fatal("上限超過が成功しました")
			}
		})
	}
}
