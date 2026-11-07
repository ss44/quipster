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

func assertCacheAccounting(t *testing.T, st *store.Store, wantBytes int64, keys ...string) {
	t.Helper()
	total, err := st.CacheBytes("clip")
	if err != nil {
		t.Fatalf("cache bytes: %v", err)
	}
	if total != wantBytes {
		t.Errorf("bytes = %d, want %d", total, wantBytes)
	}
	entries, err := st.OldestCacheEntries("clip")
	if err != nil {
		t.Fatalf("oldest entries: %v", err)
	}
	if len(entries) != len(keys) {
		t.Fatalf("entries = %+v, want keys %v", entries, keys)
	}
	for i, key := range keys {
		if entries[i].CacheKey != key {
			t.Errorf("entry %d key = %q, want %q", i, entries[i].CacheKey, key)
		}
	}
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

func TestEvictRemoveFailureKeepsAccountingAndContinues(t *testing.T) {
	st := openTestStore(t)
	lru := &cache.LRU{St: st, MaxBytes: 150, Kinds: []string{"clip"}}

	root := t.TempDir()
	nonEmptyDir := filepath.Join(root, "dir.mp4")
	if err := os.Mkdir(nonEmptyDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	child := filepath.Join(nonEmptyDir, "child")
	writeFile(t, child, "x")
	paths := []string{nonEmptyDir, filepath.Join(root, "second.mp4"), filepath.Join(root, "third.mp4")}
	for i, path := range paths {
		if i > 0 {
			writeFile(t, path, "data")
		}
		if err := st.UpsertCacheEntry(path, path, "clip", 100); err != nil {
			t.Fatalf("upsert entry: %v", err)
		}
		if _, err := st.DB().Exec(`UPDATE cache_entries SET last_access=? WHERE cache_key=?`, i, path); err != nil {
			t.Fatalf("set access order: %v", err)
		}
	}

	if err := lru.Evict(context.Background()); err != nil {
		t.Fatalf("evict: %v", err)
	}
	total, err := st.CacheBytes("clip")
	if err != nil {
		t.Fatalf("cache bytes: %v", err)
	}
	if total != 100 {
		t.Errorf("bytes = %d, want 100", total)
	}
	entries, err := st.OldestCacheEntries("clip")
	if err != nil {
		t.Fatalf("oldest entries: %v", err)
	}
	if len(entries) != 1 || entries[0].CacheKey != nonEmptyDir || entries[0].Bytes != 100 {
		t.Errorf("entries = %+v, want only failed candidate with 100 bytes", entries)
	}
	assertPresent(t, child)
	assertMissing(t, paths[1])
	assertMissing(t, paths[2])
}

func TestEvictMissingFileReconciles(t *testing.T) {
	st := openTestStore(t)
	lru := &cache.LRU{St: st, MaxBytes: 100, Kinds: []string{"clip"}}
	missing := filepath.Join(t.TempDir(), "gone.mp4")
	if err := st.UpsertCacheEntry("missing", missing, "clip", 200); err != nil {
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
	entry, err := st.GetCacheEntry("missing")
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry != nil {
		t.Errorf("missing file entry should be removed: %+v", entry)
	}
}

func TestEvictDeleteFailureKeepsAccountingAndRetries(t *testing.T) {
	st := openTestStore(t)
	lru := &cache.LRU{St: st, MaxBytes: 150, Kinds: []string{"clip"}}
	root := t.TempDir()
	paths := []string{filepath.Join(root, "first.mp4"), filepath.Join(root, "second.mp4"), filepath.Join(root, "third.mp4")}
	for i, path := range paths {
		writeFile(t, path, "data")
		if err := st.UpsertCacheEntry(path, path, "clip", 100); err != nil {
			t.Fatalf("upsert entry: %v", err)
		}
		if _, err := st.DB().Exec(`UPDATE cache_entries SET last_access=? WHERE cache_key=?`, i, path); err != nil {
			t.Fatalf("set access order: %v", err)
		}
	}
	if _, err := st.DB().Exec(`CREATE TRIGGER fail_cache_delete BEFORE DELETE ON cache_entries
		WHEN OLD.last_access = 0 BEGIN SELECT RAISE(FAIL, 'injected delete failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	for attempt := range 2 {
		if err := lru.Evict(context.Background()); err != nil {
			t.Fatalf("evict attempt %d: %v", attempt, err)
		}
		assertCacheAccounting(t, st, 100, paths[0])
		for _, path := range paths {
			assertMissing(t, path)
		}
		lru.MaxBytes = 50
	}

	if _, err := st.DB().Exec(`DROP TRIGGER fail_cache_delete`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	if err := lru.Evict(context.Background()); err != nil {
		t.Fatalf("retry evict: %v", err)
	}
	assertCacheAccounting(t, st, 0)
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
