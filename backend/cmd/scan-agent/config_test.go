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
)

func clearAgentEnvironment(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"CANTEEN_SERVER_URL", "CANTEEN_INTERNAL_URL", "CANTEEN_PUBLIC_URL", "CANTEEN_TERMINAL_KEY", "CANTEEN_TERMINAL_CREDENTIAL_FILE", "CANTEEN_SCANNER_DEVICE", "CANTEEN_SCANNER_MODE", "CANTEEN_SCANNER_BAUD"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CANTEEN_SCANNER_DEVICE", "/dev/input/event-test")
	return "trm_" + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
}

func TestInvalidScannerConfigurationFailsAtStartup(t *testing.T) {
	key := clearAgentEnvironment(t)
	t.Setenv("CANTEEN_TERMINAL_KEY", key)
	t.Setenv("CANTEEN_SCANNER_DEVICE", "/dev/ttyACM0")
	for _, tc := range []struct{ mode, baud string }{{"unknown", "9600"}, {"serial", "invalid"}, {"serial", "0"}} {
		t.Setenv("CANTEEN_SCANNER_MODE", tc.mode)
		t.Setenv("CANTEEN_SCANNER_BAUD", tc.baud)
		if _, err := loadConfig(); err == nil {
			t.Fatal("invalid scanner configuration was silently accepted")
		}
	}
}

func TestScannerModeInferenceAndExplicitStableAlias(t *testing.T) {
	key := clearAgentEnvironment(t)
	t.Setenv("CANTEEN_TERMINAL_KEY", key)
	for _, tc := range []struct{ device, mode, want string }{
		{"/dev/ttyACM0", "", "serial"},
		{"/dev/ttyUSB1", "", "serial"},
		{"/dev/ttyS0", "", "serial"},
		{"/dev/input/event7", "", "evdev"},
		{"/dev/hidraw2", "", "hidraw"},
		{"/dev/canteen-scanner", "serial", "serial"},
	} {
		t.Setenv("CANTEEN_SCANNER_DEVICE", tc.device)
		t.Setenv("CANTEEN_SCANNER_MODE", tc.mode)
		cfg, err := loadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.scanner.Mode != tc.want || cfg.scanner.Device != tc.device || cfg.scanner.Baud != 9600 {
			t.Fatalf("unexpected scanner configuration: %+v", cfg.scanner)
		}
	}
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
