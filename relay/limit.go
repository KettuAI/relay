package relay

import (
	"sync"
	"time"
)

// maxLimiterKeys bounds what a minute's counts may hold; a key past it is refused, so a burst
// of new addresses or ids cannot grow the map without end.
const maxLimiterKeys = 65536

// limiter counts per key in the current minute and drops every count when the next one starts.
type limiter struct {
	mu     sync.Mutex
	minute int64
	counts map[string]int
}

func newLimiter() *limiter { return &limiter{counts: map[string]int{}} }

func (l *limiter) take(key string, limit int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if minute := time.Now().Unix() / 60; minute != l.minute {
		l.minute = minute
		// A fresh map gives back the memory a burst took.
		l.counts = map[string]int{}
	}
	count, ok := l.counts[key]
	if !ok && len(l.counts) >= maxLimiterKeys {
		return false
	}
	l.counts[key] = count + 1
	return count < limit
}
