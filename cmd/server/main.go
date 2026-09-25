// Command server runs the resume screening web app and its background pipeline.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chetanmeniya431-ai/resume-screener-go/internal/breaker"
	"github.com/chetanmeniya431-ai/resume-screener-go/internal/config"
	"github.com/chetanmeniya431-ai/resume-screener-go/internal/ollama"
	"github.com/chetanmeniya431-ai/resume-screener-go/internal/pipeline"
	"github.com/chetanmeniya431-ai/resume-screener-go/internal/seed"
	"github.com/chetanmeniya431-ai/resume-screener-go/internal/server"
	"github.com/chetanmeniya431-ai/resume-screener-go/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// ctx is cancelled on Ctrl+C or `docker stop` (SIGTERM).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DSN())
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	if cfg.SeedDemoData {
		seeded, err := seed.Run(ctx, st, cfg.AutoScreenSeed)
		if err != nil {
			return err
		}
		if seeded {
			log.Info("demo data created")
		}
	}

	ai := ollama.New(cfg.OllamaURL, cfg.ChatModel, cfg.EmbedModel, cfg.OllamaTimeout)
	engine := pipeline.New(st, ai, breaker.New(5, 30*time.Second), pipeline.Options{
		Workers:     cfg.InitialWorkers,
		MaxWorkers:  cfg.MaxWorkers,
		QueueSize:   cfg.QueueSize,
		MaxAttempts: cfg.MaxAttempts,
		TaskTimeout: cfg.OllamaTimeout * time.Duration(cfg.MaxAttempts),
	}, log)

	// The web app starts at once. The pipeline starts when the AI models are ready,
	// which can take a few minutes on a fresh server while they download.
	pipelineCtx, stopPipeline := context.WithCancel(context.Background())
	go startWhenReady(ctx, pipelineCtx, ai, engine, cfg.PullModels, log)

	srv, err := server.New(st, engine, ai, log, server.Options{
		MaxUploadFiles: cfg.MaxUploadFiles,
		MaxUploadBytes: cfg.MaxUploadBytes,
		MaxJobs:        30,
		ChatModel:      cfg.ChatModel,
		EmbedModel:     cfg.EmbedModel,
	})
	if err != nil {
		stopPipeline()
		return err
	}
	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No WriteTimeout: the dashboard and chat stream for a long time.
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr)
		errCh <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		stopPipeline()
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
	}

	// Graceful shutdown: stop taking requests, then stop the pipeline. Resumes that
	// were mid-way go back to "queued" and run again after the next start.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	stopPipeline()
	engine.Wait()
	log.Info("stopped cleanly")
	return nil
}

func startWhenReady(appCtx, pipelineCtx context.Context, ai *ollama.Client, engine *pipeline.Engine, pull bool, log *slog.Logger) {
	for {
		missing, err := ai.MissingModels(appCtx)
		switch {
		case err != nil:
			log.Warn("waiting for Ollama", "err", err)
		case len(missing) == 0:
			if err := engine.Start(pipelineCtx); err != nil {
				log.Error("pipeline start", "err", err)
				return
			}
			log.Info("pipeline started")
			return
		case pull:
			failed := false
			for _, m := range missing {
				log.Info("downloading model (first start only)", "model", m)
				if err := ai.Pull(appCtx, m); err != nil {
					log.Error("model download failed", "model", m, "err", err)
					failed = true
				}
			}
			if !failed {
				continue // check again, then start
			}
		default:
			log.Error("models missing and OLLAMA_PULL_MODELS=false", "missing", missing)
		}
		select {
		case <-appCtx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}
