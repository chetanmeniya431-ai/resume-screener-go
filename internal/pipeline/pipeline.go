// Package pipeline screens resumes in parallel.
//
// How it works:
//
//	MySQL (status = queued) ──dispatcher──▶ buffered channel ──▶ N workers ──▶ Ollama
//
//   - The database is the source of truth. If the app restarts, nothing is lost:
//     queued resumes are picked up again.
//   - The dispatcher fills a buffered channel. When every worker is busy and the
//     channel is full, the dispatcher blocks. That is back-pressure: we never load
//     more work into memory than the workers can take.
//   - The number of workers can be changed while it runs. Extra workers finish their
//     current resume and then stop, so no work is thrown away.
//   - Each AI call goes through a circuit breaker and is retried with back-off.
//   - A job can be cancelled; resumes being processed stop through their context.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chetanmeniya431-ai/resume-screener-go/internal/breaker"
	"github.com/chetanmeniya431-ai/resume-screener-go/internal/ollama"
	"github.com/chetanmeniya431-ai/resume-screener-go/internal/rag"
	"github.com/chetanmeniya431-ai/resume-screener-go/internal/scoring"
	"github.com/chetanmeniya431-ai/resume-screener-go/internal/store"
)

// Repo is the part of the store the pipeline needs. Tests pass an in-memory fake.
type Repo interface {
	ResetInterrupted(ctx context.Context) error
	QueuedIDs(ctx context.Context, limit int, skip map[int64]bool) ([]int64, error)
	ClaimForProcessing(ctx context.Context, id int64) (bool, error)
	CountAttempt(ctx context.Context, id int64) error
	GetCandidate(ctx context.Context, id int64) (store.Candidate, error)
	GetJob(ctx context.Context, id int64) (store.Job, error)
	JobEmbedding(ctx context.Context, jobID int64) ([]float32, error)
	SetJobEmbedding(ctx context.Context, jobID int64, v []float32) error
	SaveResult(ctx context.Context, c store.Candidate, chunks []store.Chunk) error
	MarkFailed(ctx context.Context, id int64, msg string) error
	SetStatus(ctx context.Context, id int64, status string) error
	CancelQueued(ctx context.Context, jobID int64) (int64, error)
}

// AI is the part of the Ollama client the pipeline needs.
type AI interface {
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
	ChatJSON(ctx context.Context, msgs []ollama.Message, schema any, out any) error
}

type Options struct {
	Workers     int
	MaxWorkers  int
	QueueSize   int
	MaxAttempts int
	TaskTimeout time.Duration // per resume, all attempts included
	Backoff     time.Duration // first retry wait; doubles each time
	Poll        time.Duration // how often the dispatcher looks for new work without a nudge
}

type Engine struct {
	repo    Repo
	ai      AI
	breaker *breaker.Breaker
	opts    Options
	log     *slog.Logger

	queue chan int64
	nudge chan struct{}

	mu       sync.Mutex
	ctx      context.Context
	workers  map[int]*worker
	nextID   int
	inflight map[int64]bool               // handed to the channel or a worker
	running  map[int64]context.CancelFunc // being processed right now
	runJob   map[int64]int64              // candidate id -> job id, for cancel

	jobVecMu sync.Mutex
	jobVecs  map[int64][]float32

	stats *stats
	wg    sync.WaitGroup
}

func New(repo Repo, ai AI, br *breaker.Breaker, opts Options, log *slog.Logger) *Engine {
	if opts.Backoff == 0 {
		opts.Backoff = time.Second
	}
	if opts.Poll == 0 {
		opts.Poll = 3 * time.Second
	}
	return &Engine{
		repo:     repo,
		ai:       ai,
		breaker:  br,
		opts:     opts,
		log:      log,
		queue:    make(chan int64, opts.QueueSize),
		nudge:    make(chan struct{}, 1),
		workers:  map[int]*worker{},
		inflight: map[int64]bool{},
		running:  map[int64]context.CancelFunc{},
		runJob:   map[int64]int64{},
		jobVecs:  map[int64][]float32{},
		stats:    newStats(),
	}
}

// Start resets interrupted work and launches the dispatcher and workers.
// It stops when ctx is cancelled; call Wait afterwards for a clean shutdown.
func (e *Engine) Start(ctx context.Context) error {
	if err := e.repo.ResetInterrupted(ctx); err != nil {
		return err
	}
	e.mu.Lock()
	e.ctx = ctx
	e.mu.Unlock()

	e.wg.Add(1)
	go e.dispatch(ctx)
	e.Resize(e.opts.Workers)
	e.stats.event("Pipeline started with %d workers", e.opts.Workers)
	return nil
}

// Wait blocks until the dispatcher and every worker have stopped.
func (e *Engine) Wait() { e.wg.Wait() }

// Notify tells the dispatcher that new resumes were queued.
func (e *Engine) Notify() {
	select {
	case e.nudge <- struct{}{}:
	default: // a nudge is already pending
	}
}

func (e *Engine) dispatch(ctx context.Context) {
	defer e.wg.Done()
	ticker := time.NewTicker(e.opts.Poll)
	defer ticker.Stop()
	for {
		e.fill(ctx)
		select {
		case <-ctx.Done():
			return
		case <-e.nudge:
		case <-ticker.C:
		}
	}
}

// fill moves queued resumes from the database into the channel until the channel is full.
func (e *Engine) fill(ctx context.Context) {
	for {
		free := cap(e.queue) - len(e.queue)
		if free == 0 {
			return
		}
		e.mu.Lock()
		skip := make(map[int64]bool, len(e.inflight))
		for id := range e.inflight {
			skip[id] = true
		}
		e.mu.Unlock()

		ids, err := e.repo.QueuedIDs(ctx, free, skip)
		if err != nil {
			if ctx.Err() == nil {
				e.log.Error("dispatcher: load queue", "err", err)
			}
			return
		}
		if len(ids) == 0 {
			return
		}
		for _, id := range ids {
			e.mu.Lock()
			e.inflight[id] = true
			e.mu.Unlock()
			select {
			case e.queue <- id: // blocks when full: back-pressure
			case <-ctx.Done():
				return
			}
		}
	}
}

// ── Worker pool ─────────────────────────────────────────────────────────────

type worker struct {
	id   int
	quit chan struct{}

	mu      sync.Mutex
	state   string
	cand    string
	since   time.Time
	handled int
}

func (w *worker) set(state, cand string) {
	w.mu.Lock()
	w.state, w.cand, w.since = state, cand, time.Now()
	w.mu.Unlock()
}

// Resize changes the number of workers while the pipeline runs.
func (e *Engine) Resize(n int) int {
	n = max(1, min(n, e.opts.MaxWorkers))
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ctx == nil {
		return 0
	}
	ids := make([]int, 0, len(e.workers))
	for id := range e.workers {
		ids = append(ids, id)
	}
	sort.Ints(ids)

	for len(ids) < n {
		e.nextID++
		w := &worker{id: e.nextID, quit: make(chan struct{}), state: "idle", since: time.Now()}
		e.workers[w.id] = w
		ids = append(ids, w.id)
		e.wg.Add(1)
		go e.run(e.ctx, w)
	}
	for len(ids) > n {
		last := ids[len(ids)-1]
		ids = ids[:len(ids)-1]
		close(e.workers[last].quit) // finishes its current resume, then exits
		delete(e.workers, last)
	}
	return n
}

func (e *Engine) run(ctx context.Context, w *worker) {
	defer e.wg.Done()
	for {
		// Check quit first so a removed worker never takes new work.
		select {
		case <-w.quit:
			return
		case <-ctx.Done():
			return
		default:
		}
		select {
		case <-w.quit:
			return
		case <-ctx.Done():
			return
		case id := <-e.queue:
			e.handle(ctx, w, id)
			w.set("idle", "")
		}
	}
}

func (e *Engine) handle(parent context.Context, w *worker, id int64) {
	defer func() {
		e.mu.Lock()
		delete(e.inflight, id)
		delete(e.running, id)
		delete(e.runJob, id)
		e.mu.Unlock()
	}()

	ok, err := e.repo.ClaimForProcessing(parent, id)
	if err != nil || !ok {
		return // cancelled while waiting in the channel, or DB error (it stays queued)
	}
	c, err := e.repo.GetCandidate(parent, id)
	if err != nil {
		e.log.Error("load candidate", "id", id, "err", err)
		return
	}

	ctx, cancel := context.WithTimeout(parent, e.opts.TaskTimeout)
	defer cancel()
	e.mu.Lock()
	e.running[id] = cancel
	e.runJob[id] = c.JobID
	e.mu.Unlock()

	start := time.Now()
	w.set("starting", c.FileName)
	res, err := e.process(ctx, w, c)
	elapsed := time.Since(start)

	// Use a fresh context for the final write: the task context may be cancelled.
	saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
	defer saveCancel()

	switch {
	case err == nil:
		res.cand.DurationMs = int(elapsed.Milliseconds())
		w.set("saving", c.FileName)
		if err := e.repo.SaveResult(saveCtx, res.cand, res.chunks); err != nil {
			e.repo.MarkFailed(saveCtx, id, "save failed: "+err.Error())
			e.stats.failed(fmt.Sprintf("Worker %d: %s could not be saved", w.id, c.FileName))
			return
		}
		e.stats.done(elapsed, fmt.Sprintf("Worker %d screened %s → %s, score %d (%.1fs)",
			w.id, c.FileName, res.cand.Name, res.cand.Score, elapsed.Seconds()))
	case parent.Err() != nil:
		// App shutting down: leave it queued so it runs again after restart.
		e.repo.SetStatus(saveCtx, id, store.StatusQueued)
	case errors.Is(ctx.Err(), context.Canceled):
		e.repo.SetStatus(saveCtx, id, store.StatusCancelled)
		e.stats.event("Worker %d: %s cancelled", w.id, c.FileName)
	default:
		e.repo.MarkFailed(saveCtx, id, err.Error())
		e.stats.failed(fmt.Sprintf("Worker %d: %s failed: %s", w.id, c.FileName, shorten(err.Error(), 90)))
	}
	w.mu.Lock()
	w.handled++
	w.mu.Unlock()
}

// Cancel stops a job: waiting resumes are marked cancelled and running ones are interrupted.
func (e *Engine) Cancel(ctx context.Context, jobID int64) (int, error) {
	n, err := e.repo.CancelQueued(ctx, jobID)
	if err != nil {
		return 0, err
	}
	stopped := 0
	e.mu.Lock()
	for id, job := range e.runJob {
		if job == jobID {
			e.running[id]()
			stopped++
		}
	}
	e.mu.Unlock()
	e.stats.event("Job %d cancelled: %d waiting and %d running resumes stopped", jobID, n, stopped)
	return int(n) + stopped, nil
}

// ── Screening one resume ────────────────────────────────────────────────────

type result struct {
	cand   store.Candidate
	chunks []store.Chunk
}

// profile is what the AI must return. The JSON schema below forces this shape.
type profile struct {
	Name            string   `json:"name"`
	CurrentTitle    string   `json:"current_title"`
	YearsExperience float64  `json:"years_experience"`
	Skills          []string `json:"skills"`
	Education       string   `json:"education"`
	Summary         string   `json:"summary"`
}

var profileSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"name":             map[string]any{"type": "string"},
		"current_title":    map[string]any{"type": "string"},
		"years_experience": map[string]any{"type": "number"},
		"skills":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"education":        map[string]any{"type": "string"},
		"summary":          map[string]any{"type": "string"},
	},
	"required": []string{"name", "current_title", "years_experience", "skills", "education", "summary"},
}

const maxResumeWords = 1800 // keeps the prompt inside the model's context window

func (e *Engine) process(ctx context.Context, w *worker, c store.Candidate) (result, error) {
	job, err := e.repo.GetJob(ctx, c.JobID)
	if err != nil {
		return result{}, err
	}

	// 1. The job's own vector (computed once, then cached).
	w.set("embedding job", c.FileName)
	jobVec, err := e.jobVector(ctx, w, c.ID, job)
	if err != nil {
		return result{}, err
	}

	// 2. Split the resume into chunks and embed them in one call (for search and chat).
	w.set("embedding resume", c.FileName)
	texts := rag.Chunk(c.RawText, 120, 20)
	if len(texts) == 0 {
		return result{}, errors.New("resume has no text")
	}
	inputs := make([]string, len(texts))
	for i, t := range texts {
		inputs[i] = ollama.DocPrefix + t
	}
	var vecs [][]float32
	err = e.callAI(ctx, w, c.ID, func(ctx context.Context) error {
		var err error
		vecs, err = e.ai.Embed(ctx, inputs)
		return err
	})
	if err != nil {
		return result{}, fmt.Errorf("embedding: %w", err)
	}
	similarity := rag.Cosine(rag.Mean(vecs), jobVec)

	// 3. Ask the AI to read the resume and return structured JSON.
	w.set("reading resume (AI)", c.FileName)
	var p profile
	msgs := extractionPrompt(job, truncateWords(c.RawText, maxResumeWords))
	err = e.callAI(ctx, w, c.ID, func(ctx context.Context) error {
		p = profile{}
		return e.ai.ChatJSON(ctx, msgs, profileSchema, &p)
	})
	if err != nil {
		return result{}, fmt.Errorf("reading resume: %w", err)
	}

	// 4. Score in plain Go so it is repeatable and explainable.
	w.set("scoring", c.FileName)
	sc := scoring.Score(scoring.Input{Job: job, Skills: p.Skills, Years: p.YearsExperience, Text: c.RawText, Similarity: similarity})

	c.Name = strings.TrimSpace(p.Name)
	if c.Name == "" {
		c.Name = strings.TrimSuffix(c.FileName, ".pdf")
	}
	c.CurrentTitle = p.CurrentTitle
	c.Years = p.YearsExperience
	c.Skills = dedupe(p.Skills)
	c.Education = p.Education
	c.Summary = p.Summary
	c.Score = sc.Score
	c.Breakdown = sc.Breakdown
	c.Matched, c.Missing, c.NiceMatched = sc.Matched, sc.Missing, sc.NiceMatched
	c.Similarity = similarity

	chunks := make([]store.Chunk, len(texts))
	for i := range texts {
		chunks[i] = store.Chunk{CandidateID: c.ID, JobID: c.JobID, Idx: i, Text: texts[i], Embedding: vecs[i]}
	}
	return result{cand: c, chunks: chunks}, nil
}

func (e *Engine) jobVector(ctx context.Context, w *worker, candID int64, job store.Job) ([]float32, error) {
	e.jobVecMu.Lock()
	v, ok := e.jobVecs[job.ID]
	e.jobVecMu.Unlock()
	if ok {
		return v, nil
	}
	v, err := e.repo.JobEmbedding(ctx, job.ID)
	if err == nil && len(v) > 0 {
		e.cacheJobVec(job.ID, v)
		return v, nil
	}
	text := ollama.QueryPrefix + job.Title + ". " + job.Description +
		" Must have: " + strings.Join(job.MustHave, ", ") + ". Nice to have: " + strings.Join(job.NiceToHave, ", ")
	err = e.callAI(ctx, w, candID, func(ctx context.Context) error {
		out, err := e.ai.Embed(ctx, []string{text})
		if err == nil {
			v = out[0]
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("embedding job: %w", err)
	}
	_ = e.repo.SetJobEmbedding(ctx, job.ID, v)
	e.cacheJobVec(job.ID, v)
	return v, nil
}

func (e *Engine) cacheJobVec(id int64, v []float32) {
	e.jobVecMu.Lock()
	e.jobVecs[id] = v
	e.jobVecMu.Unlock()
}

// ForgetJob drops a cached job vector (call after a job's text changes).
func (e *Engine) ForgetJob(id int64) {
	e.jobVecMu.Lock()
	delete(e.jobVecs, id)
	e.jobVecMu.Unlock()
}

// callAI runs fn through the circuit breaker and retries it with exponential
// back-off and jitter. Permanent errors (such as an unknown model) are not retried.
func (e *Engine) callAI(ctx context.Context, w *worker, candID int64, fn func(context.Context) error) error {
	var err error
	for attempt := 1; attempt <= e.opts.MaxAttempts; attempt++ {
		if e.breaker.State() != breaker.Closed {
			w.set("waiting: AI server paused", w.cand)
		}
		if err := e.breaker.Wait(ctx); err != nil {
			return err
		}
		err = fn(ctx)
		if err == nil {
			e.breaker.Success()
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if ollama.IsPermanent(err) {
			e.breaker.Success() // the server answered; it is not down
			return err
		}
		e.breaker.Failure()
		if attempt == e.opts.MaxAttempts {
			break
		}
		_ = e.repo.CountAttempt(ctx, candID)
		wait := e.opts.Backoff << (attempt - 1)
		wait += time.Duration(rand.Int64N(int64(wait)/2 + 1)) // jitter so workers do not retry in step
		e.stats.retried(fmt.Sprintf("Worker %d: retry %d for %s in %.1fs (%s)", w.id, attempt, w.cand, wait.Seconds(), shorten(err.Error(), 60)))
		w.set(fmt.Sprintf("retry %d in %.0fs", attempt, wait.Seconds()), w.cand)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return err
}

func extractionPrompt(job store.Job, resume string) []ollama.Message {
	sys := `You read resumes for a recruiter. Return JSON only.
Rules:
- Use only facts written in the resume. Never guess or invent.
- "name": the candidate's full name as written.
- "current_title": their latest job title.
- "years_experience": total years of professional work, counted from the job dates. Use 0 if unclear.
- "skills": technical skills, tools and languages named in the resume. Short names, like "Go", "MySQL", "Docker".
- "education": highest degree and school, in one short line.
- "summary": ONE plain sentence (max 30 words) on how well this person fits the job below. Name the strongest match and the biggest gap.`
	user := fmt.Sprintf("JOB: %s\nMust have: %s\nNice to have: %s\nMinimum years: %d\n\nRESUME:\n%s",
		job.Title, strings.Join(job.MustHave, ", "), strings.Join(job.NiceToHave, ", "), job.MinYears, resume)
	return []ollama.Message{{Role: "system", Content: sys}, {Role: "user", Content: user}}
}

func truncateWords(s string, n int) string {
	f := strings.Fields(s)
	if len(f) <= n {
		return s
	}
	return strings.Join(f[:n], " ")
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		k := strings.ToLower(s)
		if s == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
