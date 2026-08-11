//go:build unix

package recordinghttp

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const hlsCacheDirectory = ".sazanami-hls-cache"

var errHLSCacheLimit = errors.New("recordinghttp: hls-cache-limit")

type hlsCacheLimits struct {
	segmentBytes   int64
	sessionBytes   int64
	totalBytes     int64
	cleanupEntries int
}

func defaultHLSCacheLimits() hlsCacheLimits {
	return hlsCacheLimits{
		segmentBytes: 32 * 1024 * 1024, sessionBytes: 512 * 1024 * 1024,
		totalBytes: 1024 * 1024 * 1024, cleanupEntries: 8192,
	}
}

type hlsCacheGenerationID struct {
	slot       uint8
	generation uint64
}

// hlsCacheは稼働中、作成途中、保持中の全世代を一つの容量上限で管理する。
// cache directoryはos.Rootで固定し、後から親pathを差し替えられても外へ出ない。
type hlsCache struct {
	mu          sync.Mutex
	root        *os.Root
	limits      hlsCacheLimits
	totalBytes  int64
	generations map[hlsCacheGenerationID]*hlsCacheGeneration
}

type hlsCacheGeneration struct {
	cache    *hlsCache
	id       hlsCacheGenerationID
	name     string
	root     *os.Root
	identity os.FileInfo
	bytes    int64
	closed   bool
	partials map[uint64]*hlsCachePartial
	segments map[uint64]*hlsCacheSegment
}

// hlsCachePartialは完成名から見えない作成途中のsegmentである。
// 書込みごとにsegment、session、全世代の3つの上限を先に予約する。
type hlsCachePartial struct {
	mu         sync.Mutex
	generation *hlsCacheGeneration
	sequence   uint64
	name       string
	finalName  string
	file       *os.File
	identity   os.FileInfo
	bytes      int64
	done       bool
}

type hlsCacheSegment struct {
	generation *hlsCacheGeneration
	sequence   uint64
	name       string
	identity   os.FileInfo
	size       int64
	modTime    time.Time
	readers    int
	retired    bool
}

// hlsCacheReadFileはHTTPのRange読出し中だけsegmentの削除を延期する。
type hlsCacheReadFile struct {
	mu      sync.Mutex
	segment *hlsCacheSegment
	file    *os.File
}

func openHLSCache(dataRoot string, limits hlsCacheLimits) (*hlsCache, error) {
	if dataRoot == "" || !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot ||
		!validHLSCacheLimits(limits) {
		return nil, errors.New("recordinghttp: invalid hls cache")
	}
	data, _, err := openVerifiedAbsoluteHLSRoot(dataRoot)
	if err != nil {
		return nil, errors.New("recordinghttp: invalid data root")
	}
	defer data.Close()
	if err := data.Mkdir(hlsCacheDirectory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, errors.New("recordinghttp: create hls cache")
	}
	root, _, err := openVerifiedChildHLSRoot(data, hlsCacheDirectory)
	if err != nil {
		return nil, errors.New("recordinghttp: invalid hls cache directory")
	}
	if err := cleanupPreviousHLSCache(root, limits.cleanupEntries); err != nil {
		_ = root.Close()
		return nil, err
	}
	return &hlsCache{root: root, limits: limits,
		generations: make(map[hlsCacheGenerationID]*hlsCacheGeneration)}, nil
}

func validHLSCacheLimits(limits hlsCacheLimits) bool {
	return limits.segmentBytes > 0 && limits.sessionBytes >= limits.segmentBytes &&
		limits.totalBytes >= limits.sessionBytes && limits.cleanupEntries > 0 && limits.cleanupEntries <= 1_000_000
}

// beginは主画面または二画面の新しい世代を、既存directoryを上書きせずに始める。
func (cache *hlsCache) begin(slot uint8, generation uint64) (*hlsCacheGeneration, error) {
	if cache == nil {
		return nil, errors.New("recordinghttp: invalid hls cache generation")
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.root == nil || slot > 1 {
		return nil, errors.New("recordinghttp: invalid hls cache generation")
	}
	id := hlsCacheGenerationID{slot: slot, generation: generation}
	if cache.generations[id] != nil {
		return nil, errors.New("recordinghttp: hls cache generation exists")
	}
	name := hlsGenerationName(id)
	if err := cache.root.Mkdir(name, 0o700); err != nil {
		return nil, errors.New("recordinghttp: create hls cache generation")
	}
	root, identity, err := openVerifiedChildHLSRoot(cache.root, name)
	if err != nil {
		return nil, errors.New("recordinghttp: invalid hls cache generation")
	}
	value := &hlsCacheGeneration{cache: cache, id: id, name: name, root: root, identity: identity,
		partials: make(map[uint64]*hlsCachePartial), segments: make(map[uint64]*hlsCacheSegment)}
	cache.generations[id] = value
	return value, nil
}

// createは一つのsequenceに対応する0600の部分fileを排他的に作る。
func (generation *hlsCacheGeneration) create(sequence uint64) (*hlsCachePartial, error) {
	if generation == nil || generation.cache == nil {
		return nil, errors.New("recordinghttp: invalid hls cache generation")
	}
	cache := generation.cache
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if generation.closed || generation.root == nil || cache.generations[generation.id] != generation ||
		generation.partials[sequence] != nil || generation.segments[sequence] != nil {
		return nil, errors.New("recordinghttp: hls segment exists")
	}
	partialName, finalName := hlsSegmentNames(sequence)
	if pathExistsInHLSRoot(generation.root, partialName) || pathExistsInHLSRoot(generation.root, finalName) {
		return nil, errors.New("recordinghttp: hls segment path exists")
	}
	file, err := generation.root.OpenFile(partialName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, errors.New("recordinghttp: create hls partial")
	}
	identity, statErr := file.Stat()
	if statErr != nil || !validHLSRegularInfo(identity, 1) ||
		!sameHLSFile(generation.root, partialName, identity, 0, 1) {
		_ = file.Close()
		return nil, errors.New("recordinghttp: invalid hls partial")
	}
	partial := &hlsCachePartial{generation: generation, sequence: sequence, name: partialName,
		finalName: finalName, file: file, identity: identity}
	generation.partials[sequence] = partial
	return partial, nil
}

// Writeは3つの容量上限を越えない範囲だけを部分fileへ書く。
func (partial *hlsCachePartial) Write(data []byte) (int, error) {
	partial.mu.Lock()
	defer partial.mu.Unlock()
	if len(data) == 0 {
		return 0, nil
	}
	if partial.done || partial.file == nil || partial.generation == nil {
		return 0, errors.New("recordinghttp: invalid hls partial write")
	}
	generation := partial.generation
	cache := generation.cache
	cache.mu.Lock()
	if generation.closed || generation.partials[partial.sequence] != partial ||
		!sameHLSFile(generation.root, partial.name, partial.identity, partial.bytes, 1) {
		cache.mu.Unlock()
		return 0, errors.New("recordinghttp: hls partial changed")
	}
	added := int64(len(data))
	if !addWithin(partial.bytes, added, cache.limits.segmentBytes) ||
		!addWithin(generation.bytes, added, cache.limits.sessionBytes) ||
		!addWithin(cache.totalBytes, added, cache.limits.totalBytes) {
		cache.mu.Unlock()
		return 0, errHLSCacheLimit
	}
	generation.bytes += added
	cache.totalBytes += added
	cache.mu.Unlock()

	written, err := partial.file.Write(data)
	if written < 0 || written > len(data) {
		written = 0
		err = errors.New("recordinghttp: invalid hls write result")
	}
	partial.bytes += int64(written)
	if written != len(data) {
		unused := added - int64(written)
		cache.mu.Lock()
		generation.bytes -= unused
		cache.totalBytes -= unused
		cache.mu.Unlock()
		if err == nil {
			err = io.ErrShortWrite
		}
	}
	if err != nil {
		return written, errors.New("recordinghttp: write hls partial")
	}
	return written, nil
}

// publishは部分fileを閉じ、既存名を置き換えないhard linkで完成名を一度だけ公開する。
// 完成名をHTTPで参照できるのは、部分名を削除してlink数が1へ戻った後だけである。
func (partial *hlsCachePartial) publish() (*hlsCacheSegment, error) {
	partial.mu.Lock()
	defer partial.mu.Unlock()
	if partial.done || partial.file == nil || partial.generation == nil || partial.bytes <= 0 {
		return nil, errors.New("recordinghttp: invalid hls publication")
	}
	generation := partial.generation
	cache := generation.cache
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if generation.closed || generation.partials[partial.sequence] != partial ||
		!sameHLSFile(generation.root, partial.name, partial.identity, partial.bytes, 1) {
		return nil, errors.New("recordinghttp: hls publication refused")
	}
	if err := partial.file.Close(); err != nil {
		partial.file = nil
		return nil, errors.New("recordinghttp: close hls partial")
	}
	partial.file = nil
	if !sameHLSFile(generation.root, partial.name, partial.identity, partial.bytes, 1) {
		return nil, errors.New("recordinghttp: hls partial changed before publication")
	}
	if err := generation.root.Link(partial.name, partial.finalName); err != nil {
		return nil, errors.New("recordinghttp: publish hls segment")
	}
	partialInfo, partialErr := generation.root.Lstat(partial.name)
	finalInfo, finalErr := generation.root.Lstat(partial.finalName)
	if partialErr != nil || finalErr != nil || !validHLSRegularInfo(partialInfo, 2) ||
		!validHLSRegularInfo(finalInfo, 2) || !os.SameFile(partial.identity, partialInfo) ||
		!os.SameFile(partialInfo, finalInfo) || partialInfo.Size() != partial.bytes {
		return nil, errors.Join(errors.New("recordinghttp: hls publication readback failed"),
			rollbackHLSPublication(partial))
	}
	if err := generation.root.Remove(partial.name); err != nil {
		return nil, errors.Join(errors.New("recordinghttp: remove published hls partial"),
			rollbackHLSPublication(partial))
	}
	finalInfo, finalErr = generation.root.Lstat(partial.finalName)
	if finalErr != nil || !validHLSRegularInfo(finalInfo, 1) || finalInfo.Size() != partial.bytes ||
		!os.SameFile(partial.identity, finalInfo) {
		return nil, errors.New("recordinghttp: hls publication final readback failed")
	}
	segment := &hlsCacheSegment{generation: generation, sequence: partial.sequence, name: partial.finalName,
		identity: finalInfo, size: partial.bytes, modTime: finalInfo.ModTime()}
	delete(generation.partials, partial.sequence)
	generation.segments[partial.sequence] = segment
	partial.done = true
	return segment, nil
}

func rollbackHLSPublication(partial *hlsCachePartial) error {
	root := partial.generation.root
	partialInfo, partialErr := root.Lstat(partial.name)
	finalInfo, finalErr := root.Lstat(partial.finalName)
	if partialErr != nil || finalErr != nil || !validHLSRegularInfo(partialInfo, 2) ||
		!validHLSRegularInfo(finalInfo, 2) || !os.SameFile(partial.identity, partialInfo) ||
		!os.SameFile(partialInfo, finalInfo) || partialInfo.Size() != partial.bytes {
		return errors.New("recordinghttp: unsafe hls publication rollback")
	}
	if err := root.Remove(partial.finalName); err != nil {
		return errors.New("recordinghttp: rollback hls publication")
	}
	if !sameHLSFile(root, partial.name, partial.identity, partial.bytes, 1) {
		return errors.New("recordinghttp: hls publication rollback readback failed")
	}
	return nil
}

// discardは作成時と同じ部分fileだけを削除し、予約した容量を戻す。
func (partial *hlsCachePartial) discard() error {
	partial.mu.Lock()
	defer partial.mu.Unlock()
	if partial.done {
		return nil
	}
	if partial.generation == nil {
		return errors.New("recordinghttp: invalid hls partial")
	}
	generation := partial.generation
	cache := generation.cache
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if generation.partials[partial.sequence] != partial {
		return errors.New("recordinghttp: unknown hls partial")
	}
	if partial.file != nil {
		if err := partial.file.Close(); err != nil {
			partial.file = nil
			return errors.New("recordinghttp: close hls partial")
		}
		partial.file = nil
	}
	if !sameHLSFile(generation.root, partial.name, partial.identity, partial.bytes, 1) {
		return errors.New("recordinghttp: hls partial changed before removal")
	}
	if err := generation.root.Remove(partial.name); err != nil {
		return errors.New("recordinghttp: remove hls partial")
	}
	delete(generation.partials, partial.sequence)
	generation.bytes -= partial.bytes
	cache.totalBytes -= partial.bytes
	partial.done = true
	return cache.removeEmptyGenerationLocked(generation)
}

// openは完成後も同じinodeであるsegmentだけを読取り専用で開く。
func (generation *hlsCacheGeneration) open(sequence uint64) (*hlsCacheReadFile, error) {
	if generation == nil || generation.cache == nil {
		return nil, errors.New("recordinghttp: invalid hls cache generation")
	}
	cache := generation.cache
	cache.mu.Lock()
	defer cache.mu.Unlock()
	segment := generation.segments[sequence]
	if segment == nil || segment.retired || generation.root == nil ||
		!sameHLSFile(generation.root, segment.name, segment.identity, segment.size, 1) {
		return nil, errors.New("recordinghttp: hls segment unavailable")
	}
	file, err := generation.root.Open(segment.name)
	if err != nil {
		return nil, errors.New("recordinghttp: open hls segment")
	}
	info, statErr := file.Stat()
	if statErr != nil || !validHLSRegularInfo(info, 1) || info.Size() != segment.size ||
		!os.SameFile(segment.identity, info) {
		_ = file.Close()
		return nil, errors.New("recordinghttp: hls segment changed")
	}
	segment.readers++
	return &hlsCacheReadFile{segment: segment, file: file}, nil
}

// retireは保持期限を迎えたsegmentを削除する。読出し中なら最後のCloseまで延期する。
func (generation *hlsCacheGeneration) retire(sequence uint64) error {
	if generation == nil || generation.cache == nil {
		return errors.New("recordinghttp: invalid hls cache generation")
	}
	cache := generation.cache
	cache.mu.Lock()
	defer cache.mu.Unlock()
	segment := generation.segments[sequence]
	if segment == nil {
		return errors.New("recordinghttp: unknown hls segment")
	}
	segment.retired = true
	if segment.readers > 0 {
		return nil
	}
	return cache.removeSegmentLocked(segment)
}

// stopは新しいsegment作成を止め、残っている部分fileだけを破棄する。
func (generation *hlsCacheGeneration) stop() error {
	if generation == nil || generation.cache == nil {
		return errors.New("recordinghttp: invalid hls cache generation")
	}
	cache := generation.cache
	cache.mu.Lock()
	generation.closed = true
	partials := make([]*hlsCachePartial, 0, len(generation.partials))
	for _, partial := range generation.partials {
		partials = append(partials, partial)
	}
	cache.mu.Unlock()
	var result error
	for _, partial := range partials {
		result = errors.Join(result, partial.discard())
	}
	cache.mu.Lock()
	result = errors.Join(result, cache.removeEmptyGenerationLocked(generation))
	cache.mu.Unlock()
	return result
}

// closeはprocess終了時にcacheと残存世代のdirectory handleを閉じる。
// 完成segmentは次回起動時の限定cleanupへ残し、ここでは削除しない。
func (cache *hlsCache) close() error {
	if cache == nil {
		return nil
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for _, generation := range cache.generations {
		if len(generation.partials) != 0 {
			return errors.New("recordinghttp: hls cache is busy")
		}
		for _, segment := range generation.segments {
			if segment.readers != 0 {
				return errors.New("recordinghttp: hls cache is busy")
			}
		}
	}
	var result error
	for _, generation := range cache.generations {
		if generation.root != nil {
			result = errors.Join(result, generation.root.Close())
			generation.root = nil
		}
	}
	if cache.root != nil {
		result = errors.Join(result, cache.root.Close())
		cache.root = nil
	}
	return result
}

// Readは完成segmentの現在位置からデータを読み、読出し前の差し替えを拒否する。
func (file *hlsCacheReadFile) Read(data []byte) (int, error) {
	file.mu.Lock()
	defer file.mu.Unlock()
	if file.file == nil || file.segment == nil || file.segment.generation.root == nil ||
		!sameHLSFile(file.segment.generation.root, file.segment.name, file.segment.identity, file.segment.size, 1) {
		return 0, errors.New("recordinghttp: hls segment changed")
	}
	return file.file.Read(data)
}

// SeekはHTTP Range処理が使う読取り位置を、検証済みsegment内で移動する。
func (file *hlsCacheReadFile) Seek(offset int64, whence int) (int64, error) {
	file.mu.Lock()
	defer file.mu.Unlock()
	if file.file == nil || file.segment == nil || file.segment.generation.root == nil ||
		!sameHLSFile(file.segment.generation.root, file.segment.name, file.segment.identity, file.segment.size, 1) {
		return 0, errors.New("recordinghttp: invalid hls segment seek")
	}
	return file.file.Seek(offset, whence)
}

// ModTimeはHTTPの条件付き応答に使う完成segmentの更新時刻を返す。
func (file *hlsCacheReadFile) ModTime() time.Time {
	if file == nil || file.segment == nil {
		return time.Time{}
	}
	return file.segment.modTime
}

// Closeは読取り用fileを一度だけ閉じ、保持期限後の削除を最後の読出しまで延期する。
func (file *hlsCacheReadFile) Close() error {
	file.mu.Lock()
	defer file.mu.Unlock()
	if file.file == nil || file.segment == nil {
		return nil
	}
	closeErr := file.file.Close()
	file.file = nil
	segment := file.segment
	cache := segment.generation.cache
	cache.mu.Lock()
	segment.readers--
	var removeErr error
	if segment.retired && segment.readers == 0 {
		removeErr = cache.removeSegmentLocked(segment)
	}
	cache.mu.Unlock()
	if closeErr != nil {
		closeErr = errors.New("recordinghttp: close hls segment")
	}
	return errors.Join(closeErr, removeErr)
}

func (cache *hlsCache) removeSegmentLocked(segment *hlsCacheSegment) error {
	generation := segment.generation
	if generation.root == nil || generation.segments[segment.sequence] != segment || segment.readers != 0 ||
		!sameHLSFile(generation.root, segment.name, segment.identity, segment.size, 1) {
		return errors.New("recordinghttp: hls segment changed before removal")
	}
	if err := generation.root.Remove(segment.name); err != nil {
		return errors.New("recordinghttp: remove hls segment")
	}
	delete(generation.segments, segment.sequence)
	generation.bytes -= segment.size
	cache.totalBytes -= segment.size
	return cache.removeEmptyGenerationLocked(generation)
}

func (cache *hlsCache) removeEmptyGenerationLocked(generation *hlsCacheGeneration) error {
	if cache.generations[generation.id] != generation {
		return nil
	}
	if !generation.closed || len(generation.partials) != 0 || len(generation.segments) != 0 {
		return nil
	}
	if generation.root == nil || !sameHLSDirectory(generation.root, generation.identity) {
		return errors.New("recordinghttp: invalid empty hls generation")
	}
	current, err := cache.root.Lstat(generation.name)
	if err != nil || !validHLSDirectoryInfo(current) || !os.SameFile(generation.identity, current) {
		return errors.New("recordinghttp: hls generation changed before removal")
	}
	if err := cache.root.Remove(generation.name); err != nil {
		return errors.New("recordinghttp: remove hls generation")
	}
	closeErr := generation.root.Close()
	generation.root = nil
	delete(cache.generations, generation.id)
	if closeErr != nil {
		return errors.New("recordinghttp: close hls generation")
	}
	return nil
}

func addWithin(current, added, limit int64) bool {
	return current >= 0 && added >= 0 && current <= limit && added <= limit-current
}

func pathExistsInHLSRoot(root *os.Root, name string) bool {
	_, err := root.Lstat(name)
	return err == nil || !errors.Is(err, os.ErrNotExist)
}

func sameHLSFile(root *os.Root, name string, identity os.FileInfo, size int64, links uint64) bool {
	if root == nil || identity == nil {
		return false
	}
	info, err := root.Lstat(name)
	return err == nil && validHLSRegularInfo(info, links) && info.Size() == size && os.SameFile(identity, info)
}

func sameHLSDirectory(root *os.Root, identity os.FileInfo) bool {
	if root == nil || identity == nil {
		return false
	}
	info, err := root.Lstat(".")
	return err == nil && validHLSDirectoryInfo(info) && os.SameFile(identity, info)
}

func openVerifiedAbsoluteHLSRoot(path string) (*os.Root, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil || !validHLSDirectoryInfo(before) {
		return nil, nil, errors.New("recordinghttp: invalid owner-only directory")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, nil, errors.New("recordinghttp: open owner-only directory")
	}
	after, afterErr := root.Lstat(".")
	current, currentErr := os.Lstat(path)
	if afterErr != nil || currentErr != nil || !validHLSDirectoryInfo(after) ||
		!validHLSDirectoryInfo(current) || !os.SameFile(before, after) || !os.SameFile(after, current) {
		_ = root.Close()
		return nil, nil, errors.New("recordinghttp: owner-only directory changed")
	}
	return root, after, nil
}

func openVerifiedChildHLSRoot(parent *os.Root, name string) (*os.Root, os.FileInfo, error) {
	before, err := parent.Lstat(name)
	if err != nil || !validHLSDirectoryInfo(before) {
		return nil, nil, errors.New("recordinghttp: invalid owner-only child directory")
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, nil, errors.New("recordinghttp: open owner-only child directory")
	}
	after, afterErr := root.Lstat(".")
	current, currentErr := parent.Lstat(name)
	if afterErr != nil || currentErr != nil || !validHLSDirectoryInfo(after) ||
		!validHLSDirectoryInfo(current) || !os.SameFile(before, after) || !os.SameFile(after, current) {
		_ = root.Close()
		return nil, nil, errors.New("recordinghttp: owner-only child directory changed")
	}
	return root, after, nil
}

func validHLSDirectoryInfo(info os.FileInfo) bool {
	if info == nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func validHLSRegularInfo(info os.FileInfo, links uint64) bool {
	actual, valid := hlsRegularLinkCount(info)
	return valid && actual == links
}

func hlsRegularLinkCount(info os.FileInfo) (uint64, bool) {
	if info == nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() < 0 {
		return 0, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return 0, false
	}
	return uint64(stat.Nlink), true
}

func hlsGenerationName(id hlsCacheGenerationID) string {
	return fmt.Sprintf("session-%d-%020d", id.slot, id.generation)
}

func hlsSegmentNames(sequence uint64) (string, string) {
	base := fmt.Sprintf("segment-%020d", sequence)
	return base + ".partial", base + ".ts"
}

type hlsCleanupGeneration struct {
	name     string
	root     *os.Root
	identity os.FileInfo
	segments map[uint64]*hlsCleanupSegment
}

type hlsCleanupFile struct {
	name     string
	identity os.FileInfo
}

type hlsCleanupSegment struct {
	partial *hlsCleanupFile
	final   *hlsCleanupFile
	linked  bool
}

// cleanupPreviousHLSCacheは固定cache直下だけを上限付きで調べ、完全に検証できた過去世代だけを削除する。
func cleanupPreviousHLSCache(root *os.Root, limit int) error {
	remaining := limit
	entries, err := readHLSDirectoryBounded(root, &remaining)
	if err != nil {
		return err
	}
	var generations []hlsCleanupGeneration
	defer func() {
		for index := range generations {
			if generations[index].root != nil {
				_ = generations[index].root.Close()
			}
		}
	}()
	for _, entry := range entries {
		id, known := parseHLSGenerationName(entry.Name())
		if !known {
			continue
		}
		name := hlsGenerationName(id)
		generationRoot, identity, err := openVerifiedChildHLSRoot(root, name)
		if err != nil {
			return errors.New("recordinghttp: unsafe previous hls generation")
		}
		generation := hlsCleanupGeneration{name: name, root: generationRoot, identity: identity,
			segments: make(map[uint64]*hlsCleanupSegment)}
		generations = append(generations, generation)
		children, err := readHLSDirectoryBounded(generationRoot, &remaining)
		if err != nil {
			return err
		}
		for _, child := range children {
			sequence, partial, valid := parseHLSSegmentName(child.Name())
			if !valid {
				return errors.New("recordinghttp: unknown path in previous hls generation")
			}
			childInfo, err := generationRoot.Lstat(child.Name())
			links, safe := hlsRegularLinkCount(childInfo)
			if err != nil || !safe || links < 1 || links > 2 {
				return errors.New("recordinghttp: unsafe previous hls segment")
			}
			segment := generations[len(generations)-1].segments[sequence]
			if segment == nil {
				segment = &hlsCleanupSegment{}
				generations[len(generations)-1].segments[sequence] = segment
			}
			file := &hlsCleanupFile{name: child.Name(), identity: childInfo}
			if partial {
				if segment.partial != nil {
					return errors.New("recordinghttp: duplicate previous hls partial")
				}
				segment.partial = file
			} else {
				if segment.final != nil {
					return errors.New("recordinghttp: duplicate previous hls segment")
				}
				segment.final = file
			}
		}
		for _, segment := range generations[len(generations)-1].segments {
			partialLinks, partialSafe := cleanupFileLinks(segment.partial)
			finalLinks, finalSafe := cleanupFileLinks(segment.final)
			if !partialSafe || !finalSafe {
				return errors.New("recordinghttp: invalid previous hls publication")
			}
			if segment.partial != nil && segment.final != nil {
				if partialLinks != 2 || finalLinks != 2 || !os.SameFile(segment.partial.identity, segment.final.identity) {
					return errors.New("recordinghttp: unsafe linked previous hls publication")
				}
				segment.linked = true
			} else if partialLinks == 2 || finalLinks == 2 {
				return errors.New("recordinghttp: incomplete linked previous hls publication")
			}
		}
	}
	for index := range generations {
		generation := &generations[index]
		for _, segment := range generation.segments {
			if err := removePreviousHLSSegment(generation.root, segment); err != nil {
				return err
			}
		}
		if !sameHLSDirectory(generation.root, generation.identity) {
			return errors.New("recordinghttp: previous hls generation changed")
		}
		current, err := root.Lstat(generation.name)
		if err != nil || !validHLSDirectoryInfo(current) || !os.SameFile(generation.identity, current) {
			return errors.New("recordinghttp: previous hls generation path changed")
		}
		if err := root.Remove(generation.name); err != nil {
			return errors.New("recordinghttp: remove previous hls generation")
		}
		if err := generation.root.Close(); err != nil {
			return errors.New("recordinghttp: close previous hls generation")
		}
		generation.root = nil
	}
	return nil
}

func cleanupFileLinks(file *hlsCleanupFile) (uint64, bool) {
	if file == nil {
		return 0, true
	}
	links, valid := hlsRegularLinkCount(file.identity)
	return links, valid && (links == 1 || links == 2)
}

func removePreviousHLSSegment(root *os.Root, segment *hlsCleanupSegment) error {
	if segment.linked {
		if !sameHLSFile(root, segment.partial.name, segment.partial.identity, segment.partial.identity.Size(), 2) ||
			!sameHLSFile(root, segment.final.name, segment.final.identity, segment.final.identity.Size(), 2) {
			return errors.New("recordinghttp: linked previous hls publication changed")
		}
		if err := root.Remove(segment.partial.name); err != nil {
			return errors.New("recordinghttp: remove linked previous hls partial")
		}
		if !sameHLSFile(root, segment.final.name, segment.final.identity, segment.final.identity.Size(), 1) {
			return errors.New("recordinghttp: linked previous hls segment changed")
		}
		if err := root.Remove(segment.final.name); err != nil {
			return errors.New("recordinghttp: remove linked previous hls segment")
		}
		return nil
	}
	for _, file := range []*hlsCleanupFile{segment.partial, segment.final} {
		if file == nil {
			continue
		}
		if !sameHLSFile(root, file.name, file.identity, file.identity.Size(), 1) {
			return errors.New("recordinghttp: previous hls segment changed")
		}
		if err := root.Remove(file.name); err != nil {
			return errors.New("recordinghttp: remove previous hls segment")
		}
	}
	return nil
}

func readHLSDirectoryBounded(root *os.Root, remaining *int) ([]os.DirEntry, error) {
	if root == nil || remaining == nil || *remaining < 0 {
		return nil, errors.New("recordinghttp: invalid hls cleanup limit")
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, errors.New("recordinghttp: open hls cleanup directory")
	}
	entries, readErr := directory.ReadDir(*remaining + 1)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) || closeErr != nil {
		return nil, errors.New("recordinghttp: read hls cleanup directory")
	}
	if len(entries) > *remaining {
		return nil, errors.New("recordinghttp: hls cleanup limit")
	}
	*remaining -= len(entries)
	return entries, nil
}

func parseHLSGenerationName(name string) (hlsCacheGenerationID, bool) {
	if len(name) != len("session-0-")+20 || !strings.HasPrefix(name, "session-") || name[9] != '-' ||
		(name[8] != '0' && name[8] != '1') {
		return hlsCacheGenerationID{}, false
	}
	value, err := strconv.ParseUint(name[10:], 10, 64)
	if err != nil || fmt.Sprintf("%020d", value) != name[10:] {
		return hlsCacheGenerationID{}, false
	}
	return hlsCacheGenerationID{slot: name[8] - '0', generation: value}, true
}

func parseHLSSegmentName(name string) (uint64, bool, bool) {
	const prefix = "segment-"
	if !strings.HasPrefix(name, prefix) {
		return 0, false, false
	}
	var number string
	var partial bool
	switch {
	case strings.HasSuffix(name, ".partial"):
		number = strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".partial")
		partial = true
	case strings.HasSuffix(name, ".ts"):
		number = strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".ts")
	default:
		return 0, false, false
	}
	value, err := strconv.ParseUint(number, 10, 64)
	return value, partial, err == nil && len(number) == 20 && fmt.Sprintf("%020d", value) == number
}
