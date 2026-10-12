//go:build linux

package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// scannerBaudRates are the baud rates the agent accepts. A serial mismatch shows
// up as mojibake rather than an error, so an unknown value fails at startup
// instead of silently falling back to a default.
var scannerBaudRates = []int{9600, 19200, 38400, 57600, 115200, 230400}

var scannerTerminators = map[string]terminator{
	"any":  terminatorAny,
	"cr":   terminatorCR,
	"lf":   terminatorLF,
	"none": terminatorNone,
}

type agentConfig struct {
	device, credential, internal, public string
	baud                                 int
	terminator                           terminator
	idle                                 time.Duration
}

func loadConfig() (agentConfig, error) {
	cfg := agentConfig{device: os.Getenv("CANTEEN_SCANNER_DEVICE")}
	if cfg.device == "" {
		return cfg, errors.New("CANTEEN_SCANNER_DEVICE is required")
	}
	// The scanner is read over a serial port only. An empty value carries no
	// mode, but a value means the configuration still expects the removed device
	// modes, which would otherwise end in a puzzling serial port error.
	if strings.TrimSpace(os.Getenv("CANTEEN_SCANNER_MODE")) != "" {
		return cfg, errors.New("CANTEEN_SCANNER_MODE has been removed: the agent reads scanners over a serial port only; delete that line and point CANTEEN_SCANNER_DEVICE at the serial port")
	}
	if err := applyScannerSettings(&cfg); err != nil {
		return cfg, err
	}
	if key, present := os.LookupEnv("CANTEEN_TERMINAL_KEY"); present {
		cfg.credential = strings.TrimSpace(key)
	} else {
		path := os.Getenv("CANTEEN_TERMINAL_CREDENTIAL_FILE")
		if path == "" {
			return cfg, errors.New("CANTEEN_TERMINAL_KEY or CANTEEN_TERMINAL_CREDENTIAL_FILE is required")
		}
		key, err := os.ReadFile(path)
		if err != nil {
			return cfg, errors.New("cannot read terminal credential file")
		}
		cfg.credential = strings.TrimSpace(string(key))
	}
	if len(cfg.credential) != 47 || !strings.HasPrefix(cfg.credential, "trm_") {
		return cfg, errors.New("invalid terminal key")
	}
	raw, err := base64.RawURLEncoding.DecodeString(cfg.credential[4:])
	if err != nil || len(raw) != 32 {
		return cfg, errors.New("invalid terminal key")
	}
	if base, present := os.LookupEnv("CANTEEN_SERVER_URL"); present {
		cfg.internal, cfg.public = base, base
	} else {
		cfg.internal = env("CANTEEN_INTERNAL_URL", "http://127.0.0.1:8081")
		cfg.public = env("CANTEEN_PUBLIC_URL", "http://127.0.0.1:5001")
	}
	for _, base := range []string{cfg.internal, cfg.public} {
		parsed, err := url.Parse(base)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || strings.ContainsAny(base, "\r\n") {
			return cfg, errors.New("server address must be an HTTP or HTTPS base URL without credentials, query or fragment")
		}
	}
	cfg.internal = strings.TrimRight(cfg.internal, "/")
	cfg.public = strings.TrimRight(cfg.public, "/")
	return cfg, nil
}

// applyScannerSettings reads the serial port parameters. Data bits, parity and
// stop bits stay fixed at 8N1, which every scanner interface in use supports.
func applyScannerSettings(cfg *agentConfig) error {
	baud, err := strconv.Atoi(env("CANTEEN_SCANNER_BAUD", "115200"))
	if err != nil || !scannerBaudAllowed(baud) {
		return fmt.Errorf("CANTEEN_SCANNER_BAUD must be one of %s", scannerBaudList())
	}
	cfg.baud = baud
	terminator, present := scannerTerminators[env("CANTEEN_SCANNER_TERMINATOR", "any")]
	if !present {
		return errors.New("CANTEEN_SCANNER_TERMINATOR must be one of any, cr, lf, none")
	}
	cfg.terminator = terminator
	idle, err := strconv.Atoi(env("CANTEEN_SCANNER_IDLE_MS", "50"))
	if err != nil || idle < 10 || idle > 500 {
		return errors.New("CANTEEN_SCANNER_IDLE_MS must be between 10 and 500")
	}
	cfg.idle = time.Duration(idle) * time.Millisecond
	return nil
}

func scannerBaudAllowed(value int) bool {
	for _, allowed := range scannerBaudRates {
		if allowed == value {
			return true
		}
	}
	return false
}

func scannerBaudList() string {
	values := make([]string, len(scannerBaudRates))
	for index, baud := range scannerBaudRates {
		values[index] = strconv.Itoa(baud)
	}
	return strings.Join(values, ", ")
}
