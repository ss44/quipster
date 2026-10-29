package events_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"quipster/internal/events"
	"quipster/internal/store"
)

type mockFlusher struct {
	httptest.ResponseRecorder
	flushed bool
}

func (m *mockFlusher) Flush() {
	m.flushed = true
}

// failingFlusher behaves like a client connection that dies mid-stream, so the
// handler's first write fails and the stream exits without spinning.
type failingFlusher struct {
	httptest.ResponseRecorder
}

func (f *failingFlusher) Flush() {}

func (f *failingFlusher) Write(_ []byte) (int, error) {
	return 0, errors.New("client disconnected")
}

// syncRecorder serializes recorder access between the handler goroutine and
// the test, which polls the body while the stream is still writing.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type syncRecorder struct {
	mu  sync.Mutex
	rec *httptest.ResponseRecorder
}

func (s *syncRecorder) Header() http.Header {
	return s.rec.Header()
}

func (s *syncRecorder) WriteHeader(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rec.WriteHeader(code)
}

func (s *syncRecorder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec.Write(p)
}

func (s *syncRecorder) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rec.Flush()
}

func (s *syncRecorder) body() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec.Body.String()
}

func TestCollector(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("st.Close: %v", err)
		}
	})

	c := &events.Collector{
		St:      st,
		MeiliUp: func() bool { return true },
	}

	snap := c.Collect()
	if snap == nil {
		t.Fatal("expected snapshot")
	}
	if !snap.MeiliUp {
		t.Error("expected MeiliUp to be true")
	}
}

func TestHandler(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("st.Close: %v", err)
		}
	})

	c := &events.Collector{
		St:      st,
		MeiliUp: func() bool { return false },
	}

	req := httptest.NewRequest("GET", "/events", nil)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)

	rec := &mockFlusher{ResponseRecorder: *httptest.NewRecorder()}

	// Start the handler in a goroutine because it blocks
	go func() {
		c.Handler()(rec, req)
	}()

	// Give it a moment to send the initial tick
	time.Sleep(50 * time.Millisecond)
	cancel() // kill it

	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("wrong content type")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: tick") {
		t.Errorf("missing tick event")
	}
	if !strings.Contains(body, "meiliUp") {
		t.Errorf("missing meiliUp in payload")
	}
}

// bareWriter is a ResponseWriter without Flush support, standing in for
// servers (or proxies) that cannot stream responses.
//
//nolint:govet // field grouping favors readability over optimal alignment.
type bareWriter struct {
	buf  bytes.Buffer
	head http.Header
	code int
}

func (b *bareWriter) Header() http.Header {
	if b.head == nil {
		b.head = make(http.Header)
	}
	return b.head
}

func (b *bareWriter) Write(p []byte) (int, error) {
	return b.buf.Write(p)
}

func (b *bareWriter) WriteHeader(code int) {
	b.code = code
}

// TestHandlerRejectsNonFlusherWriters verifies the guard for servers without
// streaming support: the handler answers 500 instead of hanging.
func TestHandlerRejectsNonFlusherWriters(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("st.Close: %v", err)
		}
	})

	c := &events.Collector{St: st}
	w := &bareWriter{}
	c.Handler()(w, httptest.NewRequest("GET", "/events", nil))
	if w.code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.code)
	}
	if !strings.Contains(w.buf.String(), "streaming unsupported") {
		t.Errorf("expected streaming-unsupported error, got %q", w.buf.String())
	}
}

// TestHandlerStopsOnWriteError covers a client that vanishes between ticks:
// the handler must return promptly rather than loop on a dead connection.
func TestHandlerStopsOnWriteError(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("st.Close: %v", err)
		}
	})

	c := &events.Collector{St: st}
	rec := &failingFlusher{}
	c.Handler()(rec, httptest.NewRequest("GET", "/events", nil))
}

// TestHandlerStreamsStateChanges verifies that a store change reaches a
// connected client on the background run loop's next tick through the notify
// channel, without reconnecting.
func TestHandlerStreamsStateChanges(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("st.Close: %v", err)
		}
	})

	c := &events.Collector{
		St:      st,
		MeiliUp: func() bool { return true },
	}

	req := httptest.NewRequest("GET", "/events", nil)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)

	rec := &syncRecorder{rec: httptest.NewRecorder()}
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Handler()(rec, req)
	}()
	t.Cleanup(cancel)

	// The run loop ticks once per second, so allow more than one interval for
	// the initial snapshot and for the state change to propagate.
	deadline := time.Now().Add(3 * time.Second)
	for strings.Count(rec.body(), "event: tick") < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if strings.Count(rec.body(), "event: tick") < 1 {
		t.Fatal("initial tick not sent")
	}

	if _, err := st.EnqueueJob("scan", "payload", 1); err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}

	deadline = time.Now().Add(4 * time.Second)
	for strings.Count(rec.body(), "event: tick") < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	body := rec.body()
	if strings.Count(body, "event: tick") < 2 {
		t.Fatal("state change was not streamed to the connected client")
	}
	if !strings.Contains(body, `"type":"scan"`) {
		t.Errorf("streamed payload missing enqueued job: %s", body)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("handler did not return after context cancel")
	}
}
