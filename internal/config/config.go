// Package config reads all settings from environment variables.
// Nothing (hosts, ports, passwords) is hard-coded in the application.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Addr string // HTTP listen address, e.g. ":8080"

	DBHost     string
	DBPort     string
	DBName     string
	DBUser     string
	DBPassword string

	OllamaURL      string
	ChatModel      string
	EmbedModel     string
	OllamaTimeout  time.Duration
	PullModels     bool // pull missing models on start-up
	InitialWorkers int  // workers started with the pipeline
	MaxWorkers     int  // upper limit for the live worker slider
	QueueSize      int  // buffered queue; a full queue slows uploads down (back-pressure)
	MaxAttempts    int  // tries per resume before it is marked failed

	SeedDemoData   bool // create synthetic jobs + resumes on an empty database
	AutoScreenSeed bool // screen the seeded resumes straight away

	MaxUploadFiles int
	MaxUploadBytes int64 // per file
}

func Load() (Config, error) {
	c := Config{
		Addr:           ":" + env("APP_INTERNAL_PORT", "8080"),
		DBHost:         env("DB_HOST", "db"),
		DBPort:         env("DB_PORT", "3306"),
		DBName:         env("DB_DATABASE", "resume_screener"),
		DBUser:         env("DB_USERNAME", "screener"),
		DBPassword:     os.Getenv("DB_PASSWORD"),
		OllamaURL:      env("OLLAMA_BASE_URL", "http://ollama:11434"),
		ChatModel:      env("OLLAMA_CHAT_MODEL", "llama3.2:3b"),
		EmbedModel:     env("OLLAMA_EMBED_MODEL", "nomic-embed-text"),
		OllamaTimeout:  time.Duration(envInt("OLLAMA_TIMEOUT_SECONDS", 300)) * time.Second,
		PullModels:     envBool("OLLAMA_PULL_MODELS", true),
		InitialWorkers: envInt("PIPELINE_WORKERS", 3),
		MaxWorkers:     envInt("PIPELINE_MAX_WORKERS", 8),
		QueueSize:      envInt("PIPELINE_QUEUE_SIZE", 64),
		MaxAttempts:    envInt("PIPELINE_MAX_ATTEMPTS", 3),
		SeedDemoData:   envBool("SEED_DEMO_DATA", true),
		AutoScreenSeed: envBool("SEED_AUTO_SCREEN", true),
		MaxUploadFiles: envInt("UPLOAD_MAX_FILES", 50),
		MaxUploadBytes: int64(envInt("UPLOAD_MAX_FILE_KB", 2048)) * 1024,
	}
	if c.DBPassword == "" {
		return c, fmt.Errorf("DB_PASSWORD is not set")
	}
	if c.InitialWorkers < 1 || c.MaxWorkers < c.InitialWorkers {
		return c, fmt.Errorf("PIPELINE_WORKERS must be between 1 and PIPELINE_MAX_WORKERS")
	}
	return c, nil
}

// DSN is the MySQL connection string. parseTime turns DATETIME columns into time.Time.
func (c Config) DSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&charset=utf8mb4&collation=utf8mb4_unicode_ci&multiStatements=true",
		c.DBUser, c.DBPassword, c.DBHost, c.DBPort, c.DBName)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	if v, err := strconv.ParseBool(os.Getenv(key)); err == nil {
		return v
	}
	return def
}
