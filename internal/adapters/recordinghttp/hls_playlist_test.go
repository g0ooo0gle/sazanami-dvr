package recordinghttp

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHLSMediaPlaylistRendersFixedOriginalQualityContract(t *testing.T) {
	playlist, err := newHLSMediaPlaylist("Main_1")
	if err != nil {
		t.Fatal(err)
	}
	for sequence, test := range []struct {
		duration      time.Duration
		discontinuity bool
	}{{time.Second, false}, {1500 * time.Millisecond, true}, {time.Second, false}} {
		if _, err := playlist.append(uint64(sequence), test.duration, test.discontinuity); err != nil {
			t.Fatal(err)
		}
	}
	data, err := playlist.render()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		t.Fatal("playlistにBOMが付いています")
	}
	want := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n" +
		"#EXT-X-DISCONTINUITY-SEQUENCE:0\n#EXT-X-MEDIA-SEQUENCE:0\n" +
		"#EXTINF:1.0,\n/komorebi/live/Main_1/0.ts\n" +
		"#EXT-X-DISCONTINUITY\n#EXTINF:1.5,\n/komorebi/live/Main_1/1.ts\n" +
		"#EXTINF:1.0,\n/komorebi/live/Main_1/2.ts\n"
	if string(data) != want {
		t.Fatalf("playlist:\n%s", data)
	}
	retention := playlist.finish()
	if len(retention) != 3 || retention[0].retainFor != 3500*time.Millisecond {
		t.Fatalf("retention=%+v", retention)
	}
	ended, err := playlist.render()
	if err != nil || !strings.HasSuffix(string(ended), "#EXT-X-ENDLIST\n") {
		t.Fatalf("ended=%q err=%v", ended, err)
	}
}

func TestHLSMediaPlaylistKeepsZeroDiscontinuitySequenceAfterEarlierEviction(t *testing.T) {
	playlist, err := newHLSMediaPlaylistWithWindow("window", 6*time.Second, 4*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for sequence := uint64(0); sequence < 4; sequence++ {
		if _, err := playlist.append(sequence, 2*time.Second, sequence == 1); err != nil {
			t.Fatal(err)
		}
	}
	data, err := playlist.render()
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "#EXT-X-DISCONTINUITY-SEQUENCE:0\n#EXT-X-MEDIA-SEQUENCE:1") ||
		!strings.Contains(text, "#EXT-X-DISCONTINUITY\n#EXTINF:2.0,\n/komorebi/live/window/1.ts") {
		t.Fatalf("playlist:\n%s", text)
	}
}

func TestHLSMediaPlaylistWindowAndRetention(t *testing.T) {
	playlist, err := newHLSMediaPlaylistWithWindow("window", 8*time.Second, 6*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var retired []hlsRetention
	for sequence := uint64(0); sequence < 6; sequence++ {
		values, appendErr := playlist.append(sequence, 2*time.Second, sequence == 1)
		if appendErr != nil {
			t.Fatal(appendErr)
		}
		retired = append(retired, values...)
	}
	if len(retired) != 2 || retired[0].sequence != 0 || retired[0].retainFor != 10*time.Second ||
		retired[1].sequence != 1 || retired[1].retainFor != 10*time.Second {
		t.Fatalf("retired=%+v", retired)
	}
	data, err := playlist.render()
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "#EXT-X-DISCONTINUITY-SEQUENCE:1\n#EXT-X-MEDIA-SEQUENCE:2") ||
		strings.Contains(text, "/0.ts") || strings.Contains(text, "/1.ts") {
		t.Fatalf("window playlist:\n%s", text)
	}
	if got := playlistDuration(playlist.segments); got != 8*time.Second {
		t.Fatalf("window duration=%v", got)
	}
}

func TestHLSMediaPlaylistRejectsInvalidInputAndSequenceReuse(t *testing.T) {
	for _, key := range []string{"", "_bad", "bad/part", strings.Repeat("a", 97)} {
		if _, err := newHLSMediaPlaylist(key); err == nil {
			t.Fatalf("不正key %qを受理しました", key)
		}
	}
	playlist, err := newHLSMediaPlaylist("valid-key")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		sequence uint64
		duration time.Duration
	}{{1, time.Second}, {0, 0}, {0, 2*time.Second + time.Nanosecond}} {
		if _, err := playlist.append(test.sequence, test.duration, false); err == nil {
			t.Fatalf("sequence=%d duration=%vを受理しました", test.sequence, test.duration)
		}
	}
	if _, err := playlist.append(0, time.Second, false); err != nil {
		t.Fatal(err)
	}
	if _, err := playlist.append(0, time.Second, false); err == nil {
		t.Fatal("同じsequenceを再利用できました")
	}
	playlist.next = math.MaxUint64
	if _, err := playlist.append(math.MaxUint64, time.Second, false); err != nil {
		t.Fatal(err)
	}
	if _, err := playlist.nextSequence(); !errors.Is(err, errHLSSequenceExhausted) {
		t.Fatalf("sequence overflow err=%v", err)
	}
	playlist.finish()
	if _, err := playlist.append(math.MaxUint64, time.Second, false); err == nil {
		t.Fatal("終了後にsegmentを追加できました")
	}
}

func TestHLSMediaPlaylistAllowsConcurrentRenderDuringAppend(t *testing.T) {
	playlist, err := newHLSMediaPlaylist("race")
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		for sequence := uint64(0); sequence < 100; sequence++ {
			if _, appendErr := playlist.append(sequence, time.Second, sequence%11 == 0); appendErr != nil {
				t.Errorf("append: %v", appendErr)
				return
			}
		}
	}()
	go func() {
		defer wait.Done()
		for index := 0; index < 100; index++ {
			_, _ = playlist.render()
		}
	}()
	wait.Wait()
	data, err := playlist.render()
	if err != nil || !strings.Contains(string(data), "#EXT-X-MEDIA-SEQUENCE:40") {
		t.Fatalf("playlist=%q err=%v", data, err)
	}
}
