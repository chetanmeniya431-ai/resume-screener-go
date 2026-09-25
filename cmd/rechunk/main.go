// Command rechunk rebuilds the search chunks of every screened resume.
// Run it after changing the chunking rules. It only calls the embedding model
// (fast); scores and AI summaries are kept as they are.
//
//	docker compose run --rm --entrypoint /app/rechunk app
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/chetanmeniya431-ai/resume-screener-go/internal/config"
	"github.com/chetanmeniya431-ai/resume-screener-go/internal/ollama"
	"github.com/chetanmeniya431-ai/resume-screener-go/internal/rag"
	"github.com/chetanmeniya431-ai/resume-screener-go/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "rechunk:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DSN())
	if err != nil {
		return err
	}
	defer st.Close()
	ai := ollama.New(cfg.OllamaURL, cfg.ChatModel, cfg.EmbedModel, cfg.OllamaTimeout)

	jobs, err := st.ListJobs(ctx)
	if err != nil {
		return err
	}
	total := 0
	for _, j := range jobs {
		cands, err := st.ListCandidates(ctx, j.ID)
		if err != nil {
			return err
		}
		for _, c := range cands {
			if c.Status != store.StatusDone {
				continue
			}
			texts := rag.Chunk(c.RawText, 120, 20)
			inputs := make([]string, len(texts))
			for i, t := range texts {
				inputs[i] = ollama.DocPrefix + t
			}
			vecs, err := ai.Embed(ctx, inputs)
			if err != nil {
				return fmt.Errorf("candidate %d: %w", c.ID, err)
			}
			chunks := make([]store.Chunk, len(texts))
			for i := range texts {
				chunks[i] = store.Chunk{Idx: i, Text: texts[i], Embedding: vecs[i]}
			}
			if err := st.ReplaceChunks(ctx, c.ID, j.ID, chunks); err != nil {
				return err
			}
			total++
			fmt.Printf("%-24s %d chunks\n", c.Name, len(chunks))
		}
	}
	fmt.Printf("done: %d resumes re-chunked\n", total)
	return nil
}
