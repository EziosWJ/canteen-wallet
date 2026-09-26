package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	PublicAddr   string
	InternalAddr string
	DatabasePath string
	TimeZone     string
}

func Load() (Config, error) {
	cfg := Config{
		PublicAddr:   value("CANTEEN_PUBLIC_ADDR", "127.0.0.1:8080"),
		InternalAddr: value("CANTEEN_INTERNAL_ADDR", "127.0.0.1:8081"),
		DatabasePath: value("CANTEEN_DB_PATH", "data/canteen.db"),
		TimeZone:     value("CANTEEN_TIME_ZONE", "Asia/Shanghai"),
	}
	if err := validateAddr("CANTEEN_PUBLIC_ADDR", cfg.PublicAddr); err != nil {
		return Config{}, err
	}
	if err := validateAddr("CANTEEN_INTERNAL_ADDR", cfg.InternalAddr); err != nil {
		return Config{}, err
	}
	internalHost, _, _ := net.SplitHostPort(cfg.InternalAddr)
	if !isInternalHost(internalHost) {
		return Config{}, fmt.Errorf("CANTEEN_INTERNAL_ADDR must use a loopback or private IP address")
	}
	if cfg.PublicAddr == cfg.InternalAddr {
		return Config{}, fmt.Errorf("public and internal listeners must use different addresses")
	}
	if strings.TrimSpace(cfg.DatabasePath) == "" {
		return Config{}, fmt.Errorf("CANTEEN_DB_PATH must not be empty")
	}
	if _, err := time.LoadLocation(cfg.TimeZone); err != nil {
		return Config{}, fmt.Errorf("CANTEEN_TIME_ZONE: %w", err)
	}
	return cfg, nil
}

func value(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func validateAddr(key, addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("%s must be a host:port address", key)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("%s must use a port between 1 and 65535", key)
	}
	return nil
}

func isInternalHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}
