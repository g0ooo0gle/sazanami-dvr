//go:build unix

package recordingfs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/recordinghttp"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

func TestNoReplaceRenamePreservesBothFilesOnCollision(t *testing.T) {
	directory := t.TempDir()
	source, target := filepath.Join(directory, "partial"), filepath.Join(directory, "final")
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 呼出し前の存在確認だけに頼らず、OS操作自体が上書きを拒否することを確認する。
	if err := renameNoReplace(source, target); !errors.Is(err, os.ErrExist) {
		t.Fatalf("collision=%v", err)
	}
	for path, want := range map[string]string{source: "new", target: "existing"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("collision changed file: got=%q want=%q err=%v", got, want, err)
		}
	}
}

func TestPublicationMovesPartialWithoutCreatingAnotherLink(t *testing.T) {
	root, err := OpenRoot(filepath.Join(t.TempDir(), "recordings"))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	plan, err := recording.NewFilePlan(time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC), fileID(t, 21))
	if err != nil {
		t.Fatal(err)
	}
	partial, err := root.CreatePartial(plan)
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte{0x47}, 188)
	if _, err := partial.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := partial.Close(); err != nil {
		t.Fatal(err)
	}
	if err := root.LinkFinal(plan); err != nil {
		t.Fatal(err)
	}
	observation, err := root.Inspect(plan)
	if err != nil || observation.Partial.Exists || !observation.Final.Regular {
		t.Fatalf("publication left an extra link: %+v err=%v", observation, err)
	}
	file, err := root.OpenFinal(plan, 188)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got, err := io.ReadAll(file)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("published bytes=%d err=%v", len(got), err)
	}
	if err := root.RemovePartial(plan); err != nil {
		t.Fatalf("already moved partial: %v", err)
	}
}

func TestFinalReadRejectsChangesToOpenedFile(t *testing.T) {
	for _, change := range []string{"truncate", "hard-link", "permissions"} {
		t.Run(change, func(t *testing.T) {
			root, plan, data := publishedReadFixture(t)
			file, err := root.OpenFinal(plan, int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			final, err := root.FinalPath(plan)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "truncate":
				err = os.Truncate(final, 188)
			case "hard-link":
				err = os.Link(final, final+".link")
			case "permissions":
				err = os.Chmod(final, 0o644)
			}
			if err != nil {
				t.Fatal(err)
			}
			if n, err := file.Read(make([]byte, 188)); n != 0 || err == nil {
				t.Fatalf("changed file was read: n=%d err=%v", n, err)
			}
		})
	}
}

func TestRemovePartialResumesLegacyHardLinkPublication(t *testing.T) {
	root, plan, data := publishedReadFixture(t)
	partial, final, _, err := root.paths(plan)
	if err != nil {
		t.Fatal(err)
	}
	// 旧版がhard linkを作った直後に停止した状態を再現する。
	if err := os.Link(final, partial); err != nil {
		t.Fatal(err)
	}
	if err := root.RemovePartial(plan); err != nil {
		t.Fatal(err)
	}
	file, err := root.OpenFinal(plan, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got, err := io.ReadAll(file)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("legacy publication bytes=%d err=%v", len(got), err)
	}
}

func TestInspectRejectsFinalOnlyWithExtraHardLink(t *testing.T) {
	root, plan, _ := publishedReadFixture(t)
	final, err := root.FinalPath(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Link(final, final+".link"); err != nil {
		t.Fatal(err)
	}
	observation, err := root.Inspect(plan)
	if err != nil || !observation.Final.Exists || observation.Final.Regular || observation.Partial.Exists {
		t.Fatalf("unplayable final reported as regular: %+v err=%v", observation, err)
	}
}

// TMPDIRを隔離した共有フォルダーに向けると、同じテストでNAS上の配信も確認できる。
func TestPublishedFinalSupportsHTTPRangeAfterDirectoryRefresh(t *testing.T) {
	root, plan, data := publishedReadFixture(t)
	final, err := root.FinalPath(plan)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Minute)
	fixture := finalHTTPFixture{root: root, item: recording.HistoryItem{
		Number: 1, State: recording.AttemptSucceeded, Reason: recording.ReasonCompleted,
		Title: "test", StationName: "test", NetworkID: 1, TransportStreamID: 2, ServiceID: 3, EventID: 4,
		PlannedStart: start, PlannedEnd: end, ActualStart: &start, ActualEnd: &end,
		ByteCount: int64(len(data)), Plan: plan, SegmentState: recording.SegmentFinalized,
		Availability: recording.AvailabilityFinal, FileSynced: true, FinalPublished: true, DirectorySynced: true,
	}}
	handler, err := recordinghttp.NewHandler(fixture, fixture)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	for _, test := range []struct {
		name, method, rangeValue string
		status                   int
		start, end               int
	}{
		{"head", http.MethodHead, "", http.StatusOK, 0, 0},
		{"full", http.MethodGet, "", http.StatusOK, 0, 564},
		{"first", http.MethodGet, "bytes=0-187", http.StatusPartialContent, 0, 188},
		{"middle", http.MethodGet, "bytes=188-375", http.StatusPartialContent, 188, 376},
		{"last", http.MethodGet, "bytes=376-563", http.StatusPartialContent, 376, 564},
	} {
		t.Run(test.name, func(t *testing.T) {
			entries, err := os.ReadDir(filepath.Dir(final))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if _, err := entry.Info(); err != nil {
					t.Fatal(err)
				}
			}
			request, err := http.NewRequest(test.method, server.URL+"/recordings/1.ts", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Range", test.rangeValue)
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != test.status || !bytes.Equal(body, data[test.start:test.end]) {
				t.Fatalf("status=%d bytes=%d err=%v", response.StatusCode, len(body), err)
			}
		})
	}
}

type finalHTTPFixture struct {
	root *Root
	item recording.HistoryItem
}

func (fixture finalHTTPFixture) OpenFinal(plan recording.FilePlan, size int64) (recordinghttp.FinalFile, error) {
	return fixture.root.OpenFinal(plan, size)
}

func (fixture finalHTTPFixture) RecordingHistory(context.Context, int, int32) ([]recording.HistoryItem, error) {
	return []recording.HistoryItem{fixture.item}, nil
}

func (fixture finalHTTPFixture) RecordingHistoryItem(_ context.Context, id int32) (*recording.HistoryItem, error) {
	if id != fixture.item.Number {
		return nil, nil
	}
	return &fixture.item, nil
}

func publishedReadFixture(t *testing.T) (*Root, recording.FilePlan, []byte) {
	t.Helper()
	root, err := OpenRoot(filepath.Join(t.TempDir(), "recordings"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	plan, err := recording.NewFilePlan(time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC), fileID(t, 20))
	if err != nil {
		t.Fatal(err)
	}
	partial, err := root.CreatePartial(plan)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 188*3)
	for index := range data {
		data[index] = byte(index)
	}
	if _, err := partial.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := partial.Close(); err != nil {
		t.Fatal(err)
	}
	if err := root.LinkFinal(plan); err != nil {
		t.Fatal(err)
	}
	if err := root.RemovePartial(plan); err != nil {
		t.Fatal(err)
	}
	return root, plan, data
}
