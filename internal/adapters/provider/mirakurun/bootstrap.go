package mirakurun

import (
	"context"
	"encoding/json"
	"math"
	"strconv"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
)

// BootstrapServiceはMirakurunのservice APIから初期channel mapへ渡す最小の値を表す。
// APIにない値は保持せず、IDとservice typeは受信値を検証したものだけを返す。
type BootstrapService struct {
	Locator          string
	NetworkID        uint16
	ServiceID        uint16
	Name             string
	ServiceType      uint16
	RemoteControlKey uint8
}

// ObserveBootstrapServicesは/api/servicesを一度だけ読み、初期channel生成用service一覧を返す。
// 応答全体を保持せず、項目数と各JSON値を上限付きで検証する。
func (adapter *Adapter) ObserveBootstrapServices(ctx context.Context) ([]BootstrapService, error) {
	if adapter == nil || adapter.client == nil {
		return nil, provider.NewFailure(provider.ReasonInternal, "nil-adapter")
	}
	operation, err := adapter.open(ctx, "/api/services", adapter.serviceCap, adapter.limits.services)
	if err != nil {
		return nil, err
	}
	if err := operation.beginArray(); err != nil {
		return nil, operation.failure(err)
	}
	services := make([]BootstrapService, 0, 32)
	locators := make(map[string]struct{}, 32)
	for operation.decoder.More() {
		if len(services) >= provider.MaxServiceOperation {
			return nil, operation.failure(provider.NewFailure(provider.ReasonOverLimit, "bootstrap-service-count-over-limit"))
		}
		service, decodeErr := decodeBootstrapService(operation.decoder)
		if decodeErr != nil {
			return nil, operation.failure(decodeErr)
		}
		if _, duplicate := locators[service.Locator]; duplicate {
			return nil, operation.failure(provider.NewFailure(provider.ReasonMalformed, "bootstrap-service-duplicate"))
		}
		locators[service.Locator] = struct{}{}
		services = append(services, service)
	}
	if err := operation.finishArray(); err != nil {
		return nil, operation.failure(err)
	}
	if err := operation.close(); err != nil {
		return nil, err
	}
	return services, nil
}

func decodeBootstrapService(decoder *json.Decoder) (BootstrapService, error) {
	var result BootstrapService
	seen, err := beginObject(decoder)
	if err != nil {
		return result, err
	}
	var id uint64
	var serviceType uint64
	var hasID, hasNetwork, hasService, hasName, hasType bool
	for decoder.More() {
		key, keyErr := readObjectKey(decoder, seen)
		if keyErr != nil {
			return result, keyErr
		}
		switch key {
		case "id":
			id, err = readUint(decoder, math.MaxInt64)
			hasID = err == nil
		case "networkId":
			value, valueErr := readUint(decoder, math.MaxUint16)
			err = valueErr
			result.NetworkID = uint16(value)
			hasNetwork = err == nil
		case "serviceId":
			value, valueErr := readUint(decoder, math.MaxUint16)
			err = valueErr
			result.ServiceID = uint16(value)
			hasService = err == nil
		case "name":
			result.Name, err = readDisplayString(decoder, 4_096)
			hasName = err == nil
		case "type":
			serviceType, err = readUint(decoder, math.MaxUint16)
			hasType = err == nil
		case "remoteControlKeyId":
			value, valueErr := readNullableUint(decoder, math.MaxUint8)
			err = valueErr
			result.RemoteControlKey = uint8(value)
		default:
			count := 0
			err = skipValue(decoder, 0, &count)
		}
		if err != nil {
			return BootstrapService{}, err
		}
	}
	if err := endObject(decoder); err != nil {
		return BootstrapService{}, err
	}
	if !hasID || !hasNetwork || !hasService || !hasName || !hasType {
		return BootstrapService{}, provider.NewFailure(provider.ReasonMalformed, "bootstrap-service-required-field-missing")
	}
	if id == 0 {
		return BootstrapService{}, provider.NewFailure(provider.ReasonMalformed, "bootstrap-service-id-invalid")
	}
	if result.Name == "" {
		return BootstrapService{}, provider.NewFailure(provider.ReasonMalformed, "bootstrap-service-name-empty")
	}
	if id != serviceProviderID(result.NetworkID, result.ServiceID) {
		return BootstrapService{}, provider.NewFailure(provider.ReasonMalformed, "bootstrap-service-id-contract-mismatch")
	}
	result.Locator = strconv.FormatUint(id, 10)
	result.ServiceType = uint16(serviceType)
	return result, nil
}

func readNullableUint(decoder *json.Decoder, maximum uint64) (uint64, error) {
	token, err := decoder.Token()
	if err != nil {
		return 0, err
	}
	if token == nil {
		return 0, nil
	}
	number, ok := token.(json.Number)
	if !ok {
		return 0, provider.NewFailure(provider.ReasonMalformed, "json-nullable-integer-required")
	}
	value, err := strconv.ParseUint(string(number), 10, 64)
	if err != nil {
		return 0, provider.NewFailure(provider.ReasonMalformed, "json-integer-invalid")
	}
	if value > maximum {
		return 0, provider.NewFailure(provider.ReasonOverLimit, "json-integer-overflow")
	}
	return value, nil
}
