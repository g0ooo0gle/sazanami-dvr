// Package runtimeは静的設定と完成済みカタログをCtrlCmdの固定応答へ接続する。
// 明示コマンドの起動時にだけ使い、provider通信や設定の自動更新は行わない。
package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"
)

const (
	channelMapFormat = "sazanami-channel-map-v1"
	maxChannelMap    = 1 * 1024 * 1024
	maxServices      = 4_096
)

type channelMap struct {
	Format    string
	BackendID string
	Services  []configuredService
	Hash      [sha256.Size]byte
}

type configuredService struct {
	ProviderLocator     string
	NetworkID           uint16
	ServiceID           uint16
	TransportStreamID   uint16
	ProviderName        string
	NetworkName         string
	TransportStreamName string
	RemoteControlKey    uint8
	PartialReception    bool
	EPGCapture          bool
	Search              bool
}

type rawChannelMap struct {
	Format    *string              `json:"format"`
	BackendID *string              `json:"backend_id"`
	Services  *[]rawServiceMapping `json:"services"`
}

type rawServiceMapping struct {
	ProviderLocator     *string `json:"provider_locator"`
	NetworkID           *uint16 `json:"network_id"`
	ServiceID           *uint16 `json:"service_id"`
	TransportStreamID   *uint16 `json:"transport_stream_id"`
	ProviderName        *string `json:"provider_name"`
	NetworkName         *string `json:"network_name"`
	TransportStreamName *string `json:"transport_stream_name"`
	RemoteControlKey    *uint8  `json:"remote_control_key_id"`
	PartialReception    *bool   `json:"partial_reception"`
	EPGCapture          *bool   `json:"epg_capture"`
	Search              *bool   `json:"search"`
}

func loadChannelMap(dataRoot, path string) (channelMap, error) {
	if dataRoot == "" || !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot ||
		path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Dir(path) != dataRoot {
		return channelMap{}, stable("channel-map-path-invalid")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return channelMap{}, stable("channel-map-path-invalid")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return channelMap{}, stable("channel-map-not-regular")
	}
	if info.Size() < 0 || info.Size() > maxChannelMap {
		return channelMap{}, stable("channel-map-over-limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return channelMap{}, stable("channel-map-path-invalid")
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxChannelMap+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return channelMap{}, stable("channel-map-read-failed")
	}
	if len(data) > maxChannelMap {
		return channelMap{}, stable("channel-map-over-limit")
	}
	if len(data) >= 3 && bytes.Equal(data[:3], []byte{0xef, 0xbb, 0xbf}) || !utf8.Valid(data) {
		return channelMap{}, stable("channel-map-json-invalid")
	}
	return decodeChannelMap(data)
}

func decodeChannelMap(data []byte) (channelMap, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var raw rawChannelMap
	if err := decoder.Decode(&raw); err != nil {
		return channelMap{}, stable("channel-map-json-invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return channelMap{}, stable("channel-map-json-invalid")
	}
	if raw.Format == nil || raw.BackendID == nil || raw.Services == nil {
		return channelMap{}, stable("channel-map-field-missing")
	}
	if *raw.Format != channelMapFormat {
		return channelMap{}, stable("channel-map-field-invalid")
	}
	if len(*raw.Services) < 1 || len(*raw.Services) > maxServices {
		return channelMap{}, stable("channel-map-count")
	}
	result := channelMap{Format: *raw.Format, BackendID: *raw.BackendID, Hash: sha256.Sum256(data)}
	result.Services = make([]configuredService, 0, len(*raw.Services))
	for _, service := range *raw.Services {
		if service.ProviderLocator == nil || service.NetworkID == nil || service.ServiceID == nil ||
			service.TransportStreamID == nil || service.ProviderName == nil || service.NetworkName == nil ||
			service.TransportStreamName == nil || service.RemoteControlKey == nil || service.PartialReception == nil ||
			service.EPGCapture == nil || service.Search == nil {
			return channelMap{}, stable("channel-map-field-missing")
		}
		result.Services = append(result.Services, configuredService{
			ProviderLocator: *service.ProviderLocator, NetworkID: *service.NetworkID, ServiceID: *service.ServiceID,
			TransportStreamID: *service.TransportStreamID, ProviderName: *service.ProviderName,
			NetworkName: *service.NetworkName, TransportStreamName: *service.TransportStreamName,
			RemoteControlKey: *service.RemoteControlKey, PartialReception: *service.PartialReception,
			EPGCapture: *service.EPGCapture, Search: *service.Search,
		})
	}
	return result, nil
}

type stable string

// Errorは設定内容やprivate pathを含まない、運用向けの安定した理由を返す。
func (err stable) Error() string { return string(err) }
