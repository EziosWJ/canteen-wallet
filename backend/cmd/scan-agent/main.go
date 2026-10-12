//go:build linux

package main

import (
	"bytes"
	"context"
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
	Status           string     `json:"status"`
	Code             string     `json:"code"`
	Message          string     `json:"message"`
	EmployeeName     string     `json:"employee_name,omitempty"`
	MealCode         string     `json:"meal_code,omitempty"`
	MealName         string     `json:"meal_name,omitempty"`
	AmountCents      int64      `json:"amount_cents,omitempty"`
	TransactionID    int64      `json:"transaction_id,omitempty"`
	TransactionNo    string     `json:"transaction_no,omitempty"`
	ConsumptionNo    string     `json:"consumption_no,omitempty"`
	OccurredAt       string     `json:"occurred_at,omitempty"`
	PendingID        string     `json:"pending_id,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	Replayed         bool       `json:"replayed,omitempty"`
	DuplicateTrigger bool       `json:"duplicate_trigger,omitempty"`
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
	watching                           map[string]bool
	pendingDisplayID                   string
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("scan agent stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	publicURL, _ := url.Parse(cfg.public) // Validated by loadConfig.
	a := &agent{client: &http.Client{Timeout: 4 * time.Second}, internal: cfg.internal, credential: cfg.credential, voiceCommand: os.Getenv("CANTEEN_TTS_COMMAND"), logger: logger, watching: make(map[string]bool), state: displayState{Status: "WAITING", ScannerStatus: "OFFLINE", DatabaseStatus: "UNKNOWN", VoiceStatus: "UNAVAILABLE", Events: make([]event, 0)}}
	if a.voiceCommand != "" {
		a.state.VoiceStatus = "READY"
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.readSerial(ctx, cfg)
	go a.heartbeat(ctx)
	go a.resumePending(ctx)
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
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !localRequest(w, r) {
			return
		}
		proxy.ServeHTTP(w, r)
	})
	server := &http.Server{Addr: env("CANTEEN_DISPLAY_ADDR", "127.0.0.1:8090"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	logger.Info("scan agent started", "display_addr", server.Addr, "scanner_device", cfg.device, "scanner_baud", cfg.baud, "scanner_terminator", cfg.terminator.String(), "scanner_idle_ms", cfg.idle.Milliseconds())
	return server.ListenAndServe()
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

func (a *agent) getPending(ctx context.Context, id string) (result, error) {
	endpoint := a.internal + "/api/v1/terminal/pending"
	if id != "" {
		endpoint += "/" + url.PathEscape(id)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return result{}, err
	}
	request.Header.Set("Authorization", "Bearer "+a.credential)
	response, err := a.client.Do(request)
	if err != nil {
		return result{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return result{}, os.ErrNotExist
	}
	if response.StatusCode != http.StatusOK {
		return result{}, fmt.Errorf("backend status %d", response.StatusCode)
	}
	var reply result
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&reply); err != nil {
		return result{}, err
	}
	return reply, nil
}

func (a *agent) resumePending(ctx context.Context) {
	for ctx.Err() == nil {
		values, err := a.getRecentPending(ctx)
		if err == nil {
			for _, value := range values {
				if value.Status == "PENDING" && value.PendingID != "" {
					go a.watchPending(ctx, value.PendingID)
				}
			}
			if len(values) > 0 {
				a.Lock()
				waiting := a.state.Status == "WAITING"
				a.Unlock()
				if waiting {
					value := values[0]
					value.Replayed = true
					a.setResult(value)
				}
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (a *agent) getRecentPending(ctx context.Context) ([]result, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.internal+"/api/v1/terminal/pending/recent", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+a.credential)
	response, err := a.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("backend status %d", response.StatusCode)
	}
	var reply struct {
		Items []result `json:"items"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&reply); err != nil {
		return nil, err
	}
	return reply.Items, nil
}

func (a *agent) watchPending(ctx context.Context, id string) {
	a.Lock()
	if a.watching[id] {
		a.Unlock()
		return
	}
	a.watching[id] = true
	a.Unlock()
	defer func() { a.Lock(); delete(a.watching, id); a.Unlock() }()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		value, err := a.getPending(ctx, id)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				a.setOffline()
				continue
			}
			return
		}
		if value.Status != "PENDING" {
			a.Lock()
			current := a.pendingDisplayID == id
			if !current {
				a.state.Events = append([]event{{ResultCode: value.Code, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}}, a.state.Events...)
				if len(a.state.Events) > 20 {
					a.state.Events = a.state.Events[:20]
				}
			}
			a.Unlock()
			if current {
				a.setResult(value)
			}
			return
		}
	}
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
	if value.Status == "PENDING" {
		a.pendingDisplayID = value.PendingID
	} else {
		a.pendingDisplayID = ""
	}
	a.state.Status = value.Status
	a.state.Result = value
	a.state.Events = append([]event{{ResultCode: value.Code, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}}, a.state.Events...)
	if len(a.state.Events) > 20 {
		a.state.Events = a.state.Events[:20]
	}
	a.Unlock()
	if value.Status == "PENDING" && value.PendingID != "" {
		go a.watchPending(context.Background(), value.PendingID)
	}
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
	if a.voiceCommand != "" && value.Message != "" && !value.Replayed && !value.DuplicateTrigger {
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
