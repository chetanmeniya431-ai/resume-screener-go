package pipeline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chetanmeniya431-ai/resume-screener-go/internal/breaker"
	"github.com/chetanmeniya431-ai/resume-screener-go/internal/ollama"
	"github.com/chetanmeniya431-ai/resume-screener-go/internal/store"
)

// ── In-memory fakes ─────────────────────────────────────────────────────────

type fakeRepo struct {
	mu    sync.Mutex
	job   store.Job
	cands map[int64]*store.Candidate
	saved map[int64]int // how many times each candidate was saved
}

func newFakeRepo(n int) *fakeRepo {
	r := &fakeRepo{
		job:   store.Job{ID: 1, Title: "Go Backend Engineer", MustHave: []string{"Go", "Docker"}, MinYears: 3},
		cands: map[int64]*store.Candidate{},
		saved: map[int64]int{},
	}
	for i := 1; i <= n; i++ {
		r.cands[int64(i)] = &store.Candidate{ID: int64(i), JobID: 1, FileName: "r.txt", Status: store.StatusQueued,
			RawText: "Jane Doe\nBackend engineer with Go and Docker.\n\nBuilt services in Go for 5 years."}
	}
	return r
}

func (r *fakeRepo) ResetInterrupted(context.Context) error { return nil }

func (r *fakeRepo) QueuedIDs(_ context.Context, limit int, skip map[int64]bool) ([]int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []int64
	for id := int64(1); id <= int64(len(r.cands)); id++ {
		if r.cands[id].Status == store.StatusQueued && !skip[id] && len(ids) < limit {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (r *fakeRepo) ClaimForProcessing(_ context.Context, id int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.cands[id]
	if c.Status != store.StatusQueued {
		return false, nil
	}
	c.Status = store.StatusProcessing
	c.Attempts++
	return true, nil
}

func (r *fakeRepo) CountAttempt(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cands[id].Attempts++
	return nil
}

func (r *fakeRepo) GetCandidate(_ context.Context, id int64) (store.Candidate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return *r.cands[id], nil
}

func (r *fakeRepo) GetJob(context.Context, int64) (store.Job, error) { return r.job, nil }
func (r *fakeRepo) JobEmbedding(context.Context, int64) ([]float32, error) {
	return nil, nil
}
func (r *fakeRepo) SetJobEmbedding(context.Context, int64, []float32) error { return nil }

func (r *fakeRepo) SaveResult(_ context.Context, c store.Candidate, _ []store.Chunk) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c.Status = store.StatusDone
	c.Attempts = r.cands[c.ID].Attempts // like the real store: saving does not touch attempts
	r.cands[c.ID] = &c
	r.saved[c.ID]++
	return nil
}

func (r *fakeRepo) MarkFailed(_ context.Context, id int64, msg string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cands[id].Status = store.StatusFailed
	r.cands[id].Error = msg
	return nil
}

func (r *fakeRepo) SetStatus(_ context.Context, id int64, st string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cands[id].Status = st
	return nil
}

func (r *fakeRepo) CancelQueued(_ context.Context, jobID int64) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var n int64
	for _, c := range r.cands {
		if c.JobID == jobID && c.Status == store.StatusQueued {
			c.Status = store.StatusCancelled
			n++
		}
	}
	return n, nil
}

func (r *fakeRepo) count(status string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.cands {
		if c.Status == status {
			n++
		}
	}
	return n
}

type fakeAI struct {
	delay     time.Duration
	failFirst int32 // fail this many chat calls before succeeding
	calls     atomic.Int32
	active    atomic.Int32
	maxActive atomic.Int32
}

func (f *fakeAI) track(ctx context.Context) error {
	n := f.active.Add(1)
	defer f.active.Add(-1)
	for {
		m := f.maxActive.Load()
		if n <= m || f.maxActive.CompareAndSwap(m, n) {
			break
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(f.delay):
		return nil
	}
}

func (f *fakeAI) Embed(ctx context.Context, in []string) ([][]float32, error) {
	if err := f.track(ctx); err != nil {
		return nil, err
	}
	out := make([][]float32, len(in))
	for i := range out {
		out[i] = []float32{1, 0.5, 0.25}
	}
	return out, nil
}

func (f *fakeAI) ChatJSON(ctx context.Context, _ []ollama.Message, _ any, out any) error {
	if err := f.track(ctx); err != nil {
		return err
	}
	if f.calls.Add(1) <= f.failFirst {
		return errors.New("connection reset")
	}
	p := out.(*profile)
	*p = profile{Name: "Jane Doe", CurrentTitle: "Backend Engineer", YearsExperience: 5, Skills: []string{"Go", "Docker"}, Summary: "Strong Go match."}
	return nil
}

func newEngine(repo Repo, ai AI, workers int) *Engine {
	return New(repo, ai, breaker.New(100, time.Second), Options{
		Workers: workers, MaxWorkers: 8, QueueSize: 4, MaxAttempts: 3,
		TaskTimeout: 5 * time.Second, Backoff: 5 * time.Millisecond, Poll: 10 * time.Millisecond,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ── Tests ───────────────────────────────────────────────────────────────────

func TestAllResumesProcessedExactlyOnceInParallel(t *testing.T) {
	repo := newFakeRepo(30)
	ai := &fakeAI{delay: 10 * time.Millisecond}
	e := newEngine(repo, ai, 4)
	ctx, cancel := context.WithCancel(context.Background())
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "all done", func() bool { return repo.count(store.StatusDone) == 30 })
	cancel()
	e.Wait()

	for id, n := range repo.saved {
		if n != 1 {
			t.Errorf("candidate %d saved %d times, want 1", id, n)
		}
	}
	if got := ai.maxActive.Load(); got < 2 {
		t.Errorf("max parallel AI calls = %d, want at least 2 (workers should overlap)", got)
	}
	if got := ai.maxActive.Load(); got > 4 {
		t.Errorf("max parallel AI calls = %d, want at most 4 workers", got)
	}
	snap := e.Snapshot()
	if snap.Completed != 30 || snap.P50Seconds < 0 {
		t.Errorf("snapshot completed=%d, want 30", snap.Completed)
	}
}

func TestTransientErrorsAreRetried(t *testing.T) {
	repo := newFakeRepo(1)
	ai := &fakeAI{failFirst: 2}
	e := newEngine(repo, ai, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.Start(ctx)
	waitFor(t, "done after retries", func() bool { return repo.count(store.StatusDone) == 1 })
	if s := e.Snapshot(); s.Retries != 2 {
		t.Errorf("retries = %d, want 2", s.Retries)
	}
	c, _ := repo.GetCandidate(ctx, 1)
	if c.Attempts != 3 {
		t.Errorf("attempts = %d, want 3", c.Attempts)
	}
}

func TestFailsAfterMaxAttempts(t *testing.T) {
	repo := newFakeRepo(1)
	ai := &fakeAI{failFirst: 100}
	e := newEngine(repo, ai, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.Start(ctx)
	waitFor(t, "failed", func() bool { return repo.count(store.StatusFailed) == 1 })
	if got := ai.calls.Load(); got != 3 {
		t.Errorf("chat calls = %d, want 3 (MaxAttempts)", got)
	}
}

func TestCancelStopsQueuedAndRunning(t *testing.T) {
	repo := newFakeRepo(20)
	ai := &fakeAI{delay: 200 * time.Millisecond}
	e := newEngine(repo, ai, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.Start(ctx)
	waitFor(t, "some running", func() bool { return e.Snapshot().Running > 0 })

	if _, err := e.Cancel(ctx, 1); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "nothing running", func() bool { return e.Snapshot().Running == 0 })
	if q := repo.count(store.StatusQueued) + repo.count(store.StatusProcessing); q != 0 {
		t.Errorf("%d resumes still queued or processing after cancel", q)
	}
	if repo.count(store.StatusCancelled) == 0 {
		t.Error("expected cancelled resumes")
	}
}

func TestResizeChangesWorkerCount(t *testing.T) {
	repo := newFakeRepo(0)
	e := newEngine(repo, &fakeAI{}, 2)
	ctx, cancel := context.WithCancel(context.Background())
	e.Start(ctx)

	if n := e.Resize(6); n != 6 || len(e.Snapshot().Workers) != 6 {
		t.Fatalf("after grow: %d workers", len(e.Snapshot().Workers))
	}
	if n := e.Resize(1); n != 1 || len(e.Snapshot().Workers) != 1 {
		t.Fatalf("after shrink: %d workers", len(e.Snapshot().Workers))
	}
	if n := e.Resize(99); n != 8 {
		t.Fatalf("Resize(99) = %d, want capped at MaxWorkers 8", n)
	}
	cancel()
	e.Wait() // must not hang: removed workers exit too
}

func TestShutdownLeavesUnfinishedWorkQueued(t *testing.T) {
	repo := newFakeRepo(10)
	ai := &fakeAI{delay: time.Second}
	e := newEngine(repo, ai, 3)
	ctx, cancel := context.WithCancel(context.Background())
	e.Start(ctx)
	waitFor(t, "some running", func() bool { return e.Snapshot().Running > 0 })
	cancel()
	e.Wait()
	if n := repo.count(store.StatusProcessing); n != 0 {
		t.Errorf("%d resumes stuck in processing after shutdown", n)
	}
	if n := repo.count(store.StatusQueued); n != 10 {
		t.Errorf("queued after shutdown = %d, want 10 (all go back to the queue)", n)
	}
}
