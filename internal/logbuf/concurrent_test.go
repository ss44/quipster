package logbuf

import (
	"bytes"
	"context"
	"io"
	"log"
	"strings"
	"sync"
	"testing"
)

func TestConcurrentWritesReadsAndExpiry(t *testing.T) {
	b := New(1024)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			for j := 0; j < 100; j++ {
				if _, err := b.Write([]byte("concurrent line\n")); err != nil {
					t.Error(err)
				}
				b.Lines(3)
				b.Len()
			}
		})
	}
	wg.Wait()
	cancel()
	<-done
	if b.bytes+cap(b.partial) > b.maxBytes {
		t.Fatal("budget exceeded")
	}
}

func TestStandardLoggerPreservesStderrAndSnapshotIsolation(t *testing.T) {
	b := New(0)
	var stderr bytes.Buffer
	logger := log.New(io.MultiWriter(&stderr, b), "", 0)
	logger.Print("stderr-and-buffer")
	if stderr.String() != "stderr-and-buffer\n" {
		t.Fatalf("stderr lost: %q", stderr.String())
	}
	entries := b.Entries()
	if len(entries) != 1 || entries[0].Time.IsZero() {
		t.Fatal("missing capture timestamp")
	}
	entries[0].Text = "modified"
	if got := b.Lines(1); len(got) != 1 || !strings.Contains(got[0], "stderr-and-buffer") {
		t.Fatal("snapshot modified buffer")
	}
}
