//go:build linux

package main

import (
	"context"
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func openSerial(path string, baud int) (*os.File, error) {
	speed, ok := map[int]uint32{
		1200: unix.B1200, 2400: unix.B2400, 4800: unix.B4800, 9600: unix.B9600,
		19200: unix.B19200, 38400: unix.B38400, 57600: unix.B57600,
		115200: unix.B115200, 230400: unix.B230400,
	}[baud]
	if !ok {
		return nil, errors.New("unsupported serial baud rate")
	}
	// Nonblocking open avoids waiting for modem carrier; os.File uses the Go
	// poller for reads, so closing the file can interrupt an idle scanner.
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	attrs, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		file.Close()
		return nil, err
	}
	attrs.Iflag, attrs.Oflag, attrs.Lflag = 0, 0, 0
	attrs.Cflag = unix.CLOCAL | unix.CREAD | unix.CS8 | speed
	attrs.Ispeed, attrs.Ospeed = speed, speed
	attrs.Cc[unix.VMIN], attrs.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, attrs); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func (a *agent) readSerial(ctx context.Context, file *os.File) {
	buffer := make([]byte, 256)
	token := make([]byte, 0, 128)
	oversized := false
	for ctx.Err() == nil {
		n, err := file.Read(buffer)
		for _, char := range buffer[:n] {
			if char == '\r' || char == '\n' {
				if len(token) > 0 && !oversized {
					a.processScan(ctx, string(token))
				}
				token = token[:0]
				oversized = false
				continue
			}
			if len(token) == cap(token) {
				oversized = true
			} else if !oversized {
				token = append(token, char)
			}
		}
		if err != nil {
			return
		}
	}
}
