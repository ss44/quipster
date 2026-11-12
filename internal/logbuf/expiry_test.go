package logbuf

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestReadsExcludeExpiredLogsAndFragments(t *testing.T) {
	b := New(1024)
	now := time.Now()
	b.now = func() time.Time { return now }
	if _, err := b.Write([]byte("old\nfragment")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour + time.Nanosecond)
	if b.Len() != 0 || len(b.Lines(0)) != 0 {
		t.Fatal("read returned expired logs")
	}
	if _, err := b.Write([]byte("fresh\n")); err != nil {
		t.Fatal(err)
	}
	if got := b.Lines(0); len(got) != 1 || got[0] != "fresh" {
		t.Fatalf("expired fragment retained: %v", got)
	}
}

func TestRunExpiresIdleBufferAndStops(t *testing.T) {
	b := New(1024)
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	b.now = func() time.Time { return time.Unix(0, clock.Load()) }
	b.sweepInterval = time.Millisecond
	if _, err := b.Write([]byte("idle\npartial")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()
	clock.Add(int64(time.Hour + time.Second))
	deadline := time.After(time.Second)
	for {
		b.mu.Lock()
		empty := len(b.lines) == 0 && len(b.partial) == 0
		b.mu.Unlock()
		if empty {
			break
		}
		select {
		case <-deadline:
			t.Fatal("idle logs not expired by background goroutine")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop")
	}
}
