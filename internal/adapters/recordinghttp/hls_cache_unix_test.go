//go:build unix

package recordinghttp

import (
	"bytes"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
)

func TestHLSCachePublishesOwnerOnlySegmentAndDelaysRemovalWhileReading(t *testing.T) {
	cache := testHLSCache(t, hlsCacheLimits{segmentBytes: 16, sessionBytes: 32, totalBytes: 64, cleanupEntries: 32})
	generation, err := cache.begin(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	partial, err := generation.create(0)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("completed")
	if written, err := partial.Write(data); err != nil || written != len(data) {
		t.Fatalf("written=%d err=%v", written, err)
	}
	if _, err := os.Lstat(hlsPartialFinalPath(partial)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("部分fileが完成名で見えています: %v", err)
	}
	segment, err := partial.publish()
	if err != nil {
		t.Fatal(err)
	}
	segmentPath := hlsSegmentPath(segment)
	if _, err := os.Lstat(hlsPartialPath(partial)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("公開後も部分名が残っています: %v", err)
	}
	info, err := os.Lstat(segmentPath)
	if err != nil || !validHLSRegularInfo(info, 1) || info.Mode().Perm() != 0o600 {
		t.Fatalf("segment info=%+v err=%v", info, err)
	}
	rootInfo, err := os.Lstat(cache.root.Name())
	if err != nil || !validHLSDirectoryInfo(rootInfo) || rootInfo.Mode().Perm() != 0o700 {
		t.Fatalf("cache info=%+v err=%v", rootInfo, err)
	}
	reader, err := generation.open(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := generation.stop(); err != nil {
		t.Fatal(err)
	}
	if err := generation.retire(0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(segmentPath); err != nil {
		t.Fatalf("読出し中にsegmentが削除されました: %v", err)
	}
	got, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("data=%q err=%v", got, err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(segmentPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Close後もsegmentが残っています: %v", err)
	}
	if cache.totalBytes != 0 || generation.bytes != 0 {
		t.Fatalf("cache=%d generation=%d", cache.totalBytes, generation.bytes)
	}
}

func TestHLSCacheRejectsExistingHardLinkedAndReplacedFiles(t *testing.T) {
	t.Run("existing final", func(t *testing.T) {
		cache := testHLSCache(t, testHLSCacheLimits())
		generation, err := cache.begin(0, 2)
		if err != nil {
			t.Fatal(err)
		}
		partial, err := generation.create(0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := partial.Write([]byte("new")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(hlsPartialFinalPath(partial), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := partial.publish(); err == nil {
			t.Fatal("既存の完成fileを置き換えました")
		}
		got, err := os.ReadFile(hlsPartialFinalPath(partial))
		if err != nil || string(got) != "old" {
			t.Fatalf("existing=%q err=%v", got, err)
		}
		if err := partial.discard(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("symlink final", func(t *testing.T) {
		cache := testHLSCache(t, testHLSCacheLimits())
		generation, err := cache.begin(0, 21)
		if err != nil {
			t.Fatal(err)
		}
		partial, err := generation.create(0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := partial.Write([]byte("new")); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, hlsPartialFinalPath(partial)); err != nil {
			t.Fatal(err)
		}
		if _, err := partial.publish(); err == nil {
			t.Fatal("symlinkの完成名を置き換えました")
		}
		if got, err := os.ReadFile(outside); err != nil || string(got) != "keep" {
			t.Fatalf("outside=%q err=%v", got, err)
		}
		if err := partial.discard(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("hard link", func(t *testing.T) {
		cache := testHLSCache(t, testHLSCacheLimits())
		generation, segment := publishedHLSSegment(t, cache, 0, 3, 0, []byte("segment"))
		segmentPath := hlsSegmentPath(segment)
		link := segmentPath + ".unknown"
		if err := os.Link(segmentPath, link); err != nil {
			t.Fatal(err)
		}
		if _, err := generation.open(0); err == nil {
			t.Fatal("hard linkを持つsegmentを開きました")
		}
		if err := generation.retire(0); err == nil {
			t.Fatal("hard linkを持つsegmentを削除しました")
		}
		if _, err := os.Lstat(segmentPath); err != nil {
			t.Fatalf("元fileが消えています: %v", err)
		}
	})

	t.Run("hard linked partial", func(t *testing.T) {
		cache := testHLSCache(t, testHLSCacheLimits())
		generation, err := cache.begin(0, 22)
		if err != nil {
			t.Fatal(err)
		}
		partial, err := generation.create(0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := partial.Write([]byte("segment")); err != nil {
			t.Fatal(err)
		}
		link := hlsPartialPath(partial) + ".unknown"
		if err := os.Link(hlsPartialPath(partial), link); err != nil {
			t.Fatal(err)
		}
		if _, err := partial.publish(); err == nil {
			t.Fatal("hard linkを持つ部分fileを公開しました")
		}
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		if err := partial.discard(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("publication failure", func(t *testing.T) {
		cache := testHLSCache(t, testHLSCacheLimits())
		generation, err := cache.begin(0, 23)
		if err != nil {
			t.Fatal(err)
		}
		partial, err := generation.create(0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := partial.Write([]byte("segment")); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(generation.root.Name(), 0o500); err != nil {
			t.Fatal(err)
		}
		if _, err := partial.publish(); err == nil {
			t.Fatal("書込み不可directoryで完成名を公開しました")
		}
		if err := os.Chmod(generation.root.Name(), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := partial.discard(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("replacement after open", func(t *testing.T) {
		cache := testHLSCache(t, testHLSCacheLimits())
		generation, segment := publishedHLSSegment(t, cache, 0, 4, 0, []byte("segment"))
		reader, err := generation.open(0)
		if err != nil {
			t.Fatal(err)
		}
		segmentPath := hlsSegmentPath(segment)
		old := segmentPath + ".old"
		if err := os.Rename(segmentPath, old); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(segmentPath, []byte("segment"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := reader.Read(make([]byte, 1)); err == nil {
			t.Fatal("open後の差替えを検出しませんでした")
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestHLSCacheCountsPartialActiveAndRetainedGenerations(t *testing.T) {
	limits := hlsCacheLimits{segmentBytes: 4, sessionBytes: 6, totalBytes: 8, cleanupEntries: 32}
	cache := testHLSCache(t, limits)
	first, err := cache.begin(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	partial, err := first.create(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := partial.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	secondPartial, err := first.create(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secondPartial.Write([]byte("123")); !errors.Is(err, errHLSCacheLimit) {
		t.Fatalf("session limit err=%v", err)
	}
	if _, err := partial.publish(); err != nil {
		t.Fatal(err)
	}
	second, err := cache.begin(1, 11)
	if err != nil {
		t.Fatal(err)
	}
	other, err := second.create(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Write([]byte("5678")); err != nil {
		t.Fatal(err)
	}
	over, err := second.create(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := over.Write([]byte("x")); !errors.Is(err, errHLSCacheLimit) {
		t.Fatalf("total limit err=%v", err)
	}
	if cache.totalBytes != 8 || first.bytes != 4 || second.bytes != 4 {
		t.Fatalf("total=%d first=%d second=%d", cache.totalBytes, first.bytes, second.bytes)
	}
	if addWithin(math.MaxInt64, 1, math.MaxInt64) || addWithin(1, math.MaxInt64, math.MaxInt64) {
		t.Fatal("算術overflowを受理しました")
	}
	if err := first.stop(); err != nil {
		t.Fatal(err)
	}
	if err := second.stop(); err != nil {
		t.Fatal(err)
	}
}

func TestHLSCacheEnforcesSegmentAndSessionBoundariesIndependently(t *testing.T) {
	t.Run("segment", func(t *testing.T) {
		cache := testHLSCache(t, hlsCacheLimits{segmentBytes: 4, sessionBytes: 8, totalBytes: 8, cleanupEntries: 32})
		generation, err := cache.begin(0, 12)
		if err != nil {
			t.Fatal(err)
		}
		partial, err := generation.create(0)
		if err != nil {
			t.Fatal(err)
		}
		if written, err := partial.Write([]byte("1234")); err != nil || written != 4 {
			t.Fatalf("boundary written=%d err=%v", written, err)
		}
		if _, err := partial.Write([]byte("x")); !errors.Is(err, errHLSCacheLimit) {
			t.Fatalf("one-over err=%v", err)
		}
		if err := generation.stop(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("session", func(t *testing.T) {
		cache := testHLSCache(t, hlsCacheLimits{segmentBytes: 4, sessionBytes: 6, totalBytes: 12, cleanupEntries: 32})
		generation, err := cache.begin(0, 13)
		if err != nil {
			t.Fatal(err)
		}
		first, err := generation.create(0)
		if err != nil {
			t.Fatal(err)
		}
		second, err := generation.create(1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := first.Write([]byte("1234")); err != nil {
			t.Fatal(err)
		}
		if written, err := second.Write([]byte("56")); err != nil || written != 2 {
			t.Fatalf("boundary written=%d err=%v", written, err)
		}
		if _, err := second.Write([]byte("x")); !errors.Is(err, errHLSCacheLimit) {
			t.Fatalf("one-over err=%v", err)
		}
		if generation.bytes != 6 || cache.totalBytes != 6 {
			t.Fatalf("generation=%d total=%d", generation.bytes, cache.totalBytes)
		}
		if err := generation.stop(); err != nil {
			t.Fatal(err)
		}
	})

	limits := defaultHLSCacheLimits()
	if limits.segmentBytes != 32*1024*1024 || limits.sessionBytes != 512*1024*1024 ||
		limits.totalBytes != 1024*1024*1024 {
		t.Fatalf("default limits=%+v", limits)
	}
}

func TestHLSCacheDirectoryHandlesStayInsideOriginalRoots(t *testing.T) {
	root := ownerOnlyTestRoot(t)
	cache, err := openHLSCache(root, testHLSCacheLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer cache.close()
	generation, err := cache.begin(0, 14)
	if err != nil {
		t.Fatal(err)
	}
	generationPath := generation.root.Name()
	moved := generationPath + ".moved"
	if err := os.Rename(generationPath, moved); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, generationPath); err != nil {
		t.Fatal(err)
	}
	partial, err := generation.create(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := partial.Write([]byte("inside")); err != nil {
		t.Fatal(err)
	}
	if _, err := partial.publish(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(moved, "segment-00000000000000000000.ts")); err != nil {
		t.Fatalf("固定した元directoryへ書けませんでした: %v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("outside entries=%v err=%v", entries, err)
	}
}

func TestHLSCacheStartupCleanupIsBoundedAndPreservesUnknownPaths(t *testing.T) {
	t.Run("known generation only", func(t *testing.T) {
		root := ownerOnlyTestRoot(t)
		limits := testHLSCacheLimits()
		cache, err := openHLSCache(root, limits)
		if err != nil {
			t.Fatal(err)
		}
		_, segment := publishedHLSSegment(t, cache, 0, 20, 0, []byte("old"))
		segmentPath := hlsSegmentPath(segment)
		unknown := filepath.Join(cache.root.Name(), "operator-note")
		if err := os.WriteFile(unknown, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := openHLSCache(root, limits); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(segmentPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("過去segmentが残っています: %v", err)
		}
		if got, err := os.ReadFile(unknown); err != nil || string(got) != "keep" {
			t.Fatalf("unknown=%q err=%v", got, err)
		}
	})

	t.Run("entry limit", func(t *testing.T) {
		root := ownerOnlyTestRoot(t)
		cacheRoot := filepath.Join(root, hlsCacheDirectory)
		if err := os.Mkdir(cacheRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"one", "two", "three"} {
			if err := os.WriteFile(filepath.Join(cacheRoot, name), []byte(name), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		limits := testHLSCacheLimits()
		limits.cleanupEntries = 2
		if _, err := openHLSCache(root, limits); err == nil {
			t.Fatal("startup cleanupの件数上限を越えました")
		}
		for _, name := range []string{"one", "two", "three"} {
			if _, err := os.Lstat(filepath.Join(cacheRoot, name)); err != nil {
				t.Fatalf("unknown path %sが削除されました: %v", name, err)
			}
		}
	})

	t.Run("unknown child", func(t *testing.T) {
		root := ownerOnlyTestRoot(t)
		cacheRoot := filepath.Join(root, hlsCacheDirectory)
		generation := filepath.Join(cacheRoot, hlsGenerationName(hlsCacheGenerationID{slot: 0, generation: 30}))
		if err := os.MkdirAll(generation, 0o700); err != nil {
			t.Fatal(err)
		}
		unknown := filepath.Join(generation, "do-not-delete")
		if err := os.WriteFile(unknown, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := openHLSCache(root, testHLSCacheLimits()); err == nil {
			t.Fatal("未知fileを含む過去世代を削除対象にしました")
		}
		if _, err := os.Lstat(unknown); err != nil {
			t.Fatalf("unknown childが削除されました: %v", err)
		}
	})

	t.Run("symlink child", func(t *testing.T) {
		root := ownerOnlyTestRoot(t)
		cacheRoot := filepath.Join(root, hlsCacheDirectory)
		generation := filepath.Join(cacheRoot, hlsGenerationName(hlsCacheGenerationID{slot: 1, generation: 31}))
		if err := os.MkdirAll(generation, 0o700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(generation, "segment-00000000000000000000.ts")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		if _, err := openHLSCache(root, testHLSCacheLimits()); err == nil {
			t.Fatal("symlinkを含む過去世代を削除対象にしました")
		}
		if got, err := os.ReadFile(outside); err != nil || string(got) != "keep" {
			t.Fatalf("outside=%q err=%v", got, err)
		}
	})

	t.Run("interrupted publication", func(t *testing.T) {
		root := ownerOnlyTestRoot(t)
		cacheRoot := filepath.Join(root, hlsCacheDirectory)
		generation := filepath.Join(cacheRoot, hlsGenerationName(hlsCacheGenerationID{slot: 0, generation: 32}))
		if err := os.MkdirAll(generation, 0o700); err != nil {
			t.Fatal(err)
		}
		partialName, finalName := hlsSegmentNames(0)
		partialPath := filepath.Join(generation, partialName)
		finalPath := filepath.Join(generation, finalName)
		if err := os.WriteFile(partialPath, []byte("interrupted"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(partialPath, finalPath); err != nil {
			t.Fatal(err)
		}
		cache, err := openHLSCache(root, testHLSCacheLimits())
		if err != nil {
			t.Fatal(err)
		}
		defer cache.close()
		if _, err := os.Lstat(generation); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("公開途中の過去世代が残っています: %v", err)
		}
	})

	t.Run("separate partial and final", func(t *testing.T) {
		root := ownerOnlyTestRoot(t)
		cacheRoot := filepath.Join(root, hlsCacheDirectory)
		generation := filepath.Join(cacheRoot, hlsGenerationName(hlsCacheGenerationID{slot: 1, generation: 35}))
		if err := os.MkdirAll(generation, 0o700); err != nil {
			t.Fatal(err)
		}
		partialName, finalName := hlsSegmentNames(0)
		partialPath := filepath.Join(generation, partialName)
		finalPath := filepath.Join(generation, finalName)
		if err := os.WriteFile(partialPath, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(finalPath, []byte("different-final"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := openHLSCache(root, testHLSCacheLimits()); err == nil {
			t.Fatal("別inodeの部分名と完成名を正規の公開途中状態として扱いました")
		}
		if got, err := os.ReadFile(partialPath); err != nil || string(got) != "partial" {
			t.Fatalf("partial=%q err=%v", got, err)
		}
		if got, err := os.ReadFile(finalPath); err != nil || string(got) != "different-final" {
			t.Fatalf("final=%q err=%v", got, err)
		}
	})
}

func TestHLSCacheRollsBackRecognizedPublicationPair(t *testing.T) {
	cache := testHLSCache(t, testHLSCacheLimits())
	generation, err := cache.begin(0, 33)
	if err != nil {
		t.Fatal(err)
	}
	partial, err := generation.create(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := partial.Write([]byte("segment")); err != nil {
		t.Fatal(err)
	}
	if err := partial.file.Close(); err != nil {
		t.Fatal(err)
	}
	partial.file = nil
	if err := generation.root.Link(partial.name, partial.finalName); err != nil {
		t.Fatal(err)
	}
	if err := rollbackHLSPublication(partial); err != nil {
		t.Fatal(err)
	}
	if _, err := generation.root.Lstat(partial.finalName); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback後も完成名が残っています: %v", err)
	}
	if !sameHLSFile(generation.root, partial.name, partial.identity, partial.bytes, 1) {
		t.Fatal("rollback後の部分fileが不正です")
	}
	if err := partial.discard(); err != nil {
		t.Fatal(err)
	}
}

func TestHLSCacheStopRejectsCompetingWriteAndPublication(t *testing.T) {
	for _, action := range []string{"write", "publish"} {
		t.Run(action, func(t *testing.T) {
			cache := testHLSCache(t, testHLSCacheLimits())
			generation, err := cache.begin(0, 34)
			if err != nil {
				t.Fatal(err)
			}
			partial, err := generation.create(0)
			if err != nil {
				t.Fatal(err)
			}
			if action == "publish" {
				if _, err := partial.Write([]byte("segment")); err != nil {
					t.Fatal(err)
				}
			}
			partial.mu.Lock()
			locked := true
			defer func() {
				if locked {
					partial.mu.Unlock()
				}
			}()
			stopped := make(chan error, 1)
			go func() { stopped <- generation.stop() }()
			waitHLSGenerationClosed(t, generation)
			partial.mu.Unlock()
			locked = false
			if action == "write" {
				if _, err := partial.Write([]byte("late")); err == nil {
					t.Fatal("stop確定後の書込みを受理しました")
				}
			} else if _, err := partial.publish(); err == nil {
				t.Fatal("stop確定後の公開を受理しました")
			}
			if err := <-stopped; err != nil {
				t.Fatal(err)
			}
			if len(generation.segments) != 0 || cache.totalBytes != 0 {
				t.Fatalf("segments=%d total=%d", len(generation.segments), cache.totalBytes)
			}
		})
	}
}

func waitHLSGenerationClosed(t *testing.T, generation *hlsCacheGeneration) {
	t.Helper()
	for attempt := 0; attempt < 100_000; attempt++ {
		generation.cache.mu.Lock()
		closed := generation.closed
		generation.cache.mu.Unlock()
		if closed {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("stopが開始されませんでした")
}

func TestHLSCacheRejectsSymlinkAndDifferentOwner(t *testing.T) {
	root := ownerOnlyTestRoot(t)
	realCache := filepath.Join(t.TempDir(), "real-cache")
	if err := os.Mkdir(realCache, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realCache, filepath.Join(root, hlsCacheDirectory)); err != nil {
		t.Fatal(err)
	}
	if _, err := openHLSCache(root, testHLSCacheLimits()); err == nil {
		t.Fatal("cache directoryのsymlinkを受理しました")
	}

	path := filepath.Join(t.TempDir(), "owned")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat := *info.Sys().(*syscall.Stat_t)
	stat.Uid++
	if validHLSRegularInfo(hlsFileInfoWithStat{FileInfo: info, stat: &stat}, 1) {
		t.Fatal("owner違いのfile情報を受理しました")
	}
}

func TestHLSCacheRetireWaitsForConcurrentReaders(t *testing.T) {
	cache := testHLSCache(t, testHLSCacheLimits())
	generation, segment := publishedHLSSegment(t, cache, 0, 40, 0, []byte("segment"))
	segmentPath := hlsSegmentPath(segment)
	const readers = 8
	files := make([]*hlsCacheReadFile, readers)
	for index := range files {
		file, err := generation.open(0)
		if err != nil {
			t.Fatal(err)
		}
		files[index] = file
	}
	if err := generation.stop(); err != nil {
		t.Fatal(err)
	}
	if err := generation.retire(0); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for _, file := range files {
		wait.Add(1)
		go func(value *hlsCacheReadFile) {
			defer wait.Done()
			if err := value.Close(); err != nil {
				t.Errorf("close: %v", err)
			}
		}(file)
	}
	wait.Wait()
	if _, err := os.Lstat(segmentPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("全readerのClose後もsegmentが残っています: %v", err)
	}
}

func testHLSCache(t *testing.T, limits hlsCacheLimits) *hlsCache {
	t.Helper()
	cache, err := openHLSCache(ownerOnlyTestRoot(t), limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.close() })
	return cache
}

func testHLSCacheLimits() hlsCacheLimits {
	return hlsCacheLimits{segmentBytes: 16, sessionBytes: 32, totalBytes: 64, cleanupEntries: 32}
}

func ownerOnlyTestRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func publishedHLSSegment(t *testing.T, cache *hlsCache, slot uint8, generationID, sequence uint64, data []byte) (*hlsCacheGeneration, *hlsCacheSegment) {
	t.Helper()
	generation, err := cache.begin(slot, generationID)
	if err != nil {
		t.Fatal(err)
	}
	partial, err := generation.create(sequence)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := partial.Write(data); err != nil {
		t.Fatal(err)
	}
	segment, err := partial.publish()
	if err != nil {
		t.Fatal(err)
	}
	return generation, segment
}

func hlsPartialPath(partial *hlsCachePartial) string {
	return filepath.Join(partial.generation.root.Name(), partial.name)
}

func hlsPartialFinalPath(partial *hlsCachePartial) string {
	return filepath.Join(partial.generation.root.Name(), partial.finalName)
}

func hlsSegmentPath(segment *hlsCacheSegment) string {
	return filepath.Join(segment.generation.root.Name(), segment.name)
}

type hlsFileInfoWithStat struct {
	os.FileInfo
	stat *syscall.Stat_t
}

func (info hlsFileInfoWithStat) Sys() any { return info.stat }
