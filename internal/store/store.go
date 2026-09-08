// ABOUTME: Owns the SQLite connection and foundation schema lifecycle.
// ABOUTME: Applies ordered migrations transactionally and exposes health checks.
package store

import (
	"context"
	"database/sql"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
)

type Migration struct {
	Version    int
	Statements []string
}

var migrations = []Migration{{Version: 1, Statements: []string{"CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)", "INSERT INTO schema_version(version) SELECT 0 WHERE NOT EXISTS (SELECT 1 FROM schema_version)"}}}

type Store struct{ DB *sql.DB }

func Open(ctx context.Context, stateDirectory string) (*Store, error) {
	if stateDirectory == "" {
		return nil, fmt.Errorf("state directory is required")
	}
	info, err := os.Stat(stateDirectory)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("state directory is unavailable: %q", stateDirectory)
	}
	db, err := sql.Open("sqlite", filepath.Join(stateDirectory, "stv-poll.sqlite")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	st := &Store{DB: db}
	if err := st.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return st, nil
}
func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, migrations[0].Statements[0]); err != nil {
		return fmt.Errorf("create schema version: %w", err)
	}
	var version int
	if err := tx.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > migrations[len(migrations)-1].Version {
		return fmt.Errorf("unsupported schema version %d", version)
	}
	for _, m := range migrations {
		if m.Version <= version {
			continue
		}
		for _, statement := range m.Statements[1:] {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply migration %d: %w", m.Version, err)
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE schema_version SET version = ?", m.Version); err != nil {
			return fmt.Errorf("record migration %d: %w", m.Version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}
func (s *Store) Healthy(ctx context.Context) bool { return s != nil && s.DB.PingContext(ctx) == nil }
func (s *Store) Close() error {
	if s == nil || s.DB == nil {
		return nil
	}
	return s.DB.Close()
}
