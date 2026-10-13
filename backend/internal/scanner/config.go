package scanner

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Device string
	Mode   string
	Baud   int
}

// Parse is shared by configuration downloads and the Linux Scan Agent.
func Parse(device, mode, baud string) (Config, error) {
	cfg := Config{Device: device, Mode: mode, Baud: 9600}
	if !strings.HasPrefix(device, "/dev/") || strings.ContainsAny(device, "\x00\r\n") || len(device) > 1024 {
		return cfg, errors.New("扫码设备路径必须是 /dev/ 下的绝对路径，且不能包含换行。")
	}
	if cfg.Mode == "" {
		name := filepath.Base(device)
		switch {
		case strings.HasPrefix(name, "ttyACM"), strings.HasPrefix(name, "ttyUSB"), strings.HasPrefix(name, "ttyS"):
			cfg.Mode = "serial"
		case strings.Contains(device, "/event"):
			cfg.Mode = "evdev"
		default:
			cfg.Mode = "hidraw"
		}
	}
	switch cfg.Mode {
	case "serial":
		if baud != "" {
			var err error
			cfg.Baud, err = strconv.Atoi(baud)
			if err != nil {
				return cfg, errors.New("串口波特率必须是整数。")
			}
		}
		switch cfg.Baud {
		case 1200, 2400, 4800, 9600, 19200, 38400, 57600, 115200, 230400:
		default:
			return cfg, errors.New("不支持该串口波特率。")
		}
	case "evdev", "hidraw":
	default:
		return cfg, errors.New("扫码设备模式必须是 serial、evdev 或 hidraw。")
	}
	return cfg, nil
}
