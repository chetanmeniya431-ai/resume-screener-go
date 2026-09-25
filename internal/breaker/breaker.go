// Package breaker is a circuit breaker: after too many failures in a row it
// stops calls for a cool-down period, so a down AI server is not hammered and
// resumes do not burn their retries while it is down.
package breaker

import (
	"context"
	"sync"
	"time"
)

type State string

const (
	Closed   State = "closed"    // normal: calls go through
	Open     State = "open"      // failing: calls wait until the cool-down ends
	HalfOpen State = "half-open" // cool-down over: one trial call decides
)

type Breaker struct {
	mu        sync.Mutex
	threshold int
	cooldown  time.Duration
	now       func() time.Time

	state    State
	failures int
	openedAt time.Time
	probing  bool
}

func New(threshold int, cooldown time.Duration) *Breaker {
	return &Breaker{threshold: threshold, cooldown: cooldown, now: time.Now, state: Closed}
}

func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refresh()
	return b.state
}

// refresh moves Open to HalfOpen once the cool-down is over. Caller holds mu.
func (b *Breaker) refresh() {
	if b.state == Open && b.now().Sub(b.openedAt) >= b.cooldown {
		b.state = HalfOpen
		b.probing = false
	}
}

// Wait blocks until a call is allowed or ctx ends. In half-open state only one
// caller at a time gets through as the trial; the others keep waiting.
func (b *Breaker) Wait(ctx context.Context) error {
	for {
		b.mu.Lock()
		b.refresh()
		switch {
		case b.state == Closed:
			b.mu.Unlock()
			return nil
		case b.state == HalfOpen && !b.probing:
			b.probing = true
			b.mu.Unlock()
			return nil
		}
		b.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// Success closes the breaker and resets the failure count.
func (b *Breaker) Success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state = Closed
	b.failures = 0
	b.probing = false
}

// Failure counts a failed call. A failed trial re-opens at once.
func (b *Breaker) Failure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	if b.state == HalfOpen || b.failures >= b.threshold {
		b.state = Open
		b.openedAt = b.now()
		b.probing = false
	}
}
