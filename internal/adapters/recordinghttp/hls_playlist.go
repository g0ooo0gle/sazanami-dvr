package recordinghttp

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	hlsTargetDuration = 2 * time.Second
	hlsMaximumWindow  = 60 * time.Second
	hlsMinimumWindow  = 6 * time.Second
)

var errHLSSequenceExhausted = errors.New("recordinghttp: hls sequence exhausted")

// hlsRetentionはプレイリストから外したsegmentを安全に残す最短時間である。
// 実際の削除時刻とHTTP読出し中の保護はcache側で管理する。
type hlsRetention struct {
	sequence  uint64
	retainFor time.Duration
}

type hlsPlaylistSegment struct {
	sequence        uint64
	duration        time.Duration
	discontinuity   bool
	longestPlaylist time.Duration
}

// hlsMediaPlaylistは一つのHLS keyに対応するMedia Playlistを保持する。
// sequenceとURLを再利用せず、完成segmentを追加したときだけ内容を更新する。
type hlsMediaPlaylist struct {
	mu                    sync.Mutex
	key                   string
	segments              []hlsPlaylistSegment
	next                  uint64
	sequenceExhausted     bool
	discontinuitySequence uint64
	terminal              bool
	maximumWindow         time.Duration
	minimumWindow         time.Duration
}

func newHLSMediaPlaylist(key string) (*hlsMediaPlaylist, error) {
	return newHLSMediaPlaylistWithWindow(key, hlsMaximumWindow, hlsMinimumWindow)
}

func newHLSMediaPlaylistWithWindow(key string, maximum, minimum time.Duration) (*hlsMediaPlaylist, error) {
	if !validHLSKey(key) || maximum <= 0 || minimum <= 0 || minimum > maximum {
		return nil, errors.New("recordinghttp: invalid hls playlist")
	}
	return &hlsMediaPlaylist{key: key, maximumWindow: maximum, minimumWindow: minimum}, nil
}

// nextSequenceは次に完成させるsegmentの番号を返す。
func (playlist *hlsMediaPlaylist) nextSequence() (uint64, error) {
	playlist.mu.Lock()
	defer playlist.mu.Unlock()
	if playlist.terminal || playlist.sequenceExhausted {
		return 0, errHLSSequenceExhausted
	}
	return playlist.next, nil
}

// appendは完成segmentを一度だけ追加し、windowから外れたsegmentの保持時間を返す。
func (playlist *hlsMediaPlaylist) append(sequence uint64, duration time.Duration, discontinuity bool) ([]hlsRetention, error) {
	playlist.mu.Lock()
	defer playlist.mu.Unlock()
	if playlist.terminal || playlist.sequenceExhausted || sequence != playlist.next || duration <= 0 || duration > hlsTargetDuration {
		return nil, errors.New("recordinghttp: invalid hls segment")
	}
	playlist.segments = append(playlist.segments, hlsPlaylistSegment{
		sequence: sequence, duration: duration, discontinuity: discontinuity,
	})
	if sequence == math.MaxUint64 {
		playlist.sequenceExhausted = true
	} else {
		playlist.next++
	}

	total := playlistDuration(playlist.segments)
	var retired []hlsRetention
	for total > playlist.maximumWindow && len(playlist.segments) > 1 {
		oldest := playlist.segments[0]
		if total-oldest.duration < playlist.minimumWindow {
			break
		}
		if oldest.discontinuity {
			if playlist.discontinuitySequence == math.MaxUint64 {
				return nil, errHLSSequenceExhausted
			}
			playlist.discontinuitySequence++
		}
		retired = append(retired, hlsRetention{
			sequence: oldest.sequence, retainFor: oldest.duration + oldest.longestPlaylist,
		})
		playlist.segments = playlist.segments[1:]
		total -= oldest.duration
	}
	for index := range playlist.segments {
		if total > playlist.segments[index].longestPlaylist {
			playlist.segments[index].longestPlaylist = total
		}
	}
	return retired, nil
}

// finishはENDLISTを固定し、最後のプレイリストに残るsegmentの保持時間を返す。
func (playlist *hlsMediaPlaylist) finish() []hlsRetention {
	playlist.mu.Lock()
	defer playlist.mu.Unlock()
	playlist.terminal = true
	retired := make([]hlsRetention, len(playlist.segments))
	for index, segment := range playlist.segments {
		retired[index] = hlsRetention{sequence: segment.sequence, retainFor: segment.longestPlaylist}
	}
	return retired
}

// renderはBOMを付けず、固定したHLS keyと完成segmentだけからMedia Playlistを作る。
func (playlist *hlsMediaPlaylist) render() ([]byte, error) {
	playlist.mu.Lock()
	defer playlist.mu.Unlock()
	if len(playlist.segments) == 0 {
		return nil, errors.New("recordinghttp: hls playlist unavailable")
	}
	var text strings.Builder
	text.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n")
	if playlist.discontinuitySequence > 0 || playlistContainsDiscontinuity(playlist.segments) {
		fmt.Fprintf(&text, "#EXT-X-DISCONTINUITY-SEQUENCE:%d\n", playlist.discontinuitySequence)
	}
	fmt.Fprintf(&text, "#EXT-X-MEDIA-SEQUENCE:%d\n", playlist.segments[0].sequence)
	for _, segment := range playlist.segments {
		if segment.discontinuity {
			text.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		fmt.Fprintf(&text, "#EXTINF:%s,\n/komorebi/live/%s/%d.ts\n",
			formatHLSDuration(segment.duration), playlist.key, segment.sequence)
	}
	if playlist.terminal {
		text.WriteString("#EXT-X-ENDLIST\n")
	}
	return []byte(text.String()), nil
}

func playlistContainsDiscontinuity(segments []hlsPlaylistSegment) bool {
	for _, segment := range segments {
		if segment.discontinuity {
			return true
		}
	}
	return false
}

func playlistDuration(segments []hlsPlaylistSegment) time.Duration {
	var total time.Duration
	for _, segment := range segments {
		total += segment.duration
	}
	return total
}

func formatHLSDuration(duration time.Duration) string {
	value := strconv.FormatFloat(duration.Seconds(), 'f', 9, 64)
	value = strings.TrimRight(value, "0")
	if strings.HasSuffix(value, ".") {
		value += "0"
	}
	return value
}

func validHLSKey(key string) bool {
	if len(key) < 1 || len(key) > 96 || !asciiAlphaNumeric(key[0]) {
		return false
	}
	for index := 1; index < len(key); index++ {
		if !asciiAlphaNumeric(key[index]) && key[index] != '_' && key[index] != '-' {
			return false
		}
	}
	return true
}

func asciiAlphaNumeric(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}
