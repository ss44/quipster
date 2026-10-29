package events_test

import (
	"context"
	"net/http/httptest"
	"strings"
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
		St:            st,
		MeiliUp:       func() bool { return true },
		CacheMaxBytes: 10 * 1024 * 1024 * 1024,
	}

	snap := c.Collect()
	if snap == nil {
		t.Fatal("expected snapshot")
	}
	if !snap.MeiliUp {
		t.Error("expected MeiliUp to be true")
	}
	// The cache quota is carried through unchanged so the library page can
	// draw a usage bar against the configured limit.
	if snap.CacheMaxBytes != 10*1024*1024*1024 {
		t.Errorf("cacheMaxBytes = %d, want the configured quota", snap.CacheMaxBytes)
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
