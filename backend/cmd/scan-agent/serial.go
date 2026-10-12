//go:build linux

package main

import (
	"context"
	"strings"
	"time"

	"go.bug.st/serial"
)

const (
	// scannerReadBuffer is larger than frameLimit so an oversized frame is
	// detected by the framer rather than silently split by the read size.
	scannerReadBuffer = 256
	openRetryDelay    = 2 * time.Second
	readRetryDelay    = time.Second
)

// readSerial reads barcode tokens from a serial scanner until the context is
// cancelled. Every failure reopens the port, so unplugging and replugging the
// scanner recovers without restarting the agent.
func (a *agent) readSerial(ctx context.Context, cfg agentConfig) {
	mode := &serial.Mode{BaudRate: cfg.baud, DataBits: 8, Parity: serial.NoParity, StopBits: serial.OneStopBit}
	lastFailure := ""
	for ctx.Err() == nil {
		port, err := serial.Open(cfg.device, mode)
		if err != nil {
			a.setScanner("OFFLINE")
			lastFailure = a.reportPortFailure(lastFailure, cfg, err)
			sleepContext(ctx, openRetryDelay)
			continue
		}
		lastFailure = ""
		a.setScanner("READY")
		a.logger.Info("scanner port open", "device", cfg.device, "baud", cfg.baud, "terminator", cfg.terminator.String(), "idle_ms", cfg.idle.Milliseconds())
		a.readFrames(ctx, port, cfg)
		port.Close()
		a.setScanner("OFFLINE")
		sleepContext(ctx, readRetryDelay)
	}
}

// readFrames reads frames until the port fails or the context is cancelled. A
// read returning no bytes means the port stayed silent for the whole idle
// window, which is what ends the frame in progress.
func (a *agent) readFrames(ctx context.Context, port serial.Port, cfg agentConfig) {
	if err := port.SetReadTimeout(cfg.idle); err != nil {
		a.logger.Warn("cannot set scanner read timeout", "device", cfg.device, "error", err)
		return
	}
	frames := framer{terminator: cfg.terminator}
	buffer := make([]byte, scannerReadBuffer)
	hinted := false
	for ctx.Err() == nil {
		count, err := port.Read(buffer)
		if err != nil {
			a.logger.Warn("scanner read failed", "device", cfg.device, "error", err)
			return
		}
		var tokens []string
		var discards []discard
		if count == 0 {
			tokens, discards = frames.idle()
		} else {
			tokens, discards = frames.write(buffer[:count])
		}
		if len(discards) > 0 {
			hinted = a.reportDiscards(cfg.device, discards, hinted)
		}
		for _, token := range tokens {
			a.processScan(ctx, token)
		}
	}
}

// reportDiscards logs frames the framer refused. The hint is emitted once per
// connection: a baud rate mismatch discards every frame, and repeating the same
// advice on each one would drown the log.
func (a *agent) reportDiscards(device string, discards []discard, hinted bool) bool {
	for _, item := range discards {
		attributes := []any{"device", device, "bytes", item.bytes, "reason", item.reason}
		if !hinted {
			attributes = append(attributes, "hint", "check CANTEEN_SCANNER_BAUD and the scanner's own suffix setting against CANTEEN_SCANNER_TERMINATOR")
			hinted = true
		}
		a.logger.Warn("discarded scanner frame", attributes...)
	}
	return hinted
}

// reportPortFailure logs an unavailable port at warn level the first time and at
// debug level while the same failure repeats, so a scanner left unplugged does
// not fill the log. Candidate ports are listed once, for the same reason.
func (a *agent) reportPortFailure(previous string, cfg agentConfig, err error) string {
	current := err.Error()
	if current == previous {
		a.logger.Debug("scanner port unavailable", "device", cfg.device, "error", err)
		return previous
	}
	a.logger.Warn("scanner port unavailable", "device", cfg.device, "error", err)
	if ports := candidatePorts(cfg.device); len(ports) > 0 {
		a.logger.Warn("serial ports found on this machine", "ports", strings.Join(ports, ", "))
	}
	return current
}

// candidatePorts lists serial ports other than the configured one. Enumeration
// is best effort: a failure here must never replace or obscure the real error.
func candidatePorts(device string) []string {
	ports, err := serial.GetPortsList()
	if err != nil {
		return nil
	}
	found := make([]string, 0, len(ports))
	for _, port := range ports {
		if port != device {
			found = append(found, port)
		}
	}
	return found
}

func sleepContext(ctx context.Context, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
