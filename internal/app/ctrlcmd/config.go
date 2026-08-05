package ctrlcmd

import (
	"fmt"
	"net"
	"time"
)

const (
	DefaultAddress       = "127.0.0.1:4510"
	DefaultConnections   = 32
	DefaultHandlers      = 16
	DefaultHeaderTimeout = 2 * time.Second
	DefaultLifetime      = 4 * time.Second
	MaximumLifetime      = 14 * time.Second
)

// ConfigはCtrlCmd listenerと1接続のresource上限をまとめる。
type Config struct {
	Address            string
	MaxConnections     int
	MaxHandlers        int
	MaxRequestBody     int
	HeaderTimeout      time.Duration
	ConnectionLifetime time.Duration
}

// RecordingConfigは番組表と予約操作の14秒上限を使う録画プロセス向け設定を返す。
func RecordingConfig() Config {
	config := DefaultConfig()
	config.ConnectionLifetime = MaximumLifetime
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
	ip := net.ParseIP(host)
	if ip == nil || !(ip.Equal(net.IPv4(127, 0, 0, 1)) || ip.Equal(net.IPv6loopback)) {
		return fmt.Errorf("ctrlcmd: non-loopback listen address is forbidden")
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

func validateBoundAddress(address net.Addr) error {
	tcp, ok := address.(*net.TCPAddr)
	if !ok || tcp.IP == nil || !(tcp.IP.Equal(net.IPv4(127, 0, 0, 1)) || tcp.IP.Equal(net.IPv6loopback)) {
		return fmt.Errorf("ctrlcmd: listener is not bound to an explicit loopback address")
	}
	return nil
}
