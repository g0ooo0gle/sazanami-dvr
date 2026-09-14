package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

const channelMapFileName = "channels.json"

// GeneratedServiceはMirakurunのservice観測からchannel-mapへ出力するidentityである。
// ServiceNameやservice typeは完成済みcatalogからBuildSnapshotが検証・取得するため、ここへ複製しない。
type GeneratedService struct {
	ProviderLocator   string
	NetworkID         uint16
	ServiceID         uint16
	TransportStreamID uint16
	RemoteControlKey  uint8
}

// PublishStatusはchannel-mapの公開結果である。
type PublishStatus string

const (
	// PublishStatusCreatedは候補を検証して新規公開した結果である。
	PublishStatusCreated PublishStatus = "created"
	// PublishStatusUnchangedは同じ内容の既存通常fileを再利用した結果である。
	PublishStatusUnchanged PublishStatus = "unchanged"
)

// PublishResultはchannel-mapの公開結果と、検証したservice件数を返す。
type PublishResult struct {
	ServiceCount int
	Status       PublishStatus
}

type generatedChannelMap struct {
	Format    string                       `json:"format"`
	BackendID string                       `json:"backend_id"`
	Services  []generatedChannelMapService `json:"services"`
}

type generatedChannelMapService struct {
	ProviderLocator     string `json:"provider_locator"`
	NetworkID           uint16 `json:"network_id"`
	ServiceID           uint16 `json:"service_id"`
	TransportStreamID   uint16 `json:"transport_stream_id"`
	ProviderName        string `json:"provider_name"`
	NetworkName         string `json:"network_name"`
	TransportStreamName string `json:"transport_stream_name"`
	RemoteControlKey    uint8  `json:"remote_control_key_id"`
	PartialReception    bool   `json:"partial_reception"`
	EPGCapture          bool   `json:"epg_capture"`
	Search              bool   `json:"search"`
}

// PublishGeneratedChannelMapは候補を決定的に生成し、最新のCOMPLETED catalogで検証してから
// data root直下のchannels.jsonへ上書きなしで公開する。候補と一時fileは呼び出し中だけ保持する。
func PublishGeneratedChannelMap(ctx context.Context, dataRoot string, backendID catalogmodel.ID,
	services []GeneratedService, reader CatalogReader,
) (PublishResult, error) {
	var empty PublishResult
	if err := bootstrapContextError(ctx); err != nil {
		return empty, err
	}
	if reader == nil {
		return empty, stable("channel-snapshot-failed")
	}
	if !validDataRoot(dataRoot) {
		return empty, stable("channel-map-path-invalid")
	}
	if _, err := catalogmodel.ParseID(backendID.String()); err != nil {
		return empty, stable("channel-map-field-invalid")
	}

	normalized, err := normalizeGeneratedServices(ctx, services)
	if err != nil {
		return empty, err
	}
	payload, err := marshalGeneratedChannelMap(ctx, backendID, normalized)
	if err != nil {
		return empty, err
	}
	temporary, err := writeGeneratedCandidate(ctx, dataRoot, payload)
	if err != nil {
		return empty, err
	}
	defer func() { _ = os.Remove(temporary) }()

	if err := bootstrapContextError(ctx); err != nil {
		return empty, err
	}
	if _, err := BuildSnapshot(ctx, dataRoot, temporary, reader); err != nil {
		if contextErr := bootstrapContextError(ctx); contextErr != nil {
			return empty, contextErr
		}
		return empty, err
	}
	if err := bootstrapContextError(ctx); err != nil {
		return empty, err
	}

	status, err := publishGeneratedCandidate(ctx, dataRoot, temporary, payload)
	if err != nil {
		return empty, err
	}
	return PublishResult{ServiceCount: len(normalized), Status: status}, nil
}

func bootstrapContextError(ctx context.Context) error {
	if ctx == nil {
		return stable("channel-snapshot-failed")
	}
	if ctx.Err() != nil {
		return stable("channel-context-ended")
	}
	return nil
}

func validDataRoot(dataRoot string) bool {
	if dataRoot == "" || !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot {
		return false
	}
	info, err := os.Lstat(dataRoot)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func normalizeGeneratedServices(ctx context.Context, services []GeneratedService) ([]generatedChannelMapService, error) {
	if len(services) < 1 || len(services) > maxServices {
		return nil, stable("channel-map-count")
	}
	result := make([]generatedChannelMapService, 0, len(services))
	locators := make(map[string]struct{}, len(services))
	identities := make(map[generatedServiceIdentity]struct{}, len(services))
	for _, service := range services {
		if err := bootstrapContextError(ctx); err != nil {
			return nil, err
		}
		if !canonicalServiceLocator(service.ProviderLocator) {
			return nil, stable("channel-map-field-invalid")
		}
		if _, exists := locators[service.ProviderLocator]; exists {
			return nil, stable("channel-service-duplicate")
		}
		locators[service.ProviderLocator] = struct{}{}
		identity := generatedServiceIdentity{
			network:   service.NetworkID,
			transport: service.TransportStreamID,
			service:   service.ServiceID,
		}
		if _, exists := identities[identity]; exists {
			return nil, stable("channel-service-duplicate")
		}
		identities[identity] = struct{}{}
		result = append(result, generatedChannelMapService{
			ProviderLocator:     service.ProviderLocator,
			NetworkID:           service.NetworkID,
			ServiceID:           service.ServiceID,
			TransportStreamID:   service.TransportStreamID,
			ProviderName:        "",
			NetworkName:         "",
			TransportStreamName: "",
			RemoteControlKey:    service.RemoteControlKey,
			PartialReception:    false,
			EPGCapture:          true,
			Search:              true,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if left.NetworkID != right.NetworkID {
			return left.NetworkID < right.NetworkID
		}
		if left.TransportStreamID != right.TransportStreamID {
			return left.TransportStreamID < right.TransportStreamID
		}
		if left.ServiceID != right.ServiceID {
			return left.ServiceID < right.ServiceID
		}
		return left.ProviderLocator < right.ProviderLocator
	})
	return result, nil
}

type generatedServiceIdentity struct {
	network   uint16
	transport uint16
	service   uint16
}

func marshalGeneratedChannelMap(ctx context.Context, backendID catalogmodel.ID,
	services []generatedChannelMapService,
) ([]byte, error) {
	if err := bootstrapContextError(ctx); err != nil {
		return nil, err
	}
	value := generatedChannelMap{Format: channelMapFormat, BackendID: backendID.String(), Services: services}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, stable("channel-map-json-invalid")
	}
	data = append(data, '\n')
	if len(data) > maxChannelMap {
		return nil, stable("channel-map-over-limit")
	}
	return data, nil
}

func writeGeneratedCandidate(ctx context.Context, dataRoot string, payload []byte) (string, error) {
	file, err := os.CreateTemp(dataRoot, ".channels.json.tmp-*")
	if err != nil {
		return "", stable("channel-map-write-failed")
	}
	temporary := file.Name()
	remove := true
	defer func() {
		if remove {
			_ = file.Close()
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", stable("channel-map-write-failed")
	}
	for len(payload) > 0 {
		if err := bootstrapContextError(ctx); err != nil {
			return "", err
		}
		written, err := file.Write(payload)
		if err != nil || written < 1 {
			return "", stable("channel-map-write-failed")
		}
		payload = payload[written:]
	}
	if err := bootstrapContextError(ctx); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", stable("channel-map-sync-failed")
	}
	if err := file.Close(); err != nil {
		return "", stable("channel-map-write-failed")
	}
	remove = false
	return temporary, nil
}

func publishGeneratedCandidate(ctx context.Context, dataRoot, temporary string, payload []byte) (PublishStatus, error) {
	if err := bootstrapContextError(ctx); err != nil {
		return "", err
	}
	finalPath := filepath.Join(dataRoot, channelMapFileName)
	for range 3 {
		if err := bootstrapContextError(ctx); err != nil {
			return "", err
		}
		if err := os.Link(temporary, finalPath); err == nil {
			// The hard link is the no-clobber commit. Removing the temporary name
			// leaves only the fully-written inode at the canonical path.
			if err := os.Remove(temporary); err != nil {
				return "", stable("channel-map-publish-failed")
			}
			if err := syncDirectory(dataRoot); err != nil {
				return "", err
			}
			return PublishStatusCreated, nil
		} else if !errors.Is(err, fs.ErrExist) {
			return "", stable("channel-map-publish-failed")
		}

		status, err := compareExistingChannelMap(ctx, finalPath, payload)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		return status, nil
	}
	return "", stable("channel-map-publish-failed")
}

func compareExistingChannelMap(ctx context.Context, path string, payload []byte) (PublishStatus, error) {
	if err := bootstrapContextError(ctx); err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", stable("channel-map-not-regular")
	}
	if info.Size() != int64(len(payload)) {
		return "", stable("channel-map-exists")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", stable("channel-map-read-failed")
	}
	data, readErr := readExistingMap(ctx, file, int64(len(payload))+1)
	closeErr := file.Close()
	if contextErr := bootstrapContextError(ctx); contextErr != nil {
		return "", contextErr
	}
	if readErr != nil || closeErr != nil {
		return "", stable("channel-map-read-failed")
	}
	if bytes.Equal(data, payload) {
		return PublishStatusUnchanged, nil
	}
	return "", stable("channel-map-exists")
}

func readExistingMap(ctx context.Context, reader io.Reader, limit int64) ([]byte, error) {
	data := make([]byte, 0, minInt64(limit, 32*1024))
	buffer := make([]byte, 32*1024)
	for int64(len(data)) < limit {
		if err := bootstrapContextError(ctx); err != nil {
			return nil, err
		}
		remaining := limit - int64(len(data))
		if int64(len(buffer)) > remaining {
			buffer = buffer[:remaining]
		}
		count, err := reader.Read(buffer)
		if count > 0 {
			data = append(data, buffer[:count]...)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return data, nil
			}
			return nil, err
		}
		if count == 0 {
			return nil, io.ErrNoProgress
		}
	}
	return data, nil
}

func minInt64(left, right int64) int {
	if left < right {
		return int(left)
	}
	return int(right)
}

func syncDirectory(dataRoot string) error {
	directory, err := os.Open(dataRoot)
	if err != nil {
		return stable("channel-map-directory-sync-failed")
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil || closeErr != nil {
		return stable("channel-map-directory-sync-failed")
	}
	return nil
}
