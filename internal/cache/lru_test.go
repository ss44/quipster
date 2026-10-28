package cache_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"quipster/internal/cache"
	"quipster/internal/store"
)

func TestLRUEvict(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	lru := &cache.LRU{
		St:       st,
		MaxBytes: 150,
		Kinds:    []string{"clip"},
	}

	dir := t.TempDir()
	f1 := filepath.Join(dir, "1.mp4")
	f2 := filepath.Join(dir, "2.mp4")
	f3 := filepath.Join(dir, "3.mp4")

	_ = os.WriteFile(f1, []byte("data"), 0600)
	_ = os.WriteFile(f2, []byte("data"), 0600)
	_ = os.WriteFile(f3, []byte("data"), 0600)

	_ = st.UpsertCacheEntry("k1", f1, "clip", 100)
	time.Sleep(10 * time.Millisecond)
	_ = st.UpsertCacheEntry("k2", f2, "clip", 100)
	time.Sleep(10 * time.Millisecond)
	_ = st.UpsertCacheEntry("k3", f3, "clip", 100)
	
	// total = 300, over by 150. Oldest are k1 and k2.
	
	err = lru.Evict(context.Background())
	if err != nil {
		t.Fatalf("evict failed: %v", err)
	}

	total, _ := st.CacheBytes("clip")
	if total != 100 {
		t.Errorf("expected 100 bytes left, got %d", total)
	}

	entries, _ := st.OldestCacheEntries("clip")
	if len(entries) != 1 {
		t.Errorf("expected 1 entry left, got %d", len(entries))
	}
	if len(entries) > 0 && entries[0].CacheKey != "k3" {
		t.Errorf("expected k3 to be kept, got %s", entries[0].CacheKey)
	}

	if _, err := os.Stat(f1); !os.IsNotExist(err) {
		t.Errorf("expected f1 to be deleted")
	}
	if _, err := os.Stat(f2); !os.IsNotExist(err) {
		t.Errorf("expected f2 to be deleted")
	}
	if _, err := os.Stat(f3); err != nil {
		t.Errorf("expected f3 to be kept")
	}
}

func TestRunDaemon(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	
	lru := &cache.LRU{
		St:       st,
		MaxBytes: 50,
		Kinds:    []string{"thumb"},
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "thumb.jpg")
	_ = os.WriteFile(f, []byte("thumb"), 0600)
	_ = st.UpsertCacheEntry("k1", f, "thumb", 100)

	ctx, cancel := context.WithCancel(context.Background())
	cache.RunDaemon(ctx, lru, 10*time.Millisecond)

	time.Sleep(50 * time.Millisecond)
	cancel()

	total, _ := st.CacheBytes("thumb")
	if total != 0 {
		t.Errorf("daemon failed to evict, bytes remaining: %d", total)
	}
}
