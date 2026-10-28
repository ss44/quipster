package cache_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"quipster/internal/cache"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s should be deleted", path)
	}
}

func assertPresent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("%s should be kept: %v", path, err)
	}
}

func TestLRUEvict(t *testing.T) {
	st := openTestStore(t)

	lru := &cache.LRU{
		St:       st,
		MaxBytes: 150,
		Kinds:    []string{"clip"},
	}

	dir := t.TempDir()
	f1 := filepath.Join(dir, "1.mp4")
	f2 := filepath.Join(dir, "2.mp4")
	f3 := filepath.Join(dir, "3.mp4")

	writeFile(t, f1, "data")
	writeFile(t, f2, "data")
	writeFile(t, f3, "data")

	if err := st.UpsertCacheEntry("k1", f1, "clip", 100); err != nil {
		t.Fatalf("upsert k1: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := st.UpsertCacheEntry("k2", f2, "clip", 100); err != nil {
		t.Fatalf("upsert k2: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := st.UpsertCacheEntry("k3", f3, "clip", 100); err != nil {
		t.Fatalf("upsert k3: %v", err)
	}

	// total = 300, over by 150. Oldest are k1 and k2.

	if err := lru.Evict(context.Background()); err != nil {
		t.Fatalf("evict failed: %v", err)
	}

	total, err := st.CacheBytes("clip")
	if err != nil {
		t.Fatalf("cache bytes: %v", err)
	}
	if total != 100 {
		t.Errorf("expected 100 bytes left, got %d", total)
	}

	entries, err := st.OldestCacheEntries("clip")
	if err != nil {
		t.Fatalf("oldest entries: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry left, got %d", len(entries))
	}
	if len(entries) > 0 && entries[0].CacheKey != "k3" {
		t.Errorf("expected k3 to be kept, got %s", entries[0].CacheKey)
	}

	assertMissing(t, f1)
	assertMissing(t, f2)
	assertPresent(t, f3)
}

func TestRunDaemon(t *testing.T) {
	st := openTestStore(t)

	lru := &cache.LRU{
		St:       st,
		MaxBytes: 50,
		Kinds:    []string{"thumb"},
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "thumb.jpg")
	writeFile(t, f, "thumb")
	if err := st.UpsertCacheEntry("k1", f, "thumb", 100); err != nil {
		t.Fatalf("upsert k1: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cache.RunDaemon(ctx, lru, 10*time.Millisecond)

	time.Sleep(50 * time.Millisecond)
	cancel()

	total, err := st.CacheBytes("thumb")
	if err != nil {
		t.Fatalf("cache bytes: %v", err)
	}
	if total != 0 {
		t.Errorf("daemon failed to evict, bytes remaining: %d", total)
	}
}
