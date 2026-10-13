//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func testSerialPort(t *testing.T) (*os.File, string) {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	master := os.NewFile(uintptr(fd), "/dev/ptmx")
	t.Cleanup(func() { master.Close() })
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	return master, fmt.Sprintf("/dev/pts/%d", number)
}

func TestSerialScannerSubmitsDelimitedTokensAndStopsOnCancellation(t *testing.T) {
	key := clearAgentEnvironment(t)
	master, path := testSerialPort(t)
	t.Setenv("CANTEEN_TERMINAL_KEY", key)
	t.Setenv("CANTEEN_SCANNER_DEVICE", path)
	t.Setenv("CANTEEN_SCANNER_MODE", "serial")
	t.Setenv("CANTEEN_SCANNER_BAUD", "115200")
	tokens := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/terminal/scan" || r.Header.Get("Authorization") != "Bearer "+key {
			t.Error("unexpected serial scan request")
		}
		var payload struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		tokens <- payload.Token
		w.Write([]byte(`{"status":"FAILED","code":"INVALID_TOKEN","message":"test"}`))
	}))
	defer server.Close()
	t.Setenv("CANTEEN_SERVER_URL", server.URL)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	a := &agent{client: server.Client(), internal: cfg.internal, credential: cfg.credential, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { a.readScanner(ctx, cfg.scanner); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		a.Lock()
		ready := a.state.ScannerStatus == "READY"
		a.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("serial scanner did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Fragmented input, CR/LF/CRLF, empty records, and an oversized record.
	for _, part := range []string{"token_", "one\r\n\nsecond-token\nthird_token\r", strings.Repeat("x", 129) + "\rfourth-token\r\n"} {
		if _, err := io.WriteString(master, part); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"token_one", "second-token", "third_token", "fourth-token"} {
		select {
		case got := <-tokens:
			if got != want {
				t.Fatalf("serial scan token = %q, want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("serial input was not submitted as a scan")
		}
	}
	port, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer port.Close()
	attrs, err := unix.IoctlGetTermios(int(port.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if attrs.Cflag&unix.CBAUD != unix.B115200 || attrs.Cflag&unix.CSIZE != unix.CS8 || attrs.Cflag&(unix.PARENB|unix.CSTOPB|unix.CRTSCTS) != 0 || attrs.Lflag&(unix.ICANON|unix.ECHO) != 0 {
		t.Fatal("serial settings are not raw 115200 8N1 without flow control")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serial reader did not stop when canceled")
	}
	select {
	case <-tokens:
		t.Fatal("serial scanner submitted an empty or oversized record")
	default:
	}
}
