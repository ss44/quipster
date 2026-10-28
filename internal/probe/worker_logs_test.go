package probe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// probeLogCapture also signals stage transitions without polling or racing the
// heartbeat goroutine's writes.
type probeLogCapture struct {
	lines chan string
	buf   bytes.Buffer
	mu    sync.Mutex
}

func (c *probeLogCapture) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, err := c.buf.Write(b)
	select {
	case c.lines <- string(b):
	default:
	}
	return n, err
}

func (c *probeLogCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func captureProbeLogs(t *testing.T) *probeLogCapture {
	t.Helper()
	c := &probeLogCapture{lines: make(chan string, 100)}
	previous := log.Writer()
	log.SetOutput(c)
	t.Cleanup(func() { log.SetOutput(previous) })
	return c
}

func assertProbeLogs(t *testing.T, c *probeLogCapture, want ...string) {
	t.Helper()
	output := c.String()
	for _, s := range want {
		if !strings.Contains(output, s) {
			t.Errorf("missing log %q in:\n%s", s, output)
		}
	}
}

func waitProbeLog(t *testing.T, c *probeLogCapture, want string) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case line := <-c.lines:
			if strings.Contains(line, want) {
				return
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %q in:\n%s", want, c.String())
		}
	}
}

func TestJobHandlerExtractionQueueCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprintf("deadline=%t", deadline), func(t *testing.T) {
			logs := captureProbeLogs(t)
			st := newTestStore(t)
			dir, binDir := t.TempDir(), t.TempDir()
			m := addMedia(t, st, filepath.Join(dir, "movie.mkv"), true)
			writeFFprobeMock(t, binDir, ffprobeJSON(videoStreamJSON, subStreamJSON(2, "subrip", "eng")))
			marker := filepath.Join(binDir, "marker")
			writeFFmpegExtractMock(t, binDir, marker)
			p := &Pipeline{St: st, SubsCacheDir: t.TempDir(), ExtractConns: 1}
			sem := p.sem()
			sem <- struct{}{}
			defer func() { <-sem }()
			ctx, cancel := context.WithCancel(context.Background())
			wantErr := context.Canceled
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
				wantErr = context.DeadlineExceeded
			}
			defer cancel()
			job := probeJob(t, m.ID)
			result := make(chan error, 1)
			go func() { result <- p.JobHandler()(ctx, job, func(float64) {}) }()
			waitProbeLog(t, logs, "stage=extraction_queue waiting")
			if !deadline {
				cancel()
			}
			select {
			case err := <-result:
				if !errors.Is(err, wantErr) {
					t.Fatalf("want %v for requeue, got %v", wantErr, err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("canceled queue acquisition did not return")
			}
			if len(sem) != 1 {
				t.Fatalf("canceled waiter changed occupied capacity: %d", len(sem))
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("ffmpeg must not start: marker stat = %v", err)
			}
			got, err := st.GetMedia(m.ID)
			if err != nil || got.Status != "pending" || got.Error != "" {
				t.Fatalf("cancellation must not change media status: %+v, %v", got, err)
			}
			if n := countRows(t, st, `SELECT COUNT(*) FROM jobs WHERE type='index'`); n != 0 {
				t.Fatalf("canceled extraction enqueued %d index jobs", n)
			}
			assertProbeLogs(t, logs, fmt.Sprintf("media %d job %d:", m.ID, job.ID),
				"stage=extraction_queue canceled wait_elapsed=", "stage=complete outcome=failed total_elapsed=")
		})
	}
}

func TestExtractionHeartbeatStopsOnCancellation(t *testing.T) {
	logs := captureProbeLogs(t)
	binDir := t.TempDir()
	t.Setenv("FFMPEG_BIN", writeMockBin(t, binDir, "ffmpeg", "while :; do :; done\n"))
	p := &Pipeline{ExtractConns: 1}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- p.extract(ctx, "movie.mkv", 0, filepath.Join(binDir, "out.srt"), log.New(logs, "", 0), 5*time.Millisecond)
	}()
	waitProbeLog(t, logs, "stage=extraction running elapsed=")
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("expected canceled ffmpeg to fail")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("extraction did not stop")
	}
	if len(p.sem()) != 0 {
		t.Fatal("extraction leaked capacity")
	}
	assertProbeLogs(t, logs, "stage=extraction_queue acquired wait_elapsed=", "active=1 capacity=1",
		"stage=extraction started", "gpu=false", "full-file container scan, storage-bound",
		"CPU subtitle conversion, no GPU", "elapsed only, not measured bytes or throughput", "stage=extraction failed elapsed=")
	before := logs.String()
	time.Sleep(20 * time.Millisecond)
	if after := logs.String(); after != before {
		t.Fatalf("heartbeat continued after extraction returned:\n%s", after)
	}
}

func TestExtractionWaitsForCapacity(t *testing.T) {
	logs := captureProbeLogs(t)
	binDir := t.TempDir()
	marker := filepath.Join(binDir, "marker")
	writeFFmpegExtractMock(t, binDir, marker)
	p := &Pipeline{ExtractConns: 1}
	sem := p.sem()
	sem <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- p.extract(ctx, "movie.mkv", 0, filepath.Join(binDir, "out.srt"), log.New(logs, "", 0), time.Hour)
	}()
	waitProbeLog(t, logs, "stage=extraction_queue waiting")
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ffmpeg started before capacity was available: %v", err)
	}
	<-sem
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("extraction did not resume after capacity became available")
	}
	if len(sem) != 0 {
		t.Fatal("successful extraction leaked capacity")
	}
	assertProbeLogs(t, logs, "stage=extraction_queue waiting active=1 capacity=1", "stage=extraction_queue acquired wait_elapsed=", "stage=extraction succeeded elapsed=")
}

func TestExtractionAlreadyCanceledWithCapacity(t *testing.T) {
	logs := captureProbeLogs(t)
	p := &Pipeline{ExtractConns: 1}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.extract(ctx, "movie.mkv", 0, "unused.srt", log.New(logs, "", 0), time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation, got %v", err)
	}
	if len(p.sem()) != 0 {
		t.Fatal("already-canceled extraction leaked capacity")
	}
	assertProbeLogs(t, logs, "stage=extraction_queue canceled wait_elapsed=")
	if strings.Contains(logs.String(), "stage=extraction started") {
		t.Fatal("already-canceled extraction must not start ffmpeg")
	}
}

func TestJobHandlerActiveExtractionCancellation(t *testing.T) {
	logs := captureProbeLogs(t)
	st := newTestStore(t)
	dir, binDir := t.TempDir(), t.TempDir()
	m := addMedia(t, st, filepath.Join(dir, "movie.mkv"), true)
	writeFFprobeMock(t, binDir, ffprobeJSON(videoStreamJSON, subStreamJSON(2, "subrip", "eng")))
	t.Setenv("FFMPEG_BIN", writeMockBin(t, binDir, "ffmpeg", "while :; do :; done\n"))
	p := &Pipeline{St: st, SubsCacheDir: t.TempDir(), ExtractConns: 1}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job := probeJob(t, m.ID)
	result := make(chan error, 1)
	go func() { result <- p.JobHandler()(ctx, job, func(float64) {}) }()
	waitProbeLog(t, logs, "stage=extraction started")
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want cancellation for requeue, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("active extraction did not stop")
	}
	if len(p.sem()) != 0 {
		t.Fatal("cancellation leaked capacity")
	}
	got, err := st.GetMedia(m.ID)
	if err != nil || got.Status != "pending" || got.Error != "" {
		t.Fatalf("cancellation must not mark no_subs: %+v, %v", got, err)
	}
	assertProbeLogs(t, logs, "stage=extraction failed elapsed=", "stage=complete outcome=failed total_elapsed=")
}
