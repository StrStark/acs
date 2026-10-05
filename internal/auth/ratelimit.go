package auth

import (
	"sync"
	"time"
)

// AttemptLimiter is a fixed-window limiter keyed by client (e.g. IP), used to
// slow down password guessing on login and setup.
type AttemptLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string]*window
}

type window struct {
	start time.Time
	count int
}

func NewAttemptLimiter(max int, per time.Duration) *AttemptLimiter {
	return &AttemptLimiter{max: max, window: per, hits: make(map[string]*window)}
}

// Allow records an attempt for key and reports whether it is within the limit.
func (l *AttemptLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	w, ok := l.hits[key]
	if !ok || now.Sub(w.start) >= l.window {
		// Opportunistically drop stale entries so the map cannot grow unbounded.
		if len(l.hits) > 10_000 {
			for k, v := range l.hits {
				if now.Sub(v.start) >= l.window {
					delete(l.hits, k)
				}
			}
		}
		l.hits[key] = &window{start: now, count: 1}
		return true
	}
	w.count++
	return w.count <= l.max
}
