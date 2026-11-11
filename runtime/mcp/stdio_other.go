//go:build !unix

// Package mcp provides fallback pollable file opening on non-Unix platforms.
package mcp

import "os"

// openPollableFile returns f directly on platforms without Unix descriptor
// duplication.
func openPollableFile(f *os.File) (*os.File, error) {
	return f, nil
}
