package web

import (
	"strconv"
	"sync"
	"time"
)

// limiter throttles pairing attempts with token buckets: one per client
// and one global (behind a proxy every client may look the same). Failed
// attempts cost extra so guessing codes is slow even in bursts.
type limiter struct {
	mu      sync.Mutex
	now     func() time.Time
	clients map[string]*bucket
	global  bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

const (
	clientBurst  = 10
	clientRefill = 6 * time.Second // one attempt per 6 s sustained
	globalBurst  = 30
	globalRefill = 2 * time.Second
	failCost     = 2 // extra tokens charged for a wrong code
)

func newLimiter(now func() time.Time) *limiter {
	return &limiter{now: now, clients: map[string]*bucket{}, global: bucket{tokens: globalBurst, at: now()}}
}

func (b *bucket) refill(now time.Time, burst float64, every time.Duration) {
	if el := now.Sub(b.at); el > 0 {
		b.tokens += float64(el) / float64(every)
		if b.tokens > burst {
			b.tokens = burst
		}
	}
	b.at = now
}

// allow takes one token for key; it reports false and the wait when empty.
func (l *limiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.clients) > 4096 {
		l.clients = map[string]*bucket{} // bounded memory; a reset only loosens limits briefly
	}
	c := l.clients[key]
	if c == nil {
		c = &bucket{tokens: clientBurst, at: now}
		l.clients[key] = c
	}
	c.refill(now, clientBurst, clientRefill)
	l.global.refill(now, globalBurst, globalRefill)
	if c.tokens < 1 {
		return false, time.Duration((1 - c.tokens) * float64(clientRefill))
	}
	if l.global.tokens < 1 {
		return false, time.Duration((1 - l.global.tokens) * float64(globalRefill))
	}
	c.tokens--
	l.global.tokens--
	return true, 0
}

// fail charges extra for a wrong code.
func (l *limiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if c := l.clients[key]; c != nil {
		c.tokens -= failCost
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
