// Package logbuf provides a bounded in-memory log sink so operators can read
// recent application logs from the admin diagnostics UI without shell access.
package logbuf

import (
	"bytes"
	"context"
	"sync"
	"time"
)

// defaultMaxBytes bounds retained log text (the last lines that fit).
const defaultMaxBytes = 2 << 20 // 2 MiB

// MaxAge is the maximum retention window, subject to the byte and line caps.
const MaxAge = time.Hour

// Entry is a complete log line with its process-local capture time.
type Entry struct {
	Time time.Time `json:"time"`
	Text string    `json:"text"`
}

// Buffer retains recent complete log lines and a bounded trailing fragment.
// It implements io.Writer for use alongside stderr with io.MultiWriter.
// Run should be started once by the owner with its lifecycle context.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type Buffer struct {
	mu            sync.Mutex
	maxBytes      int
	lines         []Entry
	bytes         int
	partial       []byte
	truncated     bool
	partialAt     time.Time
	now           func() time.Time
	sweepInterval time.Duration
}

// New creates a buffer with the given byte budget; non-positive values use 2 MiB.
func New(maxBytes int) *Buffer {
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	return &Buffer{maxBytes: maxBytes, now: time.Now, sweepInterval: time.Minute}
}

// Run expires idle logs in a separate goroutine owned by the caller. It returns
// on context cancellation; reads also expire logs between sweeps.
func (b *Buffer) Run(ctx context.Context) {
	if b == nil {
		return
	}
	ticker := time.NewTicker(b.sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.mu.Lock()
			b.expire(b.now())
			b.mu.Unlock()
		}
	}
}

// Write appends complete lines and retains only a bounded trailing fragment.
// Oversized logical lines are replaced with a marker; stderr is unaffected.
// The reported count always covers all p.
func (b *Buffer) Write(p []byte) (int, error) {
	if b == nil {
		return len(p), nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.expire(now)
	data := p
	for len(data) > 0 {
		idx := bytes.IndexByte(data, '\n')
		fragment := data
		if idx >= 0 {
			fragment = data[:idx]
		}
		// Bound text allocations before copying any caller-owned write.
		room := min(b.maxBytes-1, 16<<10)
		b.appendFragment(fragment, room)
		if b.partialAt.IsZero() {
			b.partialAt = now
		}
		if idx < 0 {
			break
		}
		b.lines = append(b.lines, Entry{Time: now, Text: string(b.partial)})
		b.bytes += len(b.partial) + 1
		b.partial = nil
		b.truncated = false
		b.partialAt = time.Time{}
		b.trim(0)
		data = data[idx+1:]
	}
	b.trim(cap(b.partial))
	return len(p), nil
}

// appendFragment replaces an oversized logical line rather than retaining a
// suffix whose credential/path prefix would no longer be available to redact.
// Capacity, not just length, is budgeted by trim.
func (b *Buffer) appendFragment(fragment []byte, room int) {
	if b.truncated {
		return
	}
	if len(fragment) > room-len(b.partial) {
		marker := "[log line truncated]"
		b.partial = make([]byte, min(len(marker), room))
		copy(b.partial, marker)
		b.truncated = true
		return
	}
	needed := len(b.partial) + len(fragment)
	if cap(b.partial) < needed {
		next := make([]byte, len(b.partial), needed)
		copy(next, b.partial)
		b.partial = next
	}
	b.partial = append(b.partial, fragment...)
}

// Entries returns a detached snapshot of unexpired complete lines, oldest first.
func (b *Buffer) Entries() []Entry {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expire(b.now())
	return append([]Entry(nil), b.lines...)
}

// Lines returns up to limit of the most recent unexpired lines, oldest first.
// A non-positive limit returns every retained line.
func (b *Buffer) Lines(limit int) []string {
	entries := b.Entries()
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	if len(entries) == 0 {
		return nil
	}
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Text
	}
	return out
}

// Len reports how many complete unexpired lines are retained.
func (b *Buffer) Len() int { return len(b.Entries()) }

// expire clears aged text even when no new writes arrive. Callers hold mu.
func (b *Buffer) expire(now time.Time) {
	cutoff := now.Add(-MaxAge)
	for len(b.lines) > 0 && b.lines[0].Time.Before(cutoff) {
		b.dropFirst()
	}
	if !b.partialAt.IsZero() && b.partialAt.Before(cutoff) {
		b.partial = nil
		b.truncated = false
		b.partialAt = time.Time{}
	}
}

// dropFirst releases evicted references as well as their byte accounting.
func (b *Buffer) dropFirst() {
	b.bytes -= len(b.lines[0].Text) + 1
	b.lines[0] = Entry{}
	b.lines = b.lines[1:]
	if len(b.lines) == 0 {
		b.lines = nil
	}
}

// trim bounds all retained text, including the trailing fragment.
func (b *Buffer) trim(partialBytes int) {
	for (b.bytes+partialBytes > b.maxBytes || len(b.lines) > 10000) && len(b.lines) > 0 {
		b.dropFirst()
	}
}
