# Resume Screener & Talent Search (Go) — CLAUDE.md

## What this product does
Recruiters get 300–1,000 resumes per job and spend days reading them. Upload a
job and a batch of resumes: a pool of Go workers reads them in parallel, a local
AI model (Ollama) pulls out skills and experience as JSON, Go scores each resume
0–100 with a reason, and a RAG chat (search the resumes first, then answer)
answers questions about the whole pile, naming the candidate for each fact.
The recruiter reads the top 10 instead of all 500. Speed depends on the server:
see Performance notes.

Shown on the owner's personal portfolio only. Do not mention any other brand in
this repo, the UI or the docs.

## Tech stack (fixed)
- Go 1.24, standard library HTTP server, html/template
- MySQL 8.0
- Ollama: llama3.2:3b (reading resumes, chat) + nomic-embed-text (embeddings)
- Tailwind CSS v3 (built in the Docker image), no JS framework
- Docker (docker compose). Final image ~19 MB, runs as non-root.

## Ports
- APP_PORT: 8011
- DB_FORWARD_PORT: 33071 (bound to 127.0.0.1)

## Quick start
    cp .env.example .env     # set DB passwords
    docker compose up -d     # starts app + MySQL + Ollama (COMPOSE_PROFILES=ollama)
    open http://localhost:8011
First start downloads the AI models (~2.3 GB) and screens 40 seeded resumes.

To use an Ollama that already runs on the machine instead, set in .env:
`COMPOSE_PROFILES=`, `COMPOSE_FILE=docker-compose.yml:docker-compose.shared-ollama.yml`,
`AI_NETWORK=<its docker network>`.

## Features (MVP)
1. Jobs with must-have / nice-to-have skills and minimum years.
2. Bulk upload (PDF, TXT, MD), parsed in parallel; bad files reported, not fatal.
3. Parallel screening pipeline: DB-backed queue → buffered channel (back-pressure)
   → resizable worker pool; retries with exponential back-off + jitter; circuit
   breaker; cancel per job; graceful shutdown puts unfinished work back in the queue.
4. Ranked shortlist with score breakdown, matched/missing skills, CSV export
   (formula-injection safe).
5. RAG chat: parallel sharded vector search (top-k heaps per goroutine), max 1
   extract per candidate, streamed answer over SSE with sources.
Plus a live pipeline dashboard (SSE, every 500 ms).

## Code map
- `cmd/server` — wiring, start-up, graceful shutdown
- `internal/pipeline` — dispatcher, worker pool, retries, stats (the core)
- `internal/breaker` — circuit breaker
- `internal/rag` — chunking (whole lines), cosine, parallel search
- `internal/scoring` — deterministic score (AI reads, Go does the maths)
- `internal/ollama` — Ollama client (JSON-schema output, streaming, pull)
- `internal/store` — all SQL; migrations in `migrate.go` (append only)
- `internal/seed` — 3 jobs + 40 synthetic resumes (all invented, example.com)
- `internal/server` — handlers, templates, rate limits, security headers
- `web/` — templates and static files (embedded in the binary)

## Tests
    docker run --rm -v "$PWD":/src -w /src golang:1.24 go test -race ./...

## Synthetic data plan
3 jobs (Senior Laravel Developer, Go Backend Engineer, Data Analyst) and 40
generated resumes of mixed fit: strong, partial, and off-target. Fixed random seed,
so the same people every time. `/samples.zip` gives visitors 12 fresh ones to upload.

## Design
Tailwind, Inter, teal accent, Heroicons. Light UI, cards with `shadow-sm`.

## Performance notes
CPU-only on a 4-core laptop: ~100–140 s per resume with 3 in parallel, chat ~2–3 min.
A server with more cores (or a GPU) is much faster; a smaller chat model
(OLLAMA_CHAT_MODEL) also helps.

## Quality checklist
- [x] Cold start works with docker compose up only
- [x] Works end-to-end on seeded synthetic data (40/40 screened)
- [x] No hard-coded credentials or localhost in code
- [x] .env.example documents every variable
- [x] `go test -race` passes
- [ ] Manually tested on the production server
