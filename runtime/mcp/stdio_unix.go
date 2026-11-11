//go:build unix

// Package mcp registers Unix stdio descriptors with the Go runtime poller so
// server shutdown can interrupt blocked reads and writes.
package mcp

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// openPollableFile returns an *os.File registered with the Go runtime poller
// when the underlying descriptor is a pipe or stream that was inherited in
// blocking mode (such as os.Stdin or os.Stdout in a child process).
func openPollableFile(f *os.File) (*os.File, error) {
	if err := f.SetDeadline(time.Time{}); err == nil {
		return f, nil
	}
	dupFD, err := syscall.Dup(int(f.Fd()))
	if err != nil {
		return nil, fmt.Errorf("duplicate stdio descriptor: %w", err)
	}
	syscall.CloseOnExec(dupFD)
	if err := syscall.SetNonblock(dupFD, true); err != nil {
		if closeErr := syscall.Close(dupFD); closeErr != nil {
			return nil, errors.Join(fmt.Errorf("set nonblocking stdio descriptor: %w", err), closeErr)
		}
		return nil, fmt.Errorf("set nonblocking stdio descriptor: %w", err)
	}
	pf := os.NewFile(uintptr(dupFD), f.Name())
	if pf == nil {
		if closeErr := syscall.Close(dupFD); closeErr != nil {
			return nil, fmt.Errorf("open pollable stdio file: %w", closeErr)
		}
		return nil, errors.New("open pollable stdio file: invalid descriptor")
	}
	if err := pf.SetDeadline(time.Time{}); errors.Is(err, os.ErrNoDeadline) {
		if nbErr := syscall.SetNonblock(dupFD, false); nbErr != nil {
			closeErr := pf.Close()
			return nil, errors.Join(fmt.Errorf("restore blocking stdio descriptor: %w", nbErr), closeErr)
		}
	}
	return pf, nil
}
