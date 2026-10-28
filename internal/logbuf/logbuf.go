// Package logbuf provides a bounded in-memory log sink so operators can read
// recent application logs from the admin diagnostics UI without shell access.
package logbuf

import (
	"bytes"
	"sync"
)

// defaultMaxBytes bounds retained log text (the last lines that fit).
const defaultMaxBytes = 2 << 20 // 2 MiB

// Buffer retains the most recent complete log lines up to a byte budget. It
// implements io.Writer so it can be installed alongside stderr with io.MultiWriter.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type Buffer struct {
	mu       sync.Mutex
	maxBytes int
	lines    []string
	bytes    int
	partial  []byte
}

// New creates a buffer with the given byte budget; non-positive values use 2 MiB.
func New(maxBytes int) *Buffer {
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	return &Buffer{maxBytes: maxBytes}
}

// Write appends complete lines from p, retaining only the newest lines that fit
// the budget. A trailing fragment without a newline is kept until the next write.
func (b *Buffer) Write(p []byte) (int, error) {
	if b == nil {
		return len(p), nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	data := p
	if len(b.partial) > 0 {
		data = append(append([]byte{}, b.partial...), p...)
		b.partial = nil
	}
	for {
		idx := bytes.IndexByte(data, '\n')
		if idx < 0 {
			break
		}
		b.appendLine(string(data[:idx]))
		data = data[idx+1:]
	}
	if len(data) > 0 {
		// Keep only the tail of an oversized fragment; an unterminated line
		// larger than the whole budget can never be retained anyway.
		if len(data) > b.maxBytes {
			data = data[len(data)-b.maxBytes:]
		}
		b.partial = append(b.partial[:0], data...)
	}
	return len(p), nil
}

// Lines returns up to limit of the most recent lines, oldest first. A
// non-positive limit returns every retained line.
func (b *Buffer) Lines(limit int) []string {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	start := 0
	if limit > 0 && len(b.lines) > limit {
		start = len(b.lines) - limit
	}
	out := make([]string, len(b.lines)-start)
	copy(out, b.lines[start:])
	if len(out) == 0 {
		return nil
	}
	return out
}

// Len reports how many complete lines are retained.
func (b *Buffer) Len() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.lines)
}

// appendLine adds one line and evicts the oldest lines until the byte budget
// is respected. Callers must hold b.mu.
func (b *Buffer) appendLine(line string) {
	b.lines = append(b.lines, line)
	b.bytes += len(line) + 1
	for b.bytes > b.maxBytes && len(b.lines) > 1 {
		b.bytes -= len(b.lines[0]) + 1
		b.lines = b.lines[1:]
	}
}
