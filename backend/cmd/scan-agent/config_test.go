//go:build linux

package main

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func clearAgentEnvironment(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"CANTEEN_SERVER_URL", "CANTEEN_INTERNAL_URL", "CANTEEN_PUBLIC_URL", "CANTEEN_TERMINAL_KEY", "CANTEEN_TERMINAL_CREDENTIAL_FILE", "CANTEEN_SCANNER_DEVICE", "CANTEEN_SCANNER_MODE", "CANTEEN_SCANNER_BAUD", "CANTEEN_SCANNER_TERMINATOR", "CANTEEN_SCANNER_IDLE_MS"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CANTEEN_SCANNER_DEVICE", "/dev/ttyUSB0")
	return "trm_" + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
}

func TestUnifiedConfigurationSendsAuthenticatedScanOverHTTPAndHTTPS(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(map[bool]string{false: "http", true: "https"}[secure], func(t *testing.T) {
			key := clearAgentEnvironment(t)
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/canteen/api/v1/terminal/scan" || r.Header.Get("Authorization") != "Bearer "+key || r.Method != http.MethodPost {
					t.Error("unexpected scan endpoint or authentication")
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"status":"SUCCESS","code":"CONSUMED"}`))
			})
			var server *httptest.Server
			if secure {
				server = httptest.NewTLSServer(handler)
			} else {
				server = httptest.NewServer(handler)
			}
			defer server.Close()
			t.Setenv("CANTEEN_SERVER_URL", server.URL+"/canteen/")
			t.Setenv("CANTEEN_TERMINAL_KEY", key)
			t.Setenv("CANTEEN_INTERNAL_URL", "invalid-old-address")
			t.Setenv("CANTEEN_PUBLIC_URL", "invalid-old-address")
			t.Setenv("CANTEEN_TERMINAL_CREDENTIAL_FILE", "/missing/old-file")
			cfg, err := loadConfig()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.public != cfg.internal {
				t.Fatal("unified address did not configure the web proxy")
			}
			a := agent{client: server.Client(), internal: cfg.internal, credential: cfg.credential}
			reply, err := a.send(context.Background(), "scan", map[string]string{"token": "test-token"})
			if err != nil || reply.Status != "SUCCESS" {
				t.Fatalf("scan failed: status=%s error=%v", reply.Status, err)
			}
		})
	}
}

func TestLegacyCredentialFileAndSeparateAddressesStillWork(t *testing.T) {
	key := clearAgentEnvironment(t)
	path := filepath.Join(t.TempDir(), "terminal.credential")
	if err := os.WriteFile(path, []byte(key+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANTEEN_TERMINAL_CREDENTIAL_FILE", path)
	t.Setenv("CANTEEN_INTERNAL_URL", "http://192.168.1.10:8081")
	t.Setenv("CANTEEN_PUBLIC_URL", "https://canteen.example.com")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.credential != key || cfg.internal != "http://192.168.1.10:8081" || cfg.public != "https://canteen.example.com" {
		t.Fatal("legacy configuration changed")
	}
}

func TestInvalidAgentConfigurationFailsWithoutDisclosingSecrets(t *testing.T) {
	key := clearAgentEnvironment(t)
	t.Setenv("CANTEEN_TERMINAL_KEY", key)
	for _, base := range []string{"", "ftp://host", "http://secret:password@host", "http://host?key=secret", "http://host/#fragment", "http://host/\n"} {
		t.Setenv("CANTEEN_SERVER_URL", base)
		_, err := loadConfig()
		if err == nil {
			t.Fatal("accepted invalid base address")
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), key) {
			t.Fatal("configuration error disclosed a secret")
		}
	}
	t.Setenv("CANTEEN_SERVER_URL", "http://localhost:5001")
	for _, badKey := range []string{"", "trm_bad-secret", "trm_" + strings.Repeat("!", 43)} {
		t.Setenv("CANTEEN_TERMINAL_KEY", badKey)
		_, err := loadConfig()
		if err == nil || strings.Contains(err.Error(), "bad-secret") {
			t.Fatal("invalid key was accepted or disclosed")
		}
	}
}

func TestScannerSerialParametersDefaultToTheContractedValues(t *testing.T) {
	key := clearAgentEnvironment(t)
	t.Setenv("CANTEEN_TERMINAL_KEY", key)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.device != "/dev/ttyUSB0" || cfg.baud != 115200 || cfg.terminator != terminatorAny || cfg.idle != 50*time.Millisecond {
		t.Fatalf("defaults changed: device=%s baud=%d terminator=%s idle=%s", cfg.device, cfg.baud, cfg.terminator, cfg.idle)
	}
}

func TestScannerSerialParametersAreConfigurable(t *testing.T) {
	key := clearAgentEnvironment(t)
	t.Setenv("CANTEEN_TERMINAL_KEY", key)
	t.Setenv("CANTEEN_SCANNER_BAUD", "9600")
	t.Setenv("CANTEEN_SCANNER_TERMINATOR", "cr")
	t.Setenv("CANTEEN_SCANNER_IDLE_MS", "120")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.baud != 9600 || cfg.terminator != terminatorCR || cfg.idle != 120*time.Millisecond {
		t.Fatalf("configured values ignored: baud=%d terminator=%s idle=%s", cfg.baud, cfg.terminator, cfg.idle)
	}
}

// A wrong baud rate shows up as mojibake rather than as an error, so an unusable
// value has to fail at startup instead of silently falling back to a default.
func TestInvalidScannerSerialParametersFailAtStartup(t *testing.T) {
	key := clearAgentEnvironment(t)
	t.Setenv("CANTEEN_TERMINAL_KEY", key)
	cases := []struct{ variable, value string }{
		{"CANTEEN_SCANNER_BAUD", "4800"},
		{"CANTEEN_SCANNER_BAUD", "1152000"},
		{"CANTEEN_SCANNER_BAUD", "fast"},
		{"CANTEEN_SCANNER_TERMINATOR", "crlf"},
		{"CANTEEN_SCANNER_TERMINATOR", "tab"},
		{"CANTEEN_SCANNER_IDLE_MS", "9"},
		{"CANTEEN_SCANNER_IDLE_MS", "501"},
		{"CANTEEN_SCANNER_IDLE_MS", "soon"},
	}
	for _, item := range cases {
		t.Run(item.variable+"="+item.value, func(t *testing.T) {
			t.Setenv(item.variable, item.value)
			_, err := loadConfig()
			if err == nil {
				t.Fatalf("accepted %s=%s", item.variable, item.value)
			}
			if !strings.Contains(err.Error(), item.variable) {
				t.Fatalf("error does not name %s: %v", item.variable, err)
			}
		})
	}
}

// The removed device modes must end in a clear notice rather than in a puzzling
// serial port error.
func TestRemovedScannerModeVariableFailsWithNotice(t *testing.T) {
	key := clearAgentEnvironment(t)
	t.Setenv("CANTEEN_TERMINAL_KEY", key)
	for _, mode := range []string{"evdev", "hidraw"} {
		t.Setenv("CANTEEN_SCANNER_MODE", mode)
		_, err := loadConfig()
		if err == nil {
			t.Fatalf("accepted the removed scanner mode %q", mode)
		}
		if !strings.Contains(err.Error(), "CANTEEN_SCANNER_MODE") || !strings.Contains(err.Error(), "removed") {
			t.Fatalf("removal notice is unclear: %v", err)
		}
	}
	// An empty value carries no mode and must not block startup.
	t.Setenv("CANTEEN_SCANNER_MODE", "")
	if _, err := loadConfig(); err != nil {
		t.Fatalf("empty scanner mode blocked startup: %v", err)
	}
}
