package channel

import (
	"context"
	"io"
	"sort"
	"strconv"
	"unicode/utf8"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
)

const (
	maxSnapshotKey     = 128
	maxProviderLocator = 256
	maxNameBytes       = 4_096
)

// Sourceは、DBやプロバイダー固有の型を漏らさず、変更されない完成済みスナップショットを返す。
type Source interface {
	Current(context.Context) (Snapshot, error)
}

// Snapshotは、1回の応答生成に使うチャンネル一覧と、その世代を識別する内部キーを保持する。
type Snapshot struct {
	Key      string
	Services []Service
}

// Serviceは、応答値をアダプター内で推測せずに出力するための完成済みデータである。
type Service struct {
	ProviderLocator     string
	ProviderName        string
	ServiceName         string
	NetworkName         string
	TransportStreamName string
	NetworkID           uint16
	TransportStreamID   uint16
	ServiceID           uint16
	ServiceType         uint8
	RemoteControlKey    uint8
	PartialReception    bool
	EPGCapture          bool
	Search              bool
	Verified            bool
	Selected            bool
}

type identity struct {
	network   uint16
	transport uint16
	service   uint16
}

func project(ctx context.Context, source Source) ([]Service, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if source == nil {
		return nil, codecFailure(codec.Internal, "missing-channel-source", 0)
	}
	snapshot, err := source.Current(ctx)
	if err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return nil, contextErr
		}
		return nil, codecFailure(codec.Internal, "channel-source-failed", 0)
	}
	return validateSnapshot(ctx, snapshot)
}

// ValidateSnapshotは待受開始前に、完成済みスナップショットの全項目と重複を検証する。
// 返されたsliceは正規順へ並べたコピーであり、入力sliceを変更しない。
func ValidateSnapshot(ctx context.Context, snapshot Snapshot) ([]Service, error) {
	return validateSnapshot(ctx, snapshot)
}

func validateSnapshot(ctx context.Context, snapshot Snapshot) ([]Service, error) {
	if !utf8.ValidString(snapshot.Key) || len(snapshot.Key) < 1 || len(snapshot.Key) > maxSnapshotKey {
		return nil, codecFailure(codec.Malformed, "snapshot-key", int64(len(snapshot.Key)))
	}
	if len(snapshot.Services) > maxServices {
		return nil, codecFailure(codec.OverLimit, "snapshot-service-count", int64(len(snapshot.Services)))
	}

	services := make([]Service, 0, len(snapshot.Services))
	identities := make(map[identity]struct{}, len(snapshot.Services))
	for _, service := range snapshot.Services {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		if !service.Verified {
			continue
		}
		if err := validateService(service); err != nil {
			return nil, err
		}
		if !service.Selected {
			continue
		}
		key := identity{network: service.NetworkID, transport: service.TransportStreamID, service: service.ServiceID}
		if _, duplicate := identities[key]; duplicate {
			return nil, codecFailure(codec.Malformed, "duplicate-service-identity", 0)
		}
		identities[key] = struct{}{}
		services = append(services, service)
	}

	sort.Slice(services, func(i, j int) bool {
		left, right := services[i], services[j]
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
	return services, nil
}

func validateService(service Service) error {
	if !utf8.ValidString(service.ProviderLocator) || len(service.ProviderLocator) < 1 || len(service.ProviderLocator) > maxProviderLocator {
		return codecFailure(codec.Malformed, "provider-locator", int64(len(service.ProviderLocator)))
	}
	names := [...]struct {
		value    string
		required bool
	}{
		{service.ProviderName, false},
		{service.ServiceName, true},
		{service.NetworkName, false},
		{service.TransportStreamName, false},
	}
	for _, name := range names {
		if !utf8.ValidString(name.value) || len(name.value) > maxNameBytes || (name.required && len(name.value) == 0) {
			return codecFailure(codec.Malformed, "service-name-field", int64(len(name.value)))
		}
		for _, forbidden := range [...]byte{0, '\t', '\r', '\n'} {
			for i := 0; i < len(name.value); i++ {
				if name.value[i] == forbidden {
					return codecFailure(codec.Malformed, "service-name-control", int64(i))
				}
			}
		}
	}
	return nil
}

func fileBodySize(ctx context.Context, services []Service, limit int64) (int64, error) {
	var size int64
	for _, service := range services {
		if err := contextError(ctx); err != nil {
			return 0, err
		}
		rowSize := int64(len(service.ServiceName) + len(service.NetworkName) + 10)
		rowSize += decimalSize(uint64(service.NetworkID))
		rowSize += decimalSize(uint64(service.TransportStreamID))
		rowSize += decimalSize(uint64(service.ServiceID))
		rowSize += decimalSize(uint64(service.ServiceType))
		rowSize += 3 // 3つのboolean flag。
		rowSize += decimalSize(uint64(service.RemoteControlKey))
		var err error
		size, err = checkedSize(size, rowSize, limit, "file-response-body")
		if err != nil {
			return 0, err
		}
	}
	return size, nil
}

func writeFile(ctx context.Context, destination io.Writer, services []Service, limits codec.Limits) error {
	limit := int64(limits.ResponseBody)
	bodySize, err := fileBodySize(ctx, services, limit)
	if err != nil {
		return err
	}
	return codec.WriteFrame(contextWriter{ctx: ctx, w: destination}, ResultSuccess, bodySize, limits, func(writer *codec.Writer) error {
		for _, service := range services {
			if err := contextError(ctx); err != nil {
				return err
			}
			if err := writeFileRow(writer, service); err != nil {
				return err
			}
		}
		return nil
	})
}

func writeFileRow(writer *codec.Writer, service Service) error {
	if err := writer.Bytes([]byte(service.ServiceName)); err != nil {
		return err
	}
	if err := writer.Bytes([]byte{'\t'}); err != nil {
		return err
	}
	if err := writer.Bytes([]byte(service.NetworkName)); err != nil {
		return err
	}
	values := [...]uint64{
		uint64(service.NetworkID), uint64(service.TransportStreamID), uint64(service.ServiceID), uint64(service.ServiceType),
		boolUint(service.PartialReception), boolUint(service.EPGCapture), boolUint(service.Search), uint64(service.RemoteControlKey),
	}
	var number [20]byte
	for _, value := range values {
		if err := writer.Bytes([]byte{'\t'}); err != nil {
			return err
		}
		encoded := strconv.AppendUint(number[:0], value, 10)
		if err := writer.Bytes(encoded); err != nil {
			return err
		}
	}
	return writer.Bytes([]byte{'\n'})
}

func serviceBodySize(ctx context.Context, services []Service, limits codec.Limits) (int64, error) {
	limit := int64(limits.ResponseBody)
	structureLimit := limits.StructureExtent
	if structureLimit == 0 {
		structureLimit = codec.MaxStructureExtent
	}
	if int64(structureLimit) < limit {
		limit = int64(structureLimit)
	}
	size := int64(8)
	for _, service := range services {
		if err := contextError(ctx); err != nil {
			return 0, err
		}
		structureSize := int64(13)
		var err error
		for _, name := range [...]string{service.ProviderName, service.ServiceName, service.NetworkName, service.TransportStreamName} {
			stringSize, err := codec.StringSize(name, limits)
			if err != nil {
				return 0, err
			}
			structureSize, err = checkedSize(structureSize, stringSize, limit, "service-structure")
			if err != nil {
				return 0, err
			}
		}
		size, err = checkedSize(size, structureSize, limit, "service-response-body")
		if err != nil {
			return 0, err
		}
	}
	return size, nil
}

// ServiceStructureSizeはServiceInfo一件を出力したときのbyte数を返す。
func ServiceStructureSize(service Service, limits codec.Limits) (int64, error) {
	if err := validateService(service); err != nil {
		return 0, err
	}
	structureSize := int64(13)
	for _, name := range [...]string{service.ProviderName, service.ServiceName, service.NetworkName, service.TransportStreamName} {
		size, err := codec.StringSize(name, limits)
		if err != nil {
			return 0, err
		}
		structureSize += size
	}
	return structureSize, nil
}

// WriteServiceStructureは検証済みServiceInfo一件を逐次出力する。
func WriteServiceStructure(writer *codec.Writer, service Service, limits codec.Limits) error {
	if writer == nil {
		return codecFailure(codec.Internal, "nil-service-writer", 0)
	}
	if _, err := ServiceStructureSize(service, limits); err != nil {
		return err
	}
	return writeService(writer, service, limits)
}

func writeServiceVector(ctx context.Context, destination io.Writer, services []Service, limits codec.Limits) error {
	bodySize, err := serviceBodySize(ctx, services, limits)
	if err != nil {
		return err
	}
	return codec.WriteFrame(contextWriter{ctx: ctx, w: destination}, ResultSuccess, bodySize, limits, func(writer *codec.Writer) error {
		if err := writer.I32(int32(bodySize)); err != nil {
			return err
		}
		if err := writer.I32(int32(len(services))); err != nil {
			return err
		}
		for _, service := range services {
			if err := contextError(ctx); err != nil {
				return err
			}
			if err := writeService(writer, service, limits); err != nil {
				return err
			}
		}
		return nil
	})
}

func writeService(writer *codec.Writer, service Service, limits codec.Limits) error {
	structureSize := int64(13)
	for _, name := range [...]string{service.ProviderName, service.ServiceName, service.NetworkName, service.TransportStreamName} {
		size, err := codec.StringSize(name, limits)
		if err != nil {
			return err
		}
		structureSize += size
	}
	if err := writer.I32(int32(structureSize)); err != nil {
		return err
	}
	if err := writer.U16(service.NetworkID); err != nil {
		return err
	}
	if err := writer.U16(service.TransportStreamID); err != nil {
		return err
	}
	if err := writer.U16(service.ServiceID); err != nil {
		return err
	}
	if err := writer.U8(service.ServiceType); err != nil {
		return err
	}
	if err := writer.U8(uint8(boolUint(service.PartialReception))); err != nil {
		return err
	}
	for _, name := range [...]string{service.ProviderName, service.ServiceName, service.NetworkName, service.TransportStreamName} {
		if err := writer.String(name); err != nil {
			return err
		}
	}
	return writer.U8(service.RemoteControlKey)
}

func decimalSize(value uint64) int64 {
	var size int64 = 1
	for value >= 10 {
		value /= 10
		size++
	}
	return size
}

func checkedSize(total, addition, limit int64, reason string) (int64, error) {
	if total < 0 || addition < 0 || limit < 0 || total > limit || addition > limit-total {
		return 0, codecFailure(codec.OverLimit, reason, limit)
	}
	return total + addition, nil
}

func boolUint(value bool) uint64 {
	if value {
		return 1
	}
	return 0
}

func contextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return codecFailure(codec.Timeout, "request-context-ended", 0)
	}
	return nil
}
