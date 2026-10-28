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

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Logf("close store: %v", cerr)
		}
	})
	return st
}

func TestEvictNoopUnderCap(t *testing.T) {
	st := openTestStore(t)
	lru := &cache.LRU{St: st, MaxBytes: 1000, Kinds: []string{"clip"}}

	f := filepath.Join(t.TempDir(), "small.mp4")
	if err := os.WriteFile(f, []byte("data"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := st.UpsertCacheEntry("small", f, "clip", 10); err != nil {
		t.Fatalf("upsert entry: %v", err)
	}

	if err := lru.Evict(context.Background()); err != nil {
		t.Fatalf("evict: %v", err)
	}
	total, err := st.CacheBytes("clip")
	if err != nil {
		t.Fatalf("cache bytes: %v", err)
	}
	if total != 10 {
		t.Errorf("bytes = %d, want 10", total)
	}
	if _, err := os.Stat(f); err != nil {
		t.Errorf("file should be kept: %v", err)
	}
}

func TestEvictStoreError(t *testing.T) {
	st := openTestStore(t)
	lru := &cache.LRU{St: st, MaxBytes: 0, Kinds: []string{"clip"}}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if err := lru.Evict(context.Background()); err == nil {
		t.Fatal("expected error from closed store")
	}
}

func TestEvictContextCanceled(t *testing.T) {
	st := openTestStore(t)
	lru := &cache.LRU{St: st, MaxBytes: 50, Kinds: []string{"clip"}}

	dir := t.TempDir()
	for i, key := range []string{"k1", "k2"} {
		f := filepath.Join(dir, key+".mp4")
		if err := os.WriteFile(f, []byte("data"), 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}
		if err := st.UpsertCacheEntry(key, f, "clip", 100); err != nil {
			t.Fatalf("upsert entry: %v", err)
		}
		time.Sleep(time.Duration(i) * time.Millisecond)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := lru.Evict(ctx); err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	total, err := st.CacheBytes("clip")
	if err != nil {
		t.Fatalf("cache bytes: %v", err)
	}
	if total != 200 {
		t.Errorf("bytes = %d, want entries untouched at 200", total)
	}
}

func TestEvictRemoveErrorsAreTolerated(t *testing.T) {
	st := openTestStore(t)
	lru := &cache.LRU{St: st, MaxBytes: 0, Kinds: []string{"clip"}}

	root := t.TempDir()
	nonEmptyDir := filepath.Join(root, "dir.mp4")
	if err := os.MkdirAll(nonEmptyDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nonEmptyDir, "child"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write child: %v", err)
	}
	missing := filepath.Join(root, "gone.mp4")

	if err := st.UpsertCacheEntry("dir", nonEmptyDir, "clip", 100); err != nil {
		t.Fatalf("upsert dir entry: %v", err)
	}
	if err := st.UpsertCacheEntry("missing", missing, "clip", 100); err != nil {
		t.Fatalf("upsert missing entry: %v", err)
	}

	if err := lru.Evict(context.Background()); err != nil {
		t.Fatalf("evict: %v", err)
	}
	total, err := st.CacheBytes("clip")
	if err != nil {
		t.Fatalf("cache bytes: %v", err)
	}
	if total != 0 {
		t.Errorf("bytes = %d, want 0", total)
	}
	if _, err := os.Stat(nonEmptyDir); err != nil {
		t.Errorf("non-empty dir should survive removal error: %v", err)
	}
}

func TestRunDaemonLogsEvictError(t *testing.T) {
	st := openTestStore(t)
	lru := &cache.LRU{St: st, MaxBytes: 0, Kinds: []string{"thumb"}}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cache.RunDaemon(ctx, lru, 10*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	cancel()
	time.Sleep(10 * time.Millisecond)
}
