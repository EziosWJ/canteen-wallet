//go:build linux

package main

import (
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"strings"
)

type agentConfig struct {
	device, credential, internal, public string
}

func loadConfig() (agentConfig, error) {
	cfg := agentConfig{device: os.Getenv("CANTEEN_SCANNER_DEVICE")}
	if cfg.device == "" {
		return cfg, errors.New("CANTEEN_SCANNER_DEVICE is required")
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
