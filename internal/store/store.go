// Package store holds all MySQL access. Handlers and the pipeline never write SQL themselves.
package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

var ErrNotFound = errors.New("not found")

// Candidate statuses.
const (
	StatusQueued     = "queued"
	StatusProcessing = "processing"
	StatusDone       = "done"
	StatusFailed     = "failed"
	StatusCancelled  = "cancelled"
)

type Store struct{ db *sql.DB }

// Open connects and waits for MySQL, which may still be starting when the app container starts.
func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)

	deadline := time.Now().Add(90 * time.Second)
	for {
		err = db.PingContext(ctx)
		if err == nil {
			return &Store{db: db}, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("database not reachable: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// ── Jobs ────────────────────────────────────────────────────────────────────

type Job struct {
	ID          int64
	Title       string
	Description string
	MustHave    []string
	NiceToHave  []string
	MinYears    int
	CreatedAt   time.Time

	// Filled by ListJobs / GetJob.
	Total, Done, Failed, Pending int
}

func (s *Store) CreateJob(ctx context.Context, j Job) (int64, error) {
	must, _ := json.Marshal(nonNil(j.MustHave))
	nice, _ := json.Marshal(nonNil(j.NiceToHave))
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO jobs (title, description, must_have, nice_to_have, min_years) VALUES (?, ?, ?, ?, ?)`,
		j.Title, j.Description, must, nice, j.MinYears)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const jobSelect = `
SELECT j.id, j.title, j.description, j.must_have, j.nice_to_have, j.min_years, j.created_at,
       COUNT(c.id),
       COALESCE(SUM(c.status = 'done'), 0),
       COALESCE(SUM(c.status = 'failed'), 0),
       COALESCE(SUM(c.status IN ('queued', 'processing')), 0)
FROM jobs j LEFT JOIN candidates c ON c.job_id = j.id`

func (s *Store) ListJobs(ctx context.Context) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, jobSelect+` GROUP BY j.id ORDER BY j.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) GetJob(ctx context.Context, id int64) (Job, error) {
	j, err := scanJob(s.db.QueryRowContext(ctx, jobSelect+` WHERE j.id = ? GROUP BY j.id`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	return j, err
}

type scanner interface{ Scan(dest ...any) error }

func scanJob(r scanner) (Job, error) {
	var j Job
	var must, nice []byte
	err := r.Scan(&j.ID, &j.Title, &j.Description, &must, &nice, &j.MinYears, &j.CreatedAt,
		&j.Total, &j.Done, &j.Failed, &j.Pending)
	if err != nil {
		return j, err
	}
	_ = json.Unmarshal(must, &j.MustHave)
	_ = json.Unmarshal(nice, &j.NiceToHave)
	return j, nil
}

func (s *Store) JobEmbedding(ctx context.Context, jobID int64) ([]float32, error) {
	var b []byte
	err := s.db.QueryRowContext(ctx, `SELECT embedding FROM jobs WHERE id = ?`, jobID).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return DecodeVector(b), err
}

func (s *Store) SetJobEmbedding(ctx context.Context, jobID int64, v []float32) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET embedding = ? WHERE id = ?`, EncodeVector(v), jobID)
	return err
}

func (s *Store) CountJobs(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs`).Scan(&n)
	return n, err
}

// ── Candidates ──────────────────────────────────────────────────────────────

// ScoreBreakdown shows how the final score was built, so a recruiter can see why.
type ScoreBreakdown struct {
	MustHave   float64 `json:"must_have"`   // 0–50
	NiceToHave float64 `json:"nice_to_have"` // 0–15
	Experience float64 `json:"experience"`   // 0–15
	Semantic   float64 `json:"semantic"`     // 0–20
}

type Candidate struct {
	ID       int64
	JobID    int64
	FileName string
	RawText  string
	Status   string
	Attempts int
	Error    string

	Name         string
	CurrentTitle string
	Years        float64
	Skills       []string
	Education    string
	Matched      []string
	Missing      []string
	NiceMatched  []string
	Summary      string
	Score        int
	Breakdown    ScoreBreakdown
	Similarity   float64
	DurationMs   int

	CreatedAt   time.Time
	ProcessedAt sql.NullTime
}

// NewCandidate is a resume that has been uploaded but not screened yet.
type NewCandidate struct {
	FileName string
	RawText  string
}

func (s *Store) InsertCandidates(ctx context.Context, jobID int64, list []NewCandidate) ([]int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO candidates (job_id, file_name, raw_text, status) VALUES (?, ?, ?, 'queued')`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	ids := make([]int64, 0, len(list))
	for _, c := range list {
		res, err := stmt.ExecContext(ctx, jobID, c.FileName, c.RawText)
		if err != nil {
			return nil, err
		}
		id, _ := res.LastInsertId()
		ids = append(ids, id)
	}
	return ids, tx.Commit()
}

const candidateSelect = `
SELECT id, job_id, file_name, raw_text, status, attempts, COALESCE(error, ''),
       COALESCE(name, ''), COALESCE(current_title, ''), COALESCE(years, 0), skills, COALESCE(education, ''),
       matched, missing, nice_matched, COALESCE(summary, ''), COALESCE(score, 0), breakdown,
       COALESCE(similarity, 0), COALESCE(duration_ms, 0), created_at, processed_at
FROM candidates`

func scanCandidate(r scanner) (Candidate, error) {
	var c Candidate
	var skills, matched, missing, nice, breakdown []byte
	err := r.Scan(&c.ID, &c.JobID, &c.FileName, &c.RawText, &c.Status, &c.Attempts, &c.Error,
		&c.Name, &c.CurrentTitle, &c.Years, &skills, &c.Education,
		&matched, &missing, &nice, &c.Summary, &c.Score, &breakdown,
		&c.Similarity, &c.DurationMs, &c.CreatedAt, &c.ProcessedAt)
	if err != nil {
		return c, err
	}
	_ = json.Unmarshal(skills, &c.Skills)
	_ = json.Unmarshal(matched, &c.Matched)
	_ = json.Unmarshal(missing, &c.Missing)
	_ = json.Unmarshal(nice, &c.NiceMatched)
	_ = json.Unmarshal(breakdown, &c.Breakdown)
	return c, nil
}

// ListCandidates returns a job's candidates, best score first, then the ones still waiting.
func (s *Store) ListCandidates(ctx context.Context, jobID int64) ([]Candidate, error) {
	rows, err := s.db.QueryContext(ctx, candidateSelect+`
		WHERE job_id = ?
		ORDER BY status = 'done' DESC, score DESC, id ASC`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		c, err := scanCandidate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetCandidate(ctx context.Context, id int64) (Candidate, error) {
	c, err := scanCandidate(s.db.QueryRowContext(ctx, candidateSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// ResetInterrupted puts resumes that were mid-way when the app stopped back in the queue.
func (s *Store) ResetInterrupted(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE candidates SET status = 'queued' WHERE status = 'processing'`)
	return err
}

// QueuedIDs returns up to limit waiting resumes, oldest first, skipping ids already handed out.
func (s *Store) QueuedIDs(ctx context.Context, limit int, skip map[int64]bool) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM candidates WHERE status = 'queued' ORDER BY id LIMIT ?`, limit+len(skip))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if !skip[id] && len(ids) < limit {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

// ClaimForProcessing moves a queued resume to processing and counts the attempt.
// It returns false if the resume is no longer queued (for example it was cancelled).
func (s *Store) ClaimForProcessing(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE candidates SET status = 'processing', attempts = attempts + 1, error = NULL WHERE id = ? AND status = 'queued'`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// CountAttempt records one more try on a resume that is already processing.
func (s *Store) CountAttempt(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE candidates SET attempts = attempts + 1 WHERE id = ?`, id)
	return err
}

// CancelQueued stops a job's waiting resumes. Resumes being processed are stopped by the pipeline.
func (s *Store) CancelQueued(ctx context.Context, jobID int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE candidates SET status = 'cancelled' WHERE job_id = ? AND status = 'queued'`, jobID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Requeue puts a job's failed and cancelled resumes back in the queue with a fresh attempt count.
func (s *Store) Requeue(ctx context.Context, jobID int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE candidates SET status = 'queued', attempts = 0, error = NULL WHERE job_id = ? AND status IN ('failed', 'cancelled')`, jobID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) SetStatus(ctx context.Context, id int64, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE candidates SET status = ? WHERE id = ?`, status, id)
	return err
}

func (s *Store) MarkFailed(ctx context.Context, id int64, msg string) error {
	if len(msg) > 500 {
		msg = msg[:500]
	}
	_, err := s.db.ExecContext(ctx, `UPDATE candidates SET status = 'failed', error = ?, processed_at = NOW() WHERE id = ?`, msg, id)
	return err
}

// Chunk is one searchable piece of a resume plus its embedding (a list of numbers that captures meaning).
type Chunk struct {
	ID            int64
	CandidateID   int64
	CandidateName string
	JobID         int64
	Idx           int
	Text          string
	Embedding     []float32
}

// SaveResult stores a screened candidate and replaces their search chunks in one transaction.
func (s *Store) SaveResult(ctx context.Context, c Candidate, chunks []Chunk) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	skills, _ := json.Marshal(nonNil(c.Skills))
	matched, _ := json.Marshal(nonNil(c.Matched))
	missing, _ := json.Marshal(nonNil(c.Missing))
	nice, _ := json.Marshal(nonNil(c.NiceMatched))
	breakdown, _ := json.Marshal(c.Breakdown)
	_, err = tx.ExecContext(ctx, `
		UPDATE candidates SET status = 'done', error = NULL,
		  name = ?, current_title = ?, years = ?, skills = ?, education = ?,
		  matched = ?, missing = ?, nice_matched = ?, summary = ?, score = ?, breakdown = ?,
		  similarity = ?, duration_ms = ?, processed_at = NOW()
		WHERE id = ?`,
		c.Name, c.CurrentTitle, c.Years, skills, c.Education,
		matched, missing, nice, c.Summary, c.Score, breakdown,
		c.Similarity, c.DurationMs, c.ID)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM chunks WHERE candidate_id = ?`, c.ID); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO chunks (candidate_id, job_id, idx, text, embedding) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, ch := range chunks {
		if _, err = stmt.ExecContext(ctx, c.ID, c.JobID, ch.Idx, ch.Text, EncodeVector(ch.Embedding)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReplaceChunks swaps a candidate's search chunks without touching the screening result.
func (s *Store) ReplaceChunks(ctx context.Context, candidateID, jobID int64, chunks []Chunk) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM chunks WHERE candidate_id = ?`, candidateID); err != nil {
		return err
	}
	for _, ch := range chunks {
		if _, err = tx.ExecContext(ctx, `INSERT INTO chunks (candidate_id, job_id, idx, text, embedding) VALUES (?, ?, ?, ?, ?)`,
			candidateID, jobID, ch.Idx, ch.Text, EncodeVector(ch.Embedding)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ChunksForJob loads every searchable chunk of a job's screened resumes.
func (s *Store) ChunksForJob(ctx context.Context, jobID int64) ([]Chunk, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT ch.id, ch.candidate_id, COALESCE(c.name, c.file_name), ch.job_id, ch.idx, ch.text, ch.embedding
		FROM chunks ch JOIN candidates c ON c.id = ch.candidate_id
		WHERE ch.job_id = ? AND c.status = 'done'`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chunk
	for rows.Next() {
		var ch Chunk
		var emb []byte
		if err := rows.Scan(&ch.ID, &ch.CandidateID, &ch.CandidateName, &ch.JobID, &ch.Idx, &ch.Text, &emb); err != nil {
			return nil, err
		}
		ch.Embedding = DecodeVector(emb)
		out = append(out, ch)
	}
	return out, rows.Err()
}

// StatusCounts returns how many resumes are in each status, across all jobs.
func (s *Store) StatusCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM candidates GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// ── Vectors ─────────────────────────────────────────────────────────────────

// EncodeVector packs float32s as little-endian bytes (4 bytes each) for a BLOB column.
func EncodeVector(v []float32) []byte {
	if len(v) == 0 {
		return nil
	}
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

func DecodeVector(b []byte) []float32 {
	if len(b) < 4 {
		return nil
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// SplitList turns "Go, MySQL,  Docker" into ["Go", "MySQL", "Docker"].
func SplitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
