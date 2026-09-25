package store

import (
	"context"
	"fmt"
)

// migrations run in order, once each. Append new ones; never edit an old one.
var migrations = []string{
	`CREATE TABLE jobs (
		id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
		title VARCHAR(200) NOT NULL,
		description TEXT NOT NULL,
		must_have JSON NOT NULL,
		nice_to_have JSON NOT NULL,
		min_years INT NOT NULL DEFAULT 0,
		embedding MEDIUMBLOB NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	`CREATE TABLE candidates (
		id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
		job_id BIGINT UNSIGNED NOT NULL,
		file_name VARCHAR(255) NOT NULL,
		raw_text MEDIUMTEXT NOT NULL,
		status VARCHAR(20) NOT NULL DEFAULT 'queued',
		attempts INT NOT NULL DEFAULT 0,
		error VARCHAR(500) NULL,
		name VARCHAR(200) NULL,
		current_title VARCHAR(200) NULL,
		years DECIMAL(4,1) NULL,
		skills JSON NULL,
		education VARCHAR(300) NULL,
		matched JSON NULL,
		missing JSON NULL,
		nice_matched JSON NULL,
		summary TEXT NULL,
		score INT NULL,
		breakdown JSON NULL,
		similarity DOUBLE NULL,
		duration_ms INT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		processed_at DATETIME NULL,
		INDEX idx_candidates_job_status (job_id, status),
		INDEX idx_candidates_status (status),
		CONSTRAINT fk_candidates_job FOREIGN KEY (job_id) REFERENCES jobs(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	`CREATE TABLE chunks (
		id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
		candidate_id BIGINT UNSIGNED NOT NULL,
		job_id BIGINT UNSIGNED NOT NULL,
		idx INT NOT NULL,
		text TEXT NOT NULL,
		embedding MEDIUMBLOB NOT NULL,
		INDEX idx_chunks_job (job_id),
		CONSTRAINT fk_chunks_candidate FOREIGN KEY (candidate_id) REFERENCES candidates(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
}

// Migrate brings the schema up to date. A version table records what has run.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INT NOT NULL)`); err != nil {
		return err
	}
	var version int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		if _, err := s.db.ExecContext(ctx, migrations[i]); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, i+1); err != nil {
			return err
		}
	}
	return nil
}
