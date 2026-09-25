# Resume Screener & Talent Search

Stop reading every resume. A pool of **Go** workers reads resumes in
parallel, a **local AI model** (Ollama) extracts skills and experience as JSON,
each candidate gets an explainable 0–100 score, and a **RAG chat** answers
questions about the whole pile — naming the candidate for every fact.

**Live demo:** https://hire.chetanmeniya.dev

## Why Go

The hard part is throughput and reliability, not the AI call:

- **DB-backed queue + buffered channel.** The dispatcher blocks when workers are busy (back-pressure); nothing is lost on restart.
- **Resizable worker pool.** Change the worker count live from the dashboard; removed workers finish their current resume first.
- **Retries with exponential back-off and jitter**, plus a **circuit breaker** that pauses all AI calls when the model server keeps failing.
- **Cancellation** through `context` — cancel a job and in-flight AI calls stop.
- **Graceful shutdown** — unfinished resumes go back to the queue.
- **Parallel vector search** — chunks are sharded across goroutines, each keeping a top-k heap, then merged.
- Streaming everywhere with **Server-Sent Events** (live dashboard, chat answers).
- All tested with `go test -race`.

## Run it

```bash
cp .env.example .env      # set the DB passwords
docker compose up -d      # app + MySQL + Ollama
```

Open http://localhost:8011. The first start downloads the models (~2.3 GB) and screens 40 made-up resumes.

## Stack

Go 1.24 (standard library HTTP) · MySQL 8 · Ollama (llama3.2:3b, nomic-embed-text) · Tailwind CSS · Docker (≈19 MB image)

All demo data is synthetic. No resume ever leaves the server.
