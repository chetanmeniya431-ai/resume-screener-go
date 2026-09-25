package breaker

import (
	"context"
	"testing"
	"time"
)

func TestOpensAfterThresholdAndRecovers(t *testing.T) {
	b := New(3, time.Minute)
	now := time.Now()
	b.now = func() time.Time { return now }

	b.Failure()
	b.Failure()
	if b.State() != Closed {
		t.Fatal("should stay closed below the threshold")
	}
	b.Failure()
	if b.State() != Open {
		t.Fatal("should open at the threshold")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := b.Wait(ctx); err == nil {
		t.Fatal("Wait should block while open")
	}

	now = now.Add(time.Minute)
	if b.State() != HalfOpen {
		t.Fatal("should be half-open after the cool-down")
	}
	if err := b.Wait(context.Background()); err != nil {
		t.Fatal("first caller should get the trial call")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	if err := b.Wait(ctx2); err == nil {
		t.Fatal("second caller must wait while the trial runs")
	}
	b.Success()
	if b.State() != Closed {
		t.Fatal("a good trial should close it")
	}
}

func TestFailedTrialReopens(t *testing.T) {
	b := New(1, time.Minute)
	now := time.Now()
	b.now = func() time.Time { return now }
	b.Failure()
	now = now.Add(time.Minute)
	_ = b.Wait(context.Background())
	b.Failure()
	if b.State() != Open {
		t.Fatal("a failed trial should re-open the breaker")
	}
}
