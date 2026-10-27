package cache

import (
	"context"
	"log"
	"os"
	"time"

	"quipster/internal/store"
)

type LRU struct {
	St       *store.Store
	MaxBytes int64
	Kinds    []string
}

// Evict removes least-recently-accessed entries until total bytes fit the cap.
func (l *LRU) Evict(ctx context.Context) error {
	total, err := l.St.CacheBytes(l.Kinds...)
	if err != nil {
		return err
	}
	if total <= l.MaxBytes {
		return nil
	}
	entries, err := l.St.OldestCacheEntries(l.Kinds...)
	if err != nil {
		return err
	}
	over := total - l.MaxBytes
	for _, e := range entries {
		if over <= 0 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := os.Remove(e.Path); err != nil && !os.IsNotExist(err) {
			log.Printf("[cache] remove %s: %v", e.Path, err)
		}
		if err := l.St.DeleteCacheEntry(e.CacheKey); err != nil {
			log.Printf("[cache] delete entry %s: %v", e.CacheKey, err)
		}
		over -= e.Bytes
	}
	return nil
}

// RunDaemon evicts periodically until ctx is cancelled.
func RunDaemon(ctx context.Context, l *LRU, interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := l.Evict(ctx); err != nil && ctx.Err() == nil {
					log.Printf("[cache] evict: %v", err)
				}
			}
		}
	}()
}
