package server

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// limiter allows each visitor (by IP) a number of actions per window. It keeps
// the public demo usable for everyone when someone clicks upload 100 times.
type limiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string][]time.Time
}

func newLimiter(limit int, window time.Duration) *limiter {
	l := &limiter{limit: limit, window: window, hits: map[string][]time.Time{}}
	go l.cleanup()
	return l
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cut := now.Add(-l.window)
	recent := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cut) {
			recent = append(recent, t)
		}
	}
	if len(recent) >= l.limit {
		l.hits[key] = recent
		return false
	}
	l.hits[key] = append(recent, now)
	return true
}

// cleanup forgets visitors who have been quiet for a full window.
func (l *limiter) cleanup() {
	for range time.Tick(l.window) {
		l.mu.Lock()
		cut := time.Now().Add(-l.window)
		for k, ts := range l.hits {
			if len(ts) == 0 || ts[len(ts)-1].Before(cut) {
				delete(l.hits, k)
			}
		}
		l.mu.Unlock()
	}
}

// clientIP uses the first X-Forwarded-For address when behind a reverse proxy.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
