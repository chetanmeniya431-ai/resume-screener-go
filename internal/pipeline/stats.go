package pipeline

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/chetanmeniya431-ai/resume-screener-go/internal/breaker"
)

// stats keeps live numbers for the dashboard. All methods are safe for concurrent use.
type stats struct {
	mu         sync.Mutex
	started    time.Time
	completed  int
	failures   int
	retries    int
	latencies  []time.Duration // last N finished resumes
	finishedAt []time.Time     // for the per-minute rate
	events     []Event         // newest last
}

type Event struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"` // info, done, failed, retry
	Text string    `json:"text"`
}

const (
	keepLatencies = 200
	keepEvents    = 40
)

func newStats() *stats { return &stats{started: time.Now()} }

func (s *stats) add(kind, text string) {
	s.events = append(s.events, Event{At: time.Now(), Kind: kind, Text: text})
	if len(s.events) > keepEvents {
		s.events = s.events[len(s.events)-keepEvents:]
	}
}

func (s *stats) event(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.add("info", fmt.Sprintf(format, args...))
}

func (s *stats) done(d time.Duration, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed++
	s.latencies = append(s.latencies, d)
	if len(s.latencies) > keepLatencies {
		s.latencies = s.latencies[1:]
	}
	s.finishedAt = append(s.finishedAt, time.Now())
	s.add("done", text)
}

func (s *stats) failed(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures++
	s.add("failed", text)
}

func (s *stats) retried(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retries++
	s.add("retry", text)
}

// WorkerView is one worker as the dashboard shows it.
type WorkerView struct {
	ID       int     `json:"id"`
	State    string  `json:"state"`
	Resume   string  `json:"resume"`
	Seconds  float64 `json:"seconds"` // time in the current state
	Handled  int     `json:"handled"`
}

// Snapshot is everything the live dashboard needs, sent as JSON every half second.
type Snapshot struct {
	Workers      []WorkerView `json:"workers"`
	MaxWorkers   int          `json:"max_workers"`
	QueueLen     int          `json:"queue_len"`
	QueueCap     int          `json:"queue_cap"`
	Running      int          `json:"running"`
	Completed    int          `json:"completed"`
	Failed       int          `json:"failed"`
	Retries      int          `json:"retries"`
	PerMinute    int          `json:"per_minute"`
	P50Seconds   float64      `json:"p50_seconds"`
	P95Seconds   float64      `json:"p95_seconds"`
	Breaker      string       `json:"breaker"`
	UptimeSecond int          `json:"uptime_seconds"`
	Events       []Event      `json:"events"`
}

func (e *Engine) Snapshot() Snapshot {
	var snap Snapshot

	e.mu.Lock()
	for _, w := range e.workers {
		w.mu.Lock()
		snap.Workers = append(snap.Workers, WorkerView{
			ID: w.id, State: w.state, Resume: w.cand,
			Seconds: time.Since(w.since).Round(100 * time.Millisecond).Seconds(), Handled: w.handled,
		})
		w.mu.Unlock()
	}
	snap.Running = len(e.running)
	e.mu.Unlock()
	sort.Slice(snap.Workers, func(i, j int) bool { return snap.Workers[i].ID < snap.Workers[j].ID })

	snap.MaxWorkers = e.opts.MaxWorkers
	snap.QueueLen = len(e.queue)
	snap.QueueCap = cap(e.queue)
	snap.Breaker = string(e.breaker.State())

	s := e.stats
	s.mu.Lock()
	defer s.mu.Unlock()
	snap.Completed, snap.Failed, snap.Retries = s.completed, s.failures, s.retries
	snap.UptimeSecond = int(time.Since(s.started).Seconds())

	cutoff := time.Now().Add(-time.Minute)
	i := sort.Search(len(s.finishedAt), func(i int) bool { return s.finishedAt[i].After(cutoff) })
	s.finishedAt = s.finishedAt[i:] // drop old ones so the slice does not grow forever
	snap.PerMinute = len(s.finishedAt)

	if n := len(s.latencies); n > 0 {
		sorted := append([]time.Duration(nil), s.latencies...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		snap.P50Seconds = round1(sorted[n*50/100].Seconds())
		snap.P95Seconds = round1(sorted[min(n-1, n*95/100)].Seconds())
	}
	// Newest first for the dashboard.
	for i := len(s.events) - 1; i >= 0; i-- {
		snap.Events = append(snap.Events, s.events[i])
	}
	return snap
}

// BreakerState is exposed for the health check.
func (e *Engine) BreakerState() breaker.State { return e.breaker.State() }

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }
