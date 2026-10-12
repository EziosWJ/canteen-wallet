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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// openPTY creates a pseudoterminal pair to stand in for a scanner: the slave side
// is opened exactly as a serial port is, and the master side plays the scanner. A
// pseudoterminal has no baud rate, so these tests cover the framing and the read
// loop, not the port configuration.
func openPTY(t *testing.T) (master *os.File, slave string) {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("pseudoterminal unavailable: %v", err)
	}
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		unix.Close(fd)
		t.Fatalf("unlock pseudoterminal: %v", err)
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		unix.Close(fd)
		t.Fatalf("read pseudoterminal number: %v", err)
	}
	master = os.NewFile(uintptr(fd), "/dev/ptmx")
	t.Cleanup(func() { master.Close() })
	return master, fmt.Sprintf("/dev/pts/%d", number)
}

// scanRecorder stands in for the terminal scan endpoint. The tokens it receives
// are the observable behavior of the device layer.
type scanRecorder struct {
	tokens chan string
	server *httptest.Server
}

func newScanRecorder(t *testing.T) *scanRecorder {
	t.Helper()
	recorder := &scanRecorder{tokens: make(chan string, 16)}
	recorder.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode scan request: %v", err)
		}
		select {
		case recorder.tokens <- payload.Token:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"SUCCESS","code":"CONSUMED"}`))
	}))
	t.Cleanup(recorder.server.Close)
	return recorder
}

// next returns the next submitted token. The deadline covers the reconnect
// delays, so it is only reached when a frame was genuinely lost.
func (r *scanRecorder) next(t *testing.T) string {
	t.Helper()
	select {
	case token := <-r.tokens:
		return token
	case <-time.After(15 * time.Second):
		t.Fatal("no scan request reached the backend")
		return ""
	}
}

func testSerialConfig(device string) agentConfig {
	return agentConfig{device: device, baud: 115200, terminator: terminatorAny, idle: 20 * time.Millisecond}
}

// startSerialAgent runs the scanner loop in the background and stops it when the
// test ends.
func startSerialAgent(t *testing.T, recorder *scanRecorder, cfg agentConfig) *agent {
	t.Helper()
	a := &agent{client: recorder.server.Client(), internal: recorder.server.URL, credential: "trm_test", logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.readSerial(ctx, cfg)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("scanner loop did not stop")
		}
	})
	return a
}

func writeScanner(t *testing.T, master *os.File, payload string) {
	t.Helper()
	if _, err := master.WriteString(payload); err != nil {
		t.Fatalf("write to scanner: %v", err)
	}
}

func TestSerialPortSubmitsTerminatedFrame(t *testing.T) {
	recorder := newScanRecorder(t)
	master, slave := openPTY(t)
	a := startSerialAgent(t, recorder, testSerialConfig(slave))
	waitForScannerStatus(t, a, "READY")
	writeScanner(t, master, "pmt_AAAA\r\n")
	if token := recorder.next(t); token != "pmt_AAAA" {
		t.Fatalf("submitted %q, want %q", token, "pmt_AAAA")
	}
}

func TestSerialPortIdleEndsFrameWithoutTerminator(t *testing.T) {
	recorder := newScanRecorder(t)
	master, slave := openPTY(t)
	startSerialAgent(t, recorder, testSerialConfig(slave))
	writeScanner(t, master, "pmt_BBBB")
	if token := recorder.next(t); token != "pmt_BBBB" {
		t.Fatalf("submitted %q, want %q", token, "pmt_BBBB")
	}
}

// A baud rate mismatch produces unprintable bytes. The frame is dropped, and the
// next valid frame must still arrive, which proves the drop rather than showing
// the garbage only later.
func TestSerialPortDropsUnprintableFrame(t *testing.T) {
	recorder := newScanRecorder(t)
	master, slave := openPTY(t)
	startSerialAgent(t, recorder, testSerialConfig(slave))
	writeScanner(t, master, "pmt_\xff\xfe\r")
	writeScanner(t, master, "pmt_GOOD\r")
	if token := recorder.next(t); token != "pmt_GOOD" {
		t.Fatalf("garbage was submitted first: %q", token)
	}
}

func TestSerialPortDropsOversizedFrame(t *testing.T) {
	recorder := newScanRecorder(t)
	master, slave := openPTY(t)
	startSerialAgent(t, recorder, testSerialConfig(slave))
	writeScanner(t, master, "pmt_"+strings.Repeat("A", 200)+"\r")
	writeScanner(t, master, "pmt_OK\r")
	if token := recorder.next(t); token != "pmt_OK" {
		t.Fatalf("oversized frame was submitted first: %q", token)
	}
}

// A scanner configured to pad short codes with NUL must keep working.
func TestSerialPortKeepsNulPaddedFrame(t *testing.T) {
	recorder := newScanRecorder(t)
	master, slave := openPTY(t)
	startSerialAgent(t, recorder, testSerialConfig(slave))
	writeScanner(t, master, "pmt_PADDED\x00\x00\x00\x00\r")
	if token := recorder.next(t); token != "pmt_PADDED" {
		t.Fatalf("submitted %q, want %q", token, "pmt_PADDED")
	}
}

// Unplugging a scanner destroys its device path, so the test points the
// configured path at the scanner through a symlink and repoints it, which is what
// a physical unplug and replug amounts to. Recovery is the behavior the field
// drill depends on.
func TestSerialPortRecoversAfterScannerReconnects(t *testing.T) {
	recorder := newScanRecorder(t)
	first, firstSlave := openPTY(t)
	port := filepath.Join(t.TempDir(), "scanner")
	if err := os.Symlink(firstSlave, port); err != nil {
		t.Fatal(err)
	}
	a := startSerialAgent(t, recorder, testSerialConfig(port))

	writeScanner(t, first, "pmt_FIRST\r")
	if token := recorder.next(t); token != "pmt_FIRST" {
		t.Fatalf("submitted %q, want %q", token, "pmt_FIRST")
	}
	waitForScannerStatus(t, a, "READY")

	// Unplug: closing the master destroys the pseudoterminal behind the path.
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	waitForScannerStatus(t, a, "OFFLINE")

	second, secondSlave := openPTY(t)
	if err := os.Remove(port); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secondSlave, port); err != nil {
		t.Fatal(err)
	}
	writeScanner(t, second, "pmt_SECOND\r")
	if token := recorder.next(t); token != "pmt_SECOND" {
		t.Fatalf("submitted %q after reconnecting, want %q", token, "pmt_SECOND")
	}
	waitForScannerStatus(t, a, "READY")
}

func scannerStatus(a *agent) string {
	a.Lock()
	defer a.Unlock()
	return a.state.ScannerStatus
}

func waitForScannerStatus(t *testing.T, a *agent, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if scannerStatus(a) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("scanner status is %s, want %s", scannerStatus(a), want)
}
