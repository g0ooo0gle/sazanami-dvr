package ctrlcmd

import (
	"fmt"
	"net"
	"net/netip"
	"time"
)

const (
	DefaultAddress       = "127.0.0.1:4510"
	DefaultConnections   = 32
	DefaultHandlers      = 16
	DefaultHeaderTimeout = 2 * time.Second
	DefaultLifetime      = 4 * time.Second
	MaximumLifetime      = 14 * time.Second
	// LongWriteTimeoutは長時間接続でclientの受信が止まったときの送信進捗上限である。
	LongWriteTimeout = 10 * time.Second
)

// ConfigはCtrlCmd listenerと1接続のresource上限をまとめる。
type Config struct {
	Address            string
	MaxConnections     int
	MaxHandlers        int
	MaxRequestBody     int
	HeaderTimeout      time.Duration
	ConnectionLifetime time.Duration
	AllowLAN           bool
}

// RecordingConfigは番組表と予約操作の14秒上限を使う録画プロセス向け設定を返す。
func RecordingConfig() Config {
	config := DefaultConfig()
	config.ConnectionLifetime = MaximumLifetime
	config.AllowLAN = true
	return config
}

// DefaultConfigはloopback限定の既定値を返す。
func DefaultConfig() Config {
	return Config{
		Address:            DefaultAddress,
		MaxConnections:     DefaultConnections,
		MaxHandlers:        DefaultHandlers,
		MaxRequestBody:     1 * 1024 * 1024,
		HeaderTimeout:      DefaultHeaderTimeout,
		ConnectionLifetime: DefaultLifetime,
	}
}

// Validateはsocket作成前にaddressと全resource上限を検証する。
func (c Config) Validate() error {
	host, port, err := net.SplitHostPort(c.Address)
	if err != nil || port == "" {
		return fmt.Errorf("ctrlcmd: listen address must include an explicit host and port")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !acceptedAddress(ip, c.AllowLAN) {
		return fmt.Errorf("ctrlcmd: listen address is outside accepted scope")
	}
	if c.MaxConnections < 1 || c.MaxConnections > DefaultConnections {
		return fmt.Errorf("ctrlcmd: max connections outside accepted limit")
	}
	if c.MaxHandlers < 1 || c.MaxHandlers > DefaultHandlers || c.MaxHandlers > c.MaxConnections {
		return fmt.Errorf("ctrlcmd: max handlers outside accepted limit")
	}
	if c.MaxRequestBody < 1 || c.MaxRequestBody > 1*1024*1024 {
		return fmt.Errorf("ctrlcmd: request body outside accepted limit")
	}
	if c.HeaderTimeout <= 0 || c.HeaderTimeout > DefaultHeaderTimeout {
		return fmt.Errorf("ctrlcmd: header timeout outside accepted limit")
	}
	if c.ConnectionLifetime <= 0 || c.ConnectionLifetime > MaximumLifetime || c.ConnectionLifetime < c.HeaderTimeout {
		return fmt.Errorf("ctrlcmd: connection lifetime outside accepted limit")
	}
	return nil
}

func validateBoundAddress(address net.Addr, allowPrivateLAN bool) error {
	tcp, ok := address.(*net.TCPAddr)
	if !ok || tcp.IP == nil {
		return fmt.Errorf("ctrlcmd: listener is outside accepted scope")
	}
	ip, ok := netip.AddrFromSlice(tcp.IP)
	if !ok || !acceptedAddress(ip.Unmap(), allowPrivateLAN) {
		return fmt.Errorf("ctrlcmd: listener is outside accepted scope")
	}
	return nil
}

func acceptedAddress(ip netip.Addr, allowPrivateLAN bool) bool {
	if !ip.IsValid() || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
		return false
	}
	if ip.IsUnspecified() {
		return allowPrivateLAN
	}
	if ip.IsLoopback() {
		return true
	}
	return allowPrivateLAN && ip.IsPrivate()
}
