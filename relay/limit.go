package relay

import (
	"sync"
	"time"
)

// limiter counts per key in the current minute and forgets every count when the next one starts.
type limiter struct {
	mu     sync.Mutex
	minute int64
	counts map[string]int
}

func newLimiter() *limiter { return &limiter{counts: map[string]int{}} }

func (l *limiter) take(key string, limit int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	minute := time.Now().Unix() / 60
	if minute != l.minute {
		l.minute = minute
		clear(l.counts)
	}
	l.counts[key]++
	return l.counts[key] <= limit
}
