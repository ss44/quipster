package mcp

import (
	"sync"
	"time"
)

// limiter is a fixed-window per-token rate limiter. A window per minute is
// precise enough for agent traffic and trivially cheap to maintain.
type limiter struct {
	hits  map[string]*window
	mu    sync.Mutex
	limit int
}

// window counts hits inside one minute bucket.
type window struct {
	bucket int64 // unix minute
	count  int
}

// newLimiter builds a limiter with the given requests-per-minute budget.
func newLimiter(limit int) *limiter {
	return &limiter{limit: limit, hits: map[string]*window{}}
}

// allow consumes one request for key. A zero or negative limit disables
// limiting entirely.
func (l *limiter) allow(key string) bool {
	if l.limit <= 0 {
		return true
	}
	bucket := time.Now().Unix() / 60
	l.mu.Lock()
	defer l.mu.Unlock()
	// Tokens come from operator config, so the map stays tiny; reset it if a
	// deployment rotates through many tokens to bound memory anyway.
	if len(l.hits) > 1024 {
		l.hits = map[string]*window{}
	}
	w := l.hits[key]
	if w == nil || w.bucket != bucket {
		w = &window{bucket: bucket}
		l.hits[key] = w
	}
	if w.count >= l.limit {
		return false
	}
	w.count++
	return true
}
