//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type result struct {
	Status        string     `json:"status"`
	Code          string     `json:"code"`
	Message       string     `json:"message"`
	EmployeeName  string     `json:"employee_name,omitempty"`
	MealCode      string     `json:"meal_code,omitempty"`
	AmountCents   int64      `json:"amount_cents,omitempty"`
	TransactionID int64      `json:"transaction_id,omitempty"`
	PendingID     string     `json:"pending_id,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
}
type event struct {
	ID         int64  `json:"id"`
	ResultCode string `json:"result_code"`
	CreatedAt  string `json:"created_at"`
}
type displayState struct {
	Status         string  `json:"status"`
	Result         result  `json:"result"`
	ScannerStatus  string  `json:"scanner_status"`
	DatabaseStatus string  `json:"database_status"`
	VoiceStatus    string  `json:"voice_status"`
	Events         []event `json:"events"`
}

type agent struct {
	sync.Mutex
	sequence                           uint64
	state                              displayState
	client                             *http.Client
	internal, credential, voiceCommand string
	logger                             *slog.Logger
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("scan agent stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	device := os.Getenv("CANTEEN_SCANNER_DEVICE")
	if device == "" {
		return errors.New("CANTEEN_SCANNER_DEVICE is required")
	}
	credentialPath := os.Getenv("CANTEEN_TERMINAL_CREDENTIAL_FILE")
	if credentialPath == "" {
		return errors.New("CANTEEN_TERMINAL_CREDENTIAL_FILE is required")
	}
	secret, err := os.ReadFile(credentialPath)
	if err != nil {
		return err
	}
	credential := strings.TrimSpace(string(secret))
	if !strings.HasPrefix(credential, "trm_") {
		return errors.New("invalid credential file")
	}
	internal := env("CANTEEN_INTERNAL_URL", "http://127.0.0.1:8081")
	public := env("CANTEEN_PUBLIC_URL", "http://127.0.0.1:5001")
	publicURL, err := url.Parse(public)
	if err != nil {
		return err
	}
	a := &agent{client: &http.Client{Timeout: 4 * time.Second}, internal: strings.TrimRight(internal, "/"), credential: credential, voiceCommand: os.Getenv("CANTEEN_TTS_COMMAND"), logger: logger, state: displayState{Status: "WAITING", ScannerStatus: "OFFLINE", DatabaseStatus: "UNKNOWN", VoiceStatus: "UNAVAILABLE", Events: make([]event, 0)}}
	if a.voiceCommand != "" {
		a.state.VoiceStatus = "READY"
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.readScanner(ctx, device)
	go a.heartbeat(ctx)
	proxy := httputil.NewSingleHostReverseProxy(publicURL)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/display/state", func(w http.ResponseWriter, r *http.Request) {
		if !localRequest(w, r) {
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
			return
		}
		a.Lock()
		snapshot := a.state
		a.Unlock()
		jsonResponse(w, snapshot)
	})
	mux.HandleFunc("/api/display/confirm", func(w http.ResponseWriter, r *http.Request) {
		if !localRequest(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		var input struct {
			PendingID string `json:"pending_id"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		a.Lock()
		expected := a.state.Result.PendingID
		a.Unlock()
		if input.PendingID == "" || input.PendingID != expected {
			http.Error(w, "pending request unavailable", 409)
			return
		}
		result, err := a.send(r.Context(), "confirm", map[string]string{"pending_id": input.PendingID})
		if err != nil {
			a.setOffline()
			http.Error(w, "backend unavailable", 503)
			return
		}
		a.setResult(result)
		a.Lock()
		snapshot := a.state
		a.Unlock()
		jsonResponse(w, snapshot)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !localRequest(w, r) {
			return
		}
		proxy.ServeHTTP(w, r)
	})
	server := &http.Server{Addr: env("CANTEEN_DISPLAY_ADDR", "127.0.0.1:8090"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	logger.Info("scan agent started", "display_addr", server.Addr, "scanner_device", device)
	return server.ListenAndServe()
}

func (a *agent) readScanner(ctx context.Context, path string) {
	mode := env("CANTEEN_SCANNER_MODE", "")
	if mode == "" {
		if strings.Contains(path, "/event") {
			mode = "evdev"
		} else {
			mode = "hidraw"
		}
	}
	for ctx.Err() == nil {
		file, err := os.Open(path)
		if err != nil {
			a.setScanner("OFFLINE")
			a.logger.Warn("scanner unavailable", "error", err)
			time.Sleep(2 * time.Second)
			continue
		}
		a.setScanner("READY")
		if mode == "evdev" {
			a.readEvdev(ctx, file)
			file.Close()
			a.setScanner("OFFLINE")
			time.Sleep(time.Second)
			continue
		}
		buffer := make([]byte, 8)
		var previous [6]byte
		var token strings.Builder
		for ctx.Err() == nil {
			_, err := io.ReadFull(file, buffer)
			if err != nil {
				break
			}
			var current [6]byte
			copy(current[:], buffer[2:8])
			for _, key := range current {
				if key == 0 || contains(previous[:], key) {
					continue
				}
				if key == 40 {
					value := token.String()
					token.Reset()
					if value != "" {
						a.processScan(ctx, value)
					}
					continue
				}
				if char := hidChar(key, buffer[0]&0x22 != 0); char != 0 && token.Len() < 128 {
					token.WriteByte(char)
				}
			}
			previous = current
		}
		file.Close()
		a.setScanner("OFFLINE")
		time.Sleep(time.Second)
	}
}

func (a *agent) readEvdev(ctx context.Context, file *os.File) {
	var token strings.Builder
	shift := false
	buffer := make([]byte, 24) // Linux input_event on the supported amd64 host.
	for ctx.Err() == nil {
		if _, err := io.ReadFull(file, buffer); err != nil {
			return
		}
		if binary.LittleEndian.Uint16(buffer[16:18]) != 1 {
			continue
		} // EV_KEY
		key := binary.LittleEndian.Uint16(buffer[18:20])
		value := binary.LittleEndian.Uint32(buffer[20:24])
		if key == 42 || key == 54 {
			shift = value != 0
			continue
		}
		if value != 1 {
			continue
		}
		if key == 28 {
			value := token.String()
			token.Reset()
			if value != "" {
				a.processScan(ctx, value)
			}
			continue
		}
		if char := eventChar(key, shift); char != 0 && token.Len() < 128 {
			token.WriteByte(char)
		}
	}
}

func eventChar(key uint16, shift bool) byte {
	const letterKeys = "\x1e\x30\x2e\x20\x12\x21\x22\x23\x17\x24\x25\x26\x32\x31\x18\x19\x10\x13\x1f\x14\x16\x2f\x11\x2d\x15\x2c"
	for index := 0; index < len(letterKeys); index++ {
		if uint16(letterKeys[index]) == key {
			char := byte('a' + index)
			if shift {
				char -= 'a' - 'A'
			}
			return char
		}
	}
	if key >= 2 && key <= 10 {
		if shift {
			return "!@#$%^&*("[key-2]
		}
		return byte('1' + key - 2)
	}
	if key == 11 {
		if shift {
			return ')'
		}
		return '0'
	}
	if key == 12 {
		if shift {
			return '_'
		}
		return '-'
	}
	return 0
}

func hidChar(key byte, shift bool) byte {
	if key >= 4 && key <= 29 {
		char := byte('a' + key - 4)
		if shift {
			char -= 'a' - 'A'
		}
		return char
	}
	if key >= 30 && key <= 38 {
		if shift {
			return "!@#$%^&*("[key-30]
		}
		return byte('1' + key - 30)
	}
	if key == 39 {
		if shift {
			return ')'
		}
		return '0'
	}
	if key == 45 {
		if shift {
			return '_'
		}
		return '-'
	}
	return 0
}
func contains(keys []byte, key byte) bool {
	for _, item := range keys {
		if item == key {
			return true
		}
	}
	return false
}

func (a *agent) processScan(ctx context.Context, token string) {
	result, err := a.send(ctx, "scan", map[string]string{"token": token})
	if err != nil {
		a.logger.Warn("scan rejected because backend is unavailable", "error", err)
		a.setOffline()
		return
	}
	a.setResult(result)
}

func (a *agent) send(ctx context.Context, action string, payload any) (result, error) {
	body, _ := json.Marshal(payload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.internal+"/api/v1/terminal/"+action, bytes.NewReader(body))
	if err != nil {
		return result{}, err
	}
	request.Header.Set("Authorization", "Bearer "+a.credential)
	request.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(request)
	if err != nil {
		return result{}, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 500 || (response.StatusCode >= 400 && response.StatusCode != http.StatusConflict) {
		return result{}, fmt.Errorf("backend status %d", response.StatusCode)
	}
	var reply result
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&reply); err != nil {
		return result{}, err
	}
	return reply, nil
}

func (a *agent) heartbeat(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		a.Lock()
		scanner := a.state.ScannerStatus
		voice := a.state.VoiceStatus
		a.Unlock()
		_, err := a.send(ctx, "heartbeat", map[string]string{"scanner_status": scanner, "voice_status": voice})
		if err != nil {
			a.setOffline()
		} else {
			a.Lock()
			a.state.DatabaseStatus = "READY"
			if a.state.Status == "OFFLINE" && a.state.ScannerStatus == "READY" {
				a.state.Status = "WAITING"
				a.state.Result = result{}
			}
			a.Unlock()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *agent) setScanner(status string) {
	a.Lock()
	a.state.ScannerStatus = status
	if status == "OFFLINE" {
		a.state.Status = "OFFLINE"
	} else if a.state.Status == "OFFLINE" {
		a.state.Status = "WAITING"
	}
	a.Unlock()
}
func (a *agent) setOffline() {
	a.Lock()
	a.sequence++
	a.state.Status = "OFFLINE"
	a.state.DatabaseStatus = "OFFLINE"
	a.state.Result = result{Status: "OFFLINE", Code: "BACKEND_UNAVAILABLE", Message: "设备离线，暂停消费"}
	a.Unlock()
}
func (a *agent) setResult(value result) {
	a.Lock()
	a.sequence++
	current := a.sequence
	a.state.Status = value.Status
	a.state.Result = value
	a.state.Events = append([]event{{ResultCode: value.Code, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}}, a.state.Events...)
	if len(a.state.Events) > 20 {
		a.state.Events = a.state.Events[:20]
	}
	a.Unlock()
	delay := 5 * time.Second
	if value.Status == "PENDING" && value.ExpiresAt != nil {
		delay = time.Until(*value.ExpiresAt)
		if delay < time.Second {
			delay = time.Second
		}
	}
	if value.Status == "PENDING" || value.Status == "SUCCESS" || value.Status == "FAILED" {
		time.AfterFunc(delay, func() {
			a.Lock()
			if a.sequence == current && a.state.Status != "OFFLINE" {
				a.state.Status = "WAITING"
				a.state.Result = result{}
			}
			a.Unlock()
		})
	}
	if a.voiceCommand != "" && value.Message != "" {
		go func() {
			command := exec.Command(a.voiceCommand, value.Message)
			if err := command.Run(); err != nil {
				a.Lock()
				a.state.VoiceStatus = "FAILED"
				a.Unlock()
			}
		}()
	}
}

func localRequest(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.Host, "127.0.0.1:") && !strings.HasPrefix(r.Host, "localhost:") {
		http.Error(w, "local access only", 403)
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
		http.Error(w, "invalid origin", 403)
		return false
	}
	return true
}
func jsonResponse(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(value)
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
