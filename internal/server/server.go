// Package server is the web interface: pages, uploads, the live dashboard feed and the chat.
package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chetanmeniya431-ai/resume-screener-go/internal/ollama"
	"github.com/chetanmeniya431-ai/resume-screener-go/internal/pipeline"
	"github.com/chetanmeniya431-ai/resume-screener-go/internal/seed"
	"github.com/chetanmeniya431-ai/resume-screener-go/internal/store"
	"github.com/chetanmeniya431-ai/resume-screener-go/internal/textract"
	"github.com/chetanmeniya431-ai/resume-screener-go/web"
)

type Options struct {
	MaxUploadFiles int
	MaxUploadBytes int64
	MaxJobs        int
	ChatModel      string
	EmbedModel     string
}

type Server struct {
	store  *store.Store
	engine *pipeline.Engine
	ai     *ollama.Client
	log    *slog.Logger
	opts   Options
	pages  map[string]*template.Template

	uploadLimit *limiter
	chatLimit   *limiter
	writeLimit  *limiter
}

func New(st *store.Store, eng *pipeline.Engine, ai *ollama.Client, log *slog.Logger, opts Options) (*Server, error) {
	s := &Server{
		store: st, engine: eng, ai: ai, log: log, opts: opts,
		uploadLimit: newLimiter(10, 10*time.Minute),
		chatLimit:   newLimiter(20, 10*time.Minute),
		writeLimit:  newLimiter(30, 10*time.Minute),
	}
	var err error
	s.pages, err = loadPages()
	return s, err
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(web.FS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(http.FileServerFS(static))))

	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("POST /jobs", s.createJob)
	mux.HandleFunc("GET /jobs/{id}", s.showJob)
	mux.HandleFunc("GET /jobs/{id}/status", s.jobStatus)
	mux.HandleFunc("POST /jobs/{id}/resumes", s.uploadResumes)
	mux.HandleFunc("POST /jobs/{id}/cancel", s.cancelJob)
	mux.HandleFunc("POST /jobs/{id}/retry", s.retryJob)
	mux.HandleFunc("GET /jobs/{id}/export.csv", s.exportCSV)
	mux.HandleFunc("POST /jobs/{id}/chat", s.chat)
	mux.HandleFunc("GET /candidates/{id}", s.showCandidate)
	mux.HandleFunc("GET /pipeline", s.pipelinePage)
	mux.HandleFunc("GET /pipeline/stream", s.pipelineStream)
	mux.HandleFunc("POST /pipeline/workers", s.setWorkers)
	mux.HandleFunc("GET /samples.zip", s.samples)
	mux.HandleFunc("GET /healthz", s.health)

	return s.recoverer(s.securityHeaders(mux))
}

// ── Pages ───────────────────────────────────────────────────────────────────

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.store.ListJobs(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, "home", map[string]any{"Jobs": jobs, "Flash": r.URL.Query().Get("msg")})
}

func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	if !s.writeLimit.allow(clientIP(r)) {
		http.Error(w, "Too many changes. Please wait a few minutes.", http.StatusTooManyRequests)
		return
	}
	if n, err := s.store.CountJobs(r.Context()); err == nil && n >= s.opts.MaxJobs {
		redirectMsg(w, r, "/", "This demo already has the maximum number of jobs. Try one of the existing jobs.")
		return
	}
	j := store.Job{
		Title:       strings.TrimSpace(r.FormValue("title")),
		Description: strings.TrimSpace(r.FormValue("description")),
		MustHave:    store.SplitList(r.FormValue("must_have")),
		NiceToHave:  store.SplitList(r.FormValue("nice_to_have")),
	}
	j.MinYears, _ = strconv.Atoi(r.FormValue("min_years"))
	switch {
	case j.Title == "" || len(j.Title) > 200:
		redirectMsg(w, r, "/", "Please give the job a title (up to 200 characters).")
		return
	case len(j.Description) < 20 || len(j.Description) > 4000:
		redirectMsg(w, r, "/", "Please write a job description of 20 to 4,000 characters.")
		return
	case len(j.MustHave) == 0 || len(j.MustHave) > 12 || len(j.NiceToHave) > 12:
		redirectMsg(w, r, "/", "Please add 1 to 12 must-have skills (and up to 12 nice-to-have).")
		return
	case j.MinYears < 0 || j.MinYears > 40:
		redirectMsg(w, r, "/", "Minimum years must be between 0 and 40.")
		return
	}
	id, err := s.store.CreateJob(r.Context(), j)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/jobs/%d?msg=%s", id, "Job created. Now upload some resumes."), http.StatusSeeOther)
}

func (s *Server) showJob(w http.ResponseWriter, r *http.Request) {
	job, ok := s.jobFromPath(w, r)
	if !ok {
		return
	}
	cands, err := s.store.ListCandidates(r.Context(), job.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, "job", map[string]any{
		"Job": job, "Candidates": cands, "Flash": r.URL.Query().Get("msg"),
		"MaxFiles": s.opts.MaxUploadFiles, "MaxKB": s.opts.MaxUploadBytes / 1024,
	})
}

// jobStatus lets the job page refresh its counts without reloading.
func (s *Server) jobStatus(w http.ResponseWriter, r *http.Request) {
	job, ok := s.jobFromPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, map[string]int{"total": job.Total, "done": job.Done, "failed": job.Failed, "pending": job.Pending})
}

func (s *Server) showCandidate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	c, err := s.store.GetCandidate(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	job, err := s.store.GetJob(r.Context(), c.JobID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, "candidate", map[string]any{"Job": job, "C": c})
}

// ── Upload ──────────────────────────────────────────────────────────────────

type parsed struct {
	name string
	text string
	err  error
}

// uploadResumes reads the files in parallel (text extraction is CPU work), saves
// the good ones as queued, and wakes the pipeline. Bad files are reported, not fatal.
func (s *Server) uploadResumes(w http.ResponseWriter, r *http.Request) {
	job, ok := s.jobFromPath(w, r)
	if !ok {
		return
	}
	if !s.uploadLimit.allow(clientIP(r)) {
		redirectMsg(w, r, jobURL(job.ID), "Too many uploads. Please wait a few minutes.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(s.opts.MaxUploadFiles)*s.opts.MaxUploadBytes+1<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		redirectMsg(w, r, jobURL(job.ID), "Upload too large. Send fewer or smaller files.")
		return
	}
	files := r.MultipartForm.File["resumes"]
	if len(files) == 0 {
		redirectMsg(w, r, jobURL(job.ID), "Please choose at least one resume file.")
		return
	}
	if len(files) > s.opts.MaxUploadFiles {
		redirectMsg(w, r, jobURL(job.ID), fmt.Sprintf("Please upload at most %d files at a time.", s.opts.MaxUploadFiles))
		return
	}

	results := make([]parsed, len(files))
	sem := make(chan struct{}, 4) // at most 4 files parsed at once
	var wg sync.WaitGroup
	for i, fh := range files {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			name := filepath.Base(fh.Filename)
			results[i].name = name
			if !textract.Allowed(name) {
				results[i].err = fmt.Errorf("%s: only PDF, TXT or MD", name)
				return
			}
			if fh.Size > s.opts.MaxUploadBytes {
				results[i].err = fmt.Errorf("%s is bigger than %d KB", name, s.opts.MaxUploadBytes/1024)
				return
			}
			f, err := fh.Open()
			if err != nil {
				results[i].err = err
				return
			}
			defer f.Close()
			data, err := io.ReadAll(io.LimitReader(f, s.opts.MaxUploadBytes+1))
			if err != nil {
				results[i].err = err
				return
			}
			results[i].text, results[i].err = textract.Extract(name, data)
		}(i)
	}
	wg.Wait()

	var good []store.NewCandidate
	var problems []string
	for _, p := range results {
		if p.err != nil {
			problems = append(problems, p.err.Error())
			continue
		}
		good = append(good, store.NewCandidate{FileName: p.name, RawText: p.text})
	}
	if len(good) > 0 {
		if _, err := s.store.InsertCandidates(r.Context(), job.ID, good); err != nil {
			s.serverError(w, r, err)
			return
		}
		s.engine.Notify()
	}
	msg := fmt.Sprintf("%d resume(s) added to the queue.", len(good))
	if len(problems) > 0 {
		msg += " Skipped: " + strings.Join(problems, "; ")
	}
	redirectMsg(w, r, jobURL(job.ID), msg)
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	job, ok := s.jobFromPath(w, r)
	if !ok {
		return
	}
	n, err := s.engine.Cancel(r.Context(), job.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	redirectMsg(w, r, jobURL(job.ID), fmt.Sprintf("Stopped %d resume(s).", n))
}

func (s *Server) retryJob(w http.ResponseWriter, r *http.Request) {
	job, ok := s.jobFromPath(w, r)
	if !ok {
		return
	}
	if !s.writeLimit.allow(clientIP(r)) {
		redirectMsg(w, r, jobURL(job.ID), "Too many changes. Please wait a few minutes.")
		return
	}
	n, err := s.store.Requeue(r.Context(), job.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.engine.Notify()
	redirectMsg(w, r, jobURL(job.ID), fmt.Sprintf("%d resume(s) queued again.", n))
}

func (s *Server) exportCSV(w http.ResponseWriter, r *http.Request) {
	job, ok := s.jobFromPath(w, r)
	if !ok {
		return
	}
	cands, err := s.store.ListCandidates(r.Context(), job.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="shortlist-job-%d.csv"`, job.ID))
	cw := csv.NewWriter(w)
	cw.Write([]string{"rank", "name", "score", "current_title", "years", "matched_must_have", "missing_must_have", "nice_to_have", "summary", "file"})
	rank := 0
	for _, c := range cands {
		if c.Status != store.StatusDone {
			continue
		}
		rank++
		cw.Write([]string{strconv.Itoa(rank), safeCSV(c.Name), strconv.Itoa(c.Score), safeCSV(c.CurrentTitle),
			strconv.FormatFloat(c.Years, 'f', 1, 64), strings.Join(c.Matched, "; "), strings.Join(c.Missing, "; "),
			strings.Join(c.NiceMatched, "; "), safeCSV(c.Summary), safeCSV(c.FileName)})
	}
	cw.Flush()
}

// safeCSV stops spreadsheet apps from running a cell as a formula.
func safeCSV(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

func (s *Server) samples(w http.ResponseWriter, r *http.Request) {
	b, err := seed.SampleZip()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="sample-resumes.zip"`)
	w.Write(b)
}

// ── Live pipeline dashboard ─────────────────────────────────────────────────

func (s *Server) pipelinePage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "pipeline", map[string]any{"Snap": s.engine.Snapshot()})
}

// pipelineStream sends a snapshot every 500 ms using Server-Sent Events
// (a simple one-way stream from server to browser).
func (s *Server) pipelineStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // tell nginx not to buffer

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var last []byte
	for {
		b, _ := json.Marshal(s.engine.Snapshot())
		if string(b) != string(last) {
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
			last = b
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) setWorkers(w http.ResponseWriter, r *http.Request) {
	if !s.writeLimit.allow(clientIP(r)) {
		http.Error(w, "Too many changes. Please wait a few minutes.", http.StatusTooManyRequests)
		return
	}
	n, err := strconv.Atoi(r.FormValue("workers"))
	if err != nil {
		http.Error(w, "workers must be a number", http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]int{"workers": s.engine.Resize(n)})
}

// ── Chat over resumes (RAG) ─────────────────────────────────────────────────

type chatRequest struct {
	Question string           `json:"question"`
	History  []ollama.Message `json:"history"`
}

type source struct {
	CandidateID int64   `json:"candidate_id"`
	Name        string  `json:"name"`
	Snippet     string  `json:"snippet"`
	Match       float64 `json:"match"`
}

// chat answers a question using only the resumes of this job. It streams the
// answer as Server-Sent Events: first the sources, then the text as it is written.
func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	job, ok := s.jobFromPath(w, r)
	if !ok {
		return
	}
	if !s.chatLimit.allow(clientIP(r)) {
		http.Error(w, "Too many questions. Please wait a few minutes.", http.StatusTooManyRequests)
		return
	}
	var req chatRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req.Question = strings.TrimSpace(req.Question)
	if req.Question == "" || len(req.Question) > 500 {
		http.Error(w, "Please ask a question of up to 500 characters.", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(event string, v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		flusher.Flush()
	}

	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Minute)
	defer cancel()

	chunks, err := s.store.ChunksForJob(ctx, job.ID)
	if err != nil {
		send("error", "Could not load resumes.")
		return
	}
	if len(chunks) == 0 {
		send("error", "No screened resumes yet for this job. Wait for screening to finish, then ask again.")
		return
	}
	send("status", "Searching resumes…")
	qv, err := s.ai.Embed(ctx, []string{ollama.QueryPrefix + req.Question})
	if err != nil {
		s.log.Error("chat embed", "err", err)
		send("error", "The AI server is not answering right now. Please try again in a minute.")
		return
	}
	hits := pipelineDiverse(chunks, qv[0])

	cands, _ := s.store.ListCandidates(ctx, job.ID)
	sources := make([]source, 0, len(hits))
	var ctxText strings.Builder
	for i, h := range hits {
		fmt.Fprintf(&ctxText, "[%d] Candidate: %s\n%s\n\n", i+1, h.Chunk.CandidateName, h.Chunk.Text)
		sources = append(sources, source{
			CandidateID: h.Chunk.CandidateID, Name: h.Chunk.CandidateName,
			Snippet: snippet(h.Chunk.Text, 200), Match: math.Round(h.Score*100) / 100,
		})
	}
	send("sources", sources)

	msgs := []ollama.Message{{Role: "system", Content: chatSystemPrompt(job, cands, ctxText.String())}}
	for _, m := range lastN(req.History, 4) {
		if (m.Role == "user" || m.Role == "assistant") && len(m.Content) <= 2000 {
			msgs = append(msgs, m)
		}
	}
	msgs = append(msgs, ollama.Message{Role: "user", Content: req.Question})

	send("status", "Writing answer…")
	err = s.ai.ChatStream(ctx, msgs, func(tok string) error {
		send("token", tok)
		return ctx.Err()
	})
	switch {
	case r.Context().Err() != nil:
		return // visitor left
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		send("error", "The AI server is busy and the answer took too long. Please try again in a minute.")
		return
	case err != nil:
		s.log.Error("chat stream", "err", err)
		send("error", "The answer stopped early. Please try again.")
		return
	}
	send("done", true)
}

func chatSystemPrompt(job store.Job, cands []store.Candidate, context string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, `You help a recruiter compare candidates for the job "%s".
Must have: %s. Nice to have: %s. Minimum years: %d.

Rules:
- Use ONLY the shortlist and resume extracts below. If the answer is not there, say you could not find it.
- Only mention candidates that match. Never list people with "not mentioned" or "no information".
- Name the candidate for every fact, like: Meera Iyer has 6 years of Laravel.
- Mention each candidate once. At most 6 bullet points, one short line each. Plain English.
- Never invent skills, companies, dates or numbers.

SHORTLIST (screened candidates, best score first):
`, job.Title, strings.Join(job.MustHave, ", "), strings.Join(job.NiceToHave, ", "), job.MinYears)
	n := 0
	for _, c := range cands {
		if c.Status != store.StatusDone {
			continue
		}
		n++
		fmt.Fprintf(&sb, "%d. %s — score %d/100, %.0f years, %s. Has: %s. Missing: %s.\n",
			n, c.Name, c.Score, c.Years, c.CurrentTitle, orNone(c.Matched), orNone(c.Missing))
		if n == 10 {
			break
		}
	}
	sb.WriteString("\nRESUME EXTRACTS (found by search for this question):\n")
	sb.WriteString(context)
	return sb.String()
}

// ── Health ──────────────────────────────────────────────────────────────────

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	out := map[string]string{"app": "ok", "database": "ok", "ai": "ok", "breaker": string(s.engine.BreakerState())}
	code := http.StatusOK
	if err := s.store.Ping(ctx); err != nil {
		out["database"] = "down"
		code = http.StatusServiceUnavailable
	}
	if err := s.ai.Ping(ctx); err != nil {
		out["ai"] = "down" // the app still serves pages; screening waits
	}
	w.WriteHeader(code)
	writeJSON(w, out)
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func (s *Server) jobFromPath(w http.ResponseWriter, r *http.Request) (store.Job, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return store.Job{}, false
	}
	job, err := s.store.GetJob(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return job, false
	} else if err != nil {
		s.serverError(w, r, err)
		return job, false
	}
	return job, true
}

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed", "path", r.URL.Path, "err", err)
	http.Error(w, "Something went wrong. Please try again.", http.StatusInternalServerError)
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("panic", "path", r.URL.Path, "err", v)
				http.Error(w, "Something went wrong.", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' https://fonts.googleapis.com; font-src https://fonts.gstatic.com; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func redirectMsg(w http.ResponseWriter, r *http.Request, to, msg string) {
	http.Redirect(w, r, to+"?msg="+urlEscape(msg), http.StatusSeeOther)
}

func jobURL(id int64) string { return fmt.Sprintf("/jobs/%d", id) }

func snippet(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := strings.LastIndex(s[:n], " ")
	if cut < n/2 {
		cut = n
	}
	return s[:cut] + "…"
}

func orNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}

func lastN(m []ollama.Message, n int) []ollama.Message {
	if len(m) > n {
		return m[len(m)-n:]
	}
	return m
}
